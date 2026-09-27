// Package drift notices when what makes a home's embeddings changes under
// it: the embedding model's manifest digest, or the Ollama version. Either
// can change the vectors the model returns for the same text, and then the
// query vectors no longer match the stored ones: search quality drops and
// nothing fails. Ollama 0.30.0 did this to nomic-embed-text, which began
// lowercasing its input.
//
// A Monitor records the fingerprint in the home's marker at its first
// successful check, compares every later check with it, and reports a
// drift until `curio reindex --all` re-embeds the library and resets the
// baseline (Rebaseline). It never reindexes by itself: a reindex of a large
// library takes hours of the user's machine.
package drift

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/samsar/curio/internal/curiohome"
)

// What a Change names.
const (
	ModelDigest   = "model_digest"
	OllamaVersion = "ollama_version"
)

// Fix is what the user runs to re-embed the library under the new
// fingerprint, which also resets the baseline.
const Fix = "curio reindex --all"

const (
	// checkInterval is how often a running daemon checks. Two small local
	// requests a minute catch an Ollama upgrade while the daemon runs.
	checkInterval = time.Minute
	// checkTimeout bounds one check, whatever embedding.timeout_seconds
	// allows an embed request.
	checkTimeout = 5 * time.Second
)

// Fingerprint identifies the build that makes a home's embeddings.
type Fingerprint struct {
	ModelDigest   string
	OllamaVersion string
}

// Change is a part of the fingerprint that differs from the recorded one.
type Change struct {
	What     string // ModelDigest or OllamaVersion
	Recorded string
	Current  string
}

// Report is what the last successful check found: the changes, none while
// the embeddings haven't drifted, and when it ran (zero before the first).
type Report struct {
	Changes   []Change
	CheckedAt time.Time
}

// Drifted reports whether the check found a change.
func (r Report) Drifted() bool { return len(r.Changes) > 0 }

// Source reads the current fingerprint; *ollama.Client is one.
type Source interface {
	Version(ctx context.Context) (string, error)
	ModelDigest(ctx context.Context) (string, error)
}

// Monitor checks a home's embedding fingerprint against Source. After the
// daemon's startup it is the marker's only writer, under its lock.
type Monitor struct {
	home     *curiohome.Home
	src      Source
	log      *slog.Logger
	interval time.Duration
	timeout  time.Duration
	now      func() time.Time
	wake     chan struct{} // capacity 1: a pending check absorbs further asks

	mu     sync.Mutex
	report Report
	warned []Change // the drift last warned about, so each is warned about once
}

// New returns a Monitor for home's embeddings, made by src's model.
func New(home *curiohome.Home, src Source, log *slog.Logger) *Monitor {
	return &Monitor{
		home:     home,
		src:      src,
		log:      log,
		interval: checkInterval,
		timeout:  checkTimeout,
		now:      time.Now,
		wake:     make(chan struct{}, 1),
	}
}

// Run checks at once, then every minute and whenever Rebaseline asks,
// until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		m.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.wake:
		}
	}
}

// Check reads the current fingerprint and holds it to the marker's. With
// none recorded yet, it records this one. When Ollama doesn't answer or
// the model isn't pulled there is nothing to compare: the last report
// stands and nothing is written.
func (m *Monitor) Check(ctx context.Context) {
	current, err := m.current(ctx)
	if err != nil {
		// Healthz and the model pull already report both, loudly.
		m.log.Debug("embedding drift check skipped", "err", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	meta, err := m.home.Meta()
	if err != nil {
		m.log.Error("embedding drift check: read the marker; retrying at the next check", "err", err)
		return
	}
	recorded := Fingerprint{ModelDigest: meta.EmbeddingModelDigest, OllamaVersion: meta.OllamaVersion}
	if recorded == (Fingerprint{}) {
		m.record(meta, current)
		return
	}
	m.report = Report{Changes: compare(recorded, current), CheckedAt: m.now().UTC()}
	m.warnOnce()
}

// current reads the fingerprint from Source within the check's timeout.
func (m *Monitor) current(ctx context.Context) (Fingerprint, error) {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	version, err := m.src.Version(ctx)
	if err != nil {
		return Fingerprint{}, fmt.Errorf("ollama version: %w", err)
	}
	digest, err := m.src.ModelDigest(ctx)
	if err != nil {
		return Fingerprint{}, fmt.Errorf("embedding model digest: %w", err)
	}
	return Fingerprint{ModelDigest: digest, OllamaVersion: version}, nil
}

// record makes fp the home's baseline, keeping every other marker field.
// The caller holds m.mu.
func (m *Monitor) record(meta curiohome.Meta, fp Fingerprint) {
	meta.EmbeddingModelDigest, meta.OllamaVersion = fp.ModelDigest, fp.OllamaVersion
	if err := m.home.WriteMeta(meta); err != nil {
		m.log.Error("record the embedding fingerprint in the marker; retrying at the next check", "err", err)
		return
	}
	m.log.Info("recorded the embedding fingerprint", "model_digest", fp.ModelDigest, "ollama_version", fp.OllamaVersion)
	m.report, m.warned = Report{CheckedAt: m.now().UTC()}, nil
}

// warnOnce logs the report's drift at WARN the first time the check finds
// it, not at every check. The caller holds m.mu.
func (m *Monitor) warnOnce() {
	if !m.report.Drifted() {
		m.warned = nil
		return
	}
	if slices.Equal(m.report.Changes, m.warned) {
		return
	}
	m.warned = m.report.Changes
	args := []any{"fix", Fix}
	for _, c := range m.report.Changes {
		args = append(args, c.What, c.Recorded+" -> "+c.Current)
	}
	m.log.Warn("embeddings may have drifted: what made the library's vectors has changed, "+
		"so searches compare vectors from two builds; run `"+Fix+"`", args...)
}

// compare lists the parts of current that differ from recorded.
func compare(recorded, current Fingerprint) []Change {
	var changes []Change
	if recorded.ModelDigest != current.ModelDigest {
		changes = append(changes, Change{What: ModelDigest, Recorded: recorded.ModelDigest, Current: current.ModelDigest})
	}
	if recorded.OllamaVersion != current.OllamaVersion {
		changes = append(changes, Change{What: OllamaVersion, Recorded: recorded.OllamaVersion, Current: current.OllamaVersion})
	}
	return changes
}

// Report is the last successful check's finding.
func (m *Monitor) Report() Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.report
	r.Changes = slices.Clone(r.Changes)
	return r
}

// Rebaseline clears the recorded fingerprint and asks for a check at once,
// so the Ollama and model serving now become the baseline. The API calls
// it once `curio reindex --all` has enqueued the re-embedding of every
// searchable document.
func (m *Monitor) Rebaseline() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	meta, err := m.home.Meta()
	if err != nil {
		return fmt.Errorf("reset the embedding baseline: %w", err)
	}
	meta.EmbeddingModelDigest, meta.OllamaVersion = "", ""
	if err := m.home.WriteMeta(meta); err != nil {
		return fmt.Errorf("reset the embedding baseline: %w", err)
	}
	m.report, m.warned = Report{}, nil
	m.log.Info("embedding baseline reset; the next check records the current one")
	m.checkSoon()
	return nil
}

// checkSoon asks Run for a check now, without waiting for it.
func (m *Monitor) checkSoon() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
