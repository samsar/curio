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
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/drift"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/keepawake"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/understand"
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
	// The log goes to stdout. A client that spawns the daemon sends both
	// streams to daemon.log; its launchd agent sends stdout there and
	// stderr to launchd.err, which so gets only what the runtime writes as
	// the process dies (a panic's trace, a fatal error).
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	// SIGHUP too: the daemon has no reload path, and a hangup should shut it
	// down cleanly rather than kill it mid-job.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	err := run(ctx, logLevel)
	// Read before stop, which cancels ctx itself: only a signal has
	// cancelled it by now, and its cause names the signal.
	signalled := ctx.Err() != nil
	cause := context.Cause(ctx)
	stop()
	os.Exit(finish(err, signalled, cause))
}

// finish logs how run ended and returns the daemon's exit status. The
// status is what the launchd agent's KeepAlive {SuccessfulExit: false}
// acts on: launchd restarts a daemon that exits non-zero, after its 10s
// throttle, and leaves one that exits 0 stopped. So the daemon exits 0
// when a restart would change nothing: it was told to stop (even if its
// shutdown failed part way, say a request outlived the API's 5s grace),
// another daemon already serves the home, or it refused to start for a
// cause that stays until someone fixes it (a refusal: a config.yaml it
// can't load, a home it won't serve). A refusal is logged once, as an
// error saying the daemon stays down until then. Whatever might clear by
// itself (a taken port, a database it can't open or migrate) or is a crash
// exits 1, and launchd's throttled retries pick up the recovery.
func finish(err error, signalled bool, cause error) int {
	var refused *refusal
	switch {
	case errors.Is(err, daemonctl.ErrAlreadyRunning):
		slog.Warn("another curio-daemon already serves this home; exiting "+
			"(to replace the running build with this one, run `curio daemon stop` first)", "err", err)
	case errors.As(err, &refused):
		slog.Error("curio-daemon refuses to start, and stays down until the cause is fixed; "+
			"then a curio command (or `curio up`) starts it", "err", err)
	case err != nil && !errors.Is(err, context.Canceled):
		slog.Error("daemon exited with error", "err", err)
	}
	if signalled {
		slog.Info("curio-daemon stopped", "cause", cause.Error())
	}
	return exitCode(err, signalled)
}

// exitCode is finish's status for run's err: 0 for a clean or requested
// end, another daemon serving the home, or a refusal, and 1 otherwise.
func exitCode(err error, signalled bool) int {
	var refused *refusal
	switch {
	case err == nil, signalled, errors.Is(err, context.Canceled), errors.Is(err, daemonctl.ErrAlreadyRunning),
		errors.As(err, &refused):
		return 0
	default:
		return 1
	}
}

// refusal is the daemon refusing to start for a cause that can't clear by
// itself: a home it can't open or create, a config.yaml it can't read,
// parse or validate, a home whose embedding contract config.yaml breaks,
// or a vector index of another width than the home's. Relaunching it
// would only fail the same way, so it exits 0 (see finish). run marks the
// refusals where it knows them, never by matching messages.
type refusal struct{ err error }

func (r *refusal) Error() string { return r.err.Error() }
func (r *refusal) Unwrap() error { return r.err }

// run is the whole daemon: it returns when ctx is cancelled (nil) or when
// startup or serving fails. The order matters. Nothing touches the database
// until this process holds the home's single-instance lock and has bound the
// API port, so a second daemon, or one that can't serve, exits without
// disturbing the jobs of the one already running. From the bind on, the API
// answers: as a starting daemon, reporting its progress, until everything
// the full API needs is ready. A migration can take minutes on a large
// library, and clients wait on those answers rather than on a silent port.
func run(ctx context.Context, logLevel *slog.LevelVar) error {
	began := time.Now()
	home, err := openHome()
	if err != nil {
		return &refusal{err}
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
		return &refusal{err}
	}
	logLevel.Set(cfg.Daemon.SlogLevel())
	if keys := cfg.DeprecatedKeys(); len(keys) > 0 {
		slog.Warn("config: these keys no longer do anything and are ignored; remove them from config.yaml",
			"keys", keys, "config", home.ConfigPath())
	}

	// A legacy home, or a config.yaml asking for another embedding model or
	// width than the home's, is refused before anything touches the
	// database.
	meta, err := home.CheckEmbedding(cfg.Embedding.Model, cfg.Embedding.Dim)
	if err != nil {
		return &refusal{err}
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Daemon.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w (is another program using the port? "+
			"check `curio daemon status`, or set a different daemon.listen in %s)",
			cfg.Daemon.Listen, err, home.ConfigPath())
	}
	// Serve closes the listener when it returns; this matters only when
	// NewServer fails, and otherwise just reports it closed already.
	defer func() { _ = ln.Close() }()
	startup := api.NewStartup()
	srv, err := api.NewServer(ln, api.ServerConfig{Home: home.Path, Startup: startup, UI: uiOptions(cfg),
		Log: slog.Default()})
	if err != nil {
		return err
	}
	slog.Info("curio-daemon starting", "version", version.String(), "home", home.Path, "pid", os.Getpid())
	if cfg.Daemon.UI {
		slog.Info("dashboard", "url", "http://"+ln.Addr().String()+"/ui/")
	}
	serving := serveAPI(ctx, srv)

	db, err := sqlitestore.Open(ctx, home.DBPath())
	if err != nil {
		return errors.Join(err, serving.stop())
	}
	// Every return from here on has stopped serving first, so no handler is
	// using the database when it closes.
	defer func() {
		if err := db.Close(); err != nil {
			slog.Warn("close database", "path", home.DBPath(), "err", err)
		}
	}()
	d, err := start(ctx, cfg, home, meta, db, startup)
	if err != nil {
		return errors.Join(err, serving.stop())
	}
	if err := srv.Ready(d.apiDeps); err != nil {
		return errors.Join(err, serving.stop())
	}
	slog.Info("curio-daemon ready", "startup_ms", time.Since(began).Milliseconds())
	return d.serve(ctx, serving)
}

