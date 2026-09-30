package drift

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// verifier is a Verifier whose answer a test sets. It counts its calls
// and how many run at once; each call signals started (when set), runs
// during (when set), then waits for release (when set) or its context.
type verifier struct {
	mu         sync.Mutex
	cmp        Comparison
	err        error
	during     func()
	started    chan struct{}
	release    chan struct{}
	calls      int
	running    int
	maxRunning int
}

func (v *verifier) answer(cmp Comparison, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cmp, v.err = cmp, err
}

func (v *verifier) Verify(ctx context.Context) (Comparison, error) {
	v.mu.Lock()
	v.calls++
	v.running++
	v.maxRunning = max(v.maxRunning, v.running)
	cmp, err, during, started, release := v.cmp, v.err, v.during, v.started, v.release
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.running--
		v.mu.Unlock()
	}()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if during != nil {
		during()
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return Comparison{}, fmt.Errorf("re-embed the sample: %w", ctx.Err())
		}
	}
	return cmp, err
}

func (v *verifier) callCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

// fake is m's verifier, which newMonitor made.
func fake(m *Monitor) *verifier { return m.verifier.(*verifier) }

// The comparisons a verification finds: every sampled chunk changed, or
// none.
var (
	differs = Comparison{Sampled: 64, Changed: 64, MinCosine: 0.9713}
	same    = Comparison{Sampled: 64, Identical: 64, MinCosine: 1}
)

// clock is a time a test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// withClock gives m a clock the test moves.
func withClock(m *Monitor) *clock {
	c := &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	m.now = c.now
	return c
}

// attrs returns the attributes of the first record logged at level whose
// message starts with prefix, by key.
func (r *recorder) attrs(t *testing.T, level slog.Level, prefix string) map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if rec.Level != level || !strings.HasPrefix(rec.Message, prefix) {
			continue
		}
		out := map[string]any{}
		rec.Attrs(func(a slog.Attr) bool {
			out[a.Key] = a.Value.Any()
			return true
		})
		return out
	}
	t.Fatalf("no %s record starts with %q", level, prefix)
	return nil
}

const (
	digestA = "sha256:0a109f422b47e3a30ba2b10eca18548e944e8a23073ee3f3e947efcf3c45e59f"
	digestB = "sha256:ac6da0dfba84d0b5b1f23d8e5b8b0e0e4f1b1e3c2f4a5b6c7d8e9f0a1b2c3d4e"
)

// newMonitor returns a Monitor over a new home whose marker records
// recorded, the home, the source and the log. Its verifier finds every
// sampled chunk changed until the test says otherwise (fake).
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
	return New(home, src, &verifier{cmp: differs}, slog.New(log)), home, src, log
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
	assert.Zero(t, fake(m).callCount(), "nothing to verify")
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
	assert.Zero(t, fake(m).callCount(), "nothing to verify")
}

// TestCheck_ReportsEachChangeAndWarnsOncePerDrift: a changed digest and
// version whose sample re-embeds differently are each reported with both
// values, and each distinct drift is verified and warned about once,
// however many checks find it.
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
	assert.Equal(t, 1, fake(m).callCount(), "verified once")
	require.Len(t, log.at(slog.LevelWarn), 1, "one warning for one drift")
	assert.Contains(t, log.at(slog.LevelWarn)[0], "curio reindex --all")
	assert.Equal(t, before, marker(t, home), "a drift is reported, never recorded over")

	src.set("0.35.0", digestB, nil)
	m.Check(context.Background())
	assert.Len(t, log.at(slog.LevelWarn), 2, "another drift is warned about")
	assert.Equal(t, 2, fake(m).callCount(), "and verified")

	src.set("0.30.0", digestA, nil)
	m.Check(context.Background())
	assert.False(t, m.Report().Drifted(), "back to the recorded build")
	src.set("0.35.0", digestB, nil)
	m.Check(context.Background())
	assert.Len(t, log.at(slog.LevelWarn), 3, "a drift that comes back is warned about again")
	assert.Equal(t, 3, fake(m).callCount(), "and verified again")
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

