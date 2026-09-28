package drift

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/ollama"
)

// source is a Source whose answers a test sets.
type source struct {
	mu              sync.Mutex
	version, digest string
	err             error
}

func (s *source) set(version, digest string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version, s.digest, s.err = version, digest, err
}

func (s *source) Version(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version, s.err
}

func (s *source) ModelDigest(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.digest, s.err
}

// recorder is a slog handler that keeps every record.
type recorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler     { return r }
func (r *recorder) WithGroup(string) slog.Handler          { return r }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Clone())
	return nil
}

// at returns the messages logged at level.
func (r *recorder) at(level slog.Level) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, rec := range r.records {
		if rec.Level == level {
			out = append(out, rec.Message)
		}
	}
	return out
}

const (
	digestA = "sha256:0a109f422b47e3a30ba2b10eca18548e944e8a23073ee3f3e947efcf3c45e59f"
	digestB = "sha256:ac6da0dfba84d0b5b1f23d8e5b8b0e0e4f1b1e3c2f4a5b6c7d8e9f0a1b2c3d4e"
)

// newMonitor returns a Monitor over a new home whose marker records
// recorded, the home, the source and the log.
func newMonitor(t *testing.T, recorded Fingerprint) (*Monitor, *curiohome.Home, *source, *recorder) {
	t.Helper()
	home, err := curiohome.Init(t.TempDir(), "qwen3-embedding:0.6b", 1024)
	require.NoError(t, err)
	meta, err := home.Meta()
	require.NoError(t, err)
	meta.SchemaVersion = 12
	meta.EmbeddingModelDigest, meta.OllamaVersion = recorded.ModelDigest, recorded.OllamaVersion
	require.NoError(t, home.WriteMeta(meta))
	src := &source{}
	log := &recorder{}
	return New(home, src, slog.New(log)), home, src, log
}

func marker(t *testing.T, home *curiohome.Home) curiohome.Meta {
	t.Helper()
	meta, err := home.Meta()
	require.NoError(t, err)
	return meta
}

// TestCheck_RecordsTheFirstFingerprint: a home without a fingerprint gets
// the current one, every other marker field kept, and no drift.
func TestCheck_RecordsTheFirstFingerprint(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{})
	before := marker(t, home)
	src.set("0.34.4", digestA, nil)

	m.Check(context.Background())

	after := marker(t, home)
	assert.Equal(t, digestA, after.EmbeddingModelDigest)
	assert.Equal(t, "0.34.4", after.OllamaVersion)
	after.EmbeddingModelDigest, after.OllamaVersion = "", ""
	after.UpdatedAt = before.UpdatedAt
	assert.Equal(t, before, after, "every other field is kept")
	report := m.Report()
	assert.False(t, report.Drifted())
	assert.False(t, report.CheckedAt.IsZero())
	assert.Equal(t, []string{"recorded the embedding fingerprint"}, log.at(slog.LevelInfo))
}

func TestCheck_SameFingerprintIsNoDrift(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	before := marker(t, home)
	src.set("0.34.4", digestA, nil)

	m.Check(context.Background())

	assert.False(t, m.Report().Drifted())
	assert.False(t, m.Report().CheckedAt.IsZero())
	assert.Equal(t, before, marker(t, home), "nothing written")
	assert.Empty(t, log.at(slog.LevelWarn))
}

// TestCheck_ReportsEachChangeAndWarnsOncePerDrift: a changed digest and
// version are each reported with both values, and each distinct drift is
// warned about once, however many checks find it.
func TestCheck_ReportsEachChangeAndWarnsOncePerDrift(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.30.0"})
	before := marker(t, home)
	src.set("0.34.4", digestB, nil)

	m.Check(context.Background())
	m.Check(context.Background())

	assert.Equal(t, []Change{
		{What: ModelDigest, Recorded: digestA, Current: digestB},
		{What: OllamaVersion, Recorded: "0.30.0", Current: "0.34.4"},
	}, m.Report().Changes)
	require.Len(t, log.at(slog.LevelWarn), 1, "one warning for one drift")
	assert.Contains(t, log.at(slog.LevelWarn)[0], "curio reindex --all")
	assert.Equal(t, before, marker(t, home), "a drift is reported, never recorded over")

	src.set("0.35.0", digestB, nil)
	m.Check(context.Background())
	assert.Len(t, log.at(slog.LevelWarn), 2, "another drift is warned about")

	src.set("0.30.0", digestA, nil)
	m.Check(context.Background())
	assert.False(t, m.Report().Drifted(), "back to the recorded build")
	src.set("0.35.0", digestB, nil)
	m.Check(context.Background())
	assert.Len(t, log.at(slog.LevelWarn), 3, "a drift that comes back is warned about again")
}

