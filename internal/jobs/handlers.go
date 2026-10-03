package jobs

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/store"
)

// Deps bundles what the job handlers need, built once by the daemon.
type Deps struct {
	Home        *curiohome.Home
	Documents   store.DocumentStore
	Extractions store.ExtractionStore
	Bookmarks   store.BookmarkStore
	Queue       store.JobQueue
	Dispatcher  fetcher.Dispatcher
	Indexer     *indexer.Indexer
	Insight     Rebuilder // the insight engine; nil unless the insight layer is wired
	// Placer places each document indexed into the current interests; nil
	// places none (the insight layer off).
	Placer Placer
	// KickInterests asks the interest scheduler to check now: the cluster
	// pool does when a rebuild starts and once its outcome is recorded.
	// nil asks nobody.
	KickInterests func()
	Log           *slog.Logger
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
func markDocFailed(d Deps) PermFailHook {
	return func(ctx context.Context, job *store.Job, jobErr error) error {
		var payload store.DocumentJobPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("decode payload to mark doc failed: %w", err)
		}
		if payload.DocumentID == "" {
			return nil // nothing to clean up
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
		doc, err := loadJobDocument(ctx, d, job)
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
		fullPath := filepath.Join(d.Home.ContentDir(), relPath)
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
		if res.Meta != nil {
			// Meta is diagnostic; one value JSON can't hold (a NaN, say)
			// shouldn't cost the document its content.
			if b, err := json.Marshal(res.Meta); err != nil {
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

// loadJobDocument loads the document a fetch or index job names. A payload
// that names none, or a document that no longer exists, fails the job
// permanently: retrying can't change either.
func loadJobDocument(ctx context.Context, d Deps, job *store.Job) (*store.Document, error) {
	var payload store.DocumentJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return nil, fmt.Errorf("%w: bad payload: %w", ErrPermanent, err)
	}
	if payload.DocumentID == "" {
		return nil, fmt.Errorf("%w: document_id required", ErrPermanent)
	}
	doc, err := d.Documents.GetByID(ctx, payload.DocumentID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: document %s not found", ErrPermanent, payload.DocumentID)
	}
	if err != nil {
		return nil, fmt.Errorf("load document: %w", err)
	}
	return doc, nil
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

// indexHandler builds the closure that runs one index job:
//  1. Load document + its current extraction.
//  2. Read the markdown file off disk.
//  3. Pull the bookmark's tags (if any) for FTS boosting — best-effort.
//  4. Run the Indexer.
//  5. Mark the document fetched, which clears any failure cause.
//  6. Place it into the current interests, best effort.
func indexHandler(d Deps) HandlerFunc {
	return func(ctx context.Context, job *store.Job) error {
		doc, err := loadJobDocument(ctx, d, job)
		if err != nil {
			return err
		}
		if doc.CurrentExtractionID == nil {
			return fmt.Errorf("%w: document %s has no current extraction", ErrPermanent, doc.ID)
		}

		ext, err := d.Extractions.GetByID(ctx, *doc.CurrentExtractionID)
		if err != nil {
			return fmt.Errorf("load extraction: %w", err)
		}
		if ext.MarkdownPath == nil || *ext.MarkdownPath == "" {
			return fmt.Errorf("%w: extraction %s has no markdown path", ErrPermanent, ext.ID)
		}

		fullPath := filepath.Join(d.Home.ContentDir(), *ext.MarkdownPath)
		md, err := os.ReadFile(fullPath)
		if err != nil {
			return fmt.Errorf("read markdown: %w", err)
		}

		title := ""
		if doc.Title != nil {
			title = *doc.Title
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

		err = d.Indexer.Index(ctx, indexer.IndexInput{
			DocumentID:   doc.ID,
			ExtractionID: ext.ID,
			Title:        title,
			Tags:         tags,
			Markdown:     string(md),
		})
		// A chunk the model can't take and a vector of the wrong width fail
		// the same way on every attempt: the document goes failed at once
		// instead of burning its retries.
		if errors.Is(err, embedder.ErrInputTooLong) || errors.Is(err, embedder.ErrWrongDimension) {
			return fmt.Errorf("%w: index: %w", ErrPermanent, err)
		}
		if err != nil {
			return fmt.Errorf("index: %w", err)
		}

		if err := d.Documents.MarkFetched(ctx, doc.ID); err != nil {
			return fmt.Errorf("mark fetched: %w", err)
		}
		if d.Placer != nil {
			d.Placer.Place(ctx, doc.TenantID, doc.ID)
		}
		return nil
	}
}
