package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func (s *testServer) docState(t *testing.T, id string) store.DocState {
	t.Helper()
	d, err := s.deps.Documents.GetByID(context.Background(), id)
	require.NoError(t, err)
	return d.State
}

// failJobInserts makes every job insert abort until the returned restore
// is called. Persistent rather than TEMP: a TEMP trigger lives on one pooled
// connection only.
func (s *testServer) failJobInserts(t *testing.T) (restore func()) {
	t.Helper()
	_, err := s.db.Exec(`CREATE TRIGGER t_fail BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	require.NoError(t, err)
	return func() {
		t.Helper()
		_, err := s.db.Exec(`DROP TRIGGER t_fail`)
		require.NoError(t, err)
	}
}

func TestRefetchDocument(t *testing.T) {
	s := newTestServer(t)
	dead := s.seedDocument(t, "https://example.com/gone", store.DocStateDead)

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/" + dead.ID + "/refetch"})
	assertProblem(t, resp, http.StatusConflict)
	assert.Equal(t, store.DocStateDead, s.docState(t, dead.ID))
	assert.Zero(t, s.count(t, "jobs"))

	resp = s.do(t, request{method: http.MethodPost, path: "/v1/documents/" + dead.ID + "/refetch?force=1"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	var body struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.body), &body))
	job, err := s.deps.Queue.GetByID(context.Background(), body.JobID)
	require.NoError(t, err)
	assert.Equal(t, store.JobKindFetch, job.Kind)
	assert.JSONEq(t, `{"document_id":"`+dead.ID+`"}`, string(job.Payload))
	assert.Equal(t, store.DocStatePending, s.docState(t, dead.ID))

	resp = s.do(t, request{method: http.MethodPost, path: "/v1/documents/00000000-0000-0000-0000-000000000000/refetch"})
	p := assertProblem(t, resp, http.StatusNotFound)
	assert.Equal(t, `document "00000000-0000-0000-0000-000000000000" not found`, p.Detail)
}

// TestRefetchDocument_StoreFailure: when the job can't be enqueued, the
// document keeps its state rather than sitting in pending with no job.
func TestRefetchDocument_StoreFailure(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFailed)
	s.failJobInserts(t)

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/" + doc.ID + "/refetch"})
	assertProblem(t, resp, http.StatusInternalServerError)
	assert.Equal(t, store.DocStateFailed, s.docState(t, doc.ID))
}

func TestRefetchAll(t *testing.T) {
	s := newTestServer(t)
	for _, st := range []store.DocState{store.DocStatePending, store.DocStateFetched, store.DocStateFailed, store.DocStateDead} {
		s.seedDocument(t, "https://example.com/"+string(st), st)
	}

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all?state=bogus"})
	assertProblem(t, resp, http.StatusBadRequest)
	assert.Zero(t, s.count(t, "jobs"))

	resp = s.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	assert.JSONEq(t, `{"jobs_enqueued":3}`, resp.body, "dead documents are skipped by default")
	assert.Equal(t, 3, s.count(t, "jobs"))

	resp = s.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all?state=dead"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	assert.JSONEq(t, `{"jobs_enqueued":1}`, resp.body)
}

// TestRefetchAll_Cause: ?cause refetches exactly the documents that failed
// for it, among the default states or the one given, each with one job and
// its cause cleared. Dead links need state=dead.
func TestRefetchAll_Cause(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	blocked := []*store.Document{s.seedFailedDocument(t, "https://a.example/1", store.FailureCauseAntiBot),
		s.seedFailedDocument(t, "https://b.example/1", store.FailureCauseAntiBot)}
	timedOut := s.seedFailedDocument(t, "https://c.example/1", store.FailureCauseTimeout)
	gone := []*store.Document{s.seedFailedDocument(t, "https://d.example/1", store.FailureCauseDeadLink),
		s.seedFailedDocument(t, "https://d.example/2", store.FailureCauseDeadLink)}
	fetched := s.seedDocument(t, "https://e.example/1", store.DocStateFetched)
	refetchAll := func(query string) response {
		return s.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all?" + query})
	}
	jobsFor := func(doc *store.Document) int {
		t.Helper()
		var n int
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM jobs WHERE document_id = ?`, doc.ID).Scan(&n))
		return n
	}

	p := assertProblem(t, refetchAll("cause=bogus"), http.StatusBadRequest)
	assert.Contains(t, p.Detail, `cause "bogus" must be one of: dead_link, anti_bot,`)
	for _, query := range []string{"cause=dead_link", "cause=dead_link&state=failed"} {
		p := assertProblem(t, refetchAll(query), http.StatusBadRequest)
		assert.Contains(t, p.Detail, "dead links are refetched only with state=dead", query)
	}
	assert.Zero(t, s.count(t, "jobs"), "a refused refetch enqueues nothing")

	resp := refetchAll("cause=anti_bot")
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	assert.JSONEq(t, `{"jobs_enqueued":2}`, resp.body)
	for _, doc := range blocked {
		got, err := s.deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStatePending, got.State)
		assert.Empty(t, got.FailureCause)
		assert.Equal(t, 1, jobsFor(doc))
	}
	for _, doc := range []*store.Document{timedOut, gone[0], gone[1], fetched} {
		got, err := s.deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, doc.State, got.State, doc.URL)
		assert.Equal(t, doc.FailureCause, got.FailureCause, doc.URL)
		assert.Zero(t, jobsFor(doc), doc.URL)
	}

	resp = refetchAll("state=fetched&cause=timeout")
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	assert.JSONEq(t, `{"jobs_enqueued":0}`, resp.body, "a pair no document has requeues nothing")

	resp = refetchAll("state=dead&cause=dead_link")
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	assert.JSONEq(t, `{"jobs_enqueued":2}`, resp.body)
	for _, doc := range gone {
		assert.Equal(t, store.DocStatePending, s.docState(t, doc.ID))
		assert.Equal(t, 1, jobsFor(doc))
	}
	assert.Equal(t, 4, s.count(t, "jobs"))
}