// TestCheck_NothingToCompare: with Ollama down, the model not pulled, or
// the check cut short, the check writes nothing, keeps its last report and
// logs only at DEBUG.
func TestCheck_NothingToCompare(t *testing.T) {
	for name, err := range map[string]error{
		"unreachable":      fmt.Errorf("%w: connection refused", ollama.ErrUnreachable),
		"model not pulled": fmt.Errorf("%w: qwen3-embedding:0.6b", ollama.ErrModelNotLoaded),
		"cut short":        fmt.Errorf("/api/tags: %w", context.DeadlineExceeded),
	} {
		t.Run(name, func(t *testing.T) {
			m, home, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.30.0"})
			src.set("0.34.4", digestA, nil)
			m.Check(context.Background())
			drifted := m.Report()
			require.True(t, drifted.Drifted())
			before := marker(t, home)

			src.set("", "", err)
			m.Check(context.Background())

			assert.Equal(t, drifted, m.Report(), "the last report stands")
			assert.Equal(t, before, marker(t, home))
			assert.Equal(t, []string{"embedding drift check skipped"}, log.at(slog.LevelDebug))
			assert.Len(t, log.at(slog.LevelWarn), 1, "only the drift")
			assert.Empty(t, log.at(slog.LevelError))
		})
	}

	t.Run("no fingerprint yet", func(t *testing.T) {
		m, home, src, _ := newMonitor(t, Fingerprint{})
		before := marker(t, home)
		src.set("", "", ollama.ErrUnreachable)
		m.Check(context.Background())
		assert.Equal(t, before, marker(t, home), "nothing is recorded without an answer")
		assert.True(t, m.Report().CheckedAt.IsZero())
	})
}

// TestCheck_UnreadableAnswerWarnsOnce: an answer the check can't read
// keeps drift detection off, and nothing else reports it, so the first of
// a run of them is a WARN; a check that reads the fingerprint ends the run.
func TestCheck_UnreadableAnswerWarnsOnce(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{})
	before := marker(t, home)
	noDigest := errors.New("/api/tags lists qwen3-embedding:0.6b without a digest")

	src.set("", "", noDigest)
	m.Check(context.Background())
	m.Check(context.Background())
	assert.Len(t, log.at(slog.LevelWarn), 1, "once per run")
	assert.Len(t, log.at(slog.LevelDebug), 1, "the rest of the run")
	assert.Equal(t, before, marker(t, home), "nothing recorded")

	src.set("0.34.4", digestA, nil)
	m.Check(context.Background())
	src.set("", "", noDigest)
	m.Check(context.Background())
	assert.Len(t, log.at(slog.LevelWarn), 2, "a run that starts again is warned about again")
}

