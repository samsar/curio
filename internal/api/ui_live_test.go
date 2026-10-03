package api_test

import (
	"cmp"
	"context"
	"encoding/json"
	"html"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xhtml "golang.org/x/net/html"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// These tests drive the pages' live parts over the real API: what each
// poll reads and renders, the controls each page offers and the state
// they show, and what the pollers find in the answers.

// reads counts the store and embedder reads the pages make, by name.
type reads struct {
	mu sync.Mutex
	n  map[string]int
	// jobs are the options of each ListWithDoc.
	jobs []store.ListJobsOpts
}

func (r *reads) add(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n[name]++
}

// take returns the counts so far and starts over.
func (r *reads) take() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.n
	r.n = map[string]int{}
	return n
}

// countReads makes a test server's stores and embedder count their reads
// into r.
func countReads(r *reads) func(*api.Deps) {
	r.n = map[string]int{}
	return func(d *api.Deps) {
		d.Documents = readDocs{d.Documents, r}
		d.Queue = &readJobs{d.Queue, r}
		d.Extractions = readExtractions{d.Extractions, r}
		d.Bookmarks = readBookmarks{d.Bookmarks, r}
		d.Insights = readInsights{d.Insights, r}
		d.Embedder = pinger{reads: r}
		d.Search = search.New(readChunks{d.Chunks, r}, d.Documents, apitest.Embedder{Dim: config.Default().Embedding.Dim},
			search.Config{Log: slog.New(slog.DiscardHandler)})
	}
}

type readDocs struct {
	store.DocumentStore
	r *reads
}

func (d readDocs) CountByState(ctx context.Context, tenantID string) (map[store.DocState]int, error) {
	d.r.add("stats")
	return d.DocumentStore.CountByState(ctx, tenantID)
}

func (d readDocs) FailureSummary(ctx context.Context, tenantID string, hosts int) (store.FailureSummary, error) {
	d.r.add("failures")
	return d.DocumentStore.FailureSummary(ctx, tenantID, hosts)
}

func (d readDocs) GetWithLastError(ctx context.Context, tenantID, id string) (*store.DocumentWithError, error) {
	d.r.add("document")
	return d.DocumentStore.GetWithLastError(ctx, tenantID, id)
}

func (d readDocs) ListWithLastError(ctx context.Context, tenantID string, opts store.ListDocumentsOpts) ([]store.DocumentWithError, error) {
	d.r.add("documents")
	return d.DocumentStore.ListWithLastError(ctx, tenantID, opts)
}

func (d readDocs) GetByIDsWithLastError(ctx context.Context, tenantID string, ids []string) ([]store.DocumentWithError, error) {
	d.r.add("member documents")
	return d.DocumentStore.GetByIDsWithLastError(ctx, tenantID, ids)
}

type readJobs struct {
	store.JobStore
	r *reads
}

func (j *readJobs) QueueCounts(ctx context.Context) (map[store.JobKind]store.QueueCount, error) {
	j.r.add("queue")
	return j.JobStore.QueueCounts(ctx)
}

func (j *readJobs) MetricsByKind(ctx context.Context, tenantID string, window time.Duration) ([]store.KindMetrics, error) {
	j.r.add("metrics")
	return j.JobStore.MetricsByKind(ctx, tenantID, window)
}

func (j *readJobs) ListWithDoc(ctx context.Context, tenantID string, opts store.ListJobsOpts) ([]store.JobWithDoc, error) {
	j.r.add("jobs")
	j.r.mu.Lock()
	j.r.jobs = append(j.r.jobs, opts)
	j.r.mu.Unlock()
	return j.JobStore.ListWithDoc(ctx, tenantID, opts)
}

type readExtractions struct {
	store.ExtractionStore
	r *reads
}

func (e readExtractions) GetByID(ctx context.Context, id string) (*store.DocumentExtraction, error) {
	e.r.add("extraction")
	return e.ExtractionStore.GetByID(ctx, id)
}

type readBookmarks struct {
	store.BookmarkStore
	r *reads
}

func (b readBookmarks) ListByDocument(ctx context.Context, tenantID, documentID string) ([]*store.Bookmark, error) {
	b.r.add("bookmarks")
	return b.BookmarkStore.ListByDocument(ctx, tenantID, documentID)
}

func (b readBookmarks) List(ctx context.Context, tenantID string, opts store.ListBookmarksOpts) ([]store.BookmarkWithDocument, error) {
	b.r.add("saves")
	return b.BookmarkStore.List(ctx, tenantID, opts)
}

type readInsights struct {
	store.InsightStore
	r *reads
}

func (i readInsights) LatestRun(ctx context.Context, tenantID string, status store.InterestRunStatus) (*store.InterestRun, error) {
	i.r.add(cmp.Or(string(status), "newest") + " run")
	return i.InsightStore.LatestRun(ctx, tenantID, status)
}

func (i readInsights) TopGroups(ctx context.Context, runID string, limit, offset int) ([]store.InterestGroup, error) {
	i.r.add("interests")
	return i.InsightStore.TopGroups(ctx, runID, limit, offset)
}

func (i readInsights) NestedGroups(ctx context.Context, runID string, limit, offset int) ([]store.InterestGroup, error) {
	i.r.add("interests")
	return i.InsightStore.NestedGroups(ctx, runID, limit, offset)
}

func (i readInsights) ChildGroups(ctx context.Context, runID string, areaIDs []string) ([]store.InterestGroup, error) {
	i.r.add("children")
	return i.InsightStore.ChildGroups(ctx, runID, areaIDs)
}