func TestRefetchAll_StoreFailure(t *testing.T) {
	s := newTestServer(t)
	failed := s.seedDocument(t, "https://example.com/a", store.DocStateFailed)
	fetched := s.seedDocument(t, "https://example.com/b", store.DocStateFetched)
	s.failJobInserts(t)

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all"})
	assertProblem(t, resp, http.StatusInternalServerError)
	assert.Equal(t, store.DocStateFailed, s.docState(t, failed.ID))
	assert.Equal(t, store.DocStateFetched, s.docState(t, fetched.ID))
}

// TestReindexAll_StoreFailure: enqueue failures are reported, not skipped.
func TestReindexAll_StoreFailure(t *testing.T) {
	s := newTestServer(t)
	s.seedContent(t, s.seedDocument(t, "https://example.com/a", store.DocStateFetched), "# A")
	s.failJobInserts(t)

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/reindex-all"})
	assertProblem(t, resp, http.StatusInternalServerError)
	assert.Contains(t, resp.body, "enqueued 0 of 1")
}

// TestReindexAll_ResetsTheDriftBaseline: reindexing the fetched documents,
// every searchable one, resets the embedding drift baseline once all their
// jobs are enqueued; reindexing another state leaves it alone.
func TestReindexAll_ResetsTheDriftBaseline(t *testing.T) {
	cases := []struct {
		path        string
		rebaselines int
	}{
		{"/v1/documents/reindex-all", 1},
		{"/v1/documents/reindex-all?state=fetched", 1},
		{"/v1/documents/reindex-all?state=failed", 0},
		{"/v1/documents/reindex-all?state=pending", 0},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			monitor := &driftMonitor{}
			s := newTestServer(t, func(d *Deps) { d.Drift = monitor })
			s.seedContent(t, s.seedDocument(t, "https://example.com/a", store.DocStateFetched), "# A")

			resp := s.do(t, request{method: http.MethodPost, path: tc.path})
			require.Equal(t, http.StatusAccepted, resp.status, resp.body)
			assert.Equal(t, tc.rebaselines, monitor.rebaselined())
		})
	}
}

