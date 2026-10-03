// Package apitest runs the daemon's real HTTP API on a loopback port, over a
// throwaway database and $CURIO_HOME, for tests of the packages that talk to
// it (internal/client, internal/cli), the way net/http/httptest runs
// servers. It is test support only: depguard keeps production code from
// importing it.
package apitest

import (
	"context"
	"errors"
	"hash/crc32"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/drift"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// TenantID is the tenant the server serves, as a local daemon does.
const TenantID = store.LocalTenantID

// Pools are the worker pool sizes the server's queue gate reports limits
// for: the daemon's defaults.
var Pools = func() jobs.PoolSizes {
	d := config.Default().Daemon
	return jobs.PoolSizes{Fetch: d.FetchWorkers, Index: d.IndexWorkers}
}()

// Server is a running API and the state behind it.
type Server struct {
	URL  string // base URL, http://127.0.0.1:<port>
	Home *curiohome.Home
	DB   *sqlite.DB
	Deps api.Deps
	// Embedder embeds the search engine's queries and AddContent's chunks,
	// at the home's width.
	Embedder Embedder
	// Startup is the progress the server reports until Ready, for a server
	// from StartNotReady: its phase is initializing until a test sets it.
	Startup *api.Startup
	// Interests is the interest scheduler healthz and the interests' next
	// report, unless an opt replaced it: a new home's at first.
	Interests *Interests

	srv *api.Server
}

// Interests is an interest scheduler whose snapshot a test sets, and which
// counts the kicks it is given. Safe for concurrent use.
type Interests struct {
	mu    sync.Mutex
	snap  insight.Snapshot
	kicks int
}

// Snapshot implements api.InterestScheduler.
func (i *Interests) Snapshot() insight.Snapshot {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.snap
}

// Kick implements api.InterestScheduler.
func (i *Interests) Kick() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.kicks++
}

// Set makes s the snapshot reported from now on.
func (i *Interests) Set(s insight.Snapshot) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.snap = s
}

// Kicks is how many kicks the scheduler was given.
func (i *Interests) Kicks() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.kicks
}

// DefaultUI is how the daemon serves the dashboard with config.yaml's
// defaults: on, with remote images off.
var DefaultUI = func() api.UIOptions {
	d := config.Default()
	return api.UIOptions{Enabled: d.Daemon.UI, LoadRemoteImages: d.UI.LoadRemoteImages}
}()

// Start serves the full API on 127.0.0.1:0 until the test ends, with the
// dashboard as DefaultUI serves it. Each opt adjusts the Deps before the
// server starts. The home is a new one with the default embedding model
// and width, and the search engine embeds queries with an Embedder of that
// width, so /v1/search and /related work without Ollama.
func Start(t testing.TB, opts ...func(*api.Deps)) *Server {
	t.Helper()
	return StartUI(t, DefaultUI, opts...)
}

// StartUI is Start with the dashboard served as pages says.
func StartUI(t testing.TB, pages api.UIOptions, opts ...func(*api.Deps)) *Server {
	t.Helper()
	s := start(t, pages, opts...)
	if err := s.Ready(); err != nil {
		t.Fatalf("ready: %v", err)
	}
	return s
}

// StartNotReady is Start for a daemon that is still starting: until Ready,
// every request answers 503 with the starting problem, and /v1/healthz
// names this process and the server's home, as a starting daemon does. The
// database and Deps are there from the start, for seeding.
func StartNotReady(t testing.TB, opts ...func(*api.Deps)) *Server {
	t.Helper()
	return start(t, DefaultUI, opts...)
}

