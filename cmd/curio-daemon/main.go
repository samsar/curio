package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/samansartipi/curio/internal/api"
	"github.com/samansartipi/curio/internal/config"
	"github.com/samansartipi/curio/internal/curiohome"
	"github.com/samansartipi/curio/internal/embedder"
	"github.com/samansartipi/curio/internal/fetcher"
	"github.com/samansartipi/curio/internal/indexer"
	"github.com/samansartipi/curio/internal/jobs"
	"github.com/samansartipi/curio/internal/search"
	sqlitestore "github.com/samansartipi/curio/internal/store/sqlite"
	"github.com/samansartipi/curio/internal/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("daemon exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Resolve / initialize $CURIO_HOME.
	homePath, err := curiohome.Resolve()
	if err != nil {
		return err
	}
	home, err := curiohome.Open(homePath)
	if err != nil {
		if !errors.Is(err, curiohome.ErrNotInitialized) {
			return err
		}
		slog.Info("initializing curio home", "path", homePath)
		// Stub model + dim; will be re-checked once config loads.
		home, err = curiohome.Init(homePath, "nomic-embed-text", 768)
		if err != nil {
			return err
		}
	}

	cfg, err := config.Load(home.ConfigPath())
	if err != nil {
		return err
	}

	// Cross-check the marker file against config; if they disagree, the
	// user changed config without reindexing. Fail loudly.
	meta, err := home.Meta()
	if err != nil {
		return err
	}
	if meta.EmbeddingModel != cfg.Embedding.Model || meta.EmbeddingDim != cfg.Embedding.Dim {
		slog.Warn("embedding model/dim mismatch between config and marker",
			"config_model", cfg.Embedding.Model,
			"config_dim", cfg.Embedding.Dim,
			"marker_model", meta.EmbeddingModel,
			"marker_dim", meta.EmbeddingDim,
		)
		slog.Warn("run `curio reindex --reason=model-swap` (not yet implemented) before continuing")
		return errors.New("embedding config/marker mismatch")
	}

	// Open DB and migrate.
	db, err := sqlitestore.Open(home.DBPath())
	if err != nil {
		return err
	}
	defer db.Close()

	if err := sqlitestore.Migrate(db); err != nil {
		return err
	}
	slog.Info("database ready", "path", home.DBPath())

	// Construct stores.
	docs := sqlitestore.NewDocuments(db)
	exts := sqlitestore.NewExtractions(db)
	bms := sqlitestore.NewBookmarks(db)
	chunks := sqlitestore.NewChunks(db, cfg.Embedding.Dim)
	queue := sqlitestore.NewJobs(db)

	// Embedder.
	emb, err := embedder.NewOllama(embedder.OllamaOptions{
		BaseURL: cfg.Embedding.BaseURL,
		Model:   cfg.Embedding.Model,
		Dim:     cfg.Embedding.Dim,
	})
	if err != nil {
		return err
	}

	// Fetcher dispatcher (M0: single backend selected by config).
	var defaultFetcher fetcher.Fetcher
	switch cfg.Fetcher.Default {
	case "native":
		defaultFetcher = fetcher.NewNative(fetcher.NativeOptions{
			Timeout:      time.Duration(cfg.Fetcher.Native.TimeoutSeconds) * time.Second,
			UserAgent:    cfg.Fetcher.Native.UserAgent,
			JinaFallback: cfg.Fetcher.Native.JinaFallback,
			JinaBaseURL:  cfg.Fetcher.Native.JinaBaseURL,
			Log:          slog.Default(),
		})
	case "web2md":
		w2m, err := fetcher.NewWeb2MD(fetcher.Web2MDOptions{
			Bin:     cfg.Fetcher.Web2MD.Bin,
			NodeBin: cfg.Fetcher.Web2MD.NodeBin,
			Timeout: time.Duration(cfg.Fetcher.Web2MD.TimeoutSeconds) * time.Second,
		})
		if err != nil {
			return err
		}
		defaultFetcher = w2m
	default:
		return fmt.Errorf("unknown fetcher.default %q", cfg.Fetcher.Default)
	}
	dispatcher := &fetcher.Single{F: defaultFetcher}

	// Indexer + search engine.
	idx := indexer.New(chunks, emb, indexer.Options{
		ChunkSize:    cfg.Chunking.SizeTokens,
		ChunkOverlap: cfg.Chunking.OverlapTokens,
	})
	engine := search.New(chunks, docs, emb, search.Config{
		BM25Weight:   cfg.Search.BM25Weight,
		VectorWeight: cfg.Search.VectorWeight,
		RRFK:         cfg.Search.RRFK,
		Collapse:     search.CollapseStrategy(cfg.Search.Collapse),
	})

	// Worker + handlers.
	worker := jobs.NewWorker(queue, jobs.WorkerOptions{Log: slog.Default()})
	jobs.Register(worker, jobs.Deps{
		Home:        home,
		Documents:   docs,
		Extractions: exts,
		Bookmarks:   bms,
		Chunks:      chunks,
		Queue:       queue,
		Dispatcher:  dispatcher,
		Indexer:     idx,
		Log:         slog.Default(),
	})

	// HTTP API.
	srv := api.NewServer(cfg.Daemon.Listen, api.Deps{
		Home:        home,
		Documents:   docs,
		Extractions: exts,
		Bookmarks:   bms,
		Chunks:      chunks,
		Queue:       queue,
		Embedder:    emb,
		Search:      engine,
		TenantID:    "local",
		Log:         slog.Default(),
	})

	slog.Info("curio-daemon starting", "version", version.String())

	// Run worker + server concurrently. First to error cancels both.
	errCh := make(chan error, 2)
	go func() { errCh <- worker.Run(ctx) }()
	go func() { errCh <- srv.Run(ctx) }()

	err = <-errCh
	stop()
	<-errCh // drain the second goroutine

	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
