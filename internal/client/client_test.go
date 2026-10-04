package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/drift"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
)

// Every exported method round-trips through the daemon's real router over a
// real database. Error cases assert the *APIError's Status and Problem
// fields: the message's wording is not part of the client's contract.

func start(t *testing.T) (*apitest.Server, *client.Client) {
	t.Helper()
	s := apitest.Start(t)
	return s, client.New(s.URL)
}

func TestHealthz(t *testing.T) {
	s, c := start(t)
	h, err := c.Healthz(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ok", h.Status)
	assert.Equal(t, os.Getpid(), h.PID)
	assert.Equal(t, s.Home.Path, h.Home)
	assert.Equal(t, s.Embedder.Dim, h.EmbeddingDim, "the home's width")
	assert.Positive(t, h.SchemaVersion)
	assert.Nil(t, h.EmbeddingDrift)
}

func TestHealthz_GenerationModel(t *testing.T) {
	s := apitest.Start(t, func(d *api.Deps) { d.GenerationModel = "gemma4:26b" })
	h, err := client.New(s.URL).Healthz(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "gemma4:26b", h.GenerationModel)
}

func TestHealthz_YouTubeFetcher(t *testing.T) {
	s := apitest.Start(t, func(d *api.Deps) { d.YouTubeFetcher = "/opt/homebrew/bin/yt-dlp" })
	h, err := client.New(s.URL).Healthz(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/opt/homebrew/bin/yt-dlp", h.YouTubeFetcher)
}

// TestHealthz_EmbeddingDrift: a drift the daemon reports reaches Health
// whole, with its evidence, verified or not.
func TestHealthz_EmbeddingDrift(t *testing.T) {
	checked := time.Date(2026, 9, 27, 14, 40, 1, 0, time.UTC)
	change := drift.Change{What: drift.OllamaVersion, Recorded: "0.30.0", Current: "0.34.4"}
	healthz := func(t *testing.T, monitor *apitest.Drift) *client.EmbeddingDrift {
		t.Helper()
		s := apitest.Start(t, func(d *api.Deps) { d.Drift = monitor })
		h, err := client.New(s.URL).Healthz(context.Background())
		require.NoError(t, err)
		return h.EmbeddingDrift
	}
	want := func(v client.DriftVerification) *client.EmbeddingDrift {
		return &client.EmbeddingDrift{
			Changes:      []client.DriftChange{{What: client.DriftOllamaVersion, Recorded: "0.30.0", Current: "0.34.4"}},
			Verification: &v,
			Fix:          "curio reindex --all",
			CheckedAt:    checked,
		}
	}

	t.Run("verified", func(t *testing.T) {
		assert.Equal(t, want(client.DriftVerification{Verified: true, Sampled: 64, Changed: 64,
			MinCosine: new(0.9713), Detail: "64 of 64 sampled chunks changed (worst cosine 0.9713)", SampledAt: checked,
		}), healthz(t, apitest.NewDrift(checked, change)))
	})
	t.Run("unverified", func(t *testing.T) {
		assert.Equal(t, want(client.DriftVerification{Detail: "not verified: after 3 attempts: ollama unreachable",
			SampledAt: checked,
		}), healthz(t, apitest.NewDrift(checked, change).Unverified("after 3 attempts: ollama unreachable")))
	})
}

// TestHealthz_EmbeddingDriftFromAnOlderDaemon: a daemon that predates
// verifying a change reports its drift without evidence.
func TestHealthz_EmbeddingDriftFromAnOlderDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","version":"v2.3.0","schema_version":15,"embedding_model":"qwen3-embedding:0.6b",`+
			`"embedding_dim":1024,"ollama_reachable":true,"embedding_drift":{"changes":[{"what":"ollama_version",`+
			`"recorded":"0.34.4","current":"0.35.0"}],"fix":"curio reindex --all","checked_at":"2026-09-27T14:40:01Z"}}`)
	}))
	t.Cleanup(srv.Close)

	h, err := client.New(srv.URL).Healthz(context.Background())
	require.NoError(t, err)
	require.NotNil(t, h.EmbeddingDrift)
	assert.Nil(t, h.EmbeddingDrift.Verification)
	assert.Equal(t, "0.35.0", h.EmbeddingDrift.Changes[0].Current)
}

