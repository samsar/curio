package jobs

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/store"
)

// Deps bundles what the job handlers need, built once by the daemon.
type Deps struct {
	// ContentDir is where the extractions' markdown files are, which their
	// markdown paths are relative to: the home's content directory.
	ContentDir  string
	Documents   store.DocumentStore
	Extractions store.ExtractionStore
	Bookmarks   store.BookmarkStore
	Queue       store.JobQueue
	Dispatcher  fetcher.Dispatcher
	Indexer     *indexer.Indexer
	// Chunks is the documents' chunks, which a re-embed keeps as embedded
	// when it can't make them anew.
	Chunks store.ChunkStore
	// DocumentIndex is the documents' fields and cards: what a chunk's
	// context names a document's site by, and the cards the cards job
	// embeds.
	DocumentIndex store.DocumentIndex
	Insight       Rebuilder // the insight engine; nil unless the insight layer is wired
	// Placer places each document indexed into the current interests; nil
	// places none (the insight layer off).
	Placer Placer
	// KickInterests asks the interest scheduler to check now: the cluster
	// pool does when a rebuild starts and once its outcome is recorded.
	// nil asks nobody.
	KickInterests func()
	// Hold is told of an embed that got no answer, and holds the index and
	// cards jobs back until the model answers; nil holds nothing.
	Hold *EmbedderHold
	// Reembed is the re-embed for a new chunk context, topped up once each
	// index job's outcome is recorded; nil when there is none.
	Reembed *Reembed
	Log     *slog.Logger
}

// Placer places a document just indexed into the current interests (the
// insight layer's Placer). It is best effort and reports nothing: a
// placement that fails never fails the index job.
type Placer interface {
	Place(ctx context.Context, tenantID, documentID string)
}

// markDocFailed is the permanent-failure hook of the fetch and index pools:
// it reads document_id from the job payload and records why the document
// failed, which also sets its state. An index job's failure is
// store.FailureCauseIndex, whatever its error; any other job's is what
// fetcher.FailureCause reads from the error it gave up with. The document
// goes dead exactly when that is a dead link (fetcher.ErrDeadLink: a hard
// 404/410 or a detected soft 404), and failed otherwise. Without the hook a
// document whose job gave up would stay pending forever, indistinguishable
// from one whose job is about to run.
//
// A re-embed job (DocumentJobPayload.Reembed) is the exception: the daemon
// queued it, for a fetched document of an upgraded home, and nobody asked
// for it, so a failure leaves the document as it was, searchable by its
// earlier vectors, and the re-embed queues it again (reembedFailed).
func markDocFailed(d Deps) PermFailHook {
	return func(ctx context.Context, job *store.Job, jobErr error) error {
		var payload store.DocumentJobPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("decode payload to mark doc failed: %w", err)
		}
		if payload.DocumentID == "" {
			return nil // nothing to clean up
		}
		if job.Kind == store.JobKindIndex && payload.Reembed {
			return reembedFailed(ctx, d, job, payload.DocumentID, jobErr)
		}
		cause := store.FailureCauseIndex
		if job.Kind != store.JobKindIndex {
			// FailureCause is "" only for a nil error, which the worker
			// never passes. Were one to come, the store would refuse the
			// empty cause and retryBookkeeping retry that refusal until its
			// deadline, leaving the document pending; other records it.
			cause = cmp.Or(fetcher.FailureCause(jobErr), store.FailureCauseOther)
		}
		if err := d.Documents.MarkFailed(ctx, payload.DocumentID, cause); err != nil {
			return fmt.Errorf("mark doc %s failed (%s): %w", payload.DocumentID, cause, err)
		}
		return nil
	}
}

// reembedMaxFailures is how many re-embed jobs of a document may fail for
// good, for its own reasons, before it keeps its earlier vectors for good:
// enough to outlast a failure that passes (each job has its own retries,
// 15 minutes of backoff, and the next is queued reembedFailedWait after),
// few enough that one that doesn't costs three jobs' retries, not every
// start's.
const reembedMaxFailures = 3