func (i readInsights) Members(ctx context.Context, runID, interestID string, fit store.InterestFit, limit, offset int) ([]store.InterestAssignment, error) {
	i.r.add("members")
	return i.InsightStore.Members(ctx, runID, interestID, fit, limit, offset)
}

func (i readInsights) GetGroup(ctx context.Context, runID, id string) (*store.InterestGroup, error) {
	i.r.add("interest")
	return i.InsightStore.GetGroup(ctx, runID, id)
}

func (i readInsights) GetRun(ctx context.Context, id string) (*store.InterestRun, error) {
	i.r.add("run")
	return i.InsightStore.GetRun(ctx, id)
}

func (i readInsights) PlacementCounts(ctx context.Context, runID string) (map[string]int, error) {
	i.r.add("new counts")
	return i.InsightStore.PlacementCounts(ctx, runID)
}

func (i readInsights) Placements(ctx context.Context, runID, interestID string, limit int) ([]store.Placement, error) {
	i.r.add("new members")
	return i.InsightStore.Placements(ctx, runID, interestID, limit)
}

func (i readInsights) RunLineage(ctx context.Context, runID string) ([]store.LineageRow, error) {
	i.r.add("lineage")
	return i.InsightStore.RunLineage(ctx, runID)
}

func (i readInsights) GetInterests(ctx context.Context, tenantID string, ids []string) ([]store.Interest, error) {
	i.r.add("identities")
	return i.InsightStore.GetInterests(ctx, tenantID, ids)
}

type readChunks struct {
	store.ChunkStore
	r *reads
}

func (c readChunks) EmbeddingsForDocument(ctx context.Context, documentID string) ([]store.ChunkEmbedding, error) {
	c.r.add("related")
	return c.ChunkStore.EmbeddingsForDocument(ctx, documentID)
}

// pinger is an embedder whose Ping, the one call health makes, always
// answers.
type pinger struct {
	embedder.Embedder
	reads *reads
}

func (p pinger) Ping(context.Context) error {
	p.reads.add("ping")
	return nil
}

// TestUI_StatusReads: the page reads every panel; its 2-second poll the
// counts, the queue and its metrics, never health (an Ollama ping) or the
// failures; its health poll health alone. A poll it doesn't take is a 400
// that reads nothing.
func TestUI_StatusReads(t *testing.T) {
	r := &reads{}
	srv := apitest.Start(t, countReads(r))
	srv.AddFailedDocument(t, "https://example.com/blocked", store.FailureCauseAntiBot)
	for _, tc := range []struct {
		path   string
		status int
		want   map[string]int
	}{
		{"/ui/status", http.StatusOK, map[string]int{"stats": 1, "queue": 1, "metrics": 1, "ping": 1, "failures": 1}},
		{"/ui/status?poll=live", http.StatusOK, map[string]int{"stats": 1, "queue": 1, "metrics": 1}},
		{"/ui/status?poll=health", http.StatusOK, map[string]int{"ping": 1}},
		{"/ui/status?poll=jobs", http.StatusBadRequest, map[string]int{}},
		{"/ui/status?poll=%3Cscript%3E", http.StatusBadRequest, map[string]int{}},
	} {
		r.take()
		getPage(t, srv, tc.path, tc.status)
		assert.Equal(t, tc.want, r.take(), tc.path)
	}
}

// TestUI_StatusPolls: each poll's answer is the frame and the panels it
// read, with their live regions: the 2-second poll's has no health, no
// callouts and no failures; the health poll's has those, and no queue.
func TestUI_StatusPolls(t *testing.T) {
	srv := apitest.Start(t, func(d *api.Deps) { d.Embedder = failingPinger{} })
	srv.AddFailedDocument(t, "https://example.com/blocked", store.FailureCauseAntiBot)

	live := getPage(t, srv, "/ui/status?poll=live", http.StatusOK)
	for _, gone := range []string{`id="health"`, `id="attention"`, "Ollama isn't ready", "Why documents failed",
		"data-poll"} {
		assert.NotContains(t, live, gone)
	}
	for _, id := range []string{"library-live", "queue-state", "queue-toggle", "queue-throttle", "queue-pools",
		"progress-live", "jobs-live"} {
		assert.Contains(t, live, `id="`+id+`"`)
	}

	health := getPage(t, srv, "/ui/status?poll=health", http.StatusOK)
	assert.Contains(t, health, `<p class="callout-title">Ollama isn't ready</p>`)
	assert.Contains(t, health, `id="health-live"`)
	for _, gone := range []string{`id="library-live"`, `id="queue-state"`, `id="progress-live"`, "Why documents failed",
		"data-poll"} {
		assert.NotContains(t, health, gone)
	}
	page := getPage(t, srv, "/ui/status", http.StatusOK)
	assert.Contains(t, page, "Why documents failed", "the page reads the failures")
	assert.Equal(t, 2, strings.Count(page, "data-poll"))
}

// failingPinger is an embedder whose Ping fails, as when Ollama is down.
type failingPinger struct{ embedder.Embedder }

func (failingPinger) Ping(context.Context) error { return errInjected }

// pageDoc parses a page.
func pageDoc(t *testing.T, body string) *xhtml.Node {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(body))
	require.NoError(t, err)
	return doc
}

// elementByID is doc's element with id, or nil.
func elementByID(doc *xhtml.Node, id string) *xhtml.Node {
	for n := range doc.Descendants() {
		if n.Type == xhtml.ElementNode && attr(n, "id") == id {
			return n
		}
	}
	return nil
}