// TestHealthz_Starting: a starting daemon's healthz answer is an error
// matching ErrStarting that says which daemon answered and how far along it
// is. Any other route's starting answer matches ErrStarting too, without
// naming the daemon.
func TestHealthz_Starting(t *testing.T) {
	s := apitest.StartNotReady(t)
	c := client.New(s.URL)
	ctx := context.Background()

	h, err := c.Healthz(ctx)
	assert.Nil(t, h)
	require.ErrorIs(t, err, client.ErrStarting)
	requireStatus(t, err, http.StatusServiceUnavailable)
	st := client.StartupOf(err)
	require.NotNil(t, st)
	assert.Equal(t, os.Getpid(), st.PID)
	assert.Equal(t, s.Home.Path, st.Home)
	assert.NotEmpty(t, st.Version)
	assert.Equal(t, client.PhaseInitializing, st.Phase)
	assert.Nil(t, st.Migrations)
	assert.Equal(t, "initializing", st.Progress())
	assert.Equal(t, "curio-daemon is starting: initializing", err.Error(),
		"a starting daemon isn't a server error to look up in the log")

	s.Startup.SetMigrating(6)
	s.Startup.MigrationApplied()
	_, err = c.Healthz(ctx)
	st = client.StartupOf(err)
	require.NotNil(t, st)
	assert.Equal(t, client.PhaseMigrating, st.Phase)
	assert.Equal(t, &client.MigrationProgress{Applied: 1, Total: 6}, st.Migrations)
	assert.Equal(t, "migrating the database, 1 of 6 migrations applied", st.Progress())

	_, err = c.Stats(ctx)
	require.ErrorIs(t, err, client.ErrStarting)
	assert.Nil(t, client.StartupOf(err), "only healthz names the daemon")

	require.NoError(t, s.Ready())
	h, err = c.Healthz(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ok", h.Status)
}

// TestErrStarting_OnlyTheStartingProblem: a 503 is a starting daemon only
// when it carries the starting problem type; the phase it reports needn't
// be one this client knows.
func TestErrStarting_OnlyTheStartingProblem(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
		starting    bool
	}{
		{"text/plain 503", http.StatusServiceUnavailable, "text/plain", "starting", false},
		{"another problem", http.StatusServiceUnavailable, "application/problem+json",
			`{"type":"about:blank","title":"unavailable","status":503}`, false},
		{"starting type on another status", http.StatusInternalServerError, "application/problem+json",
			`{"type":"urn:curio:problem:daemon-starting","title":"daemon starting","status":500}`, false},
		{"an unknown phase", http.StatusServiceUnavailable, "application/problem+json",
			`{"type":"urn:curio:problem:daemon-starting","title":"daemon starting","status":503,` +
				`"pid":42,"home":"/h","version":"v9","phase":"compacting"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := c.Healthz(context.Background())
			require.Error(t, err)
			assert.Equal(t, tc.starting, errors.Is(err, client.ErrStarting))
			if tc.starting {
				st := client.StartupOf(err)
				require.NotNil(t, st)
				assert.Equal(t, 42, st.PID)
				assert.Equal(t, "compacting", st.Progress())
			} else {
				assert.Nil(t, client.StartupOf(err))
			}
		})
	}
}

// TestHealthz_Upstreams: the upstreams a daemon reports decode with their
// times, classes, counts and site pauses; the times it leaves out stay
// zero.
func TestHealthz_Upstreams(t *testing.T) {
	failure := time.Date(2026, 9, 27, 14, 40, 1, 0, time.UTC)
	s := apitest.Start(t, func(d *api.Deps) {
		d.Upstreams = func() []fetcher.UpstreamHealth {
			return []fetcher.UpstreamHealth{{Name: "jina", Enabled: true, State: fetcher.UpstreamFailing,
				LastFailure: failure, LastFailureClass: fetcher.CallAuth, Window: 15 * time.Minute,
				Recent:     map[fetcher.CallClass]int{fetcher.CallAuth: 5},
				SitePauses: []fetcher.SitePause{{Site: "twitter.com", Until: failure.Add(time.Hour)}}}}
		}
	})
	h, err := client.New(s.URL).Healthz(context.Background())
	require.NoError(t, err)
	require.Len(t, h.Upstreams, 1)
	u := h.Upstreams[0]
	assert.Equal(t, client.UpstreamHealth{Name: "jina", Enabled: true, State: client.UpstreamFailing,
		LastFailureAt: failure, LastFailureClass: client.CallAuth, WindowSeconds: 900,
		Recent:     map[string]int{client.CallAuth: 5},
		SitePauses: []client.SitePause{{Site: "twitter.com", Until: failure.Add(time.Hour)}}}, u)
	assert.True(t, u.LastSuccessAt.IsZero())
	assert.True(t, u.CooldownUntil.IsZero())
}

// TestHealthz_LegacyDaemon: a daemon from before healthz named the daemon
// and its upstreams still decodes, with no identity and no upstreams.
func TestHealthz_LegacyDaemon(t *testing.T) {
	c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","version":"v0.2.0"}`)
	})
	h, err := c.Healthz(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v0.2.0", h.Version)
	assert.Zero(t, h.PID)
	assert.Empty(t, h.Home)
	assert.Nil(t, h.Upstreams)
}

// requireStatus asserts that err is the daemon answering status, and
// returns the problem it sent.
func requireStatus(t *testing.T, err error, status int) client.Problem {
	t.Helper()
	var apiErr *client.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, status, apiErr.Status, err.Error())
	return apiErr.Problem
}

func TestDaemonUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	c := client.New("http://" + addr)

	_, err = c.Healthz(context.Background())
	require.ErrorIs(t, err, client.ErrDaemonUnreachable)
	var opErr *net.OpError
	require.ErrorAs(t, err, &opErr, "the dial error stays in the chain")
	assert.Equal(t, "dial", opErr.Op)
	_, err = c.GetDocumentContent(context.Background(), "any")
	require.ErrorIs(t, err, client.ErrDaemonUnreachable)
}