// reembedFailed is markDocFailed for a re-embed job that failed for good,
// cause: the document stays as it was, searchable by its earlier vectors,
// and the re-embed queues it again reembedFailedWait later. It keeps those
// vectors for good, recorded as current (ChunkStore.KeepEmbedded), so that
// a failure that recurs whatever is tried can't queue it forever, at its
// reembedMaxFailures-th failure once another document was indexed since
// its first (ReembedFailures), whatever the cause: the embedding model
// embedded meanwhile, so the failures are the document's own, and a
// failure every document meets (a width that doesn't fit, a model missing,
// a model runner crashing at every embed) indexes nobody, so it keeps
// nothing, and the brake (Reembed.Failed) stops the feed until the model is
// fixed. Read from the cause, a 5xx would be every document's, and one
// that a document alone draws (Ollama failing to encode its NaN vector)
// would queue it every half hour for good. Each failure is one WARN.
func reembedFailed(ctx context.Context, d Deps, job *store.Job, docID string, cause error) error {
	failures, err := d.Documents.ReembedFailures(ctx, job.TenantID, docID)
	if err != nil {
		return err
	}
	if failures.Failed < reembedMaxFailures || !failures.OthersIndexed {
		d.Log.Warn("chunk context: a document's re-embed failed for good; it keeps its earlier vectors, and the "+
			"re-embed tries it again", "document_id", docID, "job_id", job.ID, "failures", failures.Failed,
			"others_indexed", failures.OthersIndexed, "err", cause)
		return nil
	}
	kept, err := d.Chunks.KeepEmbedded(ctx, docID, indexer.ContextVersion)
	if err != nil {
		return fmt.Errorf("keep the earlier vectors of document %s: %w", docID, err)
	}
	d.Log.Warn("chunk context: a document's re-embed failed for good again, while others were re-embedded; it "+
		"keeps its earlier vectors, and a refetch re-indexes it", "document_id", docID, "job_id", job.ID,
		"failures", failures.Failed, "chunks", kept, "err", cause)
	return nil
}

// timedOut reports whether err is a timeout: a deadline passed, or a
// network operation that timed out.
func timedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// fetchHandler builds the closure that runs one fetch job:
//  1. Load document; look up the right Fetcher via the dispatcher.
//  2. Call Fetcher.Fetch(ctx, document.URL). A failure is ErrPermanent for
//     a fetcher.PermanentError, a *DeferError for a fetcher.DeferError, and
//     retryable otherwise.
//  3. Write the resulting markdown to $CURIO_HOME/content/<doc>/<ext>.md.
//  4. Create a document_extractions row pointing at that file.
//  5. Apply the fetched metadata to the document and point
//     current_extraction_id at the new extraction.
//  6. Enqueue an index job for the same document.
//
// Idempotent on retry: each attempt creates a new extraction row (history)
// and rewrites current_extraction_id. The previous extraction's file stays
// on disk for diff/history; can be GC'd by a future retention job.
func fetchHandler(d Deps) HandlerFunc {
	return func(ctx context.Context, job *store.Job) error {
		doc, _, err := loadJobDocument(ctx, d, job)
		if err != nil {
			return err
		}

		f, err := d.Dispatcher.For(doc.URL)
		if err != nil {
			return fmt.Errorf("%w: no fetcher for %s: %w", ErrPermanent, doc.URL, err)
		}

		res, err := f.Fetch(ctx, doc.URL)
		if err != nil {
			var pe *fetcher.PermanentError
			if errors.As(err, &pe) {
				// Double-%w keeps the fetcher's sentinel chain (e.g.
				// fetcher.ErrDeadLink) matchable by the permanent-failure
				// hook, which picks the document's terminal state from it.
				return fmt.Errorf("%w: %w", ErrPermanent, pe.Err)
			}
			err = fmt.Errorf("fetch failed: %w", err)
			// The fetcher held the call back, for a limit of its own or one
			// an upstream named the end of: the job waits for the hold
			// instead of spending an attempt, and nothing is written.
			if de, ok := errors.AsType[*fetcher.DeferError](err); ok {
				return &DeferError{Until: de.Until, Reason: de.Reason, Err: err}
			}
			return err
		}

		// Pre-generate the extraction ID so we can write the file under
		// its final path BEFORE creating the DB row. Order matters: if
		// file write fails we have no orphan row; if DB create fails the
		// orphan file is recoverable.
		extID := uuid.NewString()
		relPath := filepath.Join(doc.ID, extID+".md")
		fullPath := filepath.Join(d.ContentDir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			return fmt.Errorf("mkdir content: %w", err)
		}
		if err := os.WriteFile(fullPath, []byte(res.Markdown), 0o600); err != nil {
			return fmt.Errorf("write markdown: %w", err)
		}

		status := store.ExtractionStatusOK
		if res.Partial {
			status = store.ExtractionStatusPartial
		}
		ext := &store.DocumentExtraction{
			ID:           extID,
			DocumentID:   doc.ID,
			Fetcher:      f.Name(),
			Status:       status,
			MarkdownPath: &relPath,
			ErrorMessage: store.NullableString(res.PartialReason),
		}
		if meta := extractionMeta(res); meta != nil {
			// Meta is diagnostic; one value JSON can't hold (a NaN, say)
			// shouldn't cost the document its content.
			if b, err := json.Marshal(meta); err != nil {
				d.Log.Warn("fetch: dropping extraction meta that doesn't encode",
					"document_id", doc.ID, "fetcher", f.Name(), "err", err)
			} else {
				ext.ExtractionMeta = b
			}
		}
		if err := d.Extractions.Create(ctx, ext); err != nil {
			return fmt.Errorf("create extraction: %w", err)
		}

		if err := d.Documents.ApplyFetch(ctx, doc.ID, fetchedMetadata(doc, res, ext.ID)); err != nil {
			return fmt.Errorf("update document: %w", err)
		}

		indexJob, err := store.NewDocumentJob(doc.TenantID, store.JobKindIndex, doc.ID)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrPermanent, err)
		}
		if err := d.Queue.Enqueue(ctx, indexJob); err != nil {
			return fmt.Errorf("enqueue index: %w", err)
		}

		return nil
	}
}