// uiOptions is how cfg says to serve the dashboard.
func uiOptions(cfg config.Config) api.UIOptions {
	return api.UIOptions{Enabled: cfg.Daemon.UI, LoadRemoteImages: cfg.UI.LoadRemoteImages}
}

// start brings the database up to date and builds everything the full API
// and the workers need, reporting its progress through startup. Everything
// that holds vectors is sized from the marker's embedding width, which
// config.yaml was checked against.
func start(ctx context.Context, cfg config.Config, home *curiohome.Home, meta curiohome.Meta,
	db *sqlitestore.DB, startup *api.Startup) (*daemon, error) {
	schemaVersion, err := sqlitestore.MigrateWithHooks(ctx, db, migrationHooks(home, startup))
	if err != nil {
		return nil, err
	}
	if err := sqlitestore.EnsureVectorIndex(ctx, db, meta.EmbeddingDim); err != nil {
		if _, ok := errors.AsType[*sqlitestore.VectorWidthError](err); ok {
			return nil, &refusal{err}
		}
		return nil, err
	}
	fields, err := ensureDocumentFields(ctx, db)
	if err != nil {
		return nil, err
	}
	saved, err := ensureSavedDates(ctx, db)
	if err != nil {
		return nil, err
	}
	// sqlite-vec's build flags say whether a released binary has its NEON
	// distance kernels. Only this log line reads them, so a sqlite-vec that
	// words its vec_debug() differently costs the line its flags, not the
	// daemon its start.
	vecVersion, vecBuild, err := sqlitestore.VectorExtension(ctx, db)
	if err != nil {
		slog.Warn("read sqlite-vec's build", "err", err)
	}
	slog.Info("database ready", "path", home.DBPath(), "schema_version", schemaVersion,
		"embedding_dim", meta.EmbeddingDim, "sqlite_vec", vecVersion, "sqlite_vec_build", vecBuild)
	// Before the full API is up, so its first healthz reports the new
	// version.
	syncMarkerSchemaVersion(home, meta, int(schemaVersion))
	startup.SetInitializing()

	d, err := newDaemon(ctx, cfg, home, meta.EmbeddingDim, db, started{fields: fields, savedDates: saved})
	if err != nil {
		return nil, err
	}
	// Settle the previous daemon's unfinished jobs before any worker can
	// claim them.
	for _, p := range d.pools {
		if err := p.Worker.RecoverOrphans(ctx); err != nil {
			return nil, err
		}
	}
	// After the orphans: a re-embed job a crash left running is pending
	// again, and isn't queued twice. Before the interests' check, so it
	// sees the fresh rebuild the re-embed owes.
	if d.reembedding, err = startReembed(ctx, d.reembed, fields); err != nil {
		return nil, err
	}
	// One check of the interests before the full API is up, so its first
	// healthz has the scheduler's state. A rebuild that is due (a library
	// whose interests an upgrade dropped) is queued once the drift monitor
	// has its first verdict, by the scheduler's Run (DriftChecked). A check
	// is best effort: one that can't read warns and the daemon starts
	// anyway.
	if d.scheduler != nil {
		d.scheduler.Check(ctx)
	}
	return d, nil
}