// TestTimeoutIsNotUnreachable: a daemon too slow for the caller's deadline
// was reached; the error says the deadline passed.
func TestTimeoutIsNotUnreachable(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := client.New(slow.URL).Stats(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotErrorIs(t, err, client.ErrDaemonUnreachable)
}

func TestHTTPErrorsAreAPIErrors(t *testing.T) {
	_, c := start(t)
	ctx := context.Background()

	_, err := c.GetDocument(ctx, "no-such-document")
	p := requireStatus(t, err, http.StatusNotFound)
	assert.Equal(t, `document "no-such-document" not found`, p.Detail)
	assert.Equal(t, `document "no-such-document" not found`, err.Error(), "the detail, not the raw problem")
	assert.NotEmpty(t, p.RequestID)
	assert.True(t, client.IsNotFound(err))

	_, err = c.GetDocumentContent(ctx, "no-such-document")
	requireStatus(t, err, http.StatusNotFound)

	_, err = c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: "javascript:alert(1)"})
	requireStatus(t, err, http.StatusBadRequest)
	assert.False(t, client.IsNotFound(err))
}

// fakeDaemon answers every request with handler.
func fakeDaemon(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return client.New(srv.URL)
}

func TestAPIError_ServerErrors(t *testing.T) {
	t.Run("a problem names its request ID and the log", func(t *testing.T) {
		c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"title":"internal error","status":500,"detail":"disk full","request_id":"req-7"}`)
		})
		_, err := c.GetDocumentContent(context.Background(), "doc")
		p := requireStatus(t, err, http.StatusInternalServerError)
		assert.Equal(t, "disk full", p.Detail)
		assert.Equal(t, "disk full (request req-7; see `curio daemon logs`)", err.Error())
	})

	t.Run("a problem's extension members are its own", func(t *testing.T) {
		c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusInternalServerError)
			// Named like a starting daemon's members, typed otherwise.
			fmt.Fprint(w, `{"title":"internal error","status":500,"detail":"disk full","pid":"n/a","home":7}`)
		})
		_, err := c.Stats(context.Background())
		p := requireStatus(t, err, http.StatusInternalServerError)
		assert.Equal(t, "disk full", p.Detail)
		assert.Nil(t, client.StartupOf(err))
	})

	t.Run("a body that isn't a problem is the detail", func(t *testing.T) {
		c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Request-Id", "req-8")
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, "upstream went away\n")
		})
		_, err := c.Stats(context.Background())
		p := requireStatus(t, err, http.StatusBadGateway)
		assert.Equal(t, "Bad Gateway", p.Title)
		assert.Equal(t, "upstream went away", p.Detail)
		assert.Equal(t, "req-8", p.RequestID, "from the header when the body has none")
	})

	t.Run("an error body is read to a bound", func(t *testing.T) {
		c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, strings.Repeat("x", 1<<20))
		})
		_, err := c.Stats(context.Background())
		p := requireStatus(t, err, http.StatusInternalServerError)
		assert.Len(t, p.Detail, 64<<10)
	})
}

// TestRequestsCarryNoFetchMetadata: the client is not a browser, and says
// nothing a browser would about where a request came from, so the daemon's
// Sec-Fetch-Site rules never apply to it.
func TestRequestsCarryNoFetchMetadata(t *testing.T) {
	var seen []http.Header
	c := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	})
	ctx := context.Background()
	_, err := c.Stats(ctx)
	require.NoError(t, err)
	_, err = c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: "https://example.com/a"})
	require.NoError(t, err)
	_, err = c.UpdateQueue(ctx, client.QueueUpdate{Paused: new(true)})
	require.NoError(t, err)
	_, err = c.DeleteJobsByStatus(ctx, "failed")
	require.NoError(t, err)

	require.Len(t, seen, 4)
	for _, h := range seen {
		for name := range h {
			assert.False(t, strings.HasPrefix(name, "Sec-Fetch-"), name)
		}
		assert.Empty(t, h.Get("Origin"))
	}
}

// TestResponsesAreReadTolerantly: a newer daemon's extra fields are ignored.
func TestResponsesAreReadTolerantly(t *testing.T) {
	c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"v9","bookmarks_total":3,"documents_total":2,"added_in_v9":{"x":1}}`)
	})
	stats, err := c.Stats(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, stats.BookmarksTotal)
}

