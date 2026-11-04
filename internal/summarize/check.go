package summarize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/store"
)

// probeTimeout bounds each page of the fitness check.
const probeTimeout = time.Minute

// probe is one made-up page of the fitness check, and what a fit model's
// raw answer to it holds.
type probe struct {
	name string
	page Input
	fits func(answer, Input) bool
}

// probes are the fitness check: an article naming made-up people and a
// made-up product, a sign-in wall, and a made-up person's profile. A
// model that names what a page doesn't, can't tell a wall from a page, or
// can't tell whom a profile is of would write cards that mislead more
// searches than they help. Every page is made up, and names nothing real.
var probes = []probe{
	{
		name: "an article",
		page: Input{Title: "Tidewater Robotics ships the Kestrel K2 pipe crawler",
			URL: "https://news.example/tidewater-kestrel-k2", Site: "Example Harbor News", ContentType: "article",
			Text: "By Marisol Fenwick-Oduya\n\nTidewater Robotics said on Tuesday that its Kestrel K2 crawler, which " +
				"maps corrosion inside water mains, is now shipping to three utilities.\n\nChief engineer Ignatius " +
				"Varga-Lindqvist said the crawler fits pipes as narrow as 15 centimetres and sends its readings " +
				"over a tether as it goes.\n\nThe company expects the Kestrel K2 to halve the time a utility " +
				"spends finding leaks."},
		fits: func(a answer, in Input) bool {
			named, src := false, source(in)
			for _, e := range a.Entities {
				if !grounded(src, e) {
					return false
				}
				named = named || strings.Contains(fold(e), "kestrel")
			}
			return a.Kind == store.SummaryKindArticle && named
		},
	},
	{
		name: "a sign-in wall",
		page: Input{Title: "Sign in", URL: "https://app.example/login?next=%2Freports%2F42", Site: "app.example",
			ContentType: "article", Text: "Sign in to continue\n\nEmail address\n\nPassword\n\nKeep me signed in\n\n" +
				"Forgot your password?\n\nNew here? Create an account"},
		fits: func(a answer, _ Input) bool { return a.Kind == store.SummaryKindNotThePage },
	},
	{
		name: "a profile",
		page: Input{Title: "Oriane Castellbo-Ferrant — About", URL: "https://castellbo.example/about",
			Site: "castellbo.example", ContentType: "article",
			Text: "Oriane Castellbo-Ferrant\n\nI am a glaciologist at the Nordvik Polar Institute, where I lead the " +
				"ice-core archive.\n\nBefore Nordvik I spent six winters drilling on the Halvard Plateau.\n\n" +
				"Write to me about core sampling, cold-lab safety or the archive's open data."},
		fits: func(a answer, _ Input) bool {
			named := strings.Contains(fold(a.Author), "castellbo") ||
				slices.ContainsFunc(a.Entities, func(e string) bool { return strings.Contains(fold(e), "castellbo") })
			return a.Kind == store.SummaryKindProfile && named
		},
	},
}

// ensureChecked runs the fitness check unless its verdict is in: nil for
// a fit model, ErrNotWriting for one found unfit (see check), and
// ErrNotChecked, or the context's error, when no verdict was reached. An
// earlier check that reached no verdict for a reason of the model's holds
// the next until its wait is over: a *WaitError, asking nothing.
func (w *Writer) ensureChecked(ctx context.Context) error {
	if w.measure {
		return nil
	}
	if !w.unchecked() {
		return w.refused()
	}
	if err := w.waiting(); err != nil {
		return err
	}
	r := w.check(ctx)
	switch {
	case r.unreached != nil:
		return r.unreached
	case r.failed != "":
		w.unload(ctx)
		return w.refused()
	}
	return nil
}

// Check runs the fitness check now, on ctx, and keeps its verdict as a
// write's does: it returns the probe the model failed, "" when it passed,
// or an error when a probe couldn't reach a verdict, which leaves the
// verdict unknown. A writer that writes nothing for another reason is an
// error wrapping ErrNotWriting. It frees no memory: it is for measuring a
// model, which goes on to ask it.
func (w *Writer) Check(ctx context.Context) (string, error) {
	switch {
	case w.gen == nil:
		return "", fmt.Errorf("%w: %s", ErrNotWriting, w.offReason)
	case w.withheld != "":
		return "", fmt.Errorf("%w: %s", ErrNotWriting, w.withheld)
	}
	r := w.check(ctx)
	return r.failed, r.unreached
}

// checkResult is what a fitness check found: the probe the model failed,
// or why no verdict was reached.
type checkResult struct {
	failed    string
	unreached error
}