// TestMonitor_ConcurrentChecksAndRebaselines: checks, verifications,
// rebaselines and report reads from many goroutines at once leave a
// readable marker whose fingerprint, when set, is the source's.
func TestMonitor_ConcurrentChecksAndRebaselines(t *testing.T) {
	m, home, src, _ := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.30.0"})
	src.set("0.34.4", digestB, nil)
	fake(m).answer(same, nil)
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

// TestCheck_VerifiesEachChangeOnce: whatever part of the build changed,
// the change is verified once, and later checks reuse the verdict.
func TestCheck_VerifiesEachChangeOnce(t *testing.T) {
	recorded := Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"}
	for name, tc := range map[string]struct {
		version, digest string
		want            []Change
	}{
		"the digest":  {"0.34.4", digestB, []Change{{What: ModelDigest, Recorded: digestA, Current: digestB}}},
		"the version": {"0.35.0", digestA, []Change{{What: OllamaVersion, Recorded: "0.34.4", Current: "0.35.0"}}},
		"both": {"0.35.0", digestB, []Change{
			{What: ModelDigest, Recorded: digestA, Current: digestB},
			{What: OllamaVersion, Recorded: "0.34.4", Current: "0.35.0"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			m, _, src, log := newMonitor(t, recorded)
			src.set(tc.version, tc.digest, nil)
			for range 3 {
				m.Check(context.Background())
			}
			assert.Equal(t, tc.want, m.Report().Changes)
			assert.Equal(t, 1, fake(m).callCount())
			assert.Len(t, log.at(slog.LevelWarn), 1)
		})
	}
}

// TestCheck_SameVectorsRecordTheBuild: a change whose sample re-embeds as
// stored is recorded, every other marker field kept, with one INFO that
// names the change and the evidence and no warning; nothing is reported.
func TestCheck_SameVectorsRecordTheBuild(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	fake(m).answer(same, nil)
	before := marker(t, home)
	src.set("0.35.0", digestA, nil)

	m.Check(context.Background())

	after := marker(t, home)
	assert.Equal(t, "0.35.0", after.OllamaVersion)
	after.OllamaVersion, after.UpdatedAt = before.OllamaVersion, before.UpdatedAt
	assert.Equal(t, before, after, "every other field is kept")
	report := m.Report()
	assert.False(t, report.Drifted())
	assert.False(t, report.CheckedAt.IsZero())
	assert.Empty(t, log.at(slog.LevelWarn))
	attrs := log.attrs(t, slog.LevelInfo, "the embedding build changed, and a re-embedded sample matches")
	assert.Equal(t, "0.34.4 -> 0.35.0", attrs[OllamaVersion])
	assert.Equal(t, "0 of 64 sampled chunks changed (worst cosine 1.0000)", attrs["evidence"])
	assert.Equal(t, int64(64), attrs["identical"])
	assert.InDelta(t, 1.0, attrs["worst_cosine"], 0)

	m.Check(context.Background())
	assert.Equal(t, 1, fake(m).callCount(), "the recorded build needs no verification")
}

// TestCheck_EmptyLibraryRecordsSilently: with nothing indexed there is
// nothing a new build could have drifted from.
func TestCheck_EmptyLibraryRecordsSilently(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	fake(m).answer(Comparison{MinCosine: 1}, nil)
	src.set("0.35.0", digestB, nil)

	m.Check(context.Background())

	assert.Equal(t, digestB, marker(t, home).EmbeddingModelDigest)
	assert.False(t, m.Report().Drifted())
	assert.Empty(t, log.at(slog.LevelWarn))
}

// TestCheck_SameVectorsMarkerWriteIsRetried: a verified build the marker
// can't take is an ERROR, and the next check retries the write without
// verifying again.
func TestCheck_SameVectorsMarkerWriteIsRetried(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	fake(m).answer(same, nil)
	src.set("0.35.0", digestA, nil)
	blocker := home.MarkerPath() + ".tmp"
	require.NoError(t, os.MkdirAll(filepath.Join(blocker, "keep"), 0o700))

	m.Check(context.Background())
	assert.Len(t, log.at(slog.LevelError), 1)
	assert.Equal(t, "0.34.4", marker(t, home).OllamaVersion)
	assert.False(t, m.Report().Drifted(), "the vectors didn't change")

	require.NoError(t, os.RemoveAll(blocker))
	m.Check(context.Background())
	assert.Equal(t, "0.35.0", marker(t, home).OllamaVersion)
	assert.Equal(t, 1, fake(m).callCount(), "only the write was retried")
	assert.Empty(t, log.at(slog.LevelWarn))
}

// TestCheck_ChangedVectorsReportTheEvidence: a drift carries what its
// sample found, warned about once with its detail and the fix; later
// checks report the same verdict, checked again.
func TestCheck_ChangedVectorsReportTheEvidence(t *testing.T) {
	m, _, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	clk := withClock(m)
	src.set("0.34.4", digestB, nil)

	m.Check(context.Background())

	verdictAt := clk.now()
	report := m.Report()
	assert.Equal(t, Evidence{Verified: true, Comparison: differs, At: verdictAt}, report.Evidence)
	assert.Equal(t, "64 of 64 sampled chunks changed (worst cosine 0.9713)", report.Evidence.Detail())
	require.Len(t, log.at(slog.LevelWarn), 1)
	assert.Equal(t, "embeddings drifted: what made the library's vectors has changed "+
		"(64 of 64 sampled chunks changed (worst cosine 0.9713)), so searches compare vectors from two builds; "+
		"run `curio reindex --all`", log.at(slog.LevelWarn)[0])

	clk.advance(time.Hour)
	m.Check(context.Background())
	report = m.Report()
	assert.Equal(t, clk.now(), report.CheckedAt, "checked again")
	assert.Equal(t, verdictAt, report.Evidence.At, "on the same verdict")
	assert.Equal(t, 1, fake(m).callCount())
	assert.Len(t, log.at(slog.LevelWarn), 1)
}

// TestCheck_UnverifiableIsReportedAtOnce: a build that can't re-embed the
// sample is reported unverified at once, with why, and never retried for
// that change.
func TestCheck_UnverifiableIsReportedAtOnce(t *testing.T) {
	m, _, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	clk := withClock(m)
	cause := fmt.Errorf("%w: re-embed 64 sampled chunks: embedding has the wrong dimension", ErrUnverifiable)
	fake(m).answer(Comparison{}, cause)
	src.set("0.34.4", digestB, nil)

	m.Check(context.Background())

	report := m.Report()
	require.True(t, report.Drifted())
	assert.Equal(t, Evidence{Reason: cause.Error(), At: clk.now()}, report.Evidence)
	assert.Equal(t, "not verified: "+cause.Error(), report.Evidence.Detail())
	require.Len(t, log.at(slog.LevelWarn), 1)
	assert.True(t, strings.HasPrefix(log.at(slog.LevelWarn)[0], "embeddings may have drifted:"))
	assert.Contains(t, log.at(slog.LevelWarn)[0], report.Evidence.Detail())

	clk.advance(5 * time.Hour)
	m.Check(context.Background())
	assert.Equal(t, 1, fake(m).callCount(), "no retries")
	assert.Len(t, log.at(slog.LevelWarn), 1)
}

// TestCheck_FailedVerificationsBackOff: a verification that fails for a
// reason the next one may not repeat changes no report and warns about
// nothing; it is retried after 15 minutes, doubling up to 4 hours. From
// the third failure in a row the change is reported unverified, once; a
// verdict then replaces that report.
func TestCheck_FailedVerificationsBackOff(t *testing.T) {
	for name, verdict := range map[string]Comparison{"differs": differs, "same": same} {
		t.Run(name, func(t *testing.T) {
			m, home, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
			clk := withClock(m)
			src.set("0.34.4", digestA, nil)
			m.Check(context.Background())
			clean := m.Report()
			unreachable := fmt.Errorf("re-embed 64 sampled chunks: %w: connection refused", ollama.ErrUnreachable)
			fake(m).answer(Comparison{}, unreachable)
			src.set("0.35.0", digestB, nil)

			m.Check(context.Background())
			assert.Equal(t, clean, m.Report(), "no report change")
			assert.Empty(t, log.at(slog.LevelWarn))
			attrs := log.attrs(t, slog.LevelInfo, "embedding drift check: couldn't verify")
			assert.Equal(t, clk.now().Add(15*time.Minute), attrs["retry_at"])

			for n, wait := range []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour,
				4 * time.Hour, 4 * time.Hour} {
				calls := fake(m).callCount()
				clk.advance(wait - time.Minute)
				m.Check(context.Background())
				require.Equal(t, calls, fake(m).callCount(), "not before %s after failure %d", wait, n+1)
				clk.advance(time.Minute)
				m.Check(context.Background())
				require.Equal(t, calls+1, fake(m).callCount(), "%s after failure %d", wait, n+1)

				failed := n + 2
				report := m.Report()
				if failed < 3 {
					assert.Equal(t, clean, report, "no report change after %d failures", failed)
					continue
				}
				require.True(t, report.Drifted(), "reported after %d failures", failed)
				assert.False(t, report.Evidence.Verified)
				assert.Equal(t, fmt.Sprintf("after %d attempts: %s", failed, unreachable), report.Evidence.Reason)
			}
			require.Len(t, log.at(slog.LevelWarn), 1, "unverified, warned about once")
			assert.True(t, strings.HasPrefix(log.at(slog.LevelWarn)[0], "embeddings may have drifted:"))

			fake(m).answer(verdict, nil)
			clk.advance(4 * time.Hour)
			m.Check(context.Background())
			if verdict == same {
				assert.False(t, m.Report().Drifted(), "recorded and cleared")
				assert.Equal(t, digestB, marker(t, home).EmbeddingModelDigest)
				assert.Len(t, log.at(slog.LevelWarn), 1)
				log.attrs(t, slog.LevelInfo, "the embedding build changed, and a re-embedded sample matches")
				return
			}
			assert.True(t, m.Report().Evidence.Verified)
			require.Len(t, log.at(slog.LevelWarn), 2, "the verdict is warned about")
			assert.True(t, strings.HasPrefix(log.at(slog.LevelWarn)[1], "embeddings drifted:"))
		})
	}
}

// TestCheck_SameVectorsClearAnUnverifiedDrift: a verdict of the same
// vectors clears the unverified drift it replaces even when the marker
// can't take the build; the next check retries the write alone.
func TestCheck_SameVectorsClearAnUnverifiedDrift(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	clk := withClock(m)
	fake(m).answer(Comparison{}, fmt.Errorf("re-embed 64 sampled chunks: %w", ollama.ErrUnreachable))
	src.set("0.35.0", digestA, nil)
	for _, wait := range []time.Duration{0, verifyRetry, 2 * verifyRetry} {
		clk.advance(wait)
		m.Check(context.Background())
	}
	require.True(t, m.Report().Drifted(), "unverified after three failures")
	require.False(t, m.Report().Evidence.Verified)

	fake(m).answer(same, nil)
	blocker := home.MarkerPath() + ".tmp"
	require.NoError(t, os.MkdirAll(filepath.Join(blocker, "keep"), 0o700))
	clk.advance(4 * verifyRetry)
	m.Check(context.Background())
	assert.False(t, m.Report().Drifted(), "the verdict clears the report")
	assert.Len(t, log.at(slog.LevelError), 1)
	assert.Equal(t, "0.34.4", marker(t, home).OllamaVersion)
	calls := fake(m).callCount()

	require.NoError(t, os.RemoveAll(blocker))
	m.Check(context.Background())
	assert.Equal(t, "0.35.0", marker(t, home).OllamaVersion)
	assert.Equal(t, calls, fake(m).callCount(), "only the write was retried")
	assert.False(t, m.Report().Drifted())
}

// TestCheck_VerificationsAreCapped: a build that changes during every
// verification outdates each one, and each asks for the next at once; at
// most 4 start in an hour, the cap is warned about once, and the next
// starts when the hour has passed.
func TestCheck_VerificationsAreCapped(t *testing.T) {
	m, _, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	clk := withClock(m)
	versions := []string{"0.35.0", "0.36.0"}
	flips := 0
	src.set(versions[0], digestA, nil)
	fake(m).during = func() {
		flips++
		src.set(versions[flips%2], digestA, nil)
	}

	for range 10 {
		m.Check(context.Background())
	}
	assert.Equal(t, 4, fake(m).callCount(), "capped")
	assert.Len(t, log.at(slog.LevelInfo), 4, "each start is logged")
	assert.False(t, m.Report().Drifted())
	require.Len(t, log.at(slog.LevelWarn), 1)
	assert.True(t, strings.HasPrefix(log.at(slog.LevelWarn)[0], "embedding drift check: the build keeps changing"))

	clk.advance(time.Hour - time.Second)
	m.Check(context.Background())
	assert.Equal(t, 4, fake(m).callCount(), "within the hour")
	clk.advance(time.Second)
	m.Check(context.Background())
	assert.Equal(t, 5, fake(m).callCount(), "the hour has passed")
	assert.Len(t, log.at(slog.LevelWarn), 1)
}

// TestCheck_UnreadableFingerprintAfterTheSampleIsAFailedAttempt: a
// verification whose build can't be read again after the sample can't
// tell which build it verified.
func TestCheck_UnreadableFingerprintAfterTheSampleIsAFailedAttempt(t *testing.T) {
	m, _, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	clk := withClock(m)
	src.set("0.34.4", digestB, nil)
	fake(m).during = func() { src.set("", "", ollama.ErrUnreachable) }

	m.Check(context.Background())
	fake(m).during = nil
	src.set("0.34.4", digestB, nil)
	assert.False(t, m.Report().Drifted())
	assert.Equal(t, 1, m.change.failed)
	logged, ok := log.attrs(t, slog.LevelInfo, "embedding drift check: couldn't verify")["err"].(error)
	require.True(t, ok)
	assert.ErrorIs(t, logged, ollama.ErrUnreachable)
	assert.ErrorContains(t, logged, "read the fingerprint after the sample")

	m.Check(context.Background())
	assert.Equal(t, 1, fake(m).callCount(), "retried after the backoff")
	clk.advance(verifyRetry)
	m.Check(context.Background())
	assert.Equal(t, 2, fake(m).callCount())
	assert.True(t, m.Report().Drifted())
}

// running starts m.Check on a verifier that blocks until release is
// called, and returns once the verification has started, with a channel
// closed when the check returns.
func running(t *testing.T, m *Monitor) (release func(), checked <-chan struct{}) {
	t.Helper()
	v := fake(m)
	v.started, v.release = make(chan struct{}, 1), make(chan struct{})
	release = sync.OnceFunc(func() { close(v.release) })
	t.Cleanup(release)
	done := make(chan struct{})
	go func() {
		m.Check(context.Background())
		close(done)
	}()
	select {
	case <-v.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the verification did not start")
	}
	return release, done
}

func waitFor(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

// TestCheck_PendingVerificationLeavesTheReport: while a change is being
// verified, Report answers at once with the last report.
func TestCheck_PendingVerificationLeavesTheReport(t *testing.T) {
	m, _, src, _ := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	src.set("0.34.4", digestB, nil)
	m.Check(context.Background())
	drifted := m.Report()
	require.True(t, drifted.Drifted())
	src.set("0.35.0", digestB, nil)

	release, done := running(t, m)
	assert.Equal(t, drifted, m.Report(), "the last report stands")

	release()
	waitFor(t, done, "the check did not return")
	assert.Equal(t, "0.35.0", m.Report().Changes[1].Current, "the verdict replaces it")
}

// TestCheck_OneVerificationAtATime: checks that find a verification
// running start none and leave the report alone.
func TestCheck_OneVerificationAtATime(t *testing.T) {
	m, _, src, _ := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	src.set("0.34.4", digestB, nil)
	before := m.Report()
	release, done := running(t, m)

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { m.Check(context.Background()) })
	}
	checked := make(chan struct{})
	go func() {
		wg.Wait()
		close(checked)
	}()
	waitFor(t, checked, "a check waited on the running verification")
	assert.Equal(t, before, m.Report())
	release()
	waitFor(t, done, "the check did not return")

	assert.Equal(t, 1, fake(m).callCount())
	assert.Equal(t, 1, fake(m).maxRunning)
	assert.True(t, m.Report().Drifted())
}