// TestImport_OmitsUnknownDates: a bookmark without a date is sent without
// saved_at, not as the year 1.
func TestImport_OmitsUnknownDates(t *testing.T) {
	var body map[string]any
	c := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"source":"html","total":1}`)
	})
	_, err := c.ImportBookmarks(context.Background(), client.ImportRequest{Source: "html",
		Bookmarks: []client.ImportBookmark{{URL: "https://example.com/a"}}})
	require.NoError(t, err)
	bookmarks, ok := body["bookmarks"].([]any)
	require.True(t, ok, "%v", body)
	require.Len(t, bookmarks, 1)
	assert.NotContains(t, bookmarks[0], "saved_at")
}

func TestBookmarks(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()

	created, err := c.CreateBookmark(ctx, client.CreateBookmarkRequest{
		URL: "https://example.com/a", Title: "A", FolderPath: "/Reading", Tags: []string{"go"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, created.JobID)
	assert.Equal(t, "https://example.com/a", created.Bookmark.URL)
	assert.Equal(t, "manual", created.Bookmark.Source)
	assert.Equal(t, "pending", created.Bookmark.DocumentState)
	require.NotNil(t, created.Bookmark.Title)
	assert.Equal(t, "A", *created.Bookmark.Title)

	res, err := c.ImportBookmarks(ctx, client.ImportRequest{Source: "chrome", Bookmarks: []client.ImportBookmark{
		{URL: "https://example.com/a", SavedAt: time.Now().UTC()},
		{URL: "https://example.com/b", FolderPath: "/Reading/Go"},
		{URL: "https://example.com/c"},
		{URL: "file:///etc/passwd"},
	}})
	require.NoError(t, err)
	assert.Equal(t, "chrome", res.Source)
	assert.Equal(t, 4, res.Total)
	assert.Equal(t, 3, res.Created)
	assert.Equal(t, 1, res.Filtered)
	assert.Equal(t, 1, res.FilteredBy["local_file"])
	assert.Equal(t, 2, res.JobsEnqueued, "example.com/a was already a document")

	first, err := c.ListBookmarks(ctx, client.BookmarkListOpts{Limit: 3})
	require.NoError(t, err)
	assert.Len(t, first.Items, 3)
	require.NotEmpty(t, first.NextCursor)
	rest, err := c.ListBookmarks(ctx, client.BookmarkListOpts{Limit: 3, Cursor: first.NextCursor})
	require.NoError(t, err)
	assert.Len(t, rest.Items, 1)
	assert.Empty(t, rest.NextCursor)

	chrome, err := c.ListBookmarks(ctx, client.BookmarkListOpts{Source: "chrome", Folder: "/Reading"})
	require.NoError(t, err)
	require.Len(t, chrome.Items, 1)
	assert.Equal(t, "https://example.com/b", chrome.Items[0].URL)

	assert.Equal(t, 4, countRows(t, s, "bookmarks"))
}

// TestImport_DryRun: a dry run reports what the import would do, with the
// URLs it would fetch, and writes nothing.
func TestImport_DryRun(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	_, err := c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: "https://example.com/a"})
	require.NoError(t, err)

	res, err := c.ImportBookmarks(ctx, client.ImportRequest{Source: "chrome", DryRun: true,
		Bookmarks: []client.ImportBookmark{{URL: "https://example.com/a"}, {URL: "https://example.com/b"}}})
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	assert.Equal(t, 2, res.Created)
	assert.Equal(t, 1, res.JobsEnqueued)
	assert.Equal(t, []string{"https://example.com/b"}, res.NewURLs)
	assert.Equal(t, 1, countRows(t, s, "bookmarks"))
}

func TestDocuments(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	fetched := s.AddDocument(t, "https://example.com/fetched", store.DocStateFetched)
	s.AddContent(t, fetched, "# Fetched\n\nThe body.")
	s.AddDocument(t, "https://example.com/pending", store.DocStatePending)

	doc, err := c.GetDocument(ctx, fetched.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/fetched", doc.URL)
	assert.Equal(t, "fetched", doc.State)
	require.NotNil(t, doc.CurrentExtraction)
	assert.Equal(t, "apitest", doc.CurrentExtraction.Fetcher)
	assert.NotEmpty(t, doc.CurrentExtraction.MarkdownPath)
	assert.Equal(t, "apitest", doc.CurrentExtraction.ExtractionMeta["source"])

	body, err := c.GetDocumentContent(ctx, fetched.ID)
	require.NoError(t, err)
	assert.Equal(t, "# Fetched\n\nThe body.", body)

	all, err := c.ListDocuments(ctx, client.ListDocumentsOpts{})
	require.NoError(t, err)
	assert.Len(t, all.Items, 2)
	pending, err := c.ListDocuments(ctx, client.ListDocumentsOpts{State: "pending", Limit: 10})
	require.NoError(t, err)
	require.Len(t, pending.Items, 1)
	assert.Equal(t, "https://example.com/pending", pending.Items[0].URL)
	withPath, err := c.ListDocuments(ctx, client.ListDocumentsOpts{State: "fetched"})
	require.NoError(t, err)
	require.Len(t, withPath.Items, 1)
	assert.Contains(t, withPath.Items[0].MarkdownPath, s.Home.ContentDir())

	first, err := c.ListDocuments(ctx, client.ListDocumentsOpts{Limit: 1})
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	rest, err := c.ListDocuments(ctx, client.ListDocumentsOpts{Limit: 1, Cursor: first.NextCursor})
	require.NoError(t, err)
	require.Len(t, rest.Items, 1)
	assert.Empty(t, rest.NextCursor)
	assert.ElementsMatch(t, []string{fetched.ID, pending.Items[0].ID}, []string{first.Items[0].ID, rest.Items[0].ID})
}

func TestRefetchAndReindex(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	dead := s.AddDocument(t, "https://example.com/gone", store.DocStateDead)
	fetched := s.AddDocument(t, "https://example.com/fetched", store.DocStateFetched)
	s.AddContent(t, fetched, "content")

	_, err := c.RefetchDocument(ctx, dead.ID, false)
	requireStatus(t, err, http.StatusConflict) // a dead document needs force
	forced, err := c.RefetchDocument(ctx, dead.ID, true)
	require.NoError(t, err)
	assert.NotEmpty(t, forced.JobID)

	all, err := c.RefetchAll(ctx, client.RefetchAllOpts{State: "fetched"})
	require.NoError(t, err)
	assert.Equal(t, 1, all.JobsEnqueued)
	_, err = c.RefetchAll(ctx, client.RefetchAllOpts{State: "bogus"})
	requireStatus(t, err, http.StatusBadRequest)

	one, err := c.ReindexDocument(ctx, fetched.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, one.JobID)
	reindexed, err := c.ReindexAll(ctx, "pending")
	require.NoError(t, err)
	assert.Equal(t, 1, reindexed.JobsEnqueued, "the refetch left the document with content pending")
	reindexed, err = c.ReindexAll(ctx, "")
	require.NoError(t, err)
	assert.Zero(t, reindexed.JobsEnqueued, "nothing is fetched any more")
}

// TestFailureCauses: a refetch-all by cause sends the state and the cause,
// escaped, and requeues only what they match; documents decode their
// causes.
func TestFailureCauses(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	blocked := s.AddFailedDocument(t, "https://example.com/blocked", store.FailureCauseAntiBot)
	s.AddFailedDocument(t, "https://example.com/slow", store.FailureCauseTimeout)
	gone := s.AddFailedDocument(t, "https://example.com/gone", store.FailureCauseDeadLink)

	doc, err := c.GetDocument(ctx, blocked.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", doc.State)
	assert.Equal(t, "anti_bot", doc.FailureCause)
	list, err := c.ListDocuments(ctx, client.ListDocumentsOpts{State: "dead"})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "dead_link", list.Items[0].FailureCause)

	var sent url.Values
	fake := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		sent = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"jobs_enqueued":3}`)
	})
	res, err := fake.RefetchAll(ctx, client.RefetchAllOpts{State: "failed", Cause: "anti bot&state=dead"})
	require.NoError(t, err)
	assert.Equal(t, 3, res.JobsEnqueued)
	assert.Equal(t, url.Values{"state": {"failed"}, "cause": {"anti bot&state=dead"}}, sent, "each value escaped")

	_, err = c.RefetchAll(ctx, client.RefetchAllOpts{Cause: "dead_link"})
	requireStatus(t, err, http.StatusBadRequest)

	res, err = c.RefetchAll(ctx, client.RefetchAllOpts{Cause: "anti_bot"})
	require.NoError(t, err)
	assert.Equal(t, 1, res.JobsEnqueued)
	res, err = c.RefetchAll(ctx, client.RefetchAllOpts{State: "dead", Cause: "dead_link"})
	require.NoError(t, err)
	assert.Equal(t, 1, res.JobsEnqueued)
	for _, d := range []*store.Document{blocked, gone} {
		got, err := c.GetDocument(ctx, d.ID)
		require.NoError(t, err)
		assert.Equal(t, "pending", got.State)
		assert.Empty(t, got.FailureCause)
	}
	assert.Equal(t, 2, countRows(t, s, "jobs"))
}