// loadJobDocument loads the document a fetch or index job names, and
// returns it with the job's payload. A payload that names none, or a
// document that no longer exists, fails the job permanently: retrying
// can't change either.
func loadJobDocument(ctx context.Context, d Deps, job *store.Job) (*store.Document, store.DocumentJobPayload,
	error) {
	var payload store.DocumentJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return nil, payload, fmt.Errorf("%w: bad payload: %w", ErrPermanent, err)
	}
	if payload.DocumentID == "" {
		return nil, payload, fmt.Errorf("%w: document_id required", ErrPermanent)
	}
	doc, err := d.Documents.GetByID(ctx, payload.DocumentID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, payload, fmt.Errorf("%w: document %s not found", ErrPermanent, payload.DocumentID)
	}
	if err != nil {
		return nil, payload, fmt.Errorf("load document: %w", err)
	}
	return doc, payload, nil
}

// extractionMeta is what an extraction records of res: the fetcher's
// meta, and the site name the page declared, under
// store.ExtractionMetaSiteName, when it declared one.
func extractionMeta(res *fetcher.Result) map[string]any {
	meta := res.Meta
	if res.SiteName != "" {
		if meta == nil {
			meta = map[string]any{}
		}
		meta[store.ExtractionMetaSiteName] = res.SiteName
	}
	return meta
}

// fetchedMetadata maps a fetch result onto the document columns it
// describes. A fetcher that can't tell the content type keeps the
// document's current one.
func fetchedMetadata(doc *store.Document, res *fetcher.Result, extractionID string) store.FetchedMetadata {
	m := store.FetchedMetadata{
		ExtractionID: extractionID,
		ContentType:  cmp.Or(res.ContentType, doc.ContentType),
		Title:        store.NullableString(res.Title),
		Author:       store.NullableString(res.Author),
		Language:     store.NullableString(res.Language),
		PublishedAt:  res.PublishedAt,
	}
	if res.FinalURL != doc.URL {
		m.URLCanonical = store.NullableString(res.FinalURL)
	}
	return m
}

