// Package drift notices when what makes a home's embeddings changes under
// it and the vectors change with it. The build is fingerprinted by the
// embedding model's manifest digest and the Ollama version. Either can
// change the vectors the model returns for the same text, and then the
// query vectors no longer match the stored ones: search quality drops and
// nothing fails. Ollama 0.30.0 did this to nomic-embed-text, which began
// lowercasing its input. Many upgrades change nothing, though: under
// Ollama 0.35.0, the chunks sampled from a library indexed under 0.34.4
// came back bit for bit.
//
// A Monitor records the fingerprint in the home's marker at its first
// successful check and compares every later check with it. A changed
// fingerprint is verified before it is reported: a Verifier re-embeds a
// sample of the library through the indexer's own path and compares each
// vector with its stored one. A sample that matches makes the new build the
// recorded one, with no warning. One that doesn't, or that the new build
// can't embed at all, is reported as a drift, with that evidence, until
// `curio reindex --all` re-embeds the library and resets the baseline
// (Rebaseline). The Monitor never reindexes by itself: a reindex of a large
// library takes hours of the user's machine.
package drift

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/ollama"
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
	// checkTimeout bounds one fingerprint read, whatever
	// embedding.timeout_seconds allows an embed request.
	checkTimeout = 5 * time.Second

	// verifyRetry is how long a verification that failed waits before its
	// next attempt, doubling with each failure in a row up to
	// maxVerifyRetry, so a machine that can't finish the sample within its
	// bound doesn't spend that bound every 15 minutes for good.
	verifyRetry    = 15 * time.Minute
	maxVerifyRetry = 4 * time.Hour
	// verifyAttempts is how many attempts in a row may fail before the
	// change is reported unverified: about 45 minutes after the first, so
	// a flaky Ollama can hide a drift for an hour at most.
	verifyAttempts = 3
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

// Evidence is what a reported drift rests on: a sample re-embedded by the
// build serving now that doesn't match the stored vectors (Verified), or
// why no sample could show whether it does.
type Evidence struct {
	Verified bool
	Comparison
	Reason string    // why it isn't verified
	At     time.Time // when the sample was compared, or given up on
}

// Detail words the evidence for the user. Every surface that shows a
// drift prints it as the daemon words it.
func (e Evidence) Detail() string {
	if !e.Verified {
		return "not verified: " + e.Reason
	}
	if e.Sampled == 0 {
		return "no indexed chunks to sample"
	}
	return fmt.Sprintf("%d of %d sampled chunks changed (worst cosine %.4f)", e.Changed, e.Sampled, floor4(e.MinCosine))
}

// floor4 cuts c to four decimals toward minus infinity, so a cosine under
// MinCosine never prints as MinCosine. The epsilon absorbs the float's
// representation of a cosine that has four decimals.
func floor4(c float64) float64 { return math.Floor(c*1e4+1e-9) / 1e4 }

// Report is what the last conclusive check found: the changes and the
// evidence they were reported on, none while the embeddings haven't
// drifted, and when it ran (zero before the first).
type Report struct {
	Changes   []Change
	Evidence  Evidence // set whenever Changes is
	CheckedAt time.Time
}

// Drifted reports whether the check found a drift.
func (r Report) Drifted() bool { return len(r.Changes) > 0 }

// Source reads the current fingerprint; *ollama.Client is one.
type Source interface {
	Version(ctx context.Context) (string, error)
	ModelDigest(ctx context.Context) (string, error)
}

// Monitor checks a home's embedding fingerprint against Source, and a
// changed one against the library through Verifier. After the daemon's
// startup it is the marker's only writer, under its lock.
type Monitor struct {
	home     *curiohome.Home
	src      Source
	verifier Verifier
	log      *slog.Logger
	interval time.Duration
	timeout  time.Duration
	now      func() time.Time
	wake     chan struct{} // capacity 1: a pending check absorbs further asks

	mu               sync.Mutex
	report           Report
	warned           []Change // the drift last warned about, so each is warned about once
	warnedVerified   bool     // whether that warning was about a verified drift
	warnedUnreadable bool     // Ollama's answers can't be read, and that was warned about
	change           verification
	verifying        bool   // a verification is running, with m.mu released
	epoch            uint64 // bumped by every write of the fingerprint to the marker
}

// pair is a change of build: the recorded fingerprint and the current one.
type pair struct {
	recorded, current Fingerprint
}

// verification is where the check of the monitor's change stands.
type verification struct {
	pair pair
	// evidence is what is reported for the change: its verdict once final,
	// or, after verifyAttempts failures in a row, why there is none yet.
	evidence *Evidence
	final    bool // evidence is the verdict: no more attempts
	same     bool // the verdict found the vectors unchanged; recording the build is owed
	failed   int  // attempts in a row that failed
	retryAt  time.Time
}

// attempt is a verification Check runs with m.mu released: the change it
// verifies, the marker it was read from and the epoch it started in.
type attempt struct {
	pair  pair
	meta  curiohome.Meta
	epoch uint64
}

