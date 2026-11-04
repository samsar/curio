package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/summarize"
)

// summarizePoolSize is the summarize pool's one goroutine: one call at a
// time, so the pass never takes a fetch or index slot, and never runs two
// pages beside the person's searches.
const summarizePoolSize = 1

// SummaryWriter is what the summarize jobs write with; *summarize.Writer
// is one.
type SummaryWriter interface {
	SummaryFeedWriter
	// Ready loads the model when it isn't loaded, on a context only a
	// shutdown ends.
	Ready(ctx context.Context) error
	// Write writes one page's summary.
	Write(ctx context.Context, in summarize.Input) (summarize.Written, error)
}

// Pacer rests the summary pass under the gentle throttle; *QueueGate is
// one.
type Pacer interface {
	// Rested is told a summarize job took took, ending at now.
	Rested(took time.Duration, now time.Time)
}

// summaryWaitReason is what a summarize job deferred for the summaries'
// model waits for, and summaryCheckReason what one deferred for its
// fitness check to run again does, each worded to follow "waiting for".
const (
	summaryWaitReason  = "the summaries' model to answer"
	summaryCheckReason = "the summaries' model's check to run again"
)

// errPreempted is the cause of a summary call cancelled because the
// person began to use a model.
var errPreempted = errors.New("a search or a plan began")

// summarizeHandler builds the closure that runs one summarize job: the
// payload's document's summary of its current extraction, through the
// writer (Deps.Writer), stored with Deps.Summaries.PutSummary. Its
// outcomes:
//
//   - nothing to do (the writer writes nothing, for good: off, withheld,
//     unfit; or for a while: an eviction hold, summarize.ErrHeld; the
//     document gone, no longer fetched, or refetched since the job was
//     queued; its summary current; a write a refetch overtook): done, with
//     a DEBUG line, so the jobs left drain and none holds the Mac awake;
//   - an input unchanged since its summary, but for its extraction: the
//     summary pointed at the new extraction, with no call;
//   - the person using a model: deferred until they pause, the attempt
//     refunded; a call in flight when a use begins is cancelled for it;
//   - the model not pulled (generator.ErrModelMissing, by the call, the
//     load or the check): done, the writer recording the outage, the feed
//     keeping no job until its checks see the model pulled;
//   - no answer from the model otherwise (generator.ErrUnreachable,
//     ErrUnauthorized), or a check that got none: deferred half the job's
//     age (deferral), as the cards job is; the writer records the outage,
//     and the feed keeps this one job;
//   - a check that reached no verdict for a reason of the model's, or a
//     load of an unchecked model that failed so (a *summarize.WaitError):
//     the job that asked deferred until the check may run again, the feed
//     keeping that one job; any other done with nothing asked, its page
//     queued again once the model passes, the feed's next job queued for
//     the wait's end (Summaries.topUp);
//   - a timeout while the model answers (summarize.ErrSlow), or a
//     provider that couldn't serve the call: a failed attempt, retried;
//   - an answer that can't be used, cut off or not JSON, a request the
//     provider refuses, or no markdown to read: ErrPermanent, which the
//     hook records (summaryFailed).
//
// The feed (Deps.SummaryFeed) is told of each summary written, each
// attempt that failed and each that ended otherwise, while the job still
// runs; and each job's duration is the gentle throttle's rest after it
// (Deps.Pace).
func summarizeHandler(d Deps) HandlerFunc {
	return func(ctx context.Context, job *store.Job) error {
		start := time.Now()
		if d.Pace != nil {
			defer func() { d.Pace.Rested(time.Since(start), time.Now()) }()
		}
		// A writer that writes nothing, for good, leaves every job nothing
		// to do, whatever its payload holds.
		if reason := d.Writer.Refusal(); reason != "" {
			d.Log.Debug("summaries: not writing a page's summary", "job_id", job.ID, "reason", reason)
			return nil
		}
		var payload store.DocumentJobPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("%w: bad payload: %w", ErrPermanent, err)
		}
		err := summarizeJob(ctx, d, job, payload)
		switch {
		case err == nil || ctx.Err() != nil:
		case deferred(err) || errors.Is(err, errNothingToDo):
			d.SummaryFeed.Settled()
		default:
			d.SummaryFeed.Failed(payload.DocumentID, err)
		}
		if errors.Is(err, errNothingToDo) {
			return nil
		}
		return err
	}
}