// TestCheck_RebaselineDuringAVerificationWins: a verification that a
// rebaseline overtook is discarded, and the check it asks for records the
// build serving now.
func TestCheck_RebaselineDuringAVerificationWins(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	src.set("0.34.4", digestB, nil)
	fake(m).during = func() { assert.NoError(t, m.Rebaseline()) }

	m.Check(context.Background())

	assert.Empty(t, marker(t, home).EmbeddingModelDigest, "the rebaseline stands")
	assert.False(t, m.Report().Drifted())
	assert.Empty(t, log.at(slog.LevelWarn))
	assert.Len(t, m.wake, 1, "a check is asked for")

	fake(m).during = nil
	m.Check(context.Background())
	assert.Equal(t, digestB, marker(t, home).EmbeddingModelDigest)
	assert.Equal(t, 1, fake(m).callCount(), "recorded without a verification")
}

// TestCheck_RecordDuringAVerificationWins: a verification that a record
// overtook is discarded, even when the change it verified is back: it
// compared vectors the record may have made stale.
func TestCheck_RecordDuringAVerificationWins(t *testing.T) {
	m, home, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	src.set("0.34.4", digestB, nil)
	fake(m).during = func() {
		assert.NoError(t, m.Rebaseline())
		src.set("0.34.4", digestA, nil)
		m.Check(context.Background()) // records A
		src.set("0.34.4", digestB, nil)
		m.Check(context.Background()) // A to B again, verification running
	}

	m.Check(context.Background())

	assert.Equal(t, digestA, marker(t, home).EmbeddingModelDigest)
	assert.False(t, m.Report().Drifted(), "discarded")
	assert.Empty(t, log.at(slog.LevelWarn))
	assert.Len(t, m.wake, 1, "a check is asked for")
	fake(m).during = nil
	m.Check(context.Background())
	assert.Equal(t, 2, fake(m).callCount(), "verified again")
	assert.True(t, m.Report().Drifted())
}