// startReembed begins the re-embed of the documents whose chunks were
// embedded under an older chunk context (jobs.Reembed.Start), and reports
// whether there is one for serve to feed. Not on a start whose search
// fields couldn't be checked: a chunk's context names its document's site
// by those fields, so a document left without its row would have its bare
// host embedded for good; the re-embed waits for a start that derives them,
// with one WARN. Like ensureDocumentFields, it never fails the start but
// for a shutdown during it: a daemon that can't begin the re-embed still
// serves everything, searching the chunks as they are, and the next start
// tries again.
func startReembed(ctx context.Context, r *jobs.Reembed, fields api.DocumentFields) (bool, error) {
	if fields.Error != "" {
		slog.Warn("chunk context: the re-embed of the library's chunks waits for a start that can derive the " +
			"documents' search fields, which their context names their site by; the next start begins it")
		return false, nil
	}
	began, err := r.Start(ctx)
	switch {
	case err != nil && ctx.Err() != nil:
		return false, err
	case err != nil:
		slog.Error("chunk context: couldn't begin the re-embed of the library's chunks; search reads them as "+
			"they are, and the next start tries again", "err", err)
		return false, nil
	}
	return began, nil
}

// ensureDocumentFields makes the documents' search fields right before any
// search reads them (sqlite.EnsureDocumentFields): it repairs their index
// when it disagrees with them, and derives the fields a new home, an
// upgrade or a change of the derivation's rules left missing. It never
// fails the start but for a shutdown during it: a daemon that can't do
// this still serves everything but a complete search, where failing would
// leave launchd restarting it every 10 s for as long as the fault lasts,
// serving nothing. What it found is reported on healthz, for status and
// doctor, and the next start tries again.
func ensureDocumentFields(ctx context.Context, db *sqlitestore.DB) (api.DocumentFields, error) {
	done, err := sqlitestore.EnsureDocumentFields(ctx, db)
	fields := api.DocumentFields{CheckedAt: time.Now(), Rebuilt: done.Rebuilt}
	if done.Rebuilt {
		slog.Warn("document fields: their search index didn't match them; rebuilt it from them")
	}
	switch {
	case err != nil && ctx.Err() != nil:
		return api.DocumentFields{}, err
	case err != nil:
		slog.Error("document fields: couldn't check or derive the documents' search fields; search may miss "+
			"documents until a start does, and the next start tries again", "err", err)
		fields.Error = err.Error()
		return fields, nil
	}
	slog.Info("document fields refreshed", "rebuilt", done.Rebuilt, "documents", done.Documents, "hosts", done.Hosts,
		"cards_stale", done.CardsStale, "took_ms", done.Took.Milliseconds())
	return fields, nil
}

// ensureSavedDates classifies how far every bookmark's saved time can be
// trusted (sqlite.EnsureSavedDates) before any search filters by it: what
// applies a change of the rule, finishes an import cut short or a
// classification that failed, and derives the rows an older daemon wrote,
// marking its Safari rows dateless first. Like ensureDocumentFields it
// never fails the start but for a shutdown during it: a date filter then
// reads the classes the bookmarks have, healthz reports the failure for
// status and doctor, and the next start tries again.
func ensureSavedDates(ctx context.Context, db *sqlitestore.DB) (api.SavedDates, error) {
	done, err := sqlitestore.EnsureSavedDates(ctx, db)
	saved := api.SavedDates{CheckedAt: time.Now(), Changed: done.Changed}
	if len(done.Skipped) > 0 {
		slog.Warn("saved dates: bookmarks whose saved or stored time doesn't parse are classified unknown",
			"skipped", len(done.Skipped), "ids", done.Skipped[:min(len(done.Skipped), skippedIDsLogged)])
	}
	switch {
	case err != nil && ctx.Err() != nil:
		return api.SavedDates{}, err
	case err != nil:
		slog.Error("saved dates: couldn't classify the bookmarks' saved dates; date filters read the classes "+
			"they have until a start does, and the next start tries again", "err", err)
		saved.Error = err.Error()
		return saved, nil
	}
	slog.Info("saved dates classified", "bookmarks", done.Bookmarks, "changed", done.Changed, "exact", done.Exact,
		"approx", done.Approx, "unknown", done.Unknown, "marked_dateless", done.MarkedDateless,
		"took_ms", done.Took.Milliseconds())
	return saved, nil
}

// skippedIDsLogged caps the IDs the start's log line names of the
// bookmarks whose times don't parse; the count says how many there are.
const skippedIDsLogged = 10