// check runs the fitness check: each probe through the prompt, the schema
// and the strict decoding, its raw answer compared before any guard. A
// model that answers a probe wrong, or whose provider refuses the request
// itself, is unfit for the life of the process. One that couldn't be
// reached leaves the verdict unknown and starts an outage, the probe
// failing with ErrNotChecked; one that the provider couldn't serve, or
// that answered too slowly, leaves it unknown too, and holds the next
// check back for a wait that grows while they keep failing
// (checkFailed), the probe failing with a *WaitError. It records and logs
// the verdict.
func (w *Writer) check(ctx context.Context) checkResult {
	for _, pr := range probes {
		a, err := w.probe(ctx, pr.page)
		if cerr := ctx.Err(); cerr != nil {
			return checkResult{unreached: fmt.Errorf("summarize: %w", cerr)}
		}
		switch {
		case err != nil && !unfitBy(err):
			// A timeout while the provider answers a listing is the model
			// being slow, not an outage.
			slow := timedOut(err) && w.answers(ctx)
			if noAnswer(err) && !slow {
				err = w.unanswered("", err)
				return checkResult{unreached: fmt.Errorf("%w: probe %q: %w", ErrNotChecked, pr.name, err)}
			}
			err = fmt.Errorf("probe %q: %w", pr.name, err)
			until := w.checkFailed(err)
			return checkResult{unreached: &WaitError{Until: until, Probed: true,
				Err: fmt.Errorf("%w: %w", ErrNotChecked, err)}}
		case err != nil || !pr.fits(a, pr.page):
			reason := fmt.Sprintf("%s didn't pass the check %q", w.model, pr.name)
			if err != nil {
				reason = fmt.Sprintf("%s didn't pass the check %q: %v", w.model, pr.name, err)
			}
			reason += "; " + UnfitFix
			w.mu.Lock()
			w.verdict, w.unfitReason, w.checkedAt = verdictUnfit, reason, w.now()
			w.checkErr, w.checkRetryAt, w.checkWait = nil, time.Time{}, 0
			w.recordLocked("", "unfit", errors.New(reason))
			w.mu.Unlock()
			w.log.Warn("document summaries: the summaries' model failed its check; no summary is written ("+
				UnfitFix+")", "model", w.model, "probe", pr.name)
			return checkResult{failed: pr.name}
		}
	}
	w.mu.Lock()
	w.verdict, w.checkedAt = verdictFit, w.now()
	w.checkErr, w.checkRetryAt, w.checkWait = nil, time.Time{}, 0
	w.mu.Unlock()
	w.log.Info("document summaries: the summaries' model passed its check", "model", w.model)
	return checkResult{}
}

// checkFailed records err, a fitness check that reached no verdict for a
// reason of the model's, or a load it needs that failed so, and returns
// when the check may run again:
// checkRetryFirst after the first of an episode, which ends with a
// verdict, then twice the last wait, at most checkRetryLast. The first of
// an episode is one WARN, the rest DEBUG. The time is wall time, as every
// end the writer sets is.
func (w *Writer) checkFailed(err error) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	first := w.checkErr == nil
	if first {
		w.checkWait = checkRetryFirst
	} else {
		w.checkWait = min(2*w.checkWait, checkRetryLast)
	}
	w.checkErr, w.checkRetryAt = err, w.now().Add(w.checkWait).Round(0)
	w.recordLocked("", "check", err)
	level := slog.LevelDebug
	if first {
		level = slog.LevelWarn
	}
	w.log.Log(context.Background(), level, "document summaries: couldn't check the summaries' model; "+
		"the pass waits to check it again", "model", w.model, "err", err, "retry_in", w.checkWait.String(),
		"retry_at", w.checkRetryAt)
	return w.checkRetryAt
}

// probe asks the model for a probe page's summary, decoding its answer
// strictly. An answer of any kind ends an outage.
func (w *Writer) probe(ctx context.Context, page Input) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	raw, err := w.gen.GenerateJSON(ctx, renderPrompt(page), schema(), w.callOptions(nil))
	if err != nil {
		return answer{}, err
	}
	w.answered()
	w.kept()
	return decodeAnswer(raw)
}

// unfitBy reports whether a probe's failure says the model is unfit: an
// answer it gave that can't be used, or a request its provider refuses as
// made, which no retry changes. Anything else, no answer, a missing model,
// a refused caller, a provider that can't serve now or a failure of no
// known kind, says nothing of the model.
func unfitBy(err error) bool {
	if _, ok := errors.AsType[*InvalidAnswer](err); ok {
		return true
	}
	return errors.Is(err, generator.ErrNotJSON) || errors.Is(err, generator.ErrTruncated) ||
		errors.Is(err, generator.ErrBadRequest)
}

// unload frees an unfit model's memory, unless another role writes with
// it: an unfit model would otherwise sit loaded for its keep-alive, beside
// the embedder, for nothing. It runs on ctx's values, not its end, and is
// bounded by unloadBound: a write that found the model unfit frees it even
// when its caller has left.
func (w *Writer) unload(ctx context.Context) {
	if w.shared || w.warmer == nil {
		return
	}
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unloadBound)
	defer cancel()
	if err := w.warmer.Unload(uctx); err != nil {
		w.log.Warn("document summaries: couldn't unload the unfit summaries' model", "model", w.model, "err", err)
		return
	}
	w.log.Info("document summaries: unloaded the unfit summaries' model", "model", w.model)
}