func hasAttribute(n *xhtml.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// pollers are a page's pollers: the URL each GETs, and the ids of the
// regions it swaps in.
func pollers(t *testing.T, doc *xhtml.Node) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for n := range doc.Descendants() {
		if n.Type == xhtml.ElementNode && hasAttribute(n, "data-poll") {
			assert.True(t, hasAttribute(n, "hidden"), "a poller is hidden")
			assert.Nil(t, n.FirstChild, "and empty")
			assert.Equal(t, "none", attr(n, "hx-swap"))
			assert.Equal(t, "this:replace", attr(n, "hx-sync"))
			out[attr(n, "hx-get")] = strings.Split(strings.ReplaceAll(attr(n, "hx-select-oob"), "#", ""), ",")
		}
	}
	return out
}

// countIDs counts id in doc.
func countIDs(doc *xhtml.Node, id string) int {
	n := 0
	for e := range doc.Descendants() {
		if e.Type == xhtml.ElementNode && attr(e, "id") == id {
			n++
		}
	}
	return n
}

// TestUI_LiveRegionsExist: every region a poller swaps in is on its page
// once, and in the poller's answer once, whatever is in flight: Status, a
// document whose fetch is queued, the Interests with a rebuild queued, and
// the Failures tab.
func TestUI_LiveRegionsExist(t *testing.T) {
	srv := apitest.Start(t)
	doc := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	_, err := srv.Deps.Documents.RequeueFetch(context.Background(), apitest.TenantID, doc.ID)
	require.NoError(t, err)
	srv.AddInterest(t, "Kafka", doc)
	enqueueRebuild(t, srv)
	srv.AddFailedDocument(t, "https://blocked.example/a", store.FailureCauseAntiBot)
	srv.AddFailedDocument(t, "https://gone.example/a", store.FailureCauseDeadLink)

	for _, path := range []string{"/ui/status", "/ui/documents/" + doc.ID, "/ui/interests", "/ui/failures"} {
		page := pageDoc(t, getPage(t, srv, path, http.StatusOK))
		polls := pollers(t, page)
		require.NotEmpty(t, polls, path)
		for href, regions := range polls {
			answer := pageDoc(t, getPage(t, srv, href, http.StatusOK))
			for _, id := range regions {
				assert.Equal(t, 1, countIDs(page, id), "%s: region %s on the page", path, id)
				assert.Equal(t, 1, countIDs(answer, id), "%s: region %s in %s", path, id, href)
			}
		}
	}
}

// enqueueRebuild queues a rebuild of the interests, as Rebuild does.
func enqueueRebuild(t *testing.T, srv *apitest.Server) *store.Job {
	t.Helper()
	job, _, err := jobs.EnqueueRebuild(context.Background(), srv.Deps.Queue, apitest.TenantID, store.RunTriggerManual)
	require.NoError(t, err)
	return job
}

// putQueue changes the queue's settings through the API, as the page's
// controls do.
func putQueue(t *testing.T, srv *apitest.Server, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, srv.URL+"/v1/queue",
		strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
}

// TestUI_StatusControls: the queue's controls show its settings after each
// change through PUT /v1/queue, and send the changes that would undo them;
// the schedule's time fields sit outside every polled region, so a poll
// never replaces a time being typed.
func TestUI_StatusControls(t *testing.T) {
	srv := apitest.Start(t)
	controls := func() *xhtml.Node {
		t.Helper()
		return pageDoc(t, getPage(t, srv, "/ui/status", http.StatusOK))
	}
	page := controls()
	assert.Equal(t, `{"paused":true}`, attr(elementByID(page, "queue-pause"), "data-body"))
	assert.Equal(t, "true", attr(elementByID(page, "throttle-normal"), "aria-pressed"))
	assert.False(t, hasAttribute(elementByID(page, "keep-awake"), "checked"))
	assert.Empty(t, attr(elementByID(page, "schedule-opens"), "value"))
	assert.Equal(t, "Off: jobs start at any hour.", textContent(elementByID(page, "schedule-state")))
	assert.Equal(t, "Off: jobs wait while the Mac sleeps.", textContent(elementByID(page, "keep-awake-hint")))

	putQueue(t, srv, `{"paused":true,"throttle":"gentle","keep_awake":true,"schedule":"22:00-07:00"}`)
	page = controls()
	pause := elementByID(page, "queue-pause")
	assert.Equal(t, "resume", attr(pause, "data-kind"))
	assert.Equal(t, `{"paused":false}`, attr(pause, "data-body"))
	assert.Equal(t, "false", attr(elementByID(page, "throttle-normal"), "aria-pressed"))
	assert.Equal(t, "true", attr(elementByID(page, "throttle-gentle"), "aria-pressed"))
	assert.True(t, hasAttribute(elementByID(page, "keep-awake"), "checked"))
	assert.Equal(t, "On, not holding the Mac awake: the queue is paused.",
		textContent(elementByID(page, "keep-awake-hint")))
	assert.Equal(t, "22:00", attr(elementByID(page, "schedule-opens"), "value"))
	assert.Equal(t, "07:00", attr(elementByID(page, "schedule-closes"), "value"))
	assert.Equal(t, "Jobs start from 22:00 to 07:00.", textContent(elementByID(page, "schedule-state")))

	regions := map[string]bool{}
	for _, ids := range pollers(t, page) {
		for _, id := range ids {
			regions[id] = true
		}
	}
	for _, field := range []string{"schedule-form", "schedule-opens", "schedule-closes"} {
		for n := elementByID(page, field); n != nil; n = n.Parent {
			assert.False(t, regions[attr(n, "id")], "%s is inside region %s", field, attr(n, "id"))
		}
	}
}