// migrationHooks log each migration and report it through startup, which
// the starting daemon's healthz answer shows. Only a database that already
// had a schema counts as migrating: creating a new one takes milliseconds,
// and clients would tell the user to wait for nothing.
func migrationHooks(home *curiohome.Home, startup *api.Startup) sqlitestore.MigrationHooks {
	return sqlitestore.MigrationHooks{
		Pending: func(current int64, pending []sqlitestore.Migration) {
			slog.Info("migrating database", "path", home.DBPath(), "pending", len(pending),
				"from_version", current, "to_version", pending[len(pending)-1].Version)
			if current > 0 {
				startup.SetMigrating(len(pending))
			}
		},
		Applying: func(m sqlitestore.Migration) {
			slog.Info("applying migration", "version", m.Version, "source", m.Source)
		},
		Applied: func(m sqlitestore.Migration, took time.Duration) {
			slog.Info("migration applied", "version", m.Version, "source", m.Source,
				"duration_ms", took.Milliseconds())
			startup.MigrationApplied()
		},
	}
}

// servingAPI is the API serving in the background, from the bind until run
// returns.
type servingAPI struct {
	cancel context.CancelFunc
	done   <-chan error // Serve's result; received exactly once, by wait
}

// serveAPI runs srv.Serve until ctx is cancelled or stop is called.
func serveAPI(ctx context.Context, srv *api.Server) *servingAPI {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	return &servingAPI{cancel: cancel, done: done}
}

// wait returns once Serve has: on its own, when the listener fails, or
// after a shutdown.
func (s *servingAPI) wait() error {
	defer s.cancel()
	return <-s.done
}

// stop shuts the API down and waits for Serve to return, the listener
// closed.
func (s *servingAPI) stop() error {
	s.cancel()
	return s.wait()
}

// openHome resolves $CURIO_HOME, initializing it on first run with the
// default embedding model and width.
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
	apiDeps    api.Deps
	pools      []jobs.Pool
	drift      *drift.Monitor
	keeper     *keepawake.Keeper
	scheduler  *insight.Scheduler // nil with insight off
	savedDates *api.SavedDateClassifier
	planner    *planner // nil while search.understand is off
	// hold holds the index and cards jobs back while the embedding model
	// doesn't answer.
	hold *jobs.EmbedderHold
	// reembed feeds the re-embed for a new chunk context, which serve runs
	// while reembedding: start began one.
	reembed     *jobs.Reembed
	reembedding bool
}

// started is what the start did to the derived rows, which healthz
// reports: the documents' search fields and the bookmarks' saved dates.
type started struct {
	fields     api.DocumentFields
	savedDates api.SavedDates
}