// errNothingToDo is a summarize job with nothing to write, which is done.
var errNothingToDo = errors.New("nothing to write")

// summarizeJob is the summarize job's work, for the job's decoded
// payload; see summarizeHandler.
func summarizeJob(ctx context.Context, d Deps, job *store.Job, payload store.DocumentJobPayload) error {
	doc, err := d.Documents.GetByID(ctx, payload.DocumentID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		d.Log.Debug("summaries: the page is gone", "document_id", payload.DocumentID)
		return errNothingToDo
	case err != nil:
		return fmt.Errorf("load document: %w", err)
	case doc.State != store.DocStateFetched || doc.CurrentExtractionID == nil ||
		*doc.CurrentExtractionID != payload.ExtractionID:
		d.Log.Debug("summaries: the page moved on since its job was queued", "document_id", doc.ID,
			"state", doc.State)
		return errNothingToDo
	}
	in, err := SummaryInput(ctx, d, doc)
	if err != nil {
		return err
	}
	if done, err := repointed(ctx, d, doc, in); done || err != nil {
		return err
	}
	if err := yield(d); err != nil {
		return err
	}
	if err := d.Writer.Ready(ctx); err != nil {
		return summaryFailure(ctx, d, job, err)
	}
	// Taken before the last check, so a use that begins after it cancels
	// the call.
	begun := d.Foreground.Begun()
	if err := yield(d); err != nil {
		return err
	}
	pctx, stop := preemptible(ctx, begun)
	defer stop()
	written, err := d.Writer.Write(pctx, in)
	if preempted := stop(); err != nil && preempted && ctx.Err() == nil {
		d.Log.Debug("summaries: a search began; the page waits for it", "document_id", doc.ID)
		return &DeferError{Until: time.Now(), Reason: foregroundWaitReason, Err: errPreempted, Routine: true}
	}
	if err != nil {
		return summaryFailure(ctx, d, job, err)
	}
	s := written.Summary
	s.DocumentID, s.ExtractionID = doc.ID, payload.ExtractionID
	if err := d.Summaries.PutSummary(ctx, doc.TenantID, s); err != nil {
		if errors.Is(err, store.ErrSuperseded) {
			d.Log.Debug("summaries: the page moved on while its summary was written; nothing stored",
				"document_id", doc.ID)
			return errNothingToDo
		}
		return fmt.Errorf("store the summary: %w", err)
	}
	d.SummaryFeed.Written()
	return nil
}

// yield is a deferral until the person's use of a model pauses
// (Deps.Foreground), nil while the models are quiet.
func yield(d Deps) error {
	if until, quiet := d.Foreground.Quiet(time.Now()); !quiet {
		return &DeferError{Until: until, Reason: foregroundWaitReason, Err: errPreempted, Routine: true}
	}
	return nil
}