// ErrNoMarkdown is a document whose current extraction has no markdown to
// index: it has no extraction, the extraction names no file, or the file
// is gone (content files may be deleted). Retrying can't bring it back.
var ErrNoMarkdown = errors.New("no markdown to index")

// indexHandler builds the closure that runs one index job: IndexDocument,
// then the document placed into the current interests, best effort. A
// document with no markdown to index (ErrNoMarkdown) fails it permanently.
// A document that moved on while it was indexed (store.ErrSuperseded: a
// refetch made another extraction current, or a re-embed's document is no
// longer fetched) is done: the write that moved it on stands. An embed
// that got no answer (ollama.ErrUnreachable) is unanswered's to judge:
// most close the embedder hold (Deps.Hold), which holds the other index
// jobs back, and defer this one, so that an outage of any length fails no
// document.
//
// A re-embed job (DocumentJobPayload.Reembed) re-embeds only a document
// still fetched whose chunks are still of an older context version: one
// being refetched (pending) is indexed by its fetch, one that failed since
// has nothing to re-embed, and one re-indexed since (a refetch, or `curio
// reindex`) has its chunks current, so each is done with nothing written.
// A document it can't re-embed however often it tries keeps its earlier
// vectors, recorded as current (ChunkStore.KeepEmbedded) so that the
// re-embed doesn't queue it again, with one WARN: one whose markdown is
// gone, until it is refetched, and one with a chunk the embedding model
// can't take even without its context. It tells the re-embed (Deps.Reembed)
// of a document re-embedded and of an attempt that failed, which its brake
// goes by; a deferral, a shutdown and a document it had no need or way to
// re-embed are neither.
func indexHandler(d Deps) HandlerFunc {
	return func(ctx context.Context, job *store.Job) error {
		start := time.Now()
		doc, payload, err := loadJobDocument(ctx, d, job)
		if err != nil {
			return err
		}
		if payload.Reembed {
			if skip, err := reembedDone(ctx, d, doc); skip || err != nil {
				return err
			}
		}
		_, err = IndexDocument(ctx, d, doc, payload.Reembed)
		if errors.Is(err, ollama.ErrUnreachable) {
			err = unanswered(ctx, d, job, start, err)
		}
		switch {
		case err == nil:
			if payload.Reembed {
				d.Reembed.Reembedded()
			}
		case errors.Is(err, store.ErrSuperseded):
			msg := "index: the document moved on while it was indexed; nothing written"
			if errors.Is(err, errChunksWritten) {
				msg = "index: the document moved on after its chunks were written, so its record wasn't; its " +
					"next index rewrites the chunks"
			}
			d.Log.Debug(msg, "document_id", doc.ID, "reembed", payload.Reembed, "err", err)
			return nil
		case ctx.Err() != nil || deferred(err):
			return err
		case payload.Reembed && errors.Is(err, ErrNoMarkdown):
			return keepEarlierVectors(ctx, d, doc,
				"a document's markdown is gone, so it keeps its earlier vectors; refetch it to re-index it", err)
		case payload.Reembed && errors.Is(err, embedder.ErrInputTooLong):
			return keepEarlierVectors(ctx, d, doc,
				"the embedding model can't take a document's chunk even without its context, so it keeps its "+
					"earlier vectors", err)
		case errors.Is(err, ErrNoMarkdown):
			return fmt.Errorf("%w: %w", ErrPermanent, err)
		default:
			if payload.Reembed {
				d.Reembed.Failed(doc.ID, err)
			}
			return err
		}
		if d.Placer != nil {
			d.Placer.Place(ctx, doc.TenantID, doc.ID)
		}
		return nil
	}
}

// errSlowEmbed is an index job's embed that timed out while the embedding
// model answers a probe: the document's batch is more than the model gets
// through within the embedder's timeout, a failed attempt like any other.
var errSlowEmbed = errors.New("the embedding model answers, but didn't embed this document's chunks within " +
	"the embedder's timeout")