// textContent is n's text, its whitespace collapsed.
func textContent(n *xhtml.Node) string {
	var b strings.Builder
	for d := range n.Descendants() {
		if d.Type == xhtml.TextNode {
			b.WriteString(d.Data)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// TestUI_DocumentActions: each state's controls: Refetch for pending,
// fetched and failed documents; for a dead one, Refetch anyway behind its
// confirm, which forces it; Reindex only with a text.
func TestUI_DocumentActions(t *testing.T) {
	srv := apitest.Start(t)
	fetched := srv.AddDocument(t, "https://example.com/fetched", store.DocStateFetched)
	srv.AddContent(t, fetched, "# Fetched")
	failed := srv.AddFailedDocument(t, "https://example.com/blocked", store.FailureCauseAntiBot)
	dead := srv.AddDocument(t, "https://example.com/gone", store.DocStateDead)
	pending := srv.AddDocument(t, "https://example.com/pending", store.DocStatePending)

	for _, doc := range []*store.Document{fetched, failed, pending} {
		page := pageDoc(t, getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK))
		refetch := elementByID(page, "refetch")
		require.NotNil(t, refetch, doc.State)
		assert.Equal(t, "/v1/documents/"+doc.ID+"/refetch", attr(refetch, "data-path"))
		assert.Equal(t, doc.State == store.DocStateFailed, strings.Contains(attr(refetch, "class"), "btn-primary"))
		assert.Nil(t, elementByID(page, "confirm-refetch"))
	}
	withText := pageDoc(t, getPage(t, srv, "/ui/documents/"+fetched.ID, http.StatusOK))
	assert.Equal(t, "/v1/documents/"+fetched.ID+"/reindex", attr(elementByID(withText, "reindex"), "data-path"))
	assert.Regexp(t, `^Fetched .+ by apitest$`, textContent(elementByID(withText, "doc-status")))
	noText := pageDoc(t, getPage(t, srv, "/ui/documents/"+pending.ID, http.StatusOK))
	assert.Equal(t, "true", attr(elementByID(noText, "reindex"), "aria-disabled"))
	assert.False(t, hasAttribute(elementByID(noText, "reindex"), "data-path"))

	gone := pageDoc(t, getPage(t, srv, "/ui/documents/"+dead.ID, http.StatusOK))
	assert.Nil(t, elementByID(gone, "refetch"))
	assert.Equal(t, "confirm-refetch", attr(elementByID(gone, "refetch-anyway"), "popovertarget"))
	assert.Equal(t, "/v1/documents/"+dead.ID+"/refetch?force=1", attr(elementByID(gone, "refetch-forced"), "data-path"))
}

// pollerHref is the URL of the page's poller whose regions include id.
func pollerHref(t *testing.T, body, id string) string {
	t.Helper()
	for href, regions := range pollers(t, pageDoc(t, body)) {
		if slices.Contains(regions, id) {
			return href
		}
	}
	require.Failf(t, "no poller", "for region %s", id)
	return ""
}

// pollerTrigger is the hx-trigger of the poller with id in body.
func pollerTrigger(t *testing.T, body, id string) string {
	t.Helper()
	n := elementByID(pageDoc(t, body), id)
	require.NotNil(t, n, id)
	return attr(n, "hx-trigger")
}

// TestUI_DocumentJobs: the document's jobs in flight and why the queue
// holds them; its poller polls every 2 seconds exactly while there is work,
// and carries the page's baseline, which a poll's answer keeps.
func TestUI_DocumentJobs(t *testing.T) {
	ctx := context.Background()
	srv := apitest.Start(t)
	doc := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	srv.AddContent(t, doc, "# A")
	body := getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	assert.Equal(t, "curio:changed from:body", pollerTrigger(t, body, "doc-poll"), "nothing in flight")
	assert.Contains(t, body, `<div class="doc-jobs" id="doc-jobs"></div>`)

	_, err := srv.Deps.Documents.RequeueFetch(ctx, apitest.TenantID, doc.ID)
	require.NoError(t, err)
	_, err = srv.Deps.Gate.Update(ctx, jobsPause())
	require.NoError(t, err)
	body = getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	assert.Contains(t, body, `<li>Fetch queued</li>`)
	assert.Contains(t, body, `<a id="doc-jobs-hold" href="/ui/status">The queue is closed: Paused</a>`)
	assert.Equal(t, "every 2s, curio:changed from:body", pollerTrigger(t, body, "doc-poll"))

	_, err = srv.Deps.Gate.Update(ctx, jobs.QueueUpdate{Paused: new(false)})
	require.NoError(t, err)
	claimed, err := srv.Deps.Queue.ClaimNext(ctx, []store.JobKind{store.JobKindFetch})
	require.NoError(t, err)
	body = getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	assert.Contains(t, body, `<li>Fetch running · attempt 1 of 5</li>`)
	assert.NotContains(t, body, "doc-jobs-hold")

	_, err = srv.Deps.Queue.MarkFailed(ctx, claimed.ID, "HTTP 503", true)
	require.NoError(t, err)
	body = getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	assert.Regexp(t, `<li>Fetch waiting, due <time datetime="[^"]+" title="[^"]+">[^<]+</time> · attempt 2 of 5`+
		`<span class="visually-hidden">: </span><span class="job-why" title="HTTP 503">HTTP 503</span></li>`, body)

	_, err = srv.Deps.Queue.ClaimNext(ctx, nil)
	require.ErrorIs(t, err, store.ErrNotFound, "backing off")
	_, err = srv.DB.ExecContext(ctx, `UPDATE jobs SET run_after = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE document_id = ?`, doc.ID)
	require.NoError(t, err)
	claimed, err = srv.Deps.Queue.ClaimNext(ctx, []store.JobKind{store.JobKindFetch})
	require.NoError(t, err)
	require.NoError(t, srv.Deps.Queue.Defer(ctx, claimed.ID, time.Now().Add(24*time.Minute),
		"waiting for GitHub's API rate limit to reset"))
	body = getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	assert.Regexp(t, `<li>Fetch waiting, due <time datetime="[^"]+" title="[^"]+">in 2[34] min</time> · attempt 2 of 5`+
		`<span class="visually-hidden">: </span><span class="job-why" title="waiting for GitHub&#39;s API rate limit to reset">`, body)
}

// TestUI_DocumentPoll: a poll's answer is the jobs' region and its poller,
// which keeps the baseline the poll carried, and says what came of the
// jobs since: a new text, a failure, or another change.
func TestUI_DocumentPoll(t *testing.T) {
	ctx := context.Background()
	srv := apitest.Start(t)
	doc := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	old := srv.AddContent(t, doc, "# A")
	page := getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	href := pollerHref(t, page, "doc-jobs")
	u, err := url.Parse(href)
	require.NoError(t, err)
	assert.Equal(t, "/ui/documents/"+doc.ID, u.Path)
	assert.Equal(t, "jobs", u.Query().Get("poll"))
	assert.Equal(t, old.ID, u.Query().Get("extraction"))

	answer := getPage(t, srv, href, http.StatusOK)
	assert.Contains(t, answer, `<div class="doc-jobs" id="doc-jobs"></div>`, "nothing changed")
	for _, gone := range []string{`<article class="prose">`, `id="related"`, `id="bookmarks"`, `id="details"`, "<h1>"} {
		assert.NotContains(t, answer, gone)
	}
	assert.Equal(t, href, html.UnescapeString(pollerHref(t, answer, "doc-jobs")), "the baseline is kept")

	// Another change: a stale updated time.
	stale := u.Query()
	stale.Set("updated", "2020-01-01T00:00:00.000Z")
	staleHref := u.Path + "?" + stale.Encode()
	answer = getPage(t, srv, staleHref, http.StatusOK)
	assert.Contains(t, answer, `Updated: <a id="doc-reload" href="/ui/documents/`+doc.ID+`">reload</a>`)
	assert.Equal(t, staleHref, html.UnescapeString(pollerHref(t, answer, "doc-jobs")),
		"the poll's own baseline, not the library's")

	// A new text.
	srv.AddContent(t, doc, "# A, refetched")
	answer = getPage(t, srv, href, http.StatusOK)
	assert.Contains(t, answer, `The text changed: <a id="doc-reload" href="/ui/documents/`+doc.ID+`">reload</a>`)

	// A failure.
	failed := srv.AddFailedDocument(t, "https://example.com/blocked", store.FailureCauseAntiBot)
	failedPage := getPage(t, srv, "/ui/documents/"+failed.ID, http.StatusOK)
	failedHref, err := url.Parse(pollerHref(t, failedPage, "doc-jobs"))
	require.NoError(t, err)
	q := failedHref.Query()
	q.Set("updated", "2020-01-01T00:00:00.000Z")
	answer = getPage(t, srv, failedHref.Path+"?"+q.Encode(), http.StatusOK)
	assert.Contains(t, answer, `It failed: <a id="doc-reload" href="/ui/documents/`+failed.ID+`">reload to see why</a>`)

	// While a job is in flight nothing is offered but a new text, and it
	// keeps polling.
	_, err = srv.Deps.Documents.RequeueFetch(ctx, apitest.TenantID, failed.ID)
	require.NoError(t, err)
	answer = getPage(t, srv, failedHref.Path+"?"+q.Encode(), http.StatusOK)
	assert.NotContains(t, answer, "doc-reload")
	assert.Equal(t, "every 2s, curio:changed from:body", pollerTrigger(t, answer, "doc-poll"))
}

// TestUI_DocumentReads: the page reads the document, its text, related
// documents, bookmarks and jobs; its poll the document and its jobs alone,
// never the text, related documents or bookmarks. The jobs are asked for
// by document, a page of them and one more. A poll it doesn't take, or
// without a baseline, is a 400 that reads nothing.
func TestUI_DocumentReads(t *testing.T) {
	r := &reads{}
	srv := apitest.Start(t, countReads(r))
	doc := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	srv.AddContent(t, doc, "# A\n\nthe text")
	page := getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	assert.Equal(t, map[string]int{"document": 1, "extraction": 1, "related": 1, "bookmarks": 1, "jobs": 1}, r.take())
	require.Len(t, r.jobs, 1)
	assert.Equal(t, doc.ID, r.jobs[0].DocumentID)
	assert.GreaterOrEqual(t, r.jobs[0].Limit, 2, "a page of jobs and one more")

	getPage(t, srv, pollerHref(t, page, "doc-jobs"), http.StatusOK)
	assert.Equal(t, map[string]int{"document": 1, "jobs": 1}, r.take())

	_, err := srv.Deps.Documents.RequeueFetch(context.Background(), apitest.TenantID, doc.ID)
	require.NoError(t, err)
	getPage(t, srv, pollerHref(t, page, "doc-jobs"), http.StatusOK)
	assert.Equal(t, map[string]int{"document": 1, "jobs": 1, "queue": 1}, r.take(), "the queue, while a job waits")

	for _, query := range []string{"?poll=live", "?poll=jobs", "?poll=jobs&updated=yesterday",
		"?poll=jobs&updated=2026-09-29"} {
		body := getPage(t, srv, "/ui/documents/"+doc.ID+query, http.StatusBadRequest)
		assert.Contains(t, body, `<div class="big-code">400</div>`, query)
		assert.Empty(t, r.take(), query)
	}
}

// TestUI_LibraryReads: the Library is one list read and the counts in
// either order: the documents in Last updated, the saves in Date saved,
// each row's document from that same read, never a read per row. A query
// it doesn't take reads nothing.
func TestUI_LibraryReads(t *testing.T) {
	r := &reads{}
	srv := apitest.Start(t, countReads(r))
	for i := range 3 {
		doc := srv.AddFailedDocument(t, "https://example.com/"+strconv.Itoa(i), store.FailureCauseAntiBot)
		bookmark(t, srv, doc.URL, store.SourceChrome, "")
		bookmark(t, srv, doc.URL, store.SourceSafari, "")
	}
	for _, tc := range []struct {
		path   string
		status int
		want   map[string]int
	}{
		{"/ui/library", http.StatusOK, map[string]int{"documents": 1, "stats": 1}},
		{"/ui/library?order=updated&state=failed", http.StatusOK, map[string]int{"documents": 1, "stats": 1}},
		{"/ui/library?order=saved", http.StatusOK, map[string]int{"saves": 1, "stats": 1}},
		{"/ui/library?order=saved&state=failed&limit=2", http.StatusOK, map[string]int{"saves": 1, "stats": 1}},
		{"/ui/library?order=bogus", http.StatusBadRequest, map[string]int{}},
		{"/ui/library?order=saved&cursor=bogus", http.StatusBadRequest, map[string]int{}},
	} {
		r.take()
		getPage(t, srv, tc.path, tc.status)
		assert.Equal(t, tc.want, r.take(), tc.path)
	}
}

// TestUI_FailuresReads: the Failures tab reads the failure summary once and
// the library's counts for its lede; its poll the summary alone, once. A
// poll it doesn't take is a 400 that reads nothing and starts over from
// the Library.
func TestUI_FailuresReads(t *testing.T) {
	r := &reads{}
	srv := apitest.Start(t, countReads(r))
	srv.AddFailedDocument(t, "https://blocked.example/a", store.FailureCauseAntiBot)
	for _, tc := range []struct {
		path   string
		status int
		want   map[string]int
	}{
		{"/ui/failures", http.StatusOK, map[string]int{"stats": 1, "failures": 1}},
		{"/ui/failures?poll=causes", http.StatusOK, map[string]int{"failures": 1}},
		{"/ui/failures?poll=live", http.StatusBadRequest, map[string]int{}},
		{"/ui/failures?poll=%3Cscript%3E", http.StatusBadRequest, map[string]int{}},
	} {
		r.take()
		body := getPage(t, srv, tc.path, tc.status)
		assert.Equal(t, tc.want, r.take(), tc.path)
		if tc.status == http.StatusBadRequest {
			assert.Contains(t, body, `<a class="btn btn-primary" href="/ui/library">Start over</a>`, tc.path)
		}
	}
}

// postChange sends a control's change as actions.js sends a button's
// without a body, and returns the status and the answer's jobs_enqueued.
func postChange(t *testing.T, srv *apitest.Server, control *xhtml.Node) (int, int) {
	t.Helper()
	require.Equal(t, http.MethodPost, attr(control, "data-method"))
	require.False(t, hasAttribute(control, "data-body"))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+attr(control, "data-path"), nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var got struct {
		JobsEnqueued int `json:"jobs_enqueued"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	return resp.StatusCode, got.JobsEnqueued
}

// TestUI_FailuresRefetch: a group's Refetch, sent as actions.js sends it,
// requeues every document of its cause, as many as its label says, and the
// poll that follows no longer shows the group, its documents in neither the
// totals nor the subnav's count; the dead links' confirm does the same for
// them. A stale repeat queues nothing.
func TestUI_FailuresRefetch(t *testing.T) {
	srv := apitest.Start(t)
	for _, u := range []string{"https://blocked.example/1", "https://blocked.example/2", "https://walled.example/3"} {
		srv.AddFailedDocument(t, u, store.FailureCauseAntiBot)
	}
	srv.AddFailedDocument(t, "https://login.example/a", store.FailureCauseLoginWall)
	srv.AddFailedDocument(t, "https://gone.example/a", store.FailureCauseDeadLink)
	srv.AddFailedDocument(t, "https://gone.example/b", store.FailureCauseDeadLink)

	body := getPage(t, srv, "/ui/failures", http.StatusOK)
	page := pageDoc(t, body)
	assert.Contains(t, body, "6 documents couldn&#39;t be fetched: 4 failed and 2 dead links, grouped by why.")
	blocked := elementByID(page, "refetch-anti_bot")
	require.NotNil(t, blocked)
	assert.Equal(t, "Refetch 3", textContent(blocked))
	status, n := postChange(t, srv, blocked)
	assert.Equal(t, http.StatusAccepted, status)
	assert.Equal(t, 3, n, "every document the label counts")

	poll := pollerHref(t, body, "failures-live")
	answer := getPage(t, srv, poll, http.StatusOK)
	doc := pageDoc(t, answer)
	assert.Nil(t, elementByID(doc, "cause-anti_bot"), "an emptied group's card is gone")
	assert.NotNil(t, elementByID(doc, "cause-login_wall"))
	assert.Contains(t, answer, "3 documents couldn&#39;t be fetched: 1 failed and 2 dead links, grouped by why.")
	assert.Equal(t, "Failures 3", textContent(elementByID(doc, "subnav-failures")))
	assert.Contains(t, answer, "Causes without documents: Blocked by bot protection, ")

	confirm := elementByID(page, "refetch-dead_link-confirm")
	require.NotNil(t, confirm)
	status, n = postChange(t, srv, confirm)
	assert.Equal(t, http.StatusAccepted, status)
	assert.Equal(t, 2, n)
	doc = pageDoc(t, getPage(t, srv, poll, http.StatusOK))
	assert.Nil(t, elementByID(doc, "cause-dead_link"))
	assert.Equal(t, "Failures 1", textContent(elementByID(doc, "subnav-failures")))

	status, n = postChange(t, srv, blocked)
	assert.Equal(t, http.StatusAccepted, status)
	assert.Zero(t, n, "a stale repeat finds nothing to queue")
}

// TestUI_InterestsRebuild: the Rebuild button, disabled while a rebuild is
// queued or running; the state of one, and why the queue holds it; then
// what the newest run came to: its interests ready to load, or its
// failure. The poller polls every 2 seconds exactly while one is in
// flight.
func TestUI_InterestsRebuild(t *testing.T) {
	ctx := context.Background()
	srv := apitest.Start(t)
	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	srv.AddInterest(t, "Kafka", a)

	body := getPage(t, srv, "/ui/interests", http.StatusOK)
	rebuild := elementByID(pageDoc(t, body), "rebuild")
	require.NotNil(t, rebuild)
	assert.Equal(t, "/v1/interests/rebuild", attr(rebuild, "data-path"))
	assert.False(t, hasAttribute(rebuild, "aria-disabled"))
	assert.Equal(t, "curio:changed from:body", pollerTrigger(t, body, "rebuild-poll"))
	href := pollerHref(t, body, "rebuild-state")

	job := enqueueRebuild(t, srv)
	_, err := srv.Deps.Gate.Update(ctx, jobsPause())
	require.NoError(t, err)
	answer := getPage(t, srv, href, http.StatusOK)
	assert.Contains(t, answer, `<p>Rebuild queued · <a id="rebuild-hold" href="/ui/status">the queue is closed: Paused</a></p>`)
	assert.Equal(t, "true", attr(elementByID(pageDoc(t, answer), "rebuild"), "aria-disabled"))
	assert.Equal(t, "every 2s, curio:changed from:body", pollerTrigger(t, answer, "rebuild-poll"))

	_, err = srv.Deps.Gate.Update(ctx, jobs.QueueUpdate{Paused: new(false)})
	require.NoError(t, err)
	claimed, err := srv.Deps.Queue.ClaimNext(ctx, []store.JobKind{store.JobKindCluster})
	require.NoError(t, err)
	require.Equal(t, job.ID, claimed.ID)
	answer = getPage(t, srv, href, http.StatusOK)
	assert.Regexp(t, `<p>Rebuilding · started <time[^>]*>just now</time></p>`, answer)

	require.NoError(t, srv.Deps.Queue.MarkDone(ctx, claimed.ID))
	srv.AddInterest(t, "Kafka streams", a)
	answer = getPage(t, srv, href, http.StatusOK)
	assert.Contains(t, answer, `<p>New interests are ready: <a id="rebuild-reload" href="/ui/interests">reload</a></p>`)
	assert.Equal(t, "curio:changed from:body", pollerTrigger(t, answer, "rebuild-poll"), "nothing in flight")
	assert.Equal(t, href, pollerHref(t, answer, "rebuild-state"),
		"the answer's poller still carries the run the page shows, not the newer one")

	srv.AddFailedRun(t, "cluster: label: <ollama> unreachable")
	body = getPage(t, srv, "/ui/interests", http.StatusOK)
	assert.Contains(t, body, "Kafka streams", "the latest done run's interests")
	assert.Contains(t, body, `The rebuild failed: cluster: label: &lt;ollama&gt; unreachable</p>`,
		"a failure newer than the interests shown, reported on the page")
}

// TestUI_InterestsRebuildPaged: past the first page, and past the last,
// the rebuild is the same: its state is the queue's, and its poller asks
// for the rebuild alone, with the run shown and no page; once the rebuild
// is done, it offers the Interests' first page.
func TestUI_InterestsRebuildPaged(t *testing.T) {
	ctx := context.Background()
	srv := apitest.Start(t)
	interests := make([]apitest.Interest, 0, 30)
	for i := range 30 {
		interests = append(interests, apitest.Interest{Label: "Topic " + strconv.Itoa(i), Size: 100 - i})
	}
	run := srv.AddInterests(t, interests...).ID
	enqueueRebuild(t, srv)
	_, err := srv.Deps.Gate.Update(ctx, jobsPause())
	require.NoError(t, err)
	want := "/ui/interests?" + url.Values{"poll": {"rebuild"}, "run": {run}}.Encode()
	for path, status := range map[string]int{"/ui/interests?page=2&run=" + run: http.StatusOK,
		"/ui/interests?page=3": http.StatusNotFound} {
		body := getPage(t, srv, path, status)
		assert.Equal(t, want, pollerHref(t, body, "rebuild-state"), path)
		assert.Equal(t, "every 2s, curio:changed from:body", pollerTrigger(t, body, "rebuild-poll"), path)
		assert.Contains(t, body, `<p>Rebuild queued · <a id="rebuild-hold" href="/ui/status">`, path)
		assert.Equal(t, "true", attr(elementByID(pageDoc(t, body), "rebuild"), "aria-disabled"), path)
	}

	srv.AddInterest(t, "Kafka streams", srv.AddDocument(t, "https://example.com/a", store.DocStateFetched))
	answer := getPage(t, srv, want, http.StatusOK)
	assert.Contains(t, answer, `<p>New interests are ready: <a id="rebuild-reload" href="/ui/interests">reload</a></p>`)
}

// TestUI_InterestsReads: the page reads a page of the top-level groups
// and the run's placement counts; then, for interests, each card's
// members and all their documents at once, and for areas their interests
// in one read, without members; and the rebuild's state. A page past the
// last reads no member; its poll reads the rebuild's state alone: the
// queue and the newest run, never the interests. A running rebuild's
// start is its one job read. An interest's page reads it, its run, its
// members, the documents placed into it, their documents, and its
// lineage, once each; an area's, its interests in one read.
func TestUI_InterestsReads(t *testing.T) {
	r := &reads{}
	srv := apitest.Start(t, countReads(r))
	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	b := srv.AddDocument(t, "https://example.com/b", store.DocStateFetched)
	flat := srv.AddInterests(t, apitest.Interest{Label: "Kafka", Size: 2, Members: []*store.Document{a, b}},
		apitest.Interest{Label: "Go", Size: 1}, apitest.Interest{Label: "Rust", Size: 1})
	r.take()
	body := getPage(t, srv, "/ui/interests", http.StatusOK)
	assert.Equal(t, map[string]int{"done run": 1, "interests": 1, "new counts": 1, "members": 3, "member documents": 1,
		"queue": 1, "newest run": 1}, r.take(), "no document or extraction read one by one")

	getPage(t, srv, "/ui/interests?page=2", http.StatusNotFound)
	assert.Equal(t, map[string]int{"done run": 1, "interests": 1, "new counts": 1, "queue": 1, "newest run": 1}, r.take())
	getPage(t, srv, "/ui/interests?page=x", http.StatusBadRequest)
	assert.Empty(t, r.take())

	interest := map[string]int{"done run": 1, "interest": 1, "run": 1, "new counts": 1, "members": 1,
		"new members": 1, "lineage": 1, "identities": 1}
	getPage(t, srv, "/ui/interests/"+flat.Interests[0], http.StatusOK)
	assert.Equal(t, with(interest, "member documents", 1), r.take())
	getPage(t, srv, "/ui/interests/"+flat.Interests[1], http.StatusOK)
	assert.Equal(t, interest, r.take(), "no member, no document read")
	getPage(t, srv, "/ui/interests/"+flat.Interests[0]+"?page=2", http.StatusNotFound)
	assert.Equal(t, with(interest, "members", 0), r.take(), "past its members, none read")
	getPage(t, srv, "/ui/interests/"+flat.Interests[0]+"?page=x", http.StatusBadRequest)
	assert.Empty(t, r.take())

	href := pollerHref(t, body, "rebuild-state")
	getPage(t, srv, href, http.StatusOK)
	assert.Equal(t, map[string]int{"queue": 1, "newest run": 1}, r.take())
	getPage(t, srv, href+"&page=x", http.StatusOK)
	assert.Equal(t, map[string]int{"queue": 1, "newest run": 1}, r.take(), "a poll ignores the page")

	areas := srv.AddAreas(t, apitest.Area{Label: "Streams", Interests: []apitest.Interest{
		{Label: "Kafka", Members: []*store.Document{a}}, {Label: "Flink", Members: []*store.Document{b}}}})
	r.take()
	getPage(t, srv, "/ui/interests", http.StatusOK)
	assert.Equal(t, map[string]int{"done run": 1, "interests": 1, "new counts": 1, "children": 1, "queue": 1,
		"newest run": 1}, r.take(), "an area card names its interests, never their members")
	getPage(t, srv, "/ui/interests/"+areas.Areas[0], http.StatusOK)
	assert.Equal(t, map[string]int{"done run": 1, "interest": 1, "run": 1, "new counts": 1, "children": 1,
		"members": 2, "member documents": 1, "lineage": 1, "identities": 1}, r.take())

	enqueueRebuild(t, srv)
	_, err := srv.Deps.Queue.ClaimNext(context.Background(), []store.JobKind{store.JobKindCluster})
	require.NoError(t, err)
	r.jobs = nil
	getPage(t, srv, href, http.StatusOK)
	assert.Equal(t, map[string]int{"queue": 1, "newest run": 1, "jobs": 1}, r.take())
	require.Len(t, r.jobs, 1)
	assert.Equal(t, store.ListJobsOpts{Kind: store.JobKindCluster, Status: store.JobStatusRunning, Limit: 2}, r.jobs[0],
		"the running one, never the queued")

	getPage(t, srv, "/ui/interests?poll=jobs", http.StatusBadRequest)
	assert.Empty(t, r.take())
}

// with is reads with name's count set to n, none when n is 0.
func with(reads map[string]int, name string, n int) map[string]int {
	out := maps.Clone(reads)
	if n == 0 {
		delete(out, name)
	} else {
		out[name] = n
	}
	return out
}

// TestUI_InterestsInsightOff: with insight off there is no Rebuild, and
// the empty page says how to turn it on, not to rebuild.
func TestUI_InterestsInsightOff(t *testing.T) {
	srv := apitest.Start(t, func(d *api.Deps) { d.InsightEnabled = false })
	body := getPage(t, srv, "/ui/interests", http.StatusOK)
	assert.Nil(t, elementByID(pageDoc(t, body), "rebuild"))
	assert.Contains(t, body, "set <code>insight.enabled: true</code> in config.yaml")
	assert.NotContains(t, body, "curio interests rebuild")
}