// New returns a Monitor for home's embeddings, made by src's model, whose
// changes verifier verifies.
func New(home *curiohome.Home, src Source, verifier Verifier, log *slog.Logger) *Monitor {
	return &Monitor{
		home:     home,
		src:      src,
		verifier: verifier,
		log:      log,
		interval: checkInterval,
		timeout:  checkTimeout,
		now:      time.Now,
		wake:     make(chan struct{}, 1),
	}
}

// Run checks at once, then every minute and whenever Rebaseline or an
// outdated verification asks, until ctx ends. A verification runs within
// a check, on ctx: it is one bounded burst of embeds per change of build,
// not a job, so the queue's pause, schedule and throttle don't hold it.
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
// none recorded yet, it records this one. When there is no fingerprint to
// read there is nothing to compare: the last report stands and nothing is
// written.
//
// A fingerprint that differs from the recorded one is verified once per
// change, with m.mu released so Report never waits on it: the sample is
// re-embedded, then the fingerprint read again. A sample that matches
// records the new build; one that doesn't, or that the build can't embed
// (ErrUnverifiable), is the change's verdict, reported until the build
// changes again or a rebaseline. While the verdict is pending, the last
// report stands. Any other failure is retried after verifyRetry, doubling;
// from the verifyAttempts-th in a row the change is reported unverified,
// and retries go on. A verification is discarded, and a check asked for at
// once, when the build changed again or a rebaseline or a record happened
// while it ran. At most one runs at a time.
func (m *Monitor) Check(ctx context.Context) {
	current, err := m.current(ctx)
	a := m.assess(current, err)
	if a == nil {
		return
	}
	cmp, err := m.verifier.Verify(ctx)
	after, afterErr := m.current(ctx)
	m.settle(ctx, a, cmp, err, after, afterErr)
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

// assess holds the fingerprint just read to the marker's and does what
// needs no verification. It returns the verification to run, if one is
// due.
func (m *Monitor) assess(current Fingerprint, err error) *attempt {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.skipped(err)
		return nil
	}
	m.warnedUnreadable = false
	meta, err := m.home.Meta()
	if err != nil {
		m.log.Error("embedding drift check: read the marker; retrying at the next check", "err", err)
		return nil
	}
	recorded := Fingerprint{ModelDigest: meta.EmbeddingModelDigest, OllamaVersion: meta.OllamaVersion}
	switch recorded {
	case Fingerprint{}:
		if err := m.record(meta, current); err != nil {
			m.log.Error("record the embedding fingerprint in the marker; retrying at the next check", "err", err)
			return nil
		}
		m.log.Info("recorded the embedding fingerprint",
			"model_digest", current.ModelDigest, "ollama_version", current.OllamaVersion)
		return nil
	case current:
		m.change = verification{}
		m.report = Report{CheckedAt: m.now().UTC()}
		m.warnOnce()
		return nil
	}

	p := pair{recorded: recorded, current: current}
	if m.change.pair != p {
		m.change = verification{pair: p}
	}
	switch {
	case m.change.same:
		m.recordVerified(meta)
		return nil
	case m.change.evidence != nil:
		m.reportDrift(*m.change.evidence)
	}
	if m.change.final || m.verifying || m.now().Before(m.change.retryAt) {
		return nil
	}
	m.verifying = true
	return &attempt{pair: p, meta: meta, epoch: m.epoch}
}

// settle applies a's outcome: cmp and err from the verifier, after and
// afterErr from reading the fingerprint again.
func (m *Monitor) settle(ctx context.Context, a *attempt, cmp Comparison, err error, after Fingerprint, afterErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verifying = false
	switch {
	case ctx.Err() != nil:
		m.log.Debug("embedding drift verification stopped", "err", ctx.Err())
	case a.epoch != m.epoch || a.pair != m.change.pair:
		m.log.Debug("embedding drift verification outdated by a newer baseline or check; checking again")
		m.checkSoon()
	case afterErr != nil:
		m.failedAttempt(fmt.Errorf("read the fingerprint after the sample: %w", afterErr))
	case after != a.pair.current:
		m.log.Debug("the embedding build changed during its verification; checking again")
		m.checkSoon()
	case errors.Is(err, ErrUnverifiable):
		m.conclude(Evidence{Reason: err.Error(), At: m.now().UTC()})
	case err != nil:
		m.failedAttempt(err)
	case cmp.Same():
		m.change.evidence = &Evidence{Verified: true, Comparison: cmp, At: m.now().UTC()}
		m.change.final, m.change.same = true, true
		m.report = Report{CheckedAt: m.now().UTC()}
		m.warnOnce()
		m.recordVerified(a.meta)
	default:
		m.conclude(Evidence{Verified: true, Comparison: cmp, At: m.now().UTC()})
	}
}

// conclude makes ev the change's verdict, a drift. The caller holds m.mu.
func (m *Monitor) conclude(ev Evidence) {
	m.change.evidence, m.change.final = &ev, true
	m.reportDrift(ev)
}