// TestCheck_BoundedByItsTimeout: a check gives up on a hung Ollama within
// its own timeout, however long the caller's context allows.
func TestCheck_BoundedByItsTimeout(t *testing.T) {
	m, _, _, _ := newMonitor(t, Fingerprint{})
	m.src = hangingSource{}
	m.timeout = 20 * time.Millisecond

	done := make(chan struct{})
	go func() {
		m.Check(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the check did not give up")
	}
}

// hangingSource answers nothing until the context ends.
type hangingSource struct{}

func (hangingSource) Version(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (hangingSource) ModelDigest(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestCheck_MarkerWriteFailureIsRetried: a fingerprint the marker can't
// take is logged at ERROR and recorded by the next check.
func TestCheck_MarkerWriteFailureIsRetried(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{})
	src.set("0.34.4", digestA, nil)
	// WriteMeta can't create its temp file where a non-empty directory
	// stands, whoever runs the test.
	blocker := home.MarkerPath() + ".tmp"
	require.NoError(t, os.MkdirAll(filepath.Join(blocker, "keep"), 0o700))

	m.Check(context.Background())
	assert.Len(t, log.at(slog.LevelError), 1)
	assert.Empty(t, marker(t, home).EmbeddingModelDigest)

	require.NoError(t, os.RemoveAll(blocker))
	m.Check(context.Background())
	assert.Equal(t, digestA, marker(t, home).EmbeddingModelDigest)
}

// TestCheck_UnreadableMarkerIsRetried: a marker that can't be parsed is
// logged at ERROR, and the check neither reports nor writes anything; once
// it is repaired, the next check records the fingerprint as usual.
func TestCheck_UnreadableMarkerIsRetried(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{})
	good, err := os.ReadFile(home.MarkerPath())
	require.NoError(t, err)
	const garbage = "{not json"
	require.NoError(t, os.WriteFile(home.MarkerPath(), []byte(garbage), 0o600))
	src.set("0.34.4", digestA, nil)
	before := m.Report()

	m.Check(context.Background())
	assert.Len(t, log.at(slog.LevelError), 1)
	assert.Equal(t, before, m.Report(), "the report is unchanged")
	written, err := os.ReadFile(home.MarkerPath())
	require.NoError(t, err)
	assert.Equal(t, garbage, string(written), "nothing written")

	require.NoError(t, os.WriteFile(home.MarkerPath(), good, 0o600))
	m.Check(context.Background())
	assert.Equal(t, digestA, marker(t, home).EmbeddingModelDigest)
	assert.Len(t, log.at(slog.LevelError), 1, "no new error")
	assert.False(t, m.Report().CheckedAt.IsZero())
}

// TestRebaseline_RecordsTheCurrentBuild: a rebaseline clears the drift and
// the recorded fingerprint, and the check it asks for records the build
// serving now.
func TestRebaseline_RecordsTheCurrentBuild(t *testing.T) {
	m, home, src, _ := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.30.0"})
	m.interval = time.Hour // only the first check and the rebaseline's run
	src.set("0.34.4", digestB, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()
	require.Eventually(t, func() bool { return m.Report().Drifted() }, 5*time.Second, 5*time.Millisecond)

	require.NoError(t, m.Rebaseline())
	assert.False(t, m.Report().Drifted(), "cleared at once")
	require.Eventually(t, func() bool { return marker(t, home).EmbeddingModelDigest == digestB },
		5*time.Second, 5*time.Millisecond)
	assert.Equal(t, "0.34.4", marker(t, home).OllamaVersion)
	assert.False(t, m.Report().Drifted())

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when its context ended")
	}
}

// TestRebaseline_ClearsTheMarker: the marker loses its fingerprint and
// nothing else.
func TestRebaseline_ClearsTheMarker(t *testing.T) {
	m, home, _, _ := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.30.0"})
	before := marker(t, home)

	require.NoError(t, m.Rebaseline())

	after := marker(t, home)
	before.EmbeddingModelDigest, before.OllamaVersion, before.UpdatedAt = "", "", after.UpdatedAt
	assert.Equal(t, before, after)
}

// TestMonitor_ConcurrentChecksAndRebaselines: checks, rebaselines and
// report reads from many goroutines at once leave a readable marker whose
// fingerprint, when set, is the source's.
func TestMonitor_ConcurrentChecksAndRebaselines(t *testing.T) {
	m, home, src, _ := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.30.0"})
	src.set("0.34.4", digestB, nil)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				m.Check(context.Background())
				_ = m.Report()
			}
		})
		wg.Go(func() {
			for range 20 {
				assert.NoError(t, m.Rebaseline())
			}
		})
	}
	wg.Wait()

	m.Check(context.Background())
	meta := marker(t, home)
	assert.Equal(t, digestB, meta.EmbeddingModelDigest)
	assert.Equal(t, "0.34.4", meta.OllamaVersion)
	assert.Equal(t, "qwen3-embedding:0.6b", meta.EmbeddingModel)
	assert.False(t, m.Report().Drifted())
}
