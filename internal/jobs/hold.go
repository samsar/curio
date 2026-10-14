package jobs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/store"
)

// ReasonEmbedderUnreachable is why an EmbedderHold is closed.
const ReasonEmbedderUnreachable GateReason = "embedder_unreachable"

// How often a closed EmbedderHold asks the embedding model whether it
// answers: soon after an Ollama restart, then less often through a long
// outage, so it opens at most a minute after Ollama answers again.
const (
	minProbeWait = 5 * time.Second
	maxProbeWait = time.Minute
)

// embedWaitReason is what a job deferred for the embedding model waits
// for, worded to follow "waiting for".
const embedWaitReason = "the embedding model to answer"

// EmbedderHold is a Gate that holds back the claims of the jobs that embed,
// index and cards, while the embedding model gets no answer
// (ollama.ErrUnreachable: Ollama stopped, restarting, or too wedged to
// answer within the embedder's timeout). The first such failure a handler
// reports (Unreachable) closes it, and Run then probes the model, every
// minProbeWait doubling to maxProbeWait, until an embed gets any answer,
// which opens it. The jobs it holds stay pending, their attempts and
// deferral budgets untouched: an outage costs the jobs it caught running
// one deferral each, and the log one WARN and one INFO, however many index
// jobs wait. Without it each pending job would be claimed, fail and be
// deferred in turn, two log lines apiece, through an import's backlog.
//
// A handler reports only an embed whose own context was still live: one a
// shutdown cancelled got no answer for that reason alone.
type EmbedderHold struct {
	probe func(context.Context) error
	log   *slog.Logger
	// minWait and maxWait are minProbeWait and maxProbeWait, which tests
	// shorten.
	minWait, maxWait time.Duration

	mu    sync.Mutex // serializes closing and opening, and guards lastErr
	state atomic.Pointer[holdState]
	// lastErr is why the hold is closed: the failure that closed it, then
	// each probe's that got no answer.
	lastErr string
	// closing tells Run the hold closed; buffered, so Unreachable never
	// blocks.
	closing chan struct{}
}

// HoldState is whether an EmbedderHold holds the jobs that embed, since
// when and why: the last embed's error that got no answer.
type HoldState struct {
	Held  bool
	Since time.Time
	Err   string
}

// holdState is one closing of the hold, or none.
type holdState struct {
	since  time.Time
	opened chan struct{} // closed when this closing ends
}

// NewEmbedderHold returns an open hold whose Run probes the embedding
// model with probe, an embed of a short text bounded by the embedder's own
// timeout. A nil log logs nothing.
func NewEmbedderHold(probe func(context.Context) error, log *slog.Logger) *EmbedderHold {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &EmbedderHold{probe: probe, log: log, minWait: minProbeWait, maxWait: maxProbeWait,
		closing: make(chan struct{}, 1)}
}

// embeds reports whether jobs of kind embed, which the hold holds back.
func embeds(kind store.JobKind) bool {
	return kind == store.JobKindIndex || kind == store.JobKindCards
}

// Admit implements Gate: closed to the kinds that embed while the hold is.
func (h *EmbedderHold) Admit(kind store.JobKind, _ int, _ time.Time) Verdict {
	st := h.state.Load()
	if st == nil || !embeds(kind) {
		return Verdict{}
	}
	return Verdict{Closed: ReasonEmbedderUnreachable, Changed: st.opened}
}

// Unreachable reports an embed that got no answer, err, which closes the
// hold unless it is closed already. A nil hold holds nothing.
func (h *EmbedderHold) Unreachable(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state.Load() != nil {
		return
	}
	h.state.Store(&holdState{since: time.Now(), opened: make(chan struct{})})
	h.lastErr = err.Error()
	h.log.Warn("embedding model not answering: index and cards jobs wait until it does", "err", err)
	select {
	case h.closing <- struct{}{}:
	default:
	}
}

// Held reports whether the hold is closed.
func (h *EmbedderHold) Held() bool { return h != nil && h.state.Load() != nil }

// State reports whether the hold is closed, since when and why. A nil hold
// holds nothing.
func (h *EmbedderHold) State() HoldState {
	if h == nil {
		return HoldState{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.state.Load()
	if st == nil {
		return HoldState{}
	}
	return HoldState{Held: true, Since: st.since, Err: h.lastErr}
}

// Answers probes the embedding model once, as Run does, and reports
// whether it got an answer of any kind: a vector, or an error other than
// ollama.ErrUnreachable. It tells an embed that timed out because the
// model answers nothing from one the model is too slow for. A nil hold
// has no probe, and answers false.
func (h *EmbedderHold) Answers(ctx context.Context) bool {
	if h == nil {
		return false
	}
	err := h.probe(ctx)
	return err == nil || !errors.Is(err, ollama.ErrUnreachable)
}

// Run probes the embedding model while the hold is closed, until ctx ends.
func (h *EmbedderHold) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.closing:
		}
		h.probeUntilAnswered(ctx)
	}
}

// probeUntilAnswered probes the model, waiting longer after each probe
// that gets no answer, and opens the hold at the first that gets one: a
// vector, or any error but ollama.ErrUnreachable, which the jobs it lets
// through meet and report their own way.
func (h *EmbedderHold) probeUntilAnswered(ctx context.Context) {
	wait := h.minWait
	for {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		err := h.probe(ctx)
		if err == nil || !errors.Is(err, ollama.ErrUnreachable) {
			h.open()
			return
		}
		if ctx.Err() != nil {
			return
		}
		h.mu.Lock()
		h.lastErr = err.Error()
		h.mu.Unlock()
		wait = min(2*wait, h.maxWait)
	}
}

// open ends the hold's closing, waking the workers it held.
func (h *EmbedderHold) open() {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.state.Load()
	if st == nil {
		return
	}
	h.state.Store(nil)
	h.lastErr = ""
	h.log.Info("embedding model answering again: index and cards jobs resume",
		"held_ms", time.Since(st.since).Milliseconds())
	close(st.opened)
}