// newDaemon builds the stores, clients, engines and pools over db, whose
// vector index is dim wide. Its classifications of an import's saved
// dates stop when ctx, the daemon's lifetime, ends.
func newDaemon(ctx context.Context, cfg config.Config, home *curiohome.Home, dim int,
	db *sqlitestore.DB, done started) (*daemon, error) {
	docs := sqlitestore.NewDocuments(db)
	exts := sqlitestore.NewExtractions(db)
	bms := sqlitestore.NewBookmarks(db)
	chunks := sqlitestore.NewChunks(db, dim)
	index := sqlitestore.NewDocumentIndex(db, dim)
	queue := sqlitestore.NewJobs(db)
	insights := sqlitestore.NewInsights(db)

	// Loaded before any worker or background pull exists, so a stored pause
	// holds from the first claim and a failure to read it stops the start
	// before anything runs: running with the queue open would break a pause
	// the user set.
	sizes := jobs.PoolSizes{Fetch: cfg.Daemon.FetchWorkers, Index: cfg.Daemon.IndexWorkers}
	gate, err := jobs.NewQueueGate(ctx, sqlitestore.NewQueueSettings(db), sizes, slog.Default())
	if err != nil {
		return nil, err
	}

	emb, err := embedder.NewOllama(embedder.OllamaOptions{
		BaseURL: cfg.Embedding.BaseURL,
		Model:   cfg.Embedding.Model,
		Dim:     dim,
		Timeout: time.Duration(cfg.Embedding.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	if err := keepPulled(ctx, pullTargets(cfg), slog.Default()); err != nil {
		return nil, err
	}

	native := newNativeFetcher(cfg)
	dispatcher, routed, err := newDispatcher(cfg, home, native)
	if err != nil {
		return nil, err
	}

	idx := indexer.New(chunks, emb, indexer.Options{
		ChunkSize:      cfg.Chunking.SizeTokens,
		ChunkOverlap:   cfg.Chunking.OverlapTokens,
		DocumentPrefix: cfg.Embedding.DocumentPrefix,
		Log:            slog.Default(),
	})
	engine := search.New(chunks, docs, index, emb, search.ConfigFrom(cfg))
	// Built only while its role is in use: with search.understand off, a
	// search that asks to be understood is told it is off.
	var planner *planner
	if slices.Contains(cfg.RolesInUse(), config.RolePlanner) {
		if planner, err = newPlanner(ctx, cfg); err != nil {
			return nil, err
		}
	}

	// A change of build is verified by re-embedding a sample through the
	// indexer, so the requests are the ones that made the stored vectors,
	// sent by the same embedder.
	sampler, err := drift.NewSampler(chunks, idx, time.Duration(cfg.Embedding.TimeoutSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	// Built after start's marker writes: from here on the monitor is the
	// marker's only writer.
	driftMonitor := drift.New(home, emb.Client(), sampler, slog.Default())
	drifted := driftHold(driftMonitor)
	checked := driftChecked(driftMonitor.Report, time.Now(), time.Now)

	// The hold's probe is one short embed, through the embedder the jobs
	// use, so it reopens the hold only for an Ollama that embeds.
	hold := jobs.NewEmbedderHold(func(ctx context.Context) error {
		_, err := emb.Embed(ctx, []string{"curio"})
		return err
	}, slog.Default())
	reembed := jobs.NewReembed(jobs.ReembedOptions{Documents: docs, Insights: insights, TenantID: store.LocalTenantID,
		Log: slog.Default()})
	jobDeps := jobs.Deps{
		ContentDir:    home.ContentDir(),
		Documents:     docs,
		Extractions:   exts,
		Bookmarks:     bms,
		Queue:         queue,
		Dispatcher:    dispatcher,
		Indexer:       idx,
		Chunks:        chunks,
		DocumentIndex: index,
		Hold:          hold,
		Reembed:       reembed,
		Log:           slog.Default(),
	}
	// With insight off nothing is rebuilt or placed on its own: there is
	// no placer and no scheduler, and a rebuild can't be asked for.
	var (
		placer    *insight.Placer
		scheduler *insight.Scheduler
	)
	if cfg.Insight.Enabled {
		placer = insight.NewPlacer(insights, chunks, insight.PlacerOptions{Drift: placementHold(drifted, checked),
			MapOff: !cfg.Insight.Map, Log: slog.Default()})
		jobDeps.Placer = placer
	}
	insightEngine, err := newInsightEngine(cfg, docs, chunks, insights, queue, placer, drifted)
	if err != nil {
		return nil, err
	}
	jobDeps.Insight = insightEngine
	if cfg.Insight.Enabled {
		scheduler, err = newScheduler(insights, docs, queue, insightEngine, placer, drifted, checked, !cfg.Insight.Map)
		if err != nil {
			return nil, err
		}
		jobDeps.KickInterests = scheduler.Kick
	}

	pools := jobs.NewPools(jobDeps, sizes, jobs.WorkerOptions{Gate: jobs.All(gate, hold), Log: slog.Default()})
	// The Jina fallback is the one upstream whose health is tracked.
	upstreams := func() []fetcher.UpstreamHealth { return []fetcher.UpstreamHealth{native.JinaHealth()} }
	keeper := newKeeper(gate, queue, pools)
	savedDates := api.NewSavedDateClassifier(ctx, api.SavedDateOptions{Bookmarks: bms, TenantID: store.LocalTenantID,
		Start: done.savedDates, Log: slog.Default()})

	apiDeps := api.Deps{
		Home:            home,
		Documents:       docs,
		Extractions:     exts,
		Bookmarks:       bms,
		Chunks:          chunks,
		Index:           index,
		Queue:           queue,
		Embedder:        emb,
		GenerationModel: cfg.Generation.Model,
		GenerationRoles: generationRoles(cfg),
		Search:          engine,
		Insights:        insights,
		InsightEnabled:  cfg.Insight.Enabled,
		MapOff:          !cfg.Insight.Map,
		Upstreams:       upstreams,
		Gate:            gate,
		Drift:           driftMonitor,
		KeepAwake:       keeper,
		YouTubeFetcher:  routed.ytdlp,
		GitHubToken:     routed.githubToken,
		DocumentFields:  done.fields,
		SavedDates:      savedDates,
		Reembed:         reembed,
		Hold:            hold,
		Log:             slog.Default(),
	}
	if scheduler != nil {
		apiDeps.Interests = scheduler
	}
	if planner != nil {
		apiDeps.Planner = planner
	}
	return &daemon{apiDeps: apiDeps, pools: pools, drift: driftMonitor, keeper: keeper, scheduler: scheduler,
		savedDates: savedDates, planner: planner, hold: hold, reembed: reembed}, nil
}

// planner is search's planner, and the end of its background work's
// context, so serve stops it however serving ended.
type planner struct {
	*understand.Planner
	stop context.CancelFunc
}

// newPlanner builds search's planner, writing with the planner role's
// model. Its background work, loading and checking that model, runs on a
// context of ctx, the daemon's lifetime, never a request's: a request
// cancelled during a cold load would abort the load itself. A provider
// without structured output gets a planner that answers every question
// off, logged once.
func newPlanner(ctx context.Context, cfg config.Config) (*planner, error) {
	gen, err := generator.New(cfg.Generation.Role(config.RolePlanner))
	if err != nil {
		return nil, err
	}
	lifetime, stop := context.WithCancel(ctx)
	return &planner{Planner: understand.New(lifetime, gen, understand.OptionsFrom(cfg)), stop: stop}, nil
}

// driftHold says how the embeddings drifted, as the monitor last reported
// (holdReason): what holds automatic rebuilds and placement, and what a
// run built meanwhile notes.
func driftHold(m *drift.Monitor) func() string {
	return func() string { return holdReason(m.Report()) }
}

// driftCheckGrace is how long after the daemon starts the interests wait
// for the embedding check's first verdict. The monitor keeps its report in
// memory, so until it concludes a check a drift that predates the start
// can't be seen; past the grace (Ollama down, say, where the check can't
// conclude but grouping needs no Ollama), rebuilds and placement go ahead.
const driftCheckGrace = 10 * time.Minute

// driftChecked reports whether the drift monitor (its Report) has
// concluded a check since started, or driftCheckGrace has passed by now
// without one.
func driftChecked(report func() drift.Report, started time.Time, now func() time.Time) func() bool {
	return func() bool {
		return !report().CheckedAt.IsZero() || now().Sub(started) >= driftCheckGrace
	}
}

// placementHold is what holds placement: a drift, or the embedding check
// not yet concluded since the daemon started, which the placer, holding
// silently, treats alike.
func placementHold(drifted func() string, checked func() bool) func() string {
	return func() string {
		if reason := drifted(); reason != "" {
			return reason
		}
		if !checked() {
			return "the embedding check hasn't concluded since the daemon started"
		}
		return ""
	}
}

// holdReason says how the embeddings drifted by r, "" while they haven't.
func holdReason(r drift.Report) string {
	switch {
	case !r.Drifted():
		return ""
	case r.Evidence.Verified:
		return "the embeddings drifted"
	default:
		return "the embeddings may have drifted"
	}
}

// newScheduler builds the interest scheduler over the stores, queuing
// rebuilds through the queue, on the timing schedulerConfig gives; mapOff
// is insight.map: false.
func newScheduler(insights store.InsightStore, docs store.DocumentStore, queue store.JobStore, engine *insight.Engine,
	placer *insight.Placer, drifted func() string, checked func() bool, mapOff bool) (*insight.Scheduler, error) {
	timing, err := schedulerConfig()
	if err != nil {
		return nil, err
	}
	return insight.NewScheduler(insight.SchedulerOptions{
		TenantID: store.LocalTenantID,
		Library:  insight.NewLibrary(insights, docs, queue),
		Enqueue: func(ctx context.Context, tenantID string, trigger store.RunTrigger) (*store.Job, bool, error) {
			return jobs.EnqueueRebuild(ctx, queue, tenantID, trigger)
		},
		Drift:         drifted,
		DriftChecked:  checked,
		ParamsChanged: engine.ParamsChanged,
		MapOff:        mapOff,
		Placer:        placer,
		Config:        timing,
		Log:           slog.Default(),
	}), nil
}

// newKeeper builds the keep-awake keeper over the queue gate's settings
// and the jobs of the pools' kinds. It holds this process's pid for
// caffeinate -w, so a hold never outlives the daemon.
func newKeeper(gate *jobs.QueueGate, queue store.JobStore, pools []jobs.Pool) *keepawake.Keeper {
	kinds := make([]store.JobKind, 0, len(pools))
	for _, p := range pools {
		kinds = append(kinds, p.Kind)
	}
	return keepawake.New(keepawake.Options{
		Settings: gate,
		Jobs:     queue,
		Kinds:    kinds,
		Probe:    keepawake.Pmset{},
		Asserter: keepawake.Caffeinate{PID: os.Getpid()},
		Log:      slog.Default(),
	})
}

// YouTube pacing: yt-dlp runs start at 2 per second, from a token bucket of
// 3, so a few videos saved together aren't serialized but an import full of
// them doesn't hammer YouTube. How many run at once is capped separately,
// by YouTubeOptions.MaxConcurrent.
const (
	youtubeFetchesPerSecond = 2
	youtubeBurst            = 3
)

// newNativeFetcher builds the Native fetcher. It is always built (pure Go,
// no external deps), so fetcher_rules.yaml can bind "native" even when
// web2md is the configured default, and healthz can report its Jina
// fallback.
func newNativeFetcher(cfg config.Config) *fetcher.Native {
	return fetcher.NewNative(fetcher.NativeOptions{
		Timeout:                   time.Duration(cfg.Fetcher.Native.TimeoutSeconds) * time.Second,
		UserAgent:                 cfg.Fetcher.Native.UserAgent,
		JinaFallback:              cfg.Fetcher.Native.JinaFallback,
		JinaBaseURL:               cfg.Fetcher.Native.JinaBaseURL,
		JinaAPIKey:                cfg.Fetcher.Native.JinaAPIKey,
		JinaSiteRequestsPerMinute: cfg.Fetcher.Native.JinaSiteRequestsPerMinute,
		DeadLinkDetection:         cfg.Fetcher.Native.DeadLinkDetection,
		Backend:                   cfg.Fetcher.Native.Backend,
		Log:                       slog.Default(),
	})
}

// routes is what healthz reports about how the dispatcher routes pages.
type routes struct {
	// ytdlp is the yt-dlp YouTube videos go to, as LookPath found it, or
	// empty when they go to the default fetcher.
	ytdlp string
	// githubToken: the GitHub fetcher sends a token.
	githubToken bool
}

// newDispatcher builds the fetcher registry and routing rules around
// nativeFetcher, and says how it routes.
func newDispatcher(cfg config.Config, home *curiohome.Home, nativeFetcher *fetcher.Native) (fetcher.Dispatcher, routes, error) {
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
			return nil, routes{}, err
		}
		defaultFetcher = w2m
	default:
		return nil, routes{}, fmt.Errorf("unknown fetcher.default %q", cfg.Fetcher.Default)
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

	ytdlp, err := exec.LookPath(cfg.Fetcher.YouTube.Bin)
	if err == nil {
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
	} else {
		// Said out loud: a launchd agent's PATH without Homebrew's
		// directories would otherwise lose YouTube silently.
		slog.Info("youtube fetcher disabled: yt-dlp not found", "bin", cfg.Fetcher.YouTube.Bin, "path", os.Getenv("PATH"))
		ytdlp = ""
	}

	return fetcher.NewRulesDispatcher(fetcher.RulesDispatcherOptions{
		Path:         home.FetcherRulesPath(),
		Registry:     registry,
		DefaultRules: rules,
		Fallback:     defaultFetcher,
		Log:          slog.Default(),
	}), routes{ytdlp: ytdlp, githubToken: ghFetcher.HasToken()}, nil
}

// pullTarget is a model the daemon keeps pulled on one Ollama server.
type pullTarget struct {
	baseURL string
	model   string
	// usedFor names every use of the model, joined: "embeddings, interest
	// labels".
	usedFor string
	// timeout is the first use's per-request timeout, which bounds the
	// check whether the model is there; a pull is bounded by its progress.
	timeout time.Duration
}

// pullTargets are the models the daemon keeps pulled: the embedding model
// with embedding.auto_pull, then, with generation.auto_pull, the writing
// model of each role in use that pulls alone (config.Role.PullsAlone),
// each pulled once however many uses share it on one server. A role that
// doesn't pull alone, the planner, joins a target another use pulls
// already, and pulls nothing of its own: curio up pulls its model, with
// consent. A role not in use pulls nothing: its multi-gigabyte model
// would serve no one.
func pullTargets(cfg config.Config) []pullTarget {
	var out []pullTarget
	find := func(baseURL, model string) int {
		for i := range out {
			if out[i].baseURL == strings.TrimRight(baseURL, "/") && out[i].model == model {
				return i
			}
		}
		return -1
	}
	add := func(baseURL, model, usedFor string, timeoutSeconds int) {
		if i := find(baseURL, model); i >= 0 {
			out[i].usedFor += ", " + usedFor
			return
		}
		out = append(out, pullTarget{baseURL: strings.TrimRight(baseURL, "/"), model: model, usedFor: usedFor,
			timeout: time.Duration(timeoutSeconds) * time.Second})
	}
	if cfg.Embedding.AutoPull {
		add(cfg.Embedding.BaseURL, cfg.Embedding.Model, "embeddings", cfg.Embedding.TimeoutSeconds)
	}
	if !cfg.Generation.AutoPull {
		return out
	}
	var riders []config.Role
	for _, r := range cfg.RolesInUse() {
		spec := cfg.Generation.Role(r)
		switch {
		case spec.Provider != config.ProviderOllama:
		case r.PullsAlone():
			add(spec.BaseURL, spec.Model, r.UsedFor(), spec.TimeoutSeconds)
		default:
			riders = append(riders, r)
		}
	}
	for _, r := range riders {
		spec := cfg.Generation.Role(r)
		if i := find(spec.BaseURL, spec.Model); i >= 0 {
			out[i].usedFor += ", " + r.UsedFor()
		}
	}
	return out
}

// keepPulled pulls each target in the background, retrying until Ollama
// serves it, so a fresh install self-heals instead of failing every index
// job. Startup isn't blocked: index jobs retry with backoff meanwhile, and
// interest labels use the term fallback until their model is ready.
func keepPulled(ctx context.Context, targets []pullTarget, log *slog.Logger) error {
	for _, t := range targets {
		c, err := ollama.New(t.baseURL, t.model, t.timeout)
		if err != nil {
			return fmt.Errorf("keep %s pulled for %s: %w", t.model, t.usedFor, err)
		}
		go c.KeepPulled(ctx, log.With("used_for", t.usedFor))
	}
	return nil
}

// generationRoles maps each role in use to the model it writes with, for
// healthz.
func generationRoles(cfg config.Config) map[string]string {
	out := map[string]string{}
	for _, r := range cfg.RolesInUse() {
		out[string(r)] = cfg.Generation.Role(r).Model
	}
	return out
}

// newInsightEngine builds the insight layer: group documents into labeled
// areas and interests with the Louvain grouper, placing the documents
// indexed during a rebuild with placer (nil places none) once it commits,
// and asking queue whether a re-embedding still drains.
// While the labels role is in use the LLM labeler is always wired: whether
// Ollama and the model are up is decided at each rebuild, where the engine
// falls back to term labels for any run that can't reach them. A startup
// check would pin that verdict for the life of the process, and the CLI
// often auto-starts the daemon before the Ollama app is running.
func newInsightEngine(cfg config.Config, docs store.DocumentStore, chunks store.ChunkStore,
	insights store.InsightStore, queue store.JobStore, placer *insight.Placer, drifted func() string,
) (*insight.Engine, error) {
	var llmLabeler insight.Labeler
	if slices.Contains(cfg.RolesInUse(), config.RoleLabels) {
		gen, err := generator.New(cfg.Generation.Role(config.RoleLabels))
		if err != nil {
			return nil, err
		}
		llmLabeler = insight.NewLLMLabeler(gen)
	}
	return insight.New(docs, chunks, insights, insight.NewLouvainGrouper(slog.Default()), llmLabeler, insight.Config{
		Labeling:        cfg.Insight.Labeling,
		Center:          cfg.Insight.CenterVectors,
		LabelingTimeout: time.Duration(cfg.Insight.LabelingTimeoutSeconds) * time.Second,
		Placer:          placer,
		Drift:           drifted,
		Indexing:        indexing(queue),
		MapOff:          !cfg.Insight.Map,
	}, slog.Default()), nil
}

// indexing reports whether queue holds index jobs pending or running.
func indexing(queue store.JobStore) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		counts, err := queue.QueueCounts(ctx)
		if err != nil {
			return false, err
		}
		index := counts[store.JobKindIndex]
		return index.Pending+index.Running > 0, nil
	}
}

// serve runs the worker pools, the embedding drift monitor, the interest
// scheduler, the keep-awake keeper, the retries of the saved-date
// classifications owed, the embedder hold's probes and the re-embed's feed
// alongside the API until ctx is cancelled or the API fails, then shuts
// them down within the documented budget: the keeper releases its hold as
// its context ends, and the planner's background work stops with its own.
func (d *daemon) serve(ctx context.Context, served *servingAPI) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var workers sync.WaitGroup
	workers.Go(func() { d.drift.Run(ctx) })
	workers.Go(func() { d.keeper.Run(ctx) })
	workers.Go(func() { d.savedDates.Run(ctx) })
	if d.scheduler != nil {
		workers.Go(func() { d.scheduler.Run(ctx) })
	}
	workers.Go(func() { d.hold.Run(ctx) })
	if d.reembedding {
		workers.Go(func() { d.reembed.Run(ctx) })
	}
	for _, p := range d.pools {
		for range p.Size {
			workers.Go(func() {
				// Run only ever returns ctx.Err(); shutdown is the only exit.
				_ = p.Worker.Run(ctx)
			})
		}
		slog.Info("worker pool started", "pool", p.Name, "workers", p.Size)
	}

	err := served.wait()
	cancel()
	if d.planner != nil {
		// Its work ends with its context: an Ollama request it has in
		// flight is abandoned at once.
		d.planner.stop()
		d.planner.Wait()
	}
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
