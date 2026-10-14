package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/devtool"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
)

// reindexReport is what -reindex did: the documents and chunks it
// re-embedded, in how long, under which chunk context, and how many vec0
// chunks held the copy's chunk vectors before and after, which a re-index
// that took new slots would grow.
type reindexReport struct {
	Documents          int     `json:"documents"`
	Chunks             int     `json:"chunks"`
	TookMS             float64 `json:"took_ms"`
	ChunksPerSecond    float64 `json:"chunks_per_second"`
	ContextVersion     int     `json:"context_version"`
	VectorBlocksBefore int     `json:"vector_blocks_before"`
	VectorBlocksAfter  int     `json:"vector_blocks_after"`
}

// reindexCopy re-embeds the copy's documents whose chunks predate the
// chunk context (DocumentStore.DocumentsToReembed) through the daemon's own
// index path, jobs.IndexDocument, as its re-embed does, with config's
// chunking and document prefix, reading each one's markdown under
// content, at most cfg.Daemon.IndexWorkers at once. It checks every
// document's markdown is there before it embeds anything: a wrong
// directory would otherwise fail mid-way, a re-embed partly done. Each
// document is committed whole, so a run stopped part way leaves every
// document re-embedded or untouched, and the next re-embeds the rest.
func reindexCopy(ctx context.Context, lib *devtool.Copy, emb embedder.Embedder, cfg config.Config, content string,
	log *slog.Logger) (*reindexReport, error) {
	stale, err := lib.Documents.DocumentsToReembed(ctx, store.LocalTenantID, indexer.ContextVersion)
	if err != nil {
		return nil, err
	}
	docs, err := withMarkdown(ctx, lib, stale, content)
	if err != nil {
		return nil, err
	}
	rep := &reindexReport{ContextVersion: indexer.ContextVersion}
	if rep.VectorBlocksBefore, err = lib.ChunkVectorBlocks(ctx); err != nil {
		return nil, err
	}
	idx := indexer.New(lib.Chunks, emb, indexer.Options{ChunkSize: cfg.Chunking.SizeTokens,
		ChunkOverlap: cfg.Chunking.OverlapTokens, DocumentPrefix: cfg.Embedding.DocumentPrefix, Log: log})
	deps := jobs.Deps{ContentDir: content, Documents: lib.Documents, Extractions: lib.Extractions,
		Bookmarks: lib.Bookmarks, Indexer: idx, Chunks: lib.Chunks, DocumentIndex: lib.Index, Log: log}

	log.Info("re-embedding the copy's chunks with their context", "documents", len(docs),
		"context_version", indexer.ContextVersion, "workers", max(cfg.Daemon.IndexWorkers, 1))
	progress := newReindexProgress(len(docs), log)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(cfg.Daemon.IndexWorkers, 1))
	for _, doc := range docs {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			n, err := jobs.IndexDocument(gctx, deps, doc, true)
			if err != nil {
				return fmt.Errorf("re-embed document %s: %w", doc.ID, err)
			}
			progress.indexed(n)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("re-embed the copy (%d documents re-embedded first): %w", progress.docs, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("re-embed the copy (%d documents re-embedded first): %w", progress.docs, err)
	}
	took := time.Since(progress.began)
	rep.Documents, rep.Chunks, rep.TookMS = progress.docs, progress.chunks, ms(took)
	if took > 0 {
		rep.ChunksPerSecond = float64(progress.chunks) / took.Seconds()
	}
	if rep.VectorBlocksAfter, err = lib.ChunkVectorBlocks(ctx); err != nil {
		return nil, err
	}
	log.Info("re-embedded the copy's chunks", "documents", rep.Documents, "chunks", rep.Chunks,
		"took_ms", int64(rep.TookMS), "chunks_per_s", rep.ChunksPerSecond)
	return rep, nil
}

// withMarkdown loads stale's documents and checks each one's markdown is
// under content, failing, with how many are missing and the first, before
// anything is re-embedded.
func withMarkdown(ctx context.Context, lib *devtool.Copy, stale []string, content string) (
	[]*store.Document, error) {
	docs := make([]*store.Document, 0, len(stale))
	var missing []string
	for _, id := range stale {
		doc, err := lib.Documents.GetByID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load document %s: %w", id, err)
		}
		gone, err := missingMarkdown(ctx, lib, doc, content)
		if err != nil {
			return nil, err
		}
		if gone != "" {
			missing = append(missing, gone)
		}
		docs = append(docs, doc)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the markdown of %d of the %d documents to re-embed is missing under %s (the "+
			"first: %s): point -content at the content directory of the home the copy is from", len(missing),
			len(stale), content, missing[0])
	}
	return docs, nil
}

// missingMarkdown says what of doc's current extraction's markdown is
// missing under content: the file's path, or that the extraction names
// none; "" when it is there.
func missingMarkdown(ctx context.Context, lib *devtool.Copy, doc *store.Document, content string) (string, error) {
	if doc.CurrentExtractionID == nil {
		return fmt.Sprintf("document %s has no extraction", doc.ID), nil
	}
	ext, err := lib.Extractions.GetByID(ctx, *doc.CurrentExtractionID)
	if err != nil {
		return "", fmt.Errorf("load the extraction of document %s: %w", doc.ID, err)
	}
	if ext.MarkdownPath == nil || *ext.MarkdownPath == "" {
		return fmt.Sprintf("document %s's extraction names no markdown file", doc.ID), nil
	}
	path := filepath.Join(content, *ext.MarkdownPath)
	switch _, err := os.Stat(path); {
	case errors.Is(err, fs.ErrNotExist):
		return path, nil
	case err != nil:
		return "", fmt.Errorf("the markdown of document %s: %w", doc.ID, err)
	}
	return "", nil
}

// reindexProgressEvery is how often a re-index says how far it got: about
// 40 lines over the owner's library's hour and a quarter.
const reindexProgressEvery = 30 * time.Second

// reindexProgress counts what a re-index did, and says so at most every
// reindexProgressEvery: documents done, of how many, chunks embedded,
// their rate and the time left at it.
type reindexProgress struct {
	of    int
	log   *slog.Logger
	began time.Time

	mu     sync.Mutex
	docs   int
	chunks int
	logged time.Time
}

func newReindexProgress(of int, log *slog.Logger) *reindexProgress {
	now := time.Now()
	return &reindexProgress{of: of, log: log, began: now, logged: now}
}

// indexed counts a document re-embedded as chunks chunks.
func (p *reindexProgress) indexed(chunks int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.docs++
	p.chunks += chunks
	if time.Since(p.logged) < reindexProgressEvery {
		return
	}
	p.logged = time.Now()
	elapsed := time.Since(p.began)
	rate := float64(p.chunks) / elapsed.Seconds()
	perDoc := elapsed / time.Duration(p.docs)
	p.log.Info("re-embedding the copy's chunks", "documents", p.docs, "of", p.of, "chunks", p.chunks,
		"chunks_per_s", rate, "eta", (perDoc * time.Duration(p.of-p.docs)).Round(time.Second).String())
}