// unanswered is what an index job makes of an embed that got no answer
// (ollama.ErrUnreachable), err:
//
//   - err itself once the job's context has ended: the shutdown cancelled
//     the embed, which says nothing of the model, so the hold stays open,
//     and the worker puts the job back;
//   - an attempt that failed (errSlowEmbed) for an embed that timed out
//     while the model answers a probe (EmbedderHold.Answers): the document
//     is too slow to embed, and a deferral would hide it for up to a day,
//     where a failed attempt is retried, then fails, as before the hold;
//   - otherwise a deferral half the job's age (deferral), with the hold
//     closed: Ollama stopped, restarting, or answering nothing.
func unanswered(ctx context.Context, d Deps, job *store.Job, start time.Time, err error) error {
	if ctx.Err() == nil && timedOut(err) && !d.Hold.Held() && d.Hold.Answers(ctx) {
		// The timeout's text alone: its chain would read as no answer.
		return fmt.Errorf("%w: %s", errSlowEmbed, err.Error())
	}
	if ctx.Err() != nil {
		return err
	}
	d.Hold.Unreachable(err)
	return &DeferError{Until: start.Add(deferral(start.Sub(job.CreatedAt))), Reason: embedWaitReason, Err: err}
}

// deferred reports whether err is a deferral.
func deferred(err error) bool {
	_, ok := errors.AsType[*DeferError](err)
	return ok
}

// reembedDone reports whether a re-embed of doc has nothing left to do:
// doc is no longer fetched, or none of its chunks is of an older context
// version.
func reembedDone(ctx context.Context, d Deps, doc *store.Document) (bool, error) {
	if doc.State != store.DocStateFetched {
		d.Log.Debug("chunk context: not re-embedding a document no longer fetched", "document_id", doc.ID,
			"state", doc.State)
		return true, nil
	}
	stale, err := d.Chunks.StaleContext(ctx, doc.ID, indexer.ContextVersion)
	if err != nil {
		return false, err
	}
	if !stale {
		d.Log.Debug("chunk context: not re-embedding a document whose chunks are current", "document_id", doc.ID)
		return true, nil
	}
	return false, nil
}

// keepEarlierVectors records doc's chunks as embedded under the current
// context, as they are, for a re-embed that can't make them anew (cause),
// and says why in a WARN.
func keepEarlierVectors(ctx context.Context, d Deps, doc *store.Document, why string, cause error) error {
	kept, err := d.Chunks.KeepEmbedded(ctx, doc.ID, indexer.ContextVersion)
	if err != nil {
		return fmt.Errorf("keep the earlier vectors of document %s: %w", doc.ID, err)
	}
	d.Log.Warn("chunk context: "+why, "document_id", doc.ID, "chunks", kept, "err", cause)
	return nil
}

// IndexDocument indexes doc as the index job does, a re-embed's when
// reembed, and returns how many chunks it wrote:
//  1. Read its current extraction's markdown under d.ContentDir.
//  2. Read what its chunks' context is built from: its bookmarks and its
//     site (indexer.DocumentContext). A failed read fails the attempt, to
//     be retried: a context built without them would be stored as the
//     document's, and nothing would build it again.
//  3. Pull the bookmarks' tags for FTS boosting — best effort.
//  4. Run the indexer.
//  5. Record the index (DocumentStore.MarkIndexed): the document fetched,
//     with no failure cause, its indexed text's word count and when its
//     vectors were written; a re-embed's records the last two alone.
//
// Steps 4 and 5 write only while the extraction read in step 1 is still
// the document's current one, and, for a re-embed, while the document is
// fetched: store.ErrSuperseded otherwise, which also wraps
// errChunksWritten when step 5 is the one that found the document moved
// on. The daemon's index jobs and searcheval's re-index both run it, so a
// copy is measured as the daemon indexes. A chunk the model can't take even without its context
// (embedder.ErrInputTooLong) and a vector of the wrong width fail the same
// way on every attempt, so they are ErrPermanent; a document with no
// markdown to read is ErrNoMarkdown. An index job fails on any of them,
// where a re-embed keeps the earlier vectors of a document with a chunk
// too long or no markdown.
func IndexDocument(ctx context.Context, d Deps, doc *store.Document, reembed bool) (int, error) {
	md, extractionID, err := readMarkdown(ctx, d, doc)
	if err != nil {
		return 0, err
	}
	about, err := documentContext(ctx, d, doc)
	if err != nil {
		return 0, err
	}

	// Pull tags from the bookmarks referencing this doc, denormalized
	// into chunks_fts for boosting. Best-effort: a lookup failure is
	// non-fatal — we'd rather index without tags than fail the job.
	var tags []string
	if t, terr := d.Bookmarks.TagsForDocument(ctx, doc.TenantID, doc.ID); terr != nil {
		d.Log.Warn("index: tag lookup failed, indexing without tags",
			"document_id", doc.ID, "err", terr)
	} else {
		tags = t
	}

	n, err := d.Indexer.Index(ctx, indexer.IndexInput{
		DocumentID:   doc.ID,
		ExtractionID: extractionID,
		Tags:         tags,
		Markdown:     md,
		Document:     about,
		Reembed:      reembed,
	})
	if errors.Is(err, embedder.ErrInputTooLong) || errors.Is(err, embedder.ErrWrongDimension) {
		return 0, fmt.Errorf("%w: index: %w", ErrPermanent, err)
	}
	if err != nil {
		return 0, fmt.Errorf("index: %w", err)
	}

	guard := store.IndexGuard{ExtractionID: extractionID, Reembed: reembed}
	err = d.Documents.MarkIndexed(ctx, doc.ID, guard, indexer.WordCount(md))
	if errors.Is(err, store.ErrSuperseded) {
		return 0, fmt.Errorf("record the index: %w: %w", err, errChunksWritten)
	}
	if err != nil {
		return 0, fmt.Errorf("record the index: %w", err)
	}
	return n, nil
}