// TestReindexAll_DriftResetFailure: a baseline that can't be reset is a
// 500 that says every job was enqueued, which they were.
func TestReindexAll_DriftResetFailure(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.Drift = &driftMonitor{err: errors.New("write marker tmp: permission denied")} })
	s.seedContent(t, s.seedDocument(t, "https://example.com/a", store.DocStateFetched), "# A")
	s.seedContent(t, s.seedDocument(t, "https://example.com/b", store.DocStateFetched), "# B")

	p := assertProblem(t, s.do(t, request{method: http.MethodPost, path: "/v1/documents/reindex-all"}),
		http.StatusInternalServerError)
	assert.Contains(t, p.Detail, "enqueued all 2 index jobs")
	assert.Contains(t, p.Detail, "permission denied")
	assert.Equal(t, 2, s.count(t, "jobs"))
}

// TestReindexAll_OwesTheInterestsAFreshRebuild: reindexing the fetched
// documents owes the interests a fresh rebuild, before any job is
// enqueued, and asks the scheduler to check; another state, or no
// document, owes nothing.
func TestReindexAll_OwesTheInterestsAFreshRebuild(t *testing.T) {
	for path, owes := range map[string]bool{
		"/v1/documents/reindex-all":              true,
		"/v1/documents/reindex-all?state=failed": false,
	} {
		t.Run(path, func(t *testing.T) {
			s := newTestServer(t)
			s.seedContent(t, s.seedDocument(t, "https://example.com/a", store.DocStateFetched), "# A")
			resp := s.do(t, request{method: http.MethodPost, path: path})
			require.Equal(t, http.StatusAccepted, resp.status, resp.body)
			st, err := s.insights().State(context.Background(), "local")
			require.NoError(t, err)
			if owes {
				assert.Equal(t, store.FreshReindex, st.FreshOwed)
			} else {
				assert.Empty(t, st.FreshOwed)
			}
			assert.Equal(t, 1, s.interests.kicked())
		})
	}

	empty := newTestServer(t)
	resp := empty.do(t, request{method: http.MethodPost, path: "/v1/documents/reindex-all"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	st, err := empty.insights().State(context.Background(), "local")
	require.NoError(t, err)
	assert.Empty(t, st.FreshOwed, "nothing re-embedded, nothing owed")
}

// unowingInsights is an insight store that can't owe a fresh rebuild.
type unowingInsights struct{ store.InsightStore }

func (unowingInsights) OweFresh(context.Context, string, store.FreshReason) error {
	return errors.New("database is locked")
}

// TestReindexAll_OweFailure: a fresh rebuild that can't be owed is a 500
// with nothing enqueued, so no re-embedding goes on unowed.
func TestReindexAll_OweFailure(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.Insights = unowingInsights{d.Insights} })
	s.seedContent(t, s.seedDocument(t, "https://example.com/a", store.DocStateFetched), "# A")
	p := assertProblem(t, s.do(t, request{method: http.MethodPost, path: "/v1/documents/reindex-all"}),
		http.StatusInternalServerError)
	assert.Contains(t, p.Detail, "enqueued no index job")
	assert.Contains(t, p.Detail, "database is locked")
	assert.Zero(t, s.count(t, "jobs"))
}

// TestGetDocumentContent_MissingFile: content deleted from disk is a 404
// that says what to do, not a 500 carrying the absolute path.
func TestGetDocumentContent_MissingFile(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	ext := s.seedContent(t, doc, "# A")
	require.NoError(t, os.Remove(filepath.Join(s.deps.Home.ContentDir(), *ext.MarkdownPath)))

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + doc.ID + "/content"})
	assertProblem(t, resp, http.StatusNotFound)
	assert.Contains(t, resp.body, "refetch")
	assert.NotContains(t, resp.body, s.deps.Home.Path)
}

