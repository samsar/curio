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
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
)

// Deps bundles the dependencies the job handlers need. Bundled so the
// daemon can construct them once and inject everywhere.
type Deps struct {
	Home        *curiohome.Home
	Documents   store.DocumentStore
	Extractions store.ExtractionStore
	Bookmarks   store.BookmarkStore
	Chunks      store.ChunkStore
	Queue       store.JobQueue
	Dispatcher  fetcher.Dispatcher
	Indexer     *indexer.Indexer
	Insight     *insight.Engine // nil unless the insight layer is wired
	Log         *slog.Logger
}

// Register wires the M0 handlers onto a worker. Also attaches
// permanent-failure hooks so when a fetch or index job exhausts its
// retries, the parent document transitions to state=failed instead of
// staying stuck in pending forever. Without this, `curio status`
// overstates how much work is actually in flight — a doc whose fetch
// gave up still shows as "pending" indistinguishable from one whose
// job is genuinely about to run.
func Register(w *Worker, d Deps) {
	w.Register(store.JobKindFetch, FetchHandler(d))
	w.Register(store.JobKindIndex, IndexHandler(d))
	w.OnPermanentFailure(store.JobKindFetch, MarkDocFailed(d))
	w.OnPermanentFailure(store.JobKindIndex, MarkDocFailed(d))
}

// MarkDocFailed is a kind-agnostic hook: read document_id from the job
// payload, set its state to failed — or dead, when the cause identifies
// the URL itself as gone (fetcher.ErrDeadLink: hard 404/410 or a detected
// soft 404). Used for both fetch and index since both payloads carry
// document_id under the same JSON key. Exported so callers wiring split
// pools (cmd/curio-daemon/main.go) can register it per kind without going
// through jobs.Register.
func MarkDocFailed(d Deps) PermFailHook {
	return func(ctx context.Context, job *store.Job, cause error) error {
		var payload store.DocumentJobPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("decode payload to mark doc failed: %w", err)
		}
		if payload.DocumentID == "" {
			return nil // nothing to clean up
		}
		state := store.DocStateFailed
		if errors.Is(cause, fetcher.ErrDeadLink) {
			state = store.DocStateDead
		}
		if err := d.Documents.UpdateState(ctx, payload.DocumentID, state); err != nil {
			return fmt.Errorf("update doc %s to %s: %w", payload.DocumentID, state, err)
		}
		return nil
	}
}

// FetchHandler builds the closure that runs one fetch job:
//  1. Load document; look up the right Fetcher via the dispatcher.
//  2. Call Fetcher.Fetch(ctx, document.URL).
//  3. Write the resulting markdown to $CURIO_HOME/content/<doc>/<ext>.md.
//  4. Create a document_extractions row pointing at that file.
//  5. Apply the fetched metadata to the document and point
//     current_extraction_id at the new extraction.
//  6. Enqueue an index job for the same document.
//
// Idempotent on retry: each attempt creates a new extraction row (history)
// and rewrites current_extraction_id. The previous extraction's file stays
// on disk for diff/history; can be GC'd by a future retention job.
func FetchHandler(d Deps) HandlerFunc {
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
			return fmt.Errorf("fetch failed: %w", err)
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
		Title:        nonEmpty(res.Title),
		Author:       nonEmpty(res.Author),
		Language:     nonEmpty(res.Language),
		PublishedAt:  res.PublishedAt,
	}
	if res.FinalURL != doc.URL {
		m.URLCanonical = nonEmpty(res.FinalURL)
	}
	return m
}

// nonEmpty is s as a nullable column value: nil when s is empty.
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// IndexHandler builds the closure that runs one index job:
//  1. Load document + its current extraction.
//  2. Read the markdown file off disk.
//  3. Pull the bookmark's tags (if any) for FTS boosting — best-effort.
//  4. Run the Indexer.
//  5. Mark document state=fetched.
func IndexHandler(d Deps) HandlerFunc {
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
		if err != nil {
			return fmt.Errorf("index: %w", err)
		}

		if err := d.Documents.UpdateState(ctx, doc.ID, store.DocStateFetched); err != nil {
			return fmt.Errorf("update state: %w", err)
		}
		return nil
	}
}