// start serves a starting daemon with the dashboard as pages says.
func start(t testing.TB, pages api.UIOptions, opts ...func(*api.Deps)) *Server {
	t.Helper()
	defaults := config.Default().Embedding
	home, err := curiohome.Init(t.TempDir(), defaults.Model, defaults.Dim)
	if err != nil {
		t.Fatalf("init curio home: %v", err)
	}
	meta, err := home.Meta()
	if err != nil {
		t.Fatalf("read curio home marker: %v", err)
	}
	emb := Embedder{Dim: meta.EmbeddingDim}
	db := sqlitetest.NewDBWithDim(t, emb.Dim)
	quiet := slog.New(slog.DiscardHandler)
	docs := sqlite.NewDocuments(db)
	chunks := sqlite.NewChunks(db, emb.Dim)
	gate, err := jobs.NewQueueGate(context.Background(), sqlite.NewQueueSettings(db), Pools, quiet)
	if err != nil {
		t.Fatalf("queue gate: %v", err)
	}
	interests := &Interests{snap: insight.Snapshot{State: insight.StateNone, RebuildAt: insight.FirstRebuildAt}}
	deps := api.Deps{
		Home:           home,
		Documents:      docs,
		Extractions:    sqlite.NewExtractions(db),
		Bookmarks:      sqlite.NewBookmarks(db),
		Chunks:         chunks,
		Queue:          sqlite.NewJobs(db),
		Search:         search.New(chunks, docs, emb, search.Config{Log: quiet}),
		Insights:       sqlite.NewInsights(db),
		InsightEnabled: true,
		Interests:      interests,
		Gate:           gate,
		TenantID:       TenantID,
		Log:            quiet,
	}
	for _, opt := range opts {
		opt(&deps)
	}

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	startup := api.NewStartup()
	srv, err := api.NewServer(ln, api.ServerConfig{Home: home.Path, Startup: startup, UI: pages, Log: deps.Log})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx) }()
	// Registered after the database's cleanup, so it runs first.
	t.Cleanup(func() {
		closeClientConns()
		cancel()
		if err := <-served; err != nil {
			t.Errorf("serve api: %v", err)
		}
	})
	return &Server{URL: "http://" + ln.Addr().String(), Home: home, DB: db, Deps: deps, Embedder: emb,
		Startup: startup, Interests: interests, srv: srv}
}

// Ready swaps in the full API, as the daemon does once it has started. It
// returns the error rather than failing the test, so a fake daemon can call
// it from whatever goroutine becomes ready.
func (s *Server) Ready() error {
	return s.srv.Ready(s.Deps)
}

// closeClientConns closes the idle connections of http.DefaultTransport,
// which internal/client and the CLI use. Between two quick requests the
// transport can dial a spare connection it then never sends a request on,
// and a graceful shutdown waits 5s before it treats such a connection as
// idle, longer than the API allows itself to drain.
func closeClientConns() {
	http.DefaultClient.CloseIdleConnections()
}

// AddDocument creates a document with no content in state. A failed one
// failed for store.FailureCauseOther and a dead one for a dead link, the
// causes the store requires of them; AddFailedDocument names the cause.
func (s *Server) AddDocument(t testing.TB, url string, state store.DocState) *store.Document {
	t.Helper()
	var cause store.FailureCause
	switch state {
	case store.DocStateFailed:
		cause = store.FailureCauseOther
	case store.DocStateDead:
		cause = store.FailureCauseDeadLink
	case store.DocStatePending, store.DocStateFetched:
	}
	return s.addDocument(t, &store.Document{TenantID: TenantID, URL: url, State: state, FailureCause: cause})
}

// AddFailedDocument creates a document with no content that failed for
// cause: dead for a dead link, failed for any other.
func (s *Server) AddFailedDocument(t testing.TB, url string, cause store.FailureCause) *store.Document {
	t.Helper()
	return s.addDocument(t, &store.Document{TenantID: TenantID, URL: url, FailureCause: cause})
}

func (s *Server) addDocument(t testing.TB, doc *store.Document) *store.Document {
	t.Helper()
	if err := s.Deps.Documents.Create(context.Background(), doc); err != nil {
		t.Fatalf("create document %s: %v", doc.URL, err)
	}
	return doc
}