func TestJobs(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	doc := s.AddDocument(t, "https://example.com/a", store.DocStateFailed)
	for _, status := range []store.JobStatus{store.JobStatusDone, store.JobStatusFailed, store.JobStatusFailed} {
		job, err := store.NewDocumentJob(apitest.TenantID, store.JobKindFetch, doc.ID)
		require.NoError(t, err)
		job.Status = status
		require.NoError(t, s.Deps.Queue.Enqueue(ctx, job))
	}
	require.NoError(t, s.Deps.Queue.Enqueue(ctx, &store.Job{TenantID: apitest.TenantID, Kind: store.JobKindCluster}))

	failed, err := c.ListJobs(ctx, client.JobListOpts{Status: "failed", Kind: "fetch", Limit: 10})
	require.NoError(t, err)
	require.Len(t, failed.Items, 2)
	assert.Equal(t, "https://example.com/a", failed.Items[0].DocURL)
	clusters, err := c.ListJobs(ctx, client.JobListOpts{Kind: "cluster"})
	require.NoError(t, err)
	require.Len(t, clusters.Items, 1)
	assert.Empty(t, clusters.Items[0].DocURL)

	seen := map[string]bool{}
	for cursor := ""; ; {
		page, err := c.ListJobs(ctx, client.JobListOpts{Limit: 3, Cursor: cursor})
		require.NoError(t, err)
		for _, j := range page.Items {
			assert.False(t, seen[j.ID], "job %s on two pages", j.ID)
			seen[j.ID] = true
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	assert.Len(t, seen, 4, "every job, over two pages")

	job, err := c.GetJob(ctx, failed.Items[0].ID)
	require.NoError(t, err)
	assert.Equal(t, failed.Items[0], *job, "one job reads as the list shows it")
	_, err = c.GetJob(ctx, "no-such-job")
	requireStatus(t, err, http.StatusNotFound)

	deleted, err := c.DeleteJobsByStatus(ctx, "failed")
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted.Deleted)
	assert.Equal(t, "status=failed", deleted.Mode)
	_, err = c.DeleteJobsByStatus(ctx, "pending")
	requireStatus(t, err, http.StatusBadRequest)

	// The done job is the only finished one left; a 0s cutoff takes it
	// unless it was written in the cutoff's millisecond. The pending
	// cluster job is live work and never pruned.
	pruned, err := c.PruneJobsOlderThan(ctx, "0s")
	require.NoError(t, err)
	assert.LessOrEqual(t, pruned.Deleted, int64(1))
	assert.Equal(t, "older_than=0s", pruned.Mode)
	_, err = c.PruneJobsOlderThan(ctx, "soon")
	requireStatus(t, err, http.StatusBadRequest)
}

func TestStatsAndMetrics(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	_, err := c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: "https://example.com/a"})
	require.NoError(t, err)
	s.AddDocument(t, "https://example.com/b", store.DocStateFetched)

	stats, err := c.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.BookmarksTotal)
	assert.Equal(t, 2, stats.DocumentsTotal)
	assert.Equal(t, map[string]int{"pending": 1, "fetched": 1}, stats.DocumentsByState)
	assert.Equal(t, map[string]int{"pending": 1}, stats.JobsByStatus)

	_, err = s.DB.Exec(`INSERT INTO jobs (id, tenant_id, kind, payload, status, started_at, updated_at)
		VALUES ('j1', 'local', 'index', '{}', 'done',
		        strftime('%Y-%m-%dT%H:%M:%fZ','now','-2 seconds'), strftime('%Y-%m-%dT%H:%M:%fZ','now'))`)
	require.NoError(t, err)
	m, err := c.Metrics(ctx, 600)
	require.NoError(t, err)
	assert.Equal(t, 600, m.WindowSeconds)
	require.Len(t, m.ByKind, 1)
	assert.Equal(t, "index", m.ByKind[0].Kind)
	assert.Equal(t, 1, m.ByKind[0].Count)
	assert.InDelta(t, 2000, m.ByKind[0].MeanMS, 50)

	m, err = c.Metrics(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, 3600, m.WindowSeconds, "the server's default window")
}

