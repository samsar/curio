package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/version"
)

// workerDrainTimeout bounds how long shutdown waits for running jobs after
// the API has stopped. Handlers see the cancellation immediately; one that
// ignores it is abandoned, and its job is recovered as an orphan on the next
// start. With the API's 5s graceful shutdown the whole budget is 20s, which
// daemonctl's stop timeout must exceed.
const workerDrainTimeout = 15 * time.Second

func main() {
	logLevel := new(slog.LevelVar)
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	// SIGHUP too: the daemon has no reload path, and a hangup should shut it
	// down cleanly rather than kill it mid-job.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	err := run(ctx, logLevel)
	stop()
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("daemon exited with error", "err", err)
		os.Exit(1)
	}
}

// run is the whole daemon: it returns when ctx is cancelled (nil) or when
// startup or serving fails. The order matters. Nothing touches the database
// until this process holds the home's single-instance lock and has bound the
// API port, so a second daemon, or one that can't serve, exits without
// disturbing the jobs of the one already running.
func run(ctx context.Context, logLevel *slog.LevelVar) error {
	home, err := openHome()
	if err != nil {
		return err
	}
	lock, err := daemonctl.AcquireLock(home)
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			slog.Warn("release daemon lock", "err", err)
		}
	}()

	cfg, err := config.Load(home.ConfigPath())
	if err != nil {
		return err
	}
	logLevel.Set(cfg.Daemon.SlogLevel())

	meta, err := checkMarker(home, cfg)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", cfg.Daemon.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w (is another program using the port? "+
			"check `curio daemon status`, or set a different daemon.listen in %s)",
			cfg.Daemon.Listen, err, home.ConfigPath())
	}
	defer ln.Close()

	db, err := sqlitestore.Open(home.DBPath())
	if err != nil {
		return err
	}
	defer db.Close()
	if err := sqlitestore.Migrate(db); err != nil {
		return err
	}
	slog.Info("database ready", "path", home.DBPath())
	syncMarkerSchemaVersion(home, db, meta)

	d, err := newDaemon(ctx, cfg, home, db)
	if err != nil {
		return err
	}
	// Settle the previous daemon's unfinished jobs before any worker can
	// claim them.
	for _, p := range d.pools {
		if err := p.worker.RecoverOrphans(ctx); err != nil {
			return err
		}
	}

	srv, err := api.NewServer(ln, d.apiDeps)
	if err != nil {
		return err
	}
	slog.Info("curio-daemon starting", "version", version.String(), "home", home.Path, "pid", os.Getpid())
	return d.serve(ctx, srv)
}

// openHome resolves $CURIO_HOME, initializing it on first run.
func openHome() (*curiohome.Home, error) {
	homePath, err := curiohome.Resolve()
	if err != nil {
		return nil, err
	}
	home, err := curiohome.Open(homePath)
	if !errors.Is(err, curiohome.ErrNotInitialized) {
		return home, err
	}
	slog.Info("initializing curio home", "path", homePath)
	defaults := config.Default().Embedding
	return curiohome.Init(homePath, defaults.Model, defaults.Dim)
}

// checkMarker cross-checks the marker file against config. If they
// disagree, the user changed the embedding config without reindexing.
func checkMarker(home *curiohome.Home, cfg config.Config) (curiohome.Meta, error) {
	meta, err := home.Meta()
	if err != nil {
		return curiohome.Meta{}, err
	}
	if meta.EmbeddingModel != cfg.Embedding.Model || meta.EmbeddingDim != cfg.Embedding.Dim {
		slog.Warn("embedding model/dim mismatch between config and marker",
			"config_model", cfg.Embedding.Model,
			"config_dim", cfg.Embedding.Dim,
			"marker_model", meta.EmbeddingModel,
			"marker_dim", meta.EmbeddingDim,
		)
		slog.Warn("run `curio reindex --reason=model-swap` (not yet implemented) before continuing")
		return curiohome.Meta{}, errors.New("embedding config/marker mismatch")
	}
	return meta, nil
}

// syncMarkerSchemaVersion copies the schema version the migrations landed
// at into the marker file, so /v1/healthz reflects reality after upgrades.
func syncMarkerSchemaVersion(home *curiohome.Home, db *sqlitestore.DB, meta curiohome.Meta) {
	v, err := sqlitestore.ReadSchemaVersion(db)
	if err != nil {
		slog.Warn("read schema version", "err", err)
		return
	}
	if v <= 0 || v == meta.SchemaVersion {
		return
	}
	meta.SchemaVersion = v
	if err := home.WriteMeta(meta); err != nil {
		slog.Warn("failed to update marker schema_version", "err", err)
	}
}

// pool is a Worker and how many goroutines run it.
type pool struct {
	name   string
	worker *jobs.Worker
	size   int
}