// errChunksWritten marks an index whose record lost the race to what moved
// its document on (store.ErrSuperseded) after its chunks were written: they
// stand until the document's next index, its refetch's, rewrites them.
var errChunksWritten = errors.New("its chunks were written before it moved on")

// readMarkdown reads doc's current extraction's markdown and returns it
// with the extraction's ID. A document with none to read is ErrNoMarkdown.
func readMarkdown(ctx context.Context, d Deps, doc *store.Document) (md, extractionID string, err error) {
	if doc.CurrentExtractionID == nil {
		return "", "", fmt.Errorf("%w: document %s has no current extraction", ErrNoMarkdown, doc.ID)
	}
	ext, err := d.Extractions.GetByID(ctx, *doc.CurrentExtractionID)
	if err != nil {
		return "", "", fmt.Errorf("load extraction: %w", err)
	}
	if ext.MarkdownPath == nil || *ext.MarkdownPath == "" {
		return "", "", fmt.Errorf("%w: extraction %s has no markdown path", ErrNoMarkdown, ext.ID)
	}
	raw, err := os.ReadFile(filepath.Join(d.ContentDir, *ext.MarkdownPath))
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", fmt.Errorf("%w: %w", ErrNoMarkdown, err)
	}
	if err != nil {
		return "", "", fmt.Errorf("read markdown: %w", err)
	}
	return string(raw), ext.ID, nil
}

// documentContext reads what doc's chunks' context is built from: its own
// title, its bookmarks, newest saved first, and its site, as its fields
// hold it (none without a fields row).
func documentContext(ctx context.Context, d Deps, doc *store.Document) (indexer.DocumentContext, error) {
	about := indexer.DocumentContext{URL: doc.URL}
	if doc.Title != nil {
		about.Title = *doc.Title
	}
	bookmarks, err := d.Bookmarks.ListByDocument(ctx, doc.TenantID, doc.ID)
	if err != nil {
		return indexer.DocumentContext{}, fmt.Errorf("read the document's bookmarks: %w", err)
	}
	for _, b := range bookmarks {
		about.Bookmarks = append(about.Bookmarks,
			docmeta.Bookmark{Title: deref(b.Title), FolderPath: deref(b.FolderPath)})
	}
	switch site, err := d.DocumentIndex.Site(ctx, doc.TenantID, doc.ID); {
	case err == nil:
		about.Site = &site
	case !errors.Is(err, store.ErrNotFound):
		return indexer.DocumentContext{}, fmt.Errorf("read the document's site: %w", err)
	}
	return about, nil
}

// deref is *s, or "" for nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