func TestSearchAndRelated(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	for i, body := range []string{"kafka partitions and consumer groups", "kafka brokers and partitions", "sourdough bread"} {
		doc := s.AddDocument(t, fmt.Sprintf("https://example.com/%d", i), store.DocStateFetched)
		s.AddContent(t, doc, body)
	}
	docs, err := c.ListDocuments(ctx, client.ListDocumentsOpts{})
	require.NoError(t, err)
	require.Len(t, docs.Items, 3)

	res, err := c.Search(ctx, client.SearchRequest{Query: "kafka partitions", K: 5})
	require.NoError(t, err)
	assert.False(t, res.Degraded)
	assert.Equal(t, 2, res.BM25Hits)
	require.Len(t, res.Items, 3, "vector search ranks every document")
	for _, hit := range res.Items[:2] {
		assert.Contains(t, []string{"https://example.com/0", "https://example.com/1"}, hit.Document.URL)
		assert.NotEmpty(t, hit.MarkdownPath)
		require.NotEmpty(t, hit.Matches)
	}
	assert.Equal(t, "https://example.com/2", res.Items[2].Document.URL)

	filtered, err := c.Search(ctx, client.SearchRequest{Query: "kafka",
		Filters: &client.SearchFilters{Host: []string{"other.example"}}})
	require.NoError(t, err)
	assert.Empty(t, filtered.Items)

	kafka := res.Items[0].Document.ID
	related, err := c.RelatedDocuments(ctx, kafka, 1)
	require.NoError(t, err)
	assert.Equal(t, kafka, related.DocID)
	require.Len(t, related.Items, 1)
	assert.NotEqual(t, kafka, related.Items[0].Document.ID)
	assert.Contains(t, []string{"https://example.com/0", "https://example.com/1"}, related.Items[0].Document.URL,
		"the other kafka document is the nearest")

	_, err = c.RelatedDocuments(ctx, "no-such-document", 0)
	requireStatus(t, err, http.StatusNotFound)
}