// TestReindexAll_OnlyDocumentsWithContent: an index job for a document with
// no extraction fails permanently and marks it failed, so reindex-all skips
// such documents; and it validates ?state like refetch-all.
func TestReindexAll_OnlyDocumentsWithContent(t *testing.T) {
	s := newTestServer(t)
	fetching := s.seedDocument(t, "https://example.com/fetching", store.DocStatePending)
	withContent := s.seedDocument(t, "https://example.com/fetched-once", store.DocStatePending)
	s.seedContent(t, withContent, "# Fetched once")

	assertProblem(t, s.do(t, request{method: http.MethodPost, path: "/v1/documents/reindex-all?state=bogus"}),
		http.StatusBadRequest)
	assert.Zero(t, s.count(t, "jobs"))

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/reindex-all?state=pending"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	assert.JSONEq(t, `{"jobs_enqueued":1}`, resp.body)
	var payload string
	require.NoError(t, s.db.QueryRow(`SELECT payload FROM jobs WHERE kind = 'index'`).Scan(&payload))
	assert.JSONEq(t, `{"document_id":"`+withContent.ID+`"}`, payload)
	assert.Equal(t, store.DocStatePending, s.docState(t, fetching.ID))
}

func TestGetDocument(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	ext := s.seedContent(t, doc, "# A")

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + doc.ID})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got DocumentResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, doc.ID, got.ID)
	assert.Equal(t, "fetched", got.State)
	assert.Equal(t, "unknown", got.ContentType)
	require.NotNil(t, got.CurrentExtraction)
	assert.Equal(t, ext.ID, got.CurrentExtraction.ID)
	assert.Equal(t, "test", got.CurrentExtraction.Fetcher)
	assert.Equal(t, filepath.Join(s.deps.Home.ContentDir(), *ext.MarkdownPath), got.CurrentExtraction.MarkdownPath,
		"an absolute path, like every other path the API returns")
	assert.Equal(t, map[string]any{"via": "test"}, got.CurrentExtraction.ExtractionMeta)

	assert.Empty(t, got.FailureCause)

	bare := s.seedDocument(t, "https://example.com/b", store.DocStatePending)
	resp = s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + bare.ID})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.NotContains(t, resp.body, "current_extraction")
	assert.NotContains(t, resp.body, "failure_cause")

	failed := s.seedFailedDocument(t, "https://example.com/expired", store.FailureCauseTLS)
	resp = s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + failed.ID})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, "failed", got.State)
	assert.Equal(t, "tls", got.FailureCause)

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/documents/no-such-document"}),
		http.StatusNotFound)
	assert.Equal(t, `document "no-such-document" not found`, p.Detail)
}

// TestLookupDocument: a URL finds its document as typed or pasted,
// normalized the way ingest stored it, and answers as GET by ID does.
func TestLookupDocument(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFailed)
	lookup := func(raw string) response {
		return s.do(t, request{method: http.MethodGet, path: "/v1/documents/lookup?url=" + url.QueryEscape(raw)})
	}

	for _, raw := range []string{
		"https://example.com/a",
		"https://EXAMPLE.com:443/a#comments",
		"https://example.com/a?utm_source=newsletter",
	} {
		resp := lookup(raw)
		require.Equal(t, http.StatusOK, resp.status, "%s: %s", raw, resp.body)
		var got DocumentResponse
		require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
		assert.Equal(t, doc.ID, got.ID, raw)
		assert.Equal(t, "failed", got.State, raw)
	}

	p := assertProblem(t, lookup("https://example.com/b"), http.StatusNotFound)
	assert.Equal(t, `document for url "https://example.com/b" not found`, p.Detail)
	assertProblem(t, lookup("not a url"), http.StatusBadRequest)
	assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/documents/lookup"}), http.StatusBadRequest)
}

// failingExtractionLookup fails every extraction lookup.
type failingExtractionLookup struct{ store.ExtractionStore }

func (failingExtractionLookup) GetByID(context.Context, string) (*store.DocumentExtraction, error) {
	return nil, errInjected
}