// failedAttempt counts a verification that failed with err, a failure the
// next attempt may not repeat, and reports the change unverified once
// verifyAttempts have failed in a row. The caller holds m.mu.
func (m *Monitor) failedAttempt(err error) {
	m.change.failed++
	m.change.retryAt = m.now().Add(retryDelay(m.change.failed))
	m.log.Info("embedding drift check: couldn't verify a change of build; retrying",
		"err", err, "attempt", m.change.failed, "retry_at", m.change.retryAt.UTC())
	if m.change.failed < verifyAttempts {
		return
	}
	ev := Evidence{Reason: fmt.Sprintf("after %d attempts: %v", m.change.failed, err), At: m.now().UTC()}
	m.change.evidence = &ev
	m.reportDrift(ev)
}

// retryDelay is how long to wait after the failed-th failure in a row:
// verifyRetry, doubling with each, up to maxVerifyRetry.
func retryDelay(failed int) time.Duration {
	d := verifyRetry
	for range failed - 1 {
		d *= 2
		if d >= maxVerifyRetry {
			return maxVerifyRetry
		}
	}
	return d
}

// reportDrift reports the change as a drift on ev. The caller holds m.mu.
func (m *Monitor) reportDrift(ev Evidence) {
	p := m.change.pair
	m.report = Report{Changes: compare(p.recorded, p.current), Evidence: ev, CheckedAt: m.now().UTC()}
	m.warnOnce()
}

// recordVerified records the change's build, whose vectors its verdict
// found unchanged. A marker that can't take it is an ERROR, and the next
// check retries the write alone. The caller holds m.mu.
func (m *Monitor) recordVerified(meta curiohome.Meta) {
	p, ev := m.change.pair, *m.change.evidence
	if err := m.record(meta, p.current); err != nil {
		m.log.Error("record the verified embedding fingerprint in the marker; retrying at the next check", "err", err)
		return
	}
	args := slices.Concat(changeArgs(compare(p.recorded, p.current)), []any{
		"evidence", ev.Detail(), "sampled", ev.Sampled, "identical", ev.Identical, "worst_cosine", ev.MinCosine,
	})
	m.log.Info("the embedding build changed, and a re-embedded sample matches the stored vectors; "+
		"recorded the new fingerprint", args...)
}

// skipped logs a check that read no fingerprint. Ollama down, the model
// not pulled and a check cut short are only DEBUG: healthz and the model
// pull report the first two, loudly. An answer the check can't read (a
// model listed without a digest, a version reply without a version) is
// reported nowhere else, and drift goes unnoticed for as long as it lasts,
// so the first of a run of them is a WARN. The caller holds m.mu.
func (m *Monitor) skipped(err error) {
	if m.warnedUnreadable || !unreadable(err) {
		m.log.Debug("embedding drift check skipped", "err", err)
		return
	}
	m.warnedUnreadable = true
	m.log.Warn("embedding drift check: can't read Ollama's answer, so a drift would go unnoticed", "err", err)
}

// unreadable reports whether err is Ollama answering something a check
// can't use, rather than not answering in time or not having the model.
func unreadable(err error) bool {
	for _, quiet := range []error{ollama.ErrUnreachable, ollama.ErrModelNotLoaded, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, quiet) {
			return false
		}
	}
	return true
}

// record makes fp the home's baseline, keeping every other marker field,
// and clears the report and the verification. The caller holds m.mu.
func (m *Monitor) record(meta curiohome.Meta, fp Fingerprint) error {
	meta.EmbeddingModelDigest, meta.OllamaVersion = fp.ModelDigest, fp.OllamaVersion
	if err := m.home.WriteMeta(meta); err != nil {
		return err
	}
	m.epoch++
	m.report, m.warned, m.change = Report{CheckedAt: m.now().UTC()}, nil, verification{}
	return nil
}

// warnOnce logs the report's drift at WARN the first time a check finds
// it, not at every check: once per set of changes and whether the drift
// is verified, so a verdict that replaces an unverified drift is warned
// about too. The caller holds m.mu.
func (m *Monitor) warnOnce() {
	if !m.report.Drifted() {
		m.warned = nil
		return
	}
	ev := m.report.Evidence
	if slices.Equal(m.report.Changes, m.warned) && ev.Verified == m.warnedVerified {
		return
	}
	m.warned, m.warnedVerified = m.report.Changes, ev.Verified
	args := slices.Concat([]any{"fix", Fix}, changeArgs(m.report.Changes))
	if ev.Verified {
		m.log.Warn("embeddings drifted: what made the library's vectors has changed ("+ev.Detail()+
			"), so searches compare vectors from two builds; run `"+Fix+"`", args...)
		return
	}
	m.log.Warn("embeddings may have drifted: what made the library's vectors has changed ("+ev.Detail()+
		"); run `"+Fix+"`", args...)
}

// changeArgs are changes as log attributes: each part, recorded -> current.
func changeArgs(changes []Change) []any {
	args := make([]any, 0, 2*len(changes))
	for _, c := range changes {
		args = append(args, c.What, c.Recorded+" -> "+c.Current)
	}
	return args
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

// Report is the last conclusive check's finding.
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
// searchable document. A verification running meanwhile is discarded.
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
	m.epoch++
	m.report, m.warned, m.change = Report{}, nil, verification{}
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