// SummaryInput reads what doc's summary is written from: its own title,
// its URL, its site's name as its fields hold it (else its host), its
// type, and its current extraction's markdown under d.ContentDir, read no
// further than the writer's window. A document with no markdown is
// ErrNoMarkdown, permanent. It reads d's Extractions, DocumentIndex and
// ContentDir alone; cmd/searcheval writes summaries from it too.
func SummaryInput(ctx context.Context, d Deps, doc *store.Document) (summarize.Input, error) {
	ext, err := d.Extractions.GetByID(ctx, *doc.CurrentExtractionID)
	if err != nil {
		return summarize.Input{}, fmt.Errorf("load extraction: %w", err)
	}
	if ext.MarkdownPath == nil || *ext.MarkdownPath == "" {
		return summarize.Input{}, fmt.Errorf("%w: %w: extraction %s has no markdown path", ErrPermanent,
			ErrNoMarkdown, ext.ID)
	}
	site := docmeta.Host(doc.URL)
	switch got, err := d.DocumentIndex.Site(ctx, doc.TenantID, doc.ID); {
	case err == nil:
		site = docmeta.SiteOrHost(got.Name, got.Host)
	case !errors.Is(err, store.ErrNotFound):
		return summarize.Input{}, fmt.Errorf("read the document's site: %w", err)
	}
	f, err := os.Open(filepath.Join(d.ContentDir, *ext.MarkdownPath))
	if errors.Is(err, fs.ErrNotExist) {
		return summarize.Input{}, fmt.Errorf("%w: %w: %w", ErrPermanent, ErrNoMarkdown, err)
	}
	if err != nil {
		return summarize.Input{}, fmt.Errorf("open markdown: %w", err)
	}
	defer f.Close()
	in, err := summarize.BuildInput(summarize.Document{ID: doc.ID, Title: deref(doc.Title), URL: doc.URL,
		Site: site, ContentType: string(doc.ContentType)}, f)
	if err != nil {
		return summarize.Input{}, fmt.Errorf("read markdown: %w", err)
	}
	return in, nil
}

// repointed reports whether doc's summary needs no call: it is current
// already, or it was written by the writer now from this exact input and
// only the extraction moved (a refetch that found the same page), when it
// is pointed at the new extraction, keeping when it was written. An owed
// summary is always written again.
func repointed(ctx context.Context, d Deps, doc *store.Document, in summarize.Input) (bool, error) {
	stored, err := d.Summaries.GetSummary(ctx, doc.TenantID, doc.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read the summary: %w", err)
	}
	s := stored.Summary
	if s == nil || stored.Owed || s.SummaryWriter != d.Writer.Identity() || s.InputHash != in.Hash() {
		return false, nil
	}
	if s.ExtractionID == *doc.CurrentExtractionID {
		return true, nil
	}
	s.ExtractionID = *doc.CurrentExtractionID
	switch err := d.Summaries.PutSummary(ctx, doc.TenantID, *s); {
	case errors.Is(err, store.ErrSuperseded):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("point the summary at the page's new extraction: %w", err)
	}
	d.Log.Debug("summaries: a refetch found the same page; its summary is kept", "document_id", doc.ID)
	d.SummaryFeed.Written()
	return true, nil
}

// summaryFailure is the outcome of a write or a load that failed with
// err (see summarizeHandler).
func summaryFailure(ctx context.Context, d Deps, job *store.Job, err error) error {
	switch {
	case ctx.Err() != nil:
		return err
	case errors.Is(err, summarize.ErrNotWriting):
		d.Log.Debug("summaries: not writing a page's summary", "job_id", job.ID, "err", err)
		return errNothingToDo
	case errors.Is(err, summarize.ErrHeld):
		// The feed keeps no job while the writer holds the pass
		// (Writer.Feed), and queues this page again after the hold: one
		// left waiting would only hold keep-awake.
		d.Log.Debug("summaries: the pass is held; the page is queued again after the hold", "job_id", job.ID,
			"err", err)
		return errNothingToDo
	case errors.Is(err, generator.ErrModelMissing):
		// Nor while the model isn't pulled, which only a pull ends, maybe
		// never: a job deferred for it would hold keep-awake for as long,
		// and fail and be queued again each day. The feed's checks see the
		// pull (Writer.Probe), and queue this page again then; the writer
		// logs the outage once.
		d.Log.Debug("summaries: the summaries' model isn't pulled; the page is queued again once it is",
			"job_id", job.ID, "err", err)
		return errNothingToDo
	}
	if we, ok := errors.AsType[*summarize.WaitError](err); ok {
		// The job whose check failed waits for the next, and sees it
		// run; any other would only wait beside it, so it is done, and
		// the feed, which keeps one job meanwhile (Writer.Feed), queued
		// for the wait's end, queues its page again once the model passes.
		// Routine: the writer logs the check's failures once an episode.
		if !we.Probed {
			d.Log.Debug("summaries: the summaries' model's check waits; the page is queued again after it",
				"job_id", job.ID, "retry_at", we.Until)
			return errNothingToDo
		}
		return &DeferError{Until: we.Until, Reason: summaryCheckReason, Err: fmt.Errorf("summarize: %w", err),
			Routine: true}
	}
	switch {
	case unansweredSummary(err) || errors.Is(err, summarize.ErrNotChecked):
		// Routine: the writer logs the outage once, however long the one
		// job the feed keeps waits out its deferrals.
		now := time.Now()
		return &DeferError{Until: now.Add(deferral(now.Sub(job.CreatedAt))), Reason: summaryWaitReason,
			Err: fmt.Errorf("summarize: %w", err), Routine: true}
	case summaryVerdict(err):
		return fmt.Errorf("%w: summarize: %w", ErrPermanent, err)
	}
	return fmt.Errorf("summarize: %w", err)
}