// TestGetDocument_ExtractionLookupFailure: a document whose extraction can't
// be read is a server error, not a document without current_extraction.
func TestGetDocument_ExtractionLookupFailure(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.Extractions = failingExtractionLookup{d.Extractions} })
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	s.seedContent(t, doc, "# A")

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + doc.ID}),
		http.StatusInternalServerError)
	assert.Contains(t, p.Detail, errInjected.Error())
	bare := s.seedDocument(t, "https://example.com/b", store.DocStatePending)
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + bare.ID})
	assert.Equal(t, http.StatusOK, resp.status, "no extraction to look up, so no error: %s", resp.body)
}

func TestListDocuments(t *testing.T) {
	s := newTestServer(t)
	fetched := s.seedDocument(t, "https://example.com/fetched", store.DocStateFetched)
	ext := s.seedContent(t, fetched, "# Fetched")
	failed := s.seedDocument(t, "https://example.com/failed", store.DocStateFailed)
	job, err := store.NewDocumentJob("local", store.JobKindFetch, failed.ID)
	require.NoError(t, err)
	job.Status = store.JobStatusFailed
	require.NoError(t, s.deps.Queue.Enqueue(context.Background(), job))
	_, err = s.db.Exec(`UPDATE jobs SET last_error = 'HTTP 503' WHERE id = ?`, job.ID)
	require.NoError(t, err)

	list := func(query string) DocumentListResponse {
		t.Helper()
		resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents" + query})
		require.Equal(t, http.StatusOK, resp.status, resp.body)
		var got DocumentListResponse
		require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
		return got
	}

	all := list("")
	assert.Len(t, all.Items, 2)

	onlyFailed := list("?state=failed")
	require.Len(t, onlyFailed.Items, 1)
	assert.Equal(t, failed.ID, onlyFailed.Items[0].ID)
	assert.Equal(t, "HTTP 503", onlyFailed.Items[0].LastError)

	onlyFetched := list("?state=fetched&limit=1")
	require.Len(t, onlyFetched.Items, 1)
	assert.Equal(t, filepath.Join(s.deps.Home.ContentDir(), *ext.MarkdownPath), onlyFetched.Items[0].MarkdownPath,
		"an absolute path, ready to cat")

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/documents?state=archived"}),
		http.StatusBadRequest)
	assert.Equal(t, `state "archived" must be one of: pending, fetched, failed, dead`, p.Detail,
		"a mistyped filter is refused, not answered with nothing")
}

// TestListDocuments_Filters: content_type, host and folder narrow the list,
// together with state, and a content type outside the set is refused.
func TestListDocuments_Filters(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	seed := func(docURL string, ct store.ContentType, state store.DocState, folder string) string {
		t.Helper()
		b := &store.Bookmark{TenantID: "local", URL: docURL, Source: store.SourceChrome,
			SavedAt: time.Now().UTC(), FolderPath: store.NullableString(folder)}
		_, err := s.deps.Bookmarks.Ingest(ctx, b)
		require.NoError(t, err)
		_, err = s.db.Exec(`UPDATE documents SET content_type = ?, state = ? WHERE id = ?`, ct, state, *b.DocumentID)
		require.NoError(t, err)
		return *b.DocumentID
	}
	paper := seed("https://arxiv.example/paper", store.ContentTypePDF, store.DocStateFetched, "/Research/ML")
	failedPaper := seed("https://arxiv.example/failed", store.ContentTypePDF, store.DocStateFailed, "/Research")
	repo := seed("https://code.example/repo", store.ContentTypeRepo, store.DocStateFetched, "/Research/ML/Code")
	video := seed("https://video.example/talk", store.ContentTypeVideo, store.DocStateFetched, "/Talks")

	ids := func(query string) []string {
		t.Helper()
		resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents" + query})
		require.Equal(t, http.StatusOK, resp.status, resp.body)
		var got DocumentListResponse
		require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
		out := make([]string, 0, len(got.Items))
		for _, item := range got.Items {
			out = append(out, item.ID)
		}
		return out
	}
	cases := []struct {
		query string
		want  []string
	}{
		{"?content_type=pdf", []string{paper, failedPaper}},
		{"?content_type=pdf&state=fetched", []string{paper}},
		{"?host=ARXIV.example", []string{paper, failedPaper}},
		{"?host=example", nil},
		{"?folder=/Research/ML", []string{paper, repo}},
		{"?folder=/Research/", []string{paper, failedPaper, repo}},
		{"?folder=/research", nil},
		{"?folder=/", []string{paper, failedPaper, repo, video}},
		{"?content_type=repo&host=code.example&folder=/Research", []string{repo}},
		{"?content_type=video&folder=/Research", nil},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			assert.ElementsMatch(t, tc.want, ids(tc.query))
		})
	}

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/documents?content_type=bogus"}),
		http.StatusBadRequest)
	assert.Equal(t, `content_type "bogus" must be one of: article, repo, video, pdf, thread, unknown`, p.Detail)
}

