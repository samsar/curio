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

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/search"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/version"
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

	// Reset orphaned 'running' jobs. Any row in that status at startup
	// belonged to a previous daemon that died (SIGKILL, SIGTERM mid-
	// handler, crash, laptop sleep). Single-daemon assumption holds for
	// v1; multi-daemon would need leasing here instead.
	if res, err := db.ExecContext(ctx, `
		UPDATE jobs SET status = 'pending',
		                started_at = NULL,
		                run_after = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE status = 'running'`); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			slog.Info("reset orphaned running jobs", "count", n)
		}
	} else {
		slog.Warn("could not reset orphaned running jobs", "err", err)
	}

	// Sync the marker file's schema_version with whatever the migration
	// run landed at, so /v1/healthz reflects reality after every upgrade.
	if v, err := sqlitestore.ReadSchemaVersion(db); err == nil && v > 0 && v != meta.SchemaVersion {
		updated := meta
		updated.SchemaVersion = v
		if err := home.WriteMeta(updated); err != nil {
			slog.Warn("failed to update marker schema_version", "err", err)
		}
	}

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

	// Run N workers + API server concurrently. First to error cancels all.
	//
	// Each worker calls Run on the same Worker struct — the JobQueue's
	// atomic ClaimNext means workers race for jobs cleanly. Tested at
	// 20 jobs / 8 workers in store/sqlite/stores_test.go.
	n := cfg.Daemon.Workers
	if n <= 0 {
		n = 1
	}
	errCh := make(chan error, n+1)
	for i := 0; i < n; i++ {
		go func() { errCh <- worker.Run(ctx) }()
	}
	go func() { errCh <- srv.Run(ctx) }()

	err = <-errCh
	stop()
	// Drain the remaining goroutines so we don't leak on shutdown.
	for i := 0; i < n; i++ {
		<-errCh
	}

	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