// unansweredSummary reports whether err got no answer from the summaries'
// model: no verdict on the page.
func unansweredSummary(err error) bool {
	return errors.Is(err, generator.ErrUnreachable) || errors.Is(err, generator.ErrModelMissing) ||
		errors.Is(err, generator.ErrUnauthorized)
}

// summaryVerdict reports whether err is the page's own, and would be on
// every attempt: an answer that can't be used, one cut off or not JSON,
// or a request the provider refuses as made.
func summaryVerdict(err error) bool {
	if _, ok := errors.AsType[*summarize.InvalidAnswer](err); ok {
		return true
	}
	return errors.Is(err, generator.ErrTruncated) || errors.Is(err, generator.ErrNotJSON) ||
		errors.Is(err, generator.ErrBadRequest)
}

// preemptible returns a context of ctx that is cancelled when begun
// closes, and stop, which releases it, waits for its watcher to return,
// and reports whether begun closed first. stop may be called again, and
// answers the same, so a caller can defer it beside its call. begun may be
// nil: nothing pre-empts then.
func preemptible(ctx context.Context, begun <-chan struct{}) (context.Context, func() bool) {
	pctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Go(func() {
		select {
		case <-begun:
			cancel(errPreempted)
		case <-done:
		case <-pctx.Done():
		}
	})
	return pctx, sync.OnceValue(func() bool {
		close(done)
		watcher.Wait()
		preempted := errors.Is(context.Cause(pctx), errPreempted)
		cancel(nil)
		return preempted
	})
}

// summaryFailed is the summarize pool's permanent-failure hook: it records
// the failure against the payload's extraction, the model and the prompt
// (SummaryStore.RecordSummaryFailure), so the feed doesn't queue the page
// again until one of them changes or SummariesFailedWait passes; but only
// for a failure of the page's own (pageOwn). It tolerates a document
// deleted meanwhile.
func summaryFailed(d Deps) PermFailHook {
	return func(ctx context.Context, job *store.Job, cause error) error {
		var payload store.DocumentJobPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("decode payload to record a failed summary: %w", err)
		}
		if payload.DocumentID == "" || !pageOwn(cause) {
			return nil
		}
		f := store.SummaryFailure{ExtractionID: payload.ExtractionID, SummaryWriter: d.Writer.Identity(),
			At: time.Now(), Error: cause.Error()}
		if err := d.Summaries.RecordSummaryFailure(ctx, job.TenantID, payload.DocumentID, f); err != nil {
			return fmt.Errorf("record the failed summary of document %s: %w", payload.DocumentID, err)
		}
		d.Log.Warn("summaries: a page's summary failed for good; it isn't tried again for a while",
			"document_id", payload.DocumentID, "job_id", job.ID, "err", cause)
		return nil
	}
}

// pageOwn reports whether a summarize job's last failure, cause, says
// something of its page. One that got no answer from the model, a check
// that reached no verdict, or a call a search or a plan pre-empted says
// nothing of the page, even once the job's day of deferrals is spent and
// such a deferral fails its attempts (a pause, a schedule or an outage
// that held the job that long).
func pageOwn(cause error) bool {
	return !unansweredSummary(cause) && !errors.Is(cause, summarize.ErrNotChecked) && !errors.Is(cause, errPreempted)
}
