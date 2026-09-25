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

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Daemon.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w (is another program using the port? "+
			"check `curio daemon status`, or set a different daemon.listen in %s)",
			cfg.Daemon.Listen, err, home.ConfigPath())
	}
	// Once serving starts, Shutdown closes the listener and this second Close
	// only reports that; it matters when startup fails before then.
	defer func() { _ = ln.Close() }()

	db, err := sqlitestore.Open(ctx, home.DBPath())
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			slog.Warn("close database", "path", home.DBPath(), "err", err)
		}
	}()
	// Logged first because a migration that rewrites a large table can
	// outlast the CLI's auto-start wait, and the log tail should say why.
	slog.Info("migrating database", "path", home.DBPath())
	schemaVersion, err := sqlitestore.Migrate(ctx, db)
	if err != nil {
		return err
	}
	slog.Info("database ready", "path", home.DBPath(), "schema_version", schemaVersion)
	syncMarkerSchemaVersion(home, meta, int(schemaVersion))

	d, err := newDaemon(ctx, cfg, home, db)
	if err != nil {
		return err
	}
	// Settle the previous daemon's unfinished jobs before any worker can
	// claim them.
	for _, p := range d.pools {
		if err := p.Worker.RecoverOrphans(ctx); err != nil {
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

// checkMarker refuses to start when config.yaml's embedding model or
// dimension differs from the ones the home was created with, recorded in
// the marker: every stored vector came from that model, and searching them
// with another model's query vectors returns noise.
func checkMarker(home *curiohome.Home, cfg config.Config) (curiohome.Meta, error) {
	meta, err := home.Meta()
	if err != nil {
		return curiohome.Meta{}, err
	}
	if meta.EmbeddingModel == cfg.Embedding.Model && meta.EmbeddingDim == cfg.Embedding.Dim {
		return meta, nil
	}
	return curiohome.Meta{}, fmt.Errorf("embedding model mismatch: %s sets embedding.model %q (dim %d), "+
		"but this home's vectors were made with %q (dim %d), as recorded in %s. "+
		"Set embedding.model and embedding.dim back to the recorded values, or use a different CURIO_HOME; "+
		"switching an existing home's embedding model isn't supported "+
		`(see docs/decisions.md "Embedding model swap")`,
		home.ConfigPath(), cfg.Embedding.Model, cfg.Embedding.Dim,
		meta.EmbeddingModel, meta.EmbeddingDim, home.MarkerPath())
}

// syncMarkerSchemaVersion copies the schema version the migrations left the
// database at into the marker file, which caches it for /v1/healthz and the
// offline `curio version` and `curio doctor`.
func syncMarkerSchemaVersion(home *curiohome.Home, meta curiohome.Meta, schemaVersion int) {
	if schemaVersion == meta.SchemaVersion {
		return
	}
	meta.SchemaVersion = schemaVersion
	if err := home.WriteMeta(meta); err != nil {
		slog.Warn("failed to update marker schema_version", "err", err)
	}
}

// daemon is everything run starts once the database is ready.
type daemon struct {
	apiDeps api.Deps
	pools   []jobs.Pool
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
	// Pull the embedding model in the background, retrying until Ollama
	// serves it, so a fresh install self-heals instead of failing every index
	// job. Startup isn't blocked; index jobs retry with backoff meanwhile.
	if cfg.Embedding.AutoPull {
		go emb.Client().KeepPulled(ctx, slog.With("used_for", "embeddings"))
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

	pools := jobs.NewPools(jobs.Deps{
		Home:        home,
		Documents:   docs,
		Extractions: exts,
		Bookmarks:   bms,
		Queue:       queue,
		Dispatcher:  dispatcher,
		Indexer:     idx,
		Insight:     insightEngine,
		Log:         slog.Default(),
	}, jobs.PoolSizes{Fetch: cfg.Daemon.FetchWorkers, Index: cfg.Daemon.IndexWorkers},
		jobs.WorkerOptions{Log: slog.Default()})

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
			Log:            slog.Default(),
		},
		pools: pools,
	}, nil
}

// YouTube pacing: yt-dlp runs start at 2 per second, from a token bucket of
// 3, so a few videos saved together aren't serialized but an import full of
// them doesn't hammer YouTube. How many run at once is capped separately,
// by YouTubeOptions.MaxConcurrent.
const (
	youtubeFetchesPerSecond = 2
	youtubeBurst            = 3
)

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
			youtubeFetchesPerSecond, youtubeBurst,
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
		// Cluster labels use the term fallback until the model is ready.
		if cfg.Generation.AutoPull {
			go gen.Client().KeepPulled(ctx, slog.With("used_for", "cluster labels"))
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
		for range p.Size {
			workers.Go(func() {
				// Run only ever returns ctx.Err(); shutdown is the only exit.
				_ = p.Worker.Run(ctx)
			})
		}
		slog.Info("worker pool started", "pool", p.Name, "workers", p.Size)
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
			stuck = append(stuck, p.Worker.InFlight()...)
		}
		return stuck, false
	}
}
