// Package apitest runs the daemon's real HTTP API on a loopback port, over a
// throwaway database and $CURIO_HOME, for tests of the packages that talk to
// it (internal/client, internal/cli), the way net/http/httptest runs
// servers. It is test support only: depguard keeps production code from
// importing it.
package apitest

import (
	"context"
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

	srv *api.Server
}

// Start serves the full API on 127.0.0.1:0 until the test ends. Each opt
// adjusts the Deps before the server starts. The home is a new one with the
// default embedding model and width, and the search engine embeds queries
// with an Embedder of that width, so /v1/search and /related work without
// Ollama.
func Start(t testing.TB, opts ...func(*api.Deps)) *Server {
	t.Helper()
	s := StartNotReady(t, opts...)
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
	srv, err := api.NewServer(ln, home.Path, startup, deps.Log)
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
		Startup: startup, srv: srv}
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

// AddDocument creates a document with no content in state.
func (s *Server) AddDocument(t testing.TB, url string, state store.DocState) *store.Document {
	t.Helper()
	doc := &store.Document{TenantID: TenantID, URL: url, State: state}
	if err := s.Deps.Documents.Create(context.Background(), doc); err != nil {
		t.Fatalf("create document %s: %v", url, err)
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

// AddInterest records a finished clustering run whose one cluster, labeled
// label, has docs as members: an interest as the API serves it.
func (s *Server) AddInterest(t testing.TB, label string, docs ...*store.Document) *store.Cluster {
	t.Helper()
	ctx := context.Background()
	ins := s.Deps.Insights
	run := &store.ClusterRun{TenantID: TenantID, Algo: "apitest"}
	if err := ins.CreateRun(ctx, run); err != nil {
		t.Fatalf("create cluster run: %v", err)
	}
	c := store.Cluster{ID: uuid.NewString(), TenantID: TenantID, RunID: run.ID, Label: &label,
		Size: len(docs), Cohesion: 0.9}
	members := make([]store.ClusterMember, len(docs))
	for i, d := range docs {
		members[i] = store.ClusterMember{ClusterID: c.ID, DocumentID: d.ID, Similarity: 0.9 - 0.1*float64(i)}
	}
	if err := ins.ReplaceClusters(ctx, run.ID, []store.ClusterWithMembers{{Cluster: c, Members: members}}); err != nil {
		t.Fatalf("write clusters: %v", err)
	}
	res := store.RunResult{Status: store.ClusterRunDone, NumDocuments: len(docs), NumClusters: 1}
	if err := ins.FinishRun(ctx, run.ID, res); err != nil {
		t.Fatalf("finish cluster run: %v", err)
	}
	return &c
}

// Drift is an embedding drift monitor for api.Deps.Drift that reports the
// drift it was given until a rebaseline, which `curio reindex --all`
// triggers, clears it, as the daemon's monitor does once its next check
// records the build serving.
type Drift struct {
	mu     sync.Mutex
	report drift.Report
}

// NewDrift returns a Drift reporting changes, found at checkedAt.
func NewDrift(checkedAt time.Time, changes ...drift.Change) *Drift {
	return &Drift{report: drift.Report{Changes: changes, CheckedAt: checkedAt}}
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