func TestInterests(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()

	none, err := c.ListInterests(ctx, client.ListInterestsOpts{})
	require.NoError(t, err)
	assert.Empty(t, none.Items)
	assert.Empty(t, none.RunID)
	assert.Equal(t, client.StateNone, none.Next.State)

	a := s.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	b := s.AddDocument(t, "https://example.com/b", store.DocStateFetched)
	loose := s.AddDocument(t, "https://example.com/loose", store.DocStateFetched)
	unsorted := s.AddDocument(t, "https://example.com/unsorted", store.DocStateFetched)
	s.AddContent(t, a, "go")
	run := s.AddRun(t, apitest.RunSpec{Areas: []apitest.Area{{Label: "Programming", Interests: []apitest.Interest{
		{Label: "Go", Members: []*store.Document{a, b}, Loose: []*store.Document{loose}},
		{Label: "Rust", Size: 1}}}}, Unsorted: []*store.Document{unsorted}})
	area, goID := run.Areas[0], run.Interests[0]

	list, err := c.ListInterests(ctx, client.ListInterestsOpts{Limit: 5, Children: 1, Members: 1})
	require.NoError(t, err)
	assert.Equal(t, run.ID, list.RunID)
	assert.Equal(t, "apitest", list.Algo)
	assert.Equal(t, "areas", list.Shape)
	assert.Equal(t, 5, list.NumDocuments)
	assert.Equal(t, 1, list.NumAreas)
	assert.Equal(t, 2, list.NumInterests)
	assert.Equal(t, 1, list.NumLoose)
	assert.Equal(t, 1, list.NumUnsorted)
	assert.Equal(t, 1, list.Total)
	assert.Equal(t, client.StateNone, list.Next.State, "the scheduler's state, which the test sets")
	require.NotNil(t, list.Rebuild)
	assert.Equal(t, "fresh", list.Rebuild.Kind)
	require.Len(t, list.Items, 1)
	got := list.Items[0]
	assert.Equal(t, client.LevelArea, got.Level)
	assert.Equal(t, "Programming", got.Label)
	assert.Equal(t, 2, got.NumChildren)
	require.Len(t, got.Children, 1, "children=1")
	assert.Equal(t, goID, got.Children[0].ID)
	assert.Equal(t, area, got.Children[0].ParentID)
	require.Len(t, got.Children[0].Members, 1, "members=1")
	assert.Equal(t, a.ID, got.Children[0].Members[0].DocID)
	assert.Equal(t, "member", got.Children[0].Members[0].Fit)
	assert.NotEmpty(t, got.Children[0].Members[0].MarkdownPath)

	flat, err := c.ListInterests(ctx, client.ListInterestsOpts{Level: client.LevelInterest, Offset: 1})
	require.NoError(t, err)
	assert.Equal(t, 2, flat.Total)
	require.Len(t, flat.Items, 1)
	assert.Equal(t, "Rust", flat.Items[0].Label)
	assert.Equal(t, "Programming", flat.Items[0].ParentLabel)

	one, err := c.GetInterest(ctx, goID, client.GetInterestOpts{Members: 10})
	require.NoError(t, err)
	assert.Equal(t, "Go", one.Label)
	assert.Equal(t, 1, one.Loose)
	require.Len(t, one.Members, 3, "members, then the loose fit")
	assert.Equal(t, "loose", one.Members[2].Fit)
	page, err := c.GetInterest(ctx, goID, client.GetInterestOpts{Members: 1, Offset: 2})
	require.NoError(t, err)
	require.Len(t, page.Members, 1)
	assert.Equal(t, loose.ID, page.Members[0].DocID)
	areaPage, err := c.GetInterest(ctx, area, client.GetInterestOpts{})
	require.NoError(t, err)
	assert.Len(t, areaPage.Children, 2)

	pile, err := c.UnsortedInterests(ctx, client.UnsortedOpts{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, pile.Total)
	require.Len(t, pile.Items, 1)
	assert.Equal(t, unsorted.ID, pile.Items[0].DocID)
	assert.Equal(t, goID, pile.Items[0].NearestID)
	assert.Equal(t, "Go", pile.Items[0].NearestLabel)

	_, err = c.GetInterest(ctx, "no-such-interest", client.GetInterestOpts{})
	requireStatus(t, err, http.StatusNotFound)
	assert.Nil(t, client.RetiredOf(err), "a 404 retired nothing")

	split := s.SplitInterest(t, run, goID, apitest.Interest{Label: "Go Web", Members: []*store.Document{a}},
		apitest.Interest{Label: "Go Tools", Members: []*store.Document{b}})
	_, err = c.GetInterest(ctx, goID, client.GetInterestOpts{})
	requireStatus(t, err, http.StatusGone)
	retired := client.RetiredOf(err)
	require.NotNil(t, retired)
	assert.Equal(t, goID, retired.ID)
	assert.Equal(t, "Go", retired.Label)
	assert.Equal(t, split.ID, retired.RunID)
	assert.False(t, retired.RetiredAt.IsZero())
	require.Len(t, retired.Successors, 2)
	assert.Equal(t, "split", retired.Successors[0].Event)
	assert.ElementsMatch(t, []string{"Go Web", "Go Tools"}, []string{retired.Successors[0].Label, retired.Successors[1].Label})
	assert.Contains(t, err.Error(), `interest "Go" was retired by the rebuild of`)

	changes, err := c.InterestChanges(ctx)
	require.NoError(t, err)
	assert.Equal(t, split.ID, changes.RunID)
	require.NotNil(t, changes.Rebuild)
	assert.Equal(t, 1, changes.Rebuild.Split)
	require.Len(t, changes.Events, 2)
	assert.Equal(t, "split", changes.Events[0].Event)
	assert.Equal(t, goID, changes.Events[0].From.ID)
	assert.True(t, changes.Events[0].From.Retired)

	rebuild, err := c.RebuildInterests(ctx, false)
	require.NoError(t, err)
	assert.NotEmpty(t, rebuild.JobID)
	again, err := c.RebuildInterests(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, rebuild.JobID, again.JobID, "the rebuild already queued")
	st, err := s.Deps.Insights.State(ctx, apitest.TenantID)
	require.NoError(t, err)
	assert.Equal(t, store.FreshManual, st.FreshOwed, "fresh reached the daemon")
	assert.Equal(t, 2, s.Scheduler.Kicks())
}

// TestInterestsState: the client reads every field of where automatic
// rebuilds stand, on healthz and as the list's next.
func TestInterestsState(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	at := func(h int) time.Time { return time.Date(2026, 10, 9, h, 0, 0, 0, time.UTC) }
	s.Scheduler.Set(insight.Snapshot{State: insight.StateFailing, LastRebuildAt: at(10), LastKind: store.RunKindWarm,
		LastTrigger: store.RunTriggerAuto, Changed: 271, RebuildAt: 263, DueSince: at(11),
		FreshOwed: string(store.FreshReindex), HeldReason: "the embeddings drifted", RetryAt: at(12), LastError: "boom",
		Map: insight.MapState{Status: store.MapFailed, Took: 2 * time.Minute, Error: "the map took longer than 2m0s"}})
	took := int64(120000)
	want := client.InterestsState{State: client.StateFailing, LastRebuildAt: at(10), LastKind: "warm",
		LastTrigger: "auto", ChangedDocuments: 271, RebuildAt: 263, DueSince: at(11), FreshOwed: client.FreshReindex,
		HeldReason: "the embeddings drifted", RetryAt: at(12), LastError: "boom",
		Map: &client.InterestsMap{Status: client.MapFailed, TookMS: &took, Error: "the map took longer than 2m0s"}}

	health, err := c.Healthz(ctx)
	require.NoError(t, err)
	require.NotNil(t, health.Interests)
	assert.Equal(t, want, *health.Interests)
	list, err := c.ListInterests(ctx, client.ListInterestsOpts{})
	require.NoError(t, err)
	assert.Equal(t, want, list.Next)
}

// TestRetiredOf_OnlyTheRetiredProblem: only a 410 of the retired-interest
// problem type is read as a retired interest.
func TestRetiredOf_OnlyTheRetiredProblem(t *testing.T) {
	body := `{"type":"urn:curio:problem:interest-retired","title":"interest retired","status":410,` +
		`"detail":"gone","id":"i1","level":"interest","retired_at":"2026-10-09T14:03:11Z","run_id":"r2",` +
		`"successors":[{"id":"i2","level":"interest","event":"merged","shared":4,"retired":false}]}`
	for name, tc := range map[string]struct {
		status int
		ctype  string
		body   string
		want   bool
	}{
		"retired":         {http.StatusGone, "application/problem+json", body, true},
		"another status":  {http.StatusNotFound, "application/problem+json", body, false},
		"another type":    {http.StatusGone, "application/problem+json", strings.Replace(body, "interest-retired", "other", 1), false},
		"not a problem":   {http.StatusGone, "text/plain", body, false},
		"without members": {http.StatusGone, "application/problem+json", `{"type":"urn:curio:problem:interest-retired","title":"gone","status":410}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			_, err := client.New(srv.URL).GetInterest(context.Background(), "i1", client.GetInterestOpts{})
			require.Error(t, err)
			r := client.RetiredOf(err)
			if !tc.want {
				assert.Nil(t, r)
				return
			}
			require.NotNil(t, r)
			assert.Equal(t, client.RetiredInterest{ID: "i1", Level: "interest", RetiredAt: time.Date(2026, 10, 9, 14, 3, 11, 0, time.UTC),
				RunID: "r2", Successors: []client.InterestSuccessor{{ID: "i2", Level: "interest", Event: "merged", Shared: 4}}}, *r)
		})
	}
}

func countRows(t *testing.T, s *apitest.Server, table string) int {
	t.Helper()
	var n int
	require.NoError(t, s.DB.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
	return n
}

// TestQueue: an update sends only the fields it sets, so the others keep
// their values.
func TestQueue(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()

	q, err := c.Queue(ctx)
	require.NoError(t, err)
	assert.Equal(t, &client.Queue{Throttle: client.ThrottleNormal, State: client.QueueOpen, Kinds: []client.QueueKind{
		{Kind: "fetch", Limit: apitest.Pools.Fetch}, {Kind: "index", Limit: apitest.Pools.Index}, {Kind: "cluster", Limit: 1},
	}}, q)

	later := time.Now().UTC().Add(24 * time.Minute).Truncate(time.Millisecond)
	require.NoError(t, s.Deps.Queue.Enqueue(ctx, &store.Job{TenantID: apitest.TenantID, Kind: store.JobKindCluster,
		RunAfter: later}))
	q, err = c.Queue(ctx)
	require.NoError(t, err)
	assert.Equal(t, client.QueueKind{Kind: "cluster", Limit: 1, Pending: 1, DueLater: 1, NextDue: later}, q.Kinds[2])

	q, err = c.UpdateQueue(ctx, client.QueueUpdate{Paused: new(true), Schedule: "22:00-07:00"})
	require.NoError(t, err)
	assert.True(t, q.Paused)
	assert.Equal(t, client.QueueClosed, q.State)
	assert.Equal(t, client.ReasonPaused, q.Reason)
	assert.Equal(t, "22:00-07:00", q.Schedule)

	q, err = c.UpdateQueue(ctx, client.QueueUpdate{Throttle: client.ThrottleGentle})
	require.NoError(t, err)
	assert.Equal(t, client.ThrottleGentle, q.Throttle)
	assert.True(t, q.Paused, "the pause is left as it was")
	assert.Equal(t, "22:00-07:00", q.Schedule, "and the schedule")
	assert.Equal(t, 4, q.Kinds[0].Limit)

	q, err = c.UpdateQueue(ctx, client.QueueUpdate{Schedule: client.ScheduleOff})
	require.NoError(t, err)
	assert.Empty(t, q.Schedule)
	assert.True(t, s.Deps.Gate.State(time.Now()).Settings.Paused)

	_, err = c.UpdateQueue(ctx, client.QueueUpdate{Throttle: "fast"})
	requireStatus(t, err, http.StatusBadRequest)
}