// TestCheck_SecondChangeRestartsTheVerification: a build that changes
// again while its verification runs is verified anew; the one in between
// is never recorded.
func TestCheck_SecondChangeRestartsTheVerification(t *testing.T) {
	m, home, src, _ := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	fake(m).answer(same, nil)
	src.set("0.35.0", digestA, nil)
	fake(m).during = func() { src.set("0.36.0", digestA, nil) }

	m.Check(context.Background())
	assert.Equal(t, "0.34.4", marker(t, home).OllamaVersion, "0.35.0 is never recorded")
	assert.Len(t, m.wake, 1, "a check is asked for")

	fake(m).during = nil
	m.Check(context.Background())
	assert.Equal(t, 2, fake(m).callCount(), "verified again")
	assert.Equal(t, "0.36.0", marker(t, home).OllamaVersion)
}

// TestRun_StopsDuringAVerification: shutdown cuts a verification short;
// Run returns, and the cut is no failed attempt and nothing to log above
// DEBUG: the only line above it is the verification's start.
func TestRun_StopsDuringAVerification(t *testing.T) {
	m, _, src, log := newMonitor(t, Fingerprint{ModelDigest: digestA, OllamaVersion: "0.34.4"})
	src.set("0.34.4", digestB, nil)
	v := fake(m)
	v.started, v.release = make(chan struct{}, 1), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()
	waitFor(t, v.started, "the verification did not start")

	cancel()
	waitFor(t, done, "Run did not return when its context ended")
	assert.Zero(t, m.change.failed)
	assert.False(t, m.verifying)
	require.Len(t, log.at(slog.LevelInfo), 1)
	assert.True(t, strings.HasPrefix(log.at(slog.LevelInfo)[0], "embedding drift check: the build changed;"))
	for _, level := range []slog.Level{slog.LevelWarn, slog.LevelError} {
		assert.Empty(t, log.at(level), "%s", level)
	}
}

func TestEvidence_Detail(t *testing.T) {
	for want, ev := range map[string]Evidence{
		"64 of 64 sampled chunks changed (worst cosine 0.9713)": {Verified: true, Comparison: differs},
		"1 of 64 sampled chunks changed (worst cosine 0.9998)": {Verified: true,
			Comparison: Comparison{Sampled: 64, Changed: 1, MinCosine: 0.99989999}},
		"1 of 3 sampled chunks changed (worst cosine 0.9999)": {Verified: true,
			Comparison: Comparison{Sampled: 3, Changed: 1, MinCosine: 0.9999}},
		"2 of 2 sampled chunks changed (worst cosine -0.5000)": {Verified: true,
			Comparison: Comparison{Sampled: 2, Changed: 2, MinCosine: -0.5}},
		"no indexed chunks to sample":                        {Verified: true, Comparison: Comparison{MinCosine: 1}},
		"not verified: after 3 attempts: ollama unreachable": {Reason: "after 3 attempts: ollama unreachable"},
	} {
		assert.Equal(t, want, ev.Detail())
	}
}