// AddContent gives doc what a fetch and an index would: markdown on disk, a
// current extraction pointing at it, and one chunk embedded by s.Embedder.
// It leaves the document's state alone.
func (s *Server) AddContent(t testing.TB, doc *store.Document, markdown string) *store.DocumentExtraction {
	t.Helper()
	ctx := context.Background()
	extID := uuid.NewString()
	rel := filepath.Join(doc.ID, extID+".md")
	full := filepath.Join(s.Home.ContentDir(), rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("create content dir: %v", err)
	}
	if err := os.WriteFile(full, []byte(markdown), 0o600); err != nil {
		t.Fatalf("write content: %v", err)
	}
	ext := &store.DocumentExtraction{ID: extID, DocumentID: doc.ID, Fetcher: "apitest",
		Status: store.ExtractionStatusOK, MarkdownPath: &rel, ExtractionMeta: []byte(`{"source":"apitest"}`)}
	if err := s.Deps.Extractions.Create(ctx, ext); err != nil {
		t.Fatalf("create extraction: %v", err)
	}
	if err := s.Deps.Documents.SetCurrentExtraction(ctx, doc.ID, ext.ID); err != nil {
		t.Fatalf("set current extraction: %v", err)
	}
	title := ""
	if doc.Title != nil {
		title = *doc.Title
	}
	chunk := store.ChunkInput{Text: markdown, TokenCount: len(strings.Fields(markdown)), Embedding: s.Embedder.Vector(markdown)}
	if err := s.Deps.Chunks.ReplaceForDocument(ctx, doc.ID, ext.ID, title, nil, []store.ChunkInput{chunk}); err != nil {
		t.Fatalf("index document: %v", err)
	}
	doc.CurrentExtractionID = &ext.ID
	return ext
}

// Interest is an interest a run the helpers commit holds: its label,
// empty for an unlabeled one; its size, at least its members' count; its
// members, most similar first, and its loose fits.
type Interest struct {
	Label   string
	Size    int
	Members []*store.Document
	Loose   []*store.Document
}

// Area is an area a run the helpers commit holds, and its interests.
type Area struct {
	Label     string
	Interests []Interest
}

// RunSpec is a run's grouping: areas holding interests, or interests
// alone (the flat shape), and the documents in none, nearest the first
// interest.
type RunSpec struct {
	Areas     []Area
	Interests []Interest
	Unsorted  []*store.Document
}

// Run is a run the helpers committed: its ID, and the identities of its
// areas and interests in the order its spec gave them.
type Run struct {
	ID        string
	Areas     []string
	Interests []string
}

// AddInterest commits a flat run whose one interest, labeled label, has
// docs as members: an interest as the API serves it.
func (s *Server) AddInterest(t testing.TB, label string, docs ...*store.Document) Run {
	t.Helper()
	return s.AddRun(t, RunSpec{Interests: []Interest{{Label: label, Members: docs}}})
}

// AddInterests commits a flat run of interests: enough for a page that
// lists interests by size.
func (s *Server) AddInterests(t testing.TB, interests ...Interest) Run {
	t.Helper()
	return s.AddRun(t, RunSpec{Interests: interests})
}

// AddAreas commits a run of areas holding interests.
func (s *Server) AddAreas(t testing.TB, areas ...Area) Run {
	t.Helper()
	return s.AddRun(t, RunSpec{Areas: areas})
}