// daemon is everything run starts once the database is ready.
type daemon struct {
	apiDeps api.Deps
	pools   []pool
}

func newDaemon(ctx context.Context, cfg config.Config, home *curiohome.Home, db *sqlitestore.DB) (*daemon, error) {
	docs := sqlitestore.NewDocuments(db)
	exts := sqlitestore.NewExtractions(db)
	bms := sqlitestore.NewBookmarks(db)
	chunks := sqlitestore.NewChunks(db, cfg.Embedding.Dim)
	queue := sqlitestore.NewJobs(db)
	insights := sqlitestore.NewInsights(db)

	emb, err := embedder.NewOllama(embedder.OllamaOptions{
		BaseURL: cfg.Embedding.BaseURL,
		Model:   cfg.Embedding.Model,
		Dim:     cfg.Embedding.Dim,
		Timeout: time.Duration(cfg.Embedding.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	// Fetch the embedding model in the background if it isn't pulled yet, so a
	// fresh install self-heals instead of failing every index job. Startup
	// isn't blocked; index jobs retry with backoff until it's ready.
	if cfg.Embedding.AutoPull {
		go func() {
			if perr := emb.EnsureModel(ctx, slog.Default()); perr != nil {
				slog.Warn("embedding model not ready; index jobs will retry until it is",
					"model", cfg.Embedding.Model, "err", perr)
			}
		}()
	}

	dispatcher, err := newDispatcher(cfg, home)
	if err != nil {
		return nil, err
	}

	idx := indexer.New(chunks, emb, indexer.Options{
		ChunkSize:      cfg.Chunking.SizeTokens,
		ChunkOverlap:   cfg.Chunking.OverlapTokens,
		DocumentPrefix: cfg.Embedding.DocumentPrefix,
	})
	engine := search.New(chunks, docs, emb, search.Config{
		BM25Weight:   cfg.Search.BM25Weight,
		VectorWeight: cfg.Search.VectorWeight,
		RRFK:         cfg.Search.RRFK,
		Collapse:     search.CollapseStrategy(cfg.Search.Collapse),
		QueryPrefix:  cfg.Embedding.QueryPrefix,
		DefaultK:     cfg.Search.DefaultK,
		EmbedTimeout: time.Duration(cfg.Search.EmbedTimeoutSeconds) * time.Second,
		Log:          slog.Default(),
	})

	insightEngine, err := newInsightEngine(ctx, cfg, docs, chunks, insights)
	if err != nil {
		return nil, err
	}

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
		Insight:     insightEngine,
		Log:         slog.Default(),
	}
	fetchWorker := jobs.NewWorker(queue, jobs.WorkerOptions{Log: slog.Default()})
	fetchWorker.Register(store.JobKindFetch, jobs.FetchHandler(deps))
	fetchWorker.OnPermanentFailure(store.JobKindFetch, jobs.MarkDocFailed(deps))

	indexWorker := jobs.NewWorker(queue, jobs.WorkerOptions{Log: slog.Default()})
	indexWorker.Register(store.JobKindIndex, jobs.IndexHandler(deps))
	indexWorker.OnPermanentFailure(store.JobKindIndex, jobs.MarkDocFailed(deps))

	// Clustering is corpus-wide and expensive; give it its own single-worker
	// pool so it neither starves fetch/index nor runs two clusterings at once.
	clusterWorker := jobs.NewWorker(queue, jobs.WorkerOptions{Log: slog.Default()})
	clusterWorker.Register(store.JobKindCluster, jobs.ClusterHandler(deps))

	return &daemon{
		apiDeps: api.Deps{
			Home:           home,
			Documents:      docs,
			Extractions:    exts,
			Bookmarks:      bms,
			Chunks:         chunks,
			Queue:          queue,
			Embedder:       emb,
			Search:         engine,
			Insights:       insights,
			InsightEnabled: cfg.Insight.Enabled,
			TenantID:       "local",
			Log:            slog.Default(),
		},
		pools: []pool{
			{name: "fetch", worker: fetchWorker, size: cfg.Daemon.FetchWorkers},
			{name: "index", worker: indexWorker, size: cfg.Daemon.IndexWorkers},
			{name: "cluster", worker: clusterWorker, size: 1},
		},
	}, nil
}

// newDispatcher builds the fetcher registry and routing rules. Native is
// always constructed (pure Go, no external deps) so fetcher_rules.yaml can
// bind "native" even when web2md is the configured default.
func newDispatcher(cfg config.Config, home *curiohome.Home) (fetcher.Dispatcher, error) {
	nativeFetcher := fetcher.NewNative(fetcher.NativeOptions{
		Timeout:           time.Duration(cfg.Fetcher.Native.TimeoutSeconds) * time.Second,
		UserAgent:         cfg.Fetcher.Native.UserAgent,
		JinaFallback:      cfg.Fetcher.Native.JinaFallback,
		JinaBaseURL:       cfg.Fetcher.Native.JinaBaseURL,
		JinaAPIKey:        cfg.Fetcher.Native.JinaAPIKey,
		DeadLinkDetection: cfg.Fetcher.Native.DeadLinkDetection,
		Backend:           cfg.Fetcher.Native.Backend,
		Log:               slog.Default(),
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
			return nil, err
		}
		defaultFetcher = w2m
	default:
		return nil, fmt.Errorf("unknown fetcher.default %q", cfg.Fetcher.Default)
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
			2, 3, // start rate; concurrent yt-dlp processes are capped inside the fetcher (MaxConcurrent)
		)
		rules = append(rules, fetcher.Rule{Hosts: fetcher.YouTubeHosts, Fetcher: ytFetcher})
		// Registry holds the rate-limited wrapper so token-bucket state
		// survives rule reloads.
		registry[ytFetcher.Name()] = ytFetcher
		slog.Info("youtube fetcher enabled", "bin", cfg.Fetcher.YouTube.Bin)
	}

	return fetcher.NewRulesDispatcher(fetcher.RulesDispatcherOptions{
		Path:         home.FetcherRulesPath(),
		Registry:     registry,
		DefaultRules: rules,
		Fallback:     defaultFetcher,
		Log:          slog.Default(),
	}), nil
}

// newInsightEngine builds the insight layer: cluster documents into labeled
// interests. With insight.labeling = "llm" the LLM labeler is always wired:
// whether Ollama and the model are up is decided at each rebuild, where the
// engine falls back to term labels for any run that can't reach them. A
// startup check would pin that verdict for the life of the process, and the
// CLI often auto-starts the daemon before the Ollama app is running.
func newInsightEngine(ctx context.Context, cfg config.Config, docs store.DocumentStore,
	chunks store.ChunkStore, insights store.InsightStore) (*insight.Engine, error) {
	var llmLabeler insight.Labeler
	if cfg.Insight.Labeling == insight.LabelingLLM {
		gen, err := generator.NewOllama(generator.OllamaOptions{
			BaseURL: cfg.Generation.BaseURL,
			Model:   cfg.Generation.Model,
			Timeout: time.Duration(cfg.Generation.TimeoutSeconds) * time.Second,
		})
		if err != nil {
			return nil, err
		}
		llmLabeler = insight.NewLLMLabeler(gen)
		if cfg.Generation.AutoPull {
			go func() {
				if err := gen.EnsureModel(ctx, slog.Default()); err != nil {
					slog.Warn("generation model not ready and the pull is not retried; cluster labels use "+
						"the term fallback until Ollama serves it (run `ollama pull`, or restart the daemon)",
						"model", cfg.Generation.Model, "err", err)
				}
			}()
		}
	}
	clusterer := insight.NewKNNGraphClusterer(insight.KNNGraphOptions{
		K:              cfg.Insight.KNN,
		MinSimilarity:  cfg.Insight.MinSimilarity,
		MinClusterSize: cfg.Insight.MinClusterSize,
	})
	return insight.New(docs, chunks, insights, clusterer, llmLabeler, insight.Config{
		Labeling:        cfg.Insight.Labeling,
		Center:          cfg.Insight.CenterVectors,
		LabelingTimeout: time.Duration(cfg.Insight.LabelingTimeoutSeconds) * time.Second,
	}, slog.Default()), nil
}

// serve runs the worker pools and the API until ctx is cancelled or the API
// fails, then shuts both down within the documented budget.
func (d *daemon) serve(ctx context.Context, srv *api.Server) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var workers sync.WaitGroup
	for _, p := range d.pools {
		for range p.size {
			workers.Go(func() {
				// Run only ever returns ctx.Err(); shutdown is the only exit.
				_ = p.worker.Run(ctx)
			})
		}
		slog.Info("worker pool started", "pool", p.name, "workers", p.size)
	}

	err := srv.Serve(ctx)
	cancel()
	if stuck, drained := d.drain(&workers, workerDrainTimeout); !drained {
		slog.Warn("jobs still running after the shutdown grace period; exiting anyway "+
			"(the next start requeues them)", "grace", workerDrainTimeout, "job_ids", stuck)
	}
	return err
}

// drain waits up to grace for the worker goroutines to return. If they don't
// all make it, drained is false and stuck lists the jobs still running.
func (d *daemon) drain(workers *sync.WaitGroup, grace time.Duration) (stuck []string, drained bool) {
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil, true
	case <-time.After(grace):
		for _, p := range d.pools {
			stuck = append(stuck, p.worker.InFlight()...)
		}
		return stuck, false
	}
}