// TestListDocuments_FilteredPaging: a walk one row a page over a filtered
// list visits every matching document once, most recently updated first.
func TestListDocuments_FilteredPaging(t *testing.T) {
	s := newTestServer(t)
	byHost, byCause := map[string]bool{}, map[string]bool{}
	for i := range 6 {
		host, cause := "keep.example", store.FailureCauseAntiBot
		if i%2 == 1 {
			host, cause = "skip.example", store.FailureCauseTimeout
		}
		doc := s.seedDocument(t, fmt.Sprintf("https://%s/%d", host, i), store.DocStateFetched)
		failed := s.seedFailedDocument(t, fmt.Sprintf("https://failed.example/%d", i), cause)
		if host == "keep.example" {
			byHost[doc.ID] = true
			byCause[failed.ID] = true
		}
	}
	for _, tc := range []struct {
		filter url.Values
		want   map[string]bool
	}{
		{url.Values{"host": {"keep.example"}}, byHost},
		{url.Values{"cause": {"anti_bot"}}, byCause},
		{url.Values{"cause": {"anti_bot"}, "state": {"failed"}, "host": {"failed.example"}}, byCause},
	} {
		t.Run(tc.filter.Encode(), func(t *testing.T) {
			var walked []DocumentListItem
			cursor := ""
			for range 10 {
				q := maps.Clone(tc.filter)
				q.Set("limit", "1")
				if cursor != "" {
					q.Set("cursor", cursor)
				}
				resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents?" + q.Encode()})
				require.Equal(t, http.StatusOK, resp.status, resp.body)
				var page DocumentListResponse
				require.NoError(t, json.Unmarshal([]byte(resp.body), &page))
				walked = append(walked, page.Items...)
				if cursor = page.NextCursor; cursor == "" {
					break
				}
			}
			got := map[string]bool{}
			for i, doc := range walked {
				assert.False(t, got[doc.ID], "visited once")
				got[doc.ID] = true
				if i > 0 {
					prev := walked[i-1]
					assert.True(t, doc.UpdatedAt.Before(prev.UpdatedAt) ||
						(doc.UpdatedAt.Equal(prev.UpdatedAt) && doc.ID < prev.ID), "updated_at DESC, id DESC")
				}
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestListDocuments_Cause: ?cause narrows the list to the documents that
// failed for it, together with the other filters, each carrying its
// cause; a cause that isn't one is refused, naming them all.
func TestListDocuments_Cause(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	filed := &store.Bookmark{TenantID: "local", URL: "https://a.example/filed", Source: store.SourceChrome,
		SavedAt: time.Now().UTC(), FolderPath: store.NullableString("/Blocked")}
	_, err := s.deps.Bookmarks.Ingest(ctx, filed)
	require.NoError(t, err)
	require.NoError(t, s.deps.Documents.MarkFailed(ctx, *filed.DocumentID, store.FailureCauseAntiBot))
	onA := s.seedFailedDocument(t, "https://a.example/1", store.FailureCauseAntiBot)
	onB := s.seedFailedDocument(t, "https://b.example/1", store.FailureCauseAntiBot)
	walled := s.seedFailedDocument(t, "https://a.example/2", store.FailureCauseLoginWall)
	gone := s.seedFailedDocument(t, "https://a.example/3", store.FailureCauseDeadLink)
	s.seedDocument(t, "https://a.example/4", store.DocStateFetched)

	list := func(query string) map[string]string {
		t.Helper()
		resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents?" + query})
		require.Equal(t, http.StatusOK, resp.status, resp.body)
		var got DocumentListResponse
		require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
		causes := map[string]string{}
		for _, item := range got.Items {
			causes[item.ID] = item.FailureCause
		}
		return causes
	}
	blocked := func(ids ...string) map[string]string {
		out := map[string]string{}
		for _, id := range ids {
			out[id] = "anti_bot"
		}
		return out
	}
	cases := []struct {
		query string
		want  map[string]string
	}{
		{"cause=anti_bot", blocked(*filed.DocumentID, onA.ID, onB.ID)},
		{"cause=anti_bot&state=failed", blocked(*filed.DocumentID, onA.ID, onB.ID)},
		{"cause=anti_bot&state=fetched", map[string]string{}},
		{"cause=anti_bot&host=a.example", blocked(*filed.DocumentID, onA.ID)},
		{"cause=anti_bot&folder=/Blocked", blocked(*filed.DocumentID)},
		{"cause=anti_bot&content_type=unknown&limit=500", blocked(*filed.DocumentID, onA.ID, onB.ID)},
		{"cause=login_wall", map[string]string{walled.ID: "login_wall"}},
		{"cause=dead_link", map[string]string{gone.ID: "dead_link"}},
		{"cause=dead_link&state=failed", map[string]string{}},
		{"state=failed&host=a.example", map[string]string{*filed.DocumentID: "anti_bot", onA.ID: "anti_bot",
			walled.ID: "login_wall"}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			assert.Equal(t, tc.want, list(tc.query))
		})
	}

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/documents?cause=bogus"}),
		http.StatusBadRequest)
	assert.Equal(t, `cause "bogus" must be one of: dead_link, anti_bot, login_wall, jina_refused, tls, `+
		`unreachable, timeout, network, rate_limited, http_error, unsupported, too_large, index, other`, p.Detail)
}

// TestListDocuments_Paging: next_cursor is set exactly when another page
// follows, and the pages walk every document once, most recently updated
// first.
func TestListDocuments_Paging(t *testing.T) {
	s := newTestServer(t)
	for i := range 5 {
		s.seedDocument(t, fmt.Sprintf("https://example.com/%d", i), store.DocStateFetched)
	}
	all := make([]DocumentListItem, 0, 5)
	cursor := ""
	for range 3 {
		resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents?limit=2&cursor=" + cursor})
		require.Equal(t, http.StatusOK, resp.status, resp.body)
		var page DocumentListResponse
		require.NoError(t, json.Unmarshal([]byte(resp.body), &page))
		all = append(all, page.Items...)
		cursor = page.NextCursor
	}
	assert.Empty(t, cursor, "the third page is the last")
	require.Len(t, all, 5)
	seen := map[string]bool{}
	for i, doc := range all {
		seen[doc.ID] = true
		if i > 0 {
			assert.False(t, doc.UpdatedAt.After(all[i-1].UpdatedAt), "most recently updated first")
		}
	}
	assert.Len(t, seen, 5, "the pages don't overlap")
}

func TestGetDocumentContent(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	// Larger than net/http's chunking buffer: for a body that fits in it,
	// net/http computes a Content-Length whether or not the handler sets one.
	content := "# A\n\n" + strings.Repeat("a line of body text\n", 64<<10/20)
	s.seedContent(t, doc, content)

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + doc.ID + "/content"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.Equal(t, "text/markdown; charset=utf-8", resp.contentType)
	assert.Equal(t, content, resp.body)
	// A declared length is what lets a client tell a copy that failed
	// partway from a complete answer.
	raw, err := http.Get(s.base + "/v1/documents/" + doc.ID + "/content")
	require.NoError(t, err)
	defer raw.Body.Close()
	assert.Equal(t, int64(len(content)), raw.ContentLength)
	assert.Empty(t, raw.TransferEncoding, "not chunked")

	bare := s.seedDocument(t, "https://example.com/b", store.DocStatePending)
	assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + bare.ID + "/content"}),
		http.StatusNotFound)
	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/documents/nope/content"}),
		http.StatusNotFound)
	assert.Equal(t, `document "nope" not found`, p.Detail)
}