// AddRun commits spec as a new grouping, built on the latest done run as
// a rebuild is, which retires every identity before it, and prunes the
// other runs, as the engine does.
func (s *Server) AddRun(t testing.TB, spec RunSpec) Run {
	t.Helper()
	shape := store.InterestShapeFlat
	if len(spec.Areas) > 0 {
		shape = store.InterestShapeAreas
	}
	b := s.newCommit(t, store.RunKindFresh, shape)
	out := Run{ID: b.c.RunID}
	for _, in := range spec.Interests {
		out.Interests = append(out.Interests, b.interest(in, ""))
	}
	for _, a := range spec.Areas {
		id := b.identity(store.InterestLevelArea, a.Label)
		group := store.InterestGroup{Interest: store.Interest{ID: id}, Cohesion: 0.4}
		for _, in := range a.Interests {
			out.Interests = append(out.Interests, b.interest(in, id))
			group.Size += max(in.Size, len(in.Members))
			group.Loose += len(in.Loose)
		}
		b.c.Groups = append(b.c.Groups, group)
		out.Areas = append(out.Areas, id)
	}
	for i, doc := range spec.Unsorted {
		a := store.InterestAssignment{DocumentID: doc.ID, Fit: store.InterestFitUnsorted,
			Similarity: 0.3 - 0.01*float64(i), AreaSeed: -1, InterestSeed: -1}
		if len(out.Interests) > 0 {
			a.NearestID = out.Interests[0]
		}
		b.c.Assignments = append(b.c.Assignments, a)
	}
	o := &b.c.Outcome
	o.NumAreas, o.NumInterests, o.Created = len(out.Areas), len(out.Interests), len(out.Interests)
	o.NumUnsorted = len(spec.Unsorted)
	s.commit(t, b)
	return out
}

// SplitInterest commits a rebuild after prev in which interest id split
// into two new interests, a and b, in its area: every other group of prev
// carries over as it was, and the documents nearest id are nearest a. It
// returns the new run, a and b in id's place.
func (s *Server) SplitInterest(t testing.TB, prev Run, id string, a, b Interest) Run {
	t.Helper()
	ctx := context.Background()
	run, err := s.insights().GetRun(ctx, prev.ID)
	if err != nil {
		t.Fatalf("read run %s: %v", prev.ID, err)
	}
	groups, err := s.insights().RunGroups(ctx, prev.ID)
	if err != nil {
		t.Fatalf("read run %s's groups: %v", prev.ID, err)
	}
	assignments, err := s.insights().RunAssignments(ctx, prev.ID)
	if err != nil {
		t.Fatalf("read run %s's assignments: %v", prev.ID, err)
	}
	cb := s.newCommit(t, store.RunKindWarm, run.Shape)
	parent := ""
	for _, g := range groups {
		if g.ID == id {
			parent = g.ParentID
			continue
		}
		cb.c.Groups = append(cb.c.Groups, store.InterestGroup{Interest: store.Interest{ID: g.ID}, ParentID: g.ParentID,
			Size: g.Size, Loose: g.Loose, Cohesion: g.Cohesion, Centroid: g.Centroid})
		if g.Level == store.InterestLevelInterest {
			cb.grouped += g.Size
		}
		cb.c.Lineage = append(cb.c.Lineage, store.LineageRow{OldID: g.ID, NewID: g.ID, Event: store.LineageKept, Shared: g.Size})
	}
	ids := []string{cb.interest(a, parent), cb.interest(b, parent)}
	for i, in := range []Interest{a, b} {
		cb.c.Lineage = append(cb.c.Lineage, store.LineageRow{OldID: id, NewID: ids[i], Event: store.LineageSplit,
			Shared: len(in.Members)})
	}
	for _, as := range assignments {
		switch {
		case as.InterestID == id:
		case as.NearestID == id:
			as.NearestID = ids[0]
			cb.c.Assignments = append(cb.c.Assignments, as)
		default:
			cb.c.Assignments = append(cb.c.Assignments, as)
		}
	}
	o := &cb.c.Outcome
	o.NumAreas, o.NumInterests = run.NumAreas, run.NumInterests+1
	o.NumUnsorted = run.NumUnsorted
	o.Kept, o.Created, o.Split = run.NumInterests-1, 2, 1
	s.commit(t, cb)
	out := Run{ID: cb.c.RunID, Areas: prev.Areas}
	for _, in := range prev.Interests {
		if in == id {
			out.Interests = append(out.Interests, ids...)
			continue
		}
		out.Interests = append(out.Interests, in)
	}
	return out
}

