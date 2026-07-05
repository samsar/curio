package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
	"github.com/samsar/curio/internal/store"
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

	// Fetcher dispatcher. Native is always constructed (pure Go, no
	// external deps, NewNative never errors) so fetcher_rules.yaml can
	// bind "native" even when web2md is the configured default.
	nativeFetcher := fetcher.NewNative(fetcher.NativeOptions{
		Timeout:      time.Duration(cfg.Fetcher.Native.TimeoutSeconds) * time.Second,
		UserAgent:    cfg.Fetcher.Native.UserAgent,
		JinaFallback: cfg.Fetcher.Native.JinaFallback,
		JinaBaseURL:  cfg.Fetcher.Native.JinaBaseURL,
		Backend:      cfg.Fetcher.Native.Backend,
		Log:          slog.Default(),
	})
	var defaultFetcher fetcher.Fetcher
	switch cfg.Fetcher.Default {
	case "native":
		defaultFetcher = nativeFetcher
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
	// Content-type-specific fetchers, routed by hostname. The built-in
	// rules below are the defaults; a user-provided fetcher_rules.yaml
	// under $CURIO_HOME overrides them (hot-reloaded, no restart needed).
	var rules []fetcher.Rule
	registry := map[string]fetcher.Fetcher{
		nativeFetcher.Name():  nativeFetcher,
		defaultFetcher.Name(): defaultFetcher,
	}

	ghFetcher := fetcher.NewGitHub(fetcher.GitHubOptions{
		Token:   cfg.Fetcher.GitHub.Token,
		Timeout: time.Duration(cfg.Fetcher.GitHub.TimeoutSeconds) * time.Second,
		Log:     slog.Default(),
	})
	rules = append(rules, fetcher.Rule{Hosts: fetcher.GitHubHosts, Fetcher: ghFetcher})
	registry[ghFetcher.Name()] = ghFetcher

	if _, err := exec.LookPath(cfg.Fetcher.YouTube.Bin); err == nil {
		ytFetcher := fetcher.NewRateLimited(
			fetcher.NewYouTube(fetcher.YouTubeOptions{
				Bin:      cfg.Fetcher.YouTube.Bin,
				Timeout:  time.Duration(cfg.Fetcher.YouTube.TimeoutSeconds) * time.Second,
				SubLangs: cfg.Fetcher.YouTube.SubLangs,
				Log:      slog.Default(),
			}),
			2, 3, // 2 req/s, burst of 3 — yt-dlp is slow per-call, this mostly limits concurrent starts
		)
		rules = append(rules, fetcher.Rule{Hosts: fetcher.YouTubeHosts, Fetcher: ytFetcher})
		// Registry holds the rate-limited wrapper so token-bucket state
		// survives rule reloads.
		registry[ytFetcher.Name()] = ytFetcher
		slog.Info("youtube fetcher enabled", "bin", cfg.Fetcher.YouTube.Bin)
	}

	dispatcher := fetcher.NewRulesDispatcher(fetcher.RulesDispatcherOptions{
		Path:         home.FetcherRulesPath(),
		Registry:     registry,
		DefaultRules: rules,
		Fallback:     defaultFetcher,
		Log:          slog.Default(),
	})

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

	// Two worker pools: fetch (network-bound, scale wide) and index
	// (Ollama-bound, narrow). They share the JobQueue but each pool's
	// workers only claim jobs of its kind. Without this split, FIFO
	// claim order let fetch jobs starve indexing entirely — measured
	// 3296 fetches done while only 55 index jobs completed.
	deps := jobs.Deps{
		Home:        home,
		Documents:   docs,
		Extractions: exts,
		Bookmarks:   bms,
		Chunks:      chunks,
		Queue:       queue,
		Dispatcher:  dispatcher,
		Indexer:     idx,
		Log:         slog.Default(),
	}
	fetchWorker := jobs.NewWorker(queue, jobs.WorkerOptions{Log: slog.Default()})
	fetchWorker.Register(store.JobKindFetch, jobs.FetchHandler(deps))
	fetchWorker.OnPermanentFailure(store.JobKindFetch, jobs.MarkDocFailed(deps))

	indexWorker := jobs.NewWorker(queue, jobs.WorkerOptions{Log: slog.Default()})
	indexWorker.Register(store.JobKindIndex, jobs.IndexHandler(deps))
	indexWorker.OnPermanentFailure(store.JobKindIndex, jobs.MarkDocFailed(deps))

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

	// Spawn the fetch and index pools + API server. First error from any
	// goroutine cancels the shared ctx.
	nFetch := cfg.Daemon.FetchWorkers
	nIndex := cfg.Daemon.IndexWorkers
	total := nFetch + nIndex + 1
	slog.Info("starting worker pools", "fetch", nFetch, "index", nIndex)

	errCh := make(chan error, total)
	for i := 0; i < nFetch; i++ {
		go func() { errCh <- fetchWorker.Run(ctx) }()
	}
	for i := 0; i < nIndex; i++ {
		go func() { errCh <- indexWorker.Run(ctx) }()
	}
	go func() { errCh <- srv.Run(ctx) }()

	err = <-errCh
	stop()
	// Drain the remaining goroutines so we don't leak on shutdown.
	for i := 0; i < total-1; i++ {
		<-errCh
	}

	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