func TestReindexDocument(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	s.seedContent(t, doc, "# A")

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/" + doc.ID + "/reindex"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	var body struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.body), &body))
	job, err := s.deps.Queue.GetByID(context.Background(), body.JobID)
	require.NoError(t, err)
	assert.Equal(t, store.JobKindIndex, job.Kind)
	assert.JSONEq(t, `{"document_id":"`+doc.ID+`"}`, string(job.Payload))
	assert.Equal(t, store.DocStateFetched, s.docState(t, doc.ID), "reindex leaves the state alone")

	p := assertProblem(t, s.do(t, request{method: http.MethodPost, path: "/v1/documents/nope/reindex"}),
		http.StatusNotFound)
	assert.Equal(t, `document "nope" not found`, p.Detail)
	s.failJobInserts(t)
	assertProblem(t, s.do(t, request{method: http.MethodPost, path: "/v1/documents/" + doc.ID + "/reindex"}),
		http.StatusInternalServerError)
}

// TestFailures: the failed and dead documents counted by cause, the most
// first, each with the hosts most of it is on; and every cause and host
// pair it names lists exactly its count through the documents list.
func TestFailures(t *testing.T) {
	s := newTestServer(t)
	get := func() response {
		t.Helper()
		resp := s.do(t, request{method: http.MethodGet, path: "/v1/failures"})
		require.Equal(t, http.StatusOK, resp.status, resp.body)
		return resp
	}
	assert.JSONEq(t, `{"total":0,"causes":[]}`, get().body, "arrays, never null")

	for i, u := range []string{"https://example.com/a", "https://example.com/b", "https://www.example.com/c",
		"https://example.com:8443/d", "https://a.example/e", "https://b.example/f", "https://c.example/g"} {
		s.seedFailedDocument(t, u, store.FailureCauseAntiBot)
		if i < 2 {
			s.seedFailedDocument(t, u+"/gone", store.FailureCauseDeadLink)
		}
	}
	s.seedFailedDocument(t, "https://slow.example/a", store.FailureCauseTimeout)
	s.seedFailedDocument(t, "https://slow.example/b", store.FailureCauseTimeout)
	s.seedFailedDocument(t, "https://walled.example/a", store.FailureCauseLoginWall)
	s.seedDocument(t, "https://example.com/fetched", store.DocStateFetched)
	other := &store.Document{TenantID: "other", URL: "https://example.com/theirs", FailureCause: store.FailureCauseAntiBot}
	require.NoError(t, s.deps.Documents.Create(context.Background(), other))

	resp := get()
	assert.JSONEq(t, `{"total":12,"causes":[
		{"cause":"anti_bot","count":7,"hosts":[
			{"host":"example.com","count":2},{"host":"a.example","count":1},{"host":"b.example","count":1},
			{"host":"c.example","count":1},{"host":"example.com:8443","count":1}]},
		{"cause":"dead_link","count":2,"hosts":[{"host":"example.com","count":2}]},
		{"cause":"timeout","count":2,"hosts":[{"host":"slow.example","count":2}]},
		{"cause":"login_wall","count":1,"hosts":[{"host":"walled.example","count":1}]}]}`, resp.body)

	var summary FailuresResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &summary))
	for _, c := range summary.Causes {
		for _, h := range c.Hosts {
			q := url.Values{"cause": {c.Cause}, "host": {h.Host}, "limit": {"500"}}
			listed := s.do(t, request{method: http.MethodGet, path: "/v1/documents?" + q.Encode()})
			require.Equal(t, http.StatusOK, listed.status, listed.body)
			var page DocumentListResponse
			require.NoError(t, json.Unmarshal([]byte(listed.body), &page))
			assert.Len(t, page.Items, h.Count, "%s at %s", c.Cause, h.Host)
		}
	}
}