// Place places doc into run's interest, "" for its Unsorted, since the
// run, as the placer writes it: run must be the latest done run.
func (s *Server) Place(t testing.TB, run Run, interest string, doc *store.Document) {
	t.Helper()
	placed, err := s.insights().PlaceDocument(context.Background(), TenantID,
		store.Placement{RunID: run.ID, DocumentID: doc.ID, InterestID: interest, Similarity: 0.5})
	if err != nil {
		t.Fatalf("place document %s: %v", doc.ID, err)
	}
	if !placed {
		t.Fatalf("place document %s: run %s isn't the latest done run, or assigned it", doc.ID, run.ID)
	}
}

// AddFailedRun records a rebuild that failed with msg, the newest run,
// and returns its ID.
func (s *Server) AddFailedRun(t testing.TB, msg string) string {
	t.Helper()
	ctx := context.Background()
	run := s.newRun(t, store.RunKindFresh, store.InterestShapeFlat)
	if err := s.insights().FailRun(ctx, run.ID, 0, msg); err != nil {
		t.Fatalf("fail run: %v", err)
	}
	return run.ID
}

// insights is the server's insight store, unwrapped: the helpers write
// as the engine writes, whatever a test wraps the server's store in.
func (s *Server) insights() store.InsightStore { return sqlite.NewInsights(s.DB) }

// commitBuilder builds a commit the helpers make.
type commitBuilder struct {
	c store.RunCommit
	// grouped counts the members of the commit's interests, which a size
	// can give more of than it has assignments for.
	grouped int
}

// newCommit creates a running run and starts its commit, built on the
// latest done run.
func (s *Server) newCommit(t testing.TB, kind store.RunKind, shape store.InterestShape) *commitBuilder {
	t.Helper()
	run := s.newRun(t, kind, shape)
	b := &commitBuilder{c: store.RunCommit{RunID: run.ID, TenantID: TenantID,
		Outcome: store.RunOutcome{Kind: kind, Shape: shape}}}
	switch prior, err := s.insights().LatestRun(context.Background(), TenantID, store.InterestRunDone); {
	case err == nil:
		b.c.PriorRunID = prior.ID
	case !errors.Is(err, store.ErrNotFound):
		t.Fatalf("read the latest done run: %v", err)
	}
	return b
}

// newRun creates a running run.
func (s *Server) newRun(t testing.TB, kind store.RunKind, shape store.InterestShape) *store.InterestRun {
	t.Helper()
	run := &store.InterestRun{TenantID: TenantID, Trigger: store.RunTriggerManual, Grouper: "apitest",
		RunOutcome: store.RunOutcome{Kind: kind, Shape: shape}}
	if err := s.insights().CreateRun(context.Background(), run); err != nil {
		t.Fatalf("create interest run: %v", err)
	}
	return run
}

// commit commits b's run, counting its documents, and prunes the runs
// before it.
func (s *Server) commit(t testing.TB, b *commitBuilder) {
	t.Helper()
	ctx := context.Background()
	c := b.c
	o := &c.Outcome
	for _, a := range c.Assignments {
		if a.Fit == store.InterestFitLoose {
			o.NumLoose++
		}
	}
	o.NumDocuments = b.grouped + o.NumLoose + o.NumUnsorted
	if err := s.insights().CommitRun(ctx, c); err != nil {
		t.Fatalf("commit interest run: %v", err)
	}
	if err := s.insights().PruneRunsExcept(ctx, TenantID, c.RunID); err != nil {
		t.Fatalf("prune interest runs: %v", err)
	}
}

