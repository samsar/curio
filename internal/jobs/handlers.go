package jobs

import (
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

// FetchPayload is the JSON body of a fetch job.
type FetchPayload struct {
	DocumentID string `json:"document_id"`
}

// IndexPayload is the JSON body of an index job.
type IndexPayload struct {
	DocumentID string `json:"document_id"`
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
		var payload struct {
			DocumentID string `json:"document_id"`
		}
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
//  5. Update document with extracted title/author/content_type and set
//     current_extraction_id.
//  6. Enqueue an index job for the same document.
//
// Idempotent on retry: each attempt creates a new extraction row (history)
// and rewrites current_extraction_id. The previous extraction's file stays
// on disk for diff/history; can be GC'd by a future retention job.
func FetchHandler(d Deps) HandlerFunc {
	return func(ctx context.Context, job *store.Job) error {
		var payload FetchPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("%w: bad payload: %v", ErrPermanent, err)
		}
		if payload.DocumentID == "" {
			return fmt.Errorf("%w: document_id required", ErrPermanent)
		}

		doc, err := d.Documents.GetByID(ctx, payload.DocumentID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w: document %s not found", ErrPermanent, payload.DocumentID)
			}
			return fmt.Errorf("load document: %w", err)
		}

		f, err := d.Dispatcher.For(doc.URL)
		if err != nil {
			return fmt.Errorf("%w: no fetcher for %s: %v", ErrPermanent, doc.URL, err)
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

		ext := &store.DocumentExtraction{
			ID:           extID,
			DocumentID:   doc.ID,
			Fetcher:      f.Name(),
			Status:       store.ExtractionStatusOK,
			MarkdownPath: &relPath,
		}
		if res.Meta != nil {
			if b, err := json.Marshal(res.Meta); err == nil {
				ext.ExtractionMeta = b
			}
		}
		if err := d.Extractions.Create(ctx, ext); err != nil {
			return fmt.Errorf("create extraction: %w", err)
		}

		// Refresh the document with extracted metadata. Upsert preserves
		// (tenant_id, url) and updates content_type/title/author/state.
		title := res.Title
		var titlePtr *string
		if title != "" {
			titlePtr = &title
		}
		var authorPtr *string
		if res.Author != "" {
			authorPtr = &res.Author
		}
		var langPtr *string
		if res.Language != "" {
			langPtr = &res.Language
		}
		updated := &store.Document{
			ID:                  doc.ID,
			TenantID:            doc.TenantID,
			URL:                 doc.URL,
			ContentType:         defaultStr(res.ContentType, doc.ContentType),
			Title:               titlePtr,
			Author:              authorPtr,
			Language:            langPtr,
			PublishedAt:         res.PublishedAt,
			State:               store.DocStatePending, // still pending until index step
			CurrentExtractionID: &ext.ID,
		}
		if res.FinalURL != "" && res.FinalURL != doc.URL {
			updated.URLCanonical = &res.FinalURL
		}
		if err := d.Documents.Upsert(ctx, updated); err != nil {
			return fmt.Errorf("update document: %w", err)
		}

		// Enqueue the index step.
		indexPayload, _ := json.Marshal(IndexPayload{DocumentID: doc.ID})
		if err := d.Queue.Enqueue(ctx, &store.Job{
			TenantID: doc.TenantID,
			Kind:     store.JobKindIndex,
			Payload:  indexPayload,
		}); err != nil {
			return fmt.Errorf("enqueue index: %w", err)
		}

		return nil
	}
}

// IndexHandler builds the closure that runs one index job:
//  1. Load document + its current extraction.
//  2. Read the markdown file off disk.
//  3. Pull the bookmark's tags (if any) for FTS boosting — best-effort.
//  4. Run the Indexer.
//  5. Mark document state=fetched.
func IndexHandler(d Deps) HandlerFunc {
	return func(ctx context.Context, job *store.Job) error {
		var payload IndexPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("%w: bad payload: %v", ErrPermanent, err)
		}
		if payload.DocumentID == "" {
			return fmt.Errorf("%w: document_id required", ErrPermanent)
		}

		doc, err := d.Documents.GetByID(ctx, payload.DocumentID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w: document %s not found", ErrPermanent, payload.DocumentID)
			}
			return fmt.Errorf("load document: %w", err)
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

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