// identity mints a labeled identity at level and returns its ID.
func (b *commitBuilder) identity(level store.InterestLevel, label string) string {
	in := store.Interest{ID: uuid.NewString(), Level: level, Label: label}
	if label != "" {
		at := time.Now().UTC()
		in.LabelSource, in.LabeledAt, in.Summary = store.LabelSourceLLM, &at, "About "+label+"."
	}
	b.c.NewIdentities = append(b.c.NewIdentities, in)
	return in.ID
}

// interest adds in to the commit, in the area parent ("" for none), its
// members each less similar than the one before, and returns its ID.
func (b *commitBuilder) interest(in Interest, parent string) string {
	id := b.identity(store.InterestLevelInterest, in.Label)
	size := max(in.Size, len(in.Members))
	b.grouped += size
	b.c.Groups = append(b.c.Groups, store.InterestGroup{Interest: store.Interest{ID: id}, ParentID: parent,
		Size: size, Loose: len(in.Loose), Cohesion: 0.7})
	for i, doc := range in.Members {
		b.c.Assignments = append(b.c.Assignments, store.InterestAssignment{DocumentID: doc.ID, InterestID: id,
			AreaID: parent, Fit: store.InterestFitMember, Similarity: 0.9 - 0.05*float64(i), AreaSeed: -1, InterestSeed: -1})
	}
	for i, doc := range in.Loose {
		b.c.Assignments = append(b.c.Assignments, store.InterestAssignment{DocumentID: doc.ID, InterestID: id,
			Fit: store.InterestFitLoose, Similarity: 0.5 - 0.01*float64(i), AreaSeed: -1, InterestSeed: -1})
	}
	return id
}

// Drift is an embedding drift monitor for api.Deps.Drift that reports the
// drift it was given until a rebaseline, which `curio reindex --all`
// triggers, clears it, as the daemon's monitor does once its next check
// records the build serving.
type Drift struct {
	mu     sync.Mutex
	report drift.Report
}

// DriftSample is the comparison a NewDrift's sample found: every chunk
// changed.
var DriftSample = drift.Comparison{Sampled: 64, Changed: 64, MinCosine: 0.9713}

// NewDrift returns a Drift reporting changes, found at checkedAt and
// verified by a sample (DriftSample) compared then.
func NewDrift(checkedAt time.Time, changes ...drift.Change) *Drift {
	return &Drift{report: drift.Report{Changes: changes, CheckedAt: checkedAt,
		Evidence: drift.Evidence{Verified: true, Comparison: DriftSample, At: checkedAt}}}
}

// Unverified makes d's drift one no sample could verify, for reason.
func (d *Drift) Unverified(reason string) *Drift {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.report.Evidence = drift.Evidence{Reason: reason, At: d.report.CheckedAt}
	return d
}

// Report is the drift, until a rebaseline.
func (d *Drift) Report() drift.Report {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.report
}

// Rebaseline clears the drift.
func (d *Drift) Rebaseline() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.report = drift.Report{CheckedAt: time.Now()}
	return nil
}

// Embedder embeds text without a model: every word adds weight to one of
// Dim buckets and the sum is normalized, so texts that share words get
// nearby vectors. That is enough for search and related-document tests to
// rank sensibly offline.
type Embedder struct {
	Dim int // the home's embedding width
}

// Embed returns the Vector of each text.
func (e Embedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = e.Vector(text)
	}
	return out, nil
}

// Vector is the embedding of text: unit length, and the first basis vector
// for text with no words.
func (e Embedder) Vector(text string) []float32 {
	v := make([]float32, e.Dim)
	for word := range strings.FieldsSeq(strings.ToLower(text)) {
		v[crc32.ChecksumIEEE([]byte(word))%uint32(e.Dim)]++ //nolint:gosec // G115: Dim is an embedding width, positive and at most store.MaxEmbeddingDim
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		v[0] = 1
		return v
	}
	norm := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= norm
	}
	return v
}
