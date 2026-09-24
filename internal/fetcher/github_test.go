package fetcher

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/urlutil"
)

// newTestGitHub points a GitHub fetcher at srv with no pacing and a fake
// clock, so rate-limit waits are recorded instead of slept.
func newTestGitHub(t *testing.T, srv *httptest.Server) *GitHub {
	t.Helper()
	return &GitHub{
		baseURL: srv.URL,
		client:  srv.Client(),
		limiter: rate.NewLimiter(rate.Inf, 1),
		maxBody: maxResponseBytes,
		clock:   newFakeClock().clock(),
		log:     slog.Default(),
	}
}

func TestGitHubFetch_Repo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/coolproject", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"description": "A cool project for testing",
			"language": "Go",
			"stargazers_count": 1234,
			"forks_count": 56,
			"topics": ["testing", "go", "example"],
			"default_branch": "main",
			"archived": false,
			"created_at": "2024-01-15T10:30:00Z",
			"license": {"name": "MIT License"}
		}`))
	})
	mux.HandleFunc("/repos/owner/coolproject/readme", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# Cool Project\n\nThis is a cool project for doing cool things.\n\n## Usage\n\n```go\ncool.Do()\n```"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestGitHub(t, srv)
	result, err := g.Fetch(t.Context(), "https://github.com/owner/coolproject")
	require.NoError(t, err)

	assert.Equal(t, "repo", result.ContentType)
	assert.Equal(t, "owner/coolproject", result.Title)
	assert.Equal(t, "owner", result.Author)
	assert.Contains(t, result.Markdown, "A cool project for testing")
	assert.Contains(t, result.Markdown, "**Language:** Go")
	assert.Contains(t, result.Markdown, "**Stars:** 1234")
	assert.Contains(t, result.Markdown, "**Topics:** testing, go, example")
	assert.Contains(t, result.Markdown, "## README")
	assert.Contains(t, result.Markdown, "cool.Do()")
	assert.Equal(t, "github-api", result.Meta["via"])
	assert.Equal(t, 1234, result.Meta["stars"])
}

func TestGitHubFetch_File(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/myrepo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"description": "My repository",
			"language": "Python",
			"stargazers_count": 42,
			"forks_count": 3,
			"topics": [],
			"default_branch": "main",
			"archived": false,
			"created_at": "2023-06-01T00:00:00Z"
		}`))
	})
	mux.HandleFunc("/repos/owner/myrepo/contents/docs/guide.md", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "v2.0", r.URL.Query().Get("ref"))
		_, _ = w.Write([]byte("# Guide\n\nThis is the guide content."))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestGitHub(t, srv)
	result, err := g.Fetch(t.Context(), "https://github.com/owner/myrepo/blob/v2.0/docs/guide.md")
	require.NoError(t, err)

	assert.Equal(t, "article", result.ContentType)
	assert.Equal(t, "docs/guide.md", result.Title)
	assert.Contains(t, result.Markdown, "# docs/guide.md")
	assert.Contains(t, result.Markdown, "**Repository:** owner/myrepo — My repository")
	assert.Contains(t, result.Markdown, "This is the guide content.")
}

func TestGitHubFetch_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/gone", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message": "Not Found"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestGitHub(t, srv)
	_, err := g.Fetch(t.Context(), "https://github.com/owner/gone")
	require.Error(t, err)

	var pe *PermanentError
	assert.True(t, errors.As(err, &pe), "404 should be permanent")
}

// TestGitHubFetch_RateLimit: a primary rate limit whose reset time is
// already past still waits a minute between attempts (never zero), gives up
// after maxAPIRetries, and stays retryable for the job queue.
func TestGitHubFetch_RateLimit(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message": "rate limit exceeded"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestGitHub(t, srv)
	fc := newFakeClock()
	g.clock = fc.clock()
	_, err := g.Fetch(t.Context(), "https://github.com/owner/repo")
	require.Error(t, err)

	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "rate limit should be transient")
	assert.Contains(t, err.Error(), "rate limited")
	assert.Equal(t, int32(maxAPIRetries), hits.Load())
	assert.Equal(t, []time.Duration{time.Minute, time.Minute}, fc.slept())
}

// TestGitHub_StatusMatrix: 404 and other deterministic 4xx answers are
// permanent (GitHub returns 410 for deleted issues and 451 for DMCA'd
// repos); server errors stay retryable. Error text quotes at most
// maxErrorBody bytes of the response body.
func TestGitHub_StatusMatrix(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool
	}{
		{http.StatusNotFound, true},
		{http.StatusBadRequest, true},
		{http.StatusGone, true},
		{http.StatusUnprocessableEntity, true},
		{http.StatusUnavailableForLegalReasons, true},
		{http.StatusUnauthorized, true},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
		{http.StatusServiceUnavailable, false},
	}
	bigBody := strings.Repeat("x", 10*maxErrorBody)
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(bigBody))
			}))
			defer srv.Close()

			_, err := newTestGitHub(t, srv).Fetch(t.Context(), "https://github.com/owner/repo/issues/1")
			require.Error(t, err)

			var pe *PermanentError
			assert.Equal(t, tc.permanent, errors.As(err, &pe), "permanent: %v", err)
			var se *HTTPStatusError
			require.ErrorAs(t, err, &se)
			assert.Equal(t, tc.status, se.StatusCode)
			assert.Less(t, len(err.Error()), maxErrorBody+256, "error text must not embed the whole body")
		})
	}
}

func TestGitHubFetch_NoReadme(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/noreadme", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"description": "A project without a README",
			"language": "Rust",
			"stargazers_count": 10,
			"forks_count": 0,
			"topics": ["experimental"],
			"default_branch": "main",
			"archived": false,
			"created_at": "2025-01-01T00:00:00Z"
		}`))
	})
	mux.HandleFunc("/repos/owner/noreadme/readme", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message": "Not Found"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestGitHub(t, srv)
	result, err := g.Fetch(t.Context(), "https://github.com/owner/noreadme")
	require.NoError(t, err)

	assert.Contains(t, result.Markdown, "A project without a README")
	assert.NotContains(t, result.Markdown, "## README")
}

func TestFormatRepoMarkdown(t *testing.T) {
	meta := &ghRepoMeta{
		Description:   "A distributed database",
		Language:      "Go",
		Stars:         5000,
		License:       "Apache-2.0",
		Topics:        []string{"database", "distributed"},
		Archived:      false,
		DefaultBranch: "main",
	}
	readme := "# MyDB\n\nA fast distributed database."

	md := formatRepoMarkdown(meta, readme)

	assert.True(t, strings.HasPrefix(md, "# A distributed database"))
	assert.Contains(t, md, "**Language:** Go")
	assert.Contains(t, md, "**Stars:** 5000")
	assert.Contains(t, md, "**License:** Apache-2.0")
	assert.Contains(t, md, "**Topics:** database, distributed")
	assert.Contains(t, md, "## README")
	assert.Contains(t, md, "A fast distributed database.")
}

func TestFormatRepoMarkdown_Archived(t *testing.T) {
	meta := &ghRepoMeta{
		Description: "Old project",
		Stars:       100,
		Archived:    true,
	}
	md := formatRepoMarkdown(meta, "")
	assert.Contains(t, md, "**Status:** archived")
	assert.NotContains(t, md, "## README")
}

func TestGitHubFetch_UnsupportedType(t *testing.T) {
	g := &GitHub{baseURL: "https://api.github.com", client: http.DefaultClient, limiter: rate.NewLimiter(rate.Inf, 1), log: slog.Default()}
	_, err := g.Fetch(t.Context(), "https://github.com/owner/repo/actions/runs/12345")
	require.Error(t, err)

	var pe *PermanentError
	assert.True(t, errors.As(err, &pe), "unsupported type should be permanent")
}

func TestGitHubFetch_Issue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/123", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"title": "Crash when parsing empty file",
			"body": "Steps to reproduce:\n1. Create an empty file\n2. Run the parser",
			"state": "closed",
			"state_reason": "completed",
			"user": {"login": "reporter"},
			"labels": [{"name": "bug"}, {"name": "parser"}],
			"comments": 2,
			"created_at": "2025-03-10T08:00:00Z"
		}`))
	})
	mux.HandleFunc("/repos/owner/repo/issues/123/comments", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"user": {"login": "maintainer"}, "body": "Can you share the stack trace?", "created_at": "2025-03-10T09:00:00Z"},
			{"user": {"login": "reporter"}, "body": "Attached above. Fixed by #124.", "created_at": "2025-03-11T10:00:00Z"}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestGitHub(t, srv)
	result, err := g.Fetch(t.Context(), "https://github.com/owner/repo/issues/123")
	require.NoError(t, err)

	assert.Equal(t, "thread", result.ContentType)
	assert.Equal(t, "owner/repo#123: Crash when parsing empty file", result.Title)
	assert.Equal(t, "reporter", result.Author)
	assert.Equal(t, "https://github.com/owner/repo/issues/123", result.FinalURL)
	assert.Contains(t, result.Markdown, "# Crash when parsing empty file (#123)")
	assert.Contains(t, result.Markdown, "**State:** closed (completed)")
	assert.Contains(t, result.Markdown, "**Labels:** bug, parser")
	assert.Contains(t, result.Markdown, "Steps to reproduce")
	assert.Contains(t, result.Markdown, "## Comments")
	assert.Contains(t, result.Markdown, "### maintainer (2025-03-10)")
	assert.Contains(t, result.Markdown, "Can you share the stack trace?")
	assert.NotContains(t, result.Markdown, "more comments not shown")
	assert.Equal(t, false, result.Meta["is_pull"])
	assert.Equal(t, 123, result.Meta["number"])
}

func TestGitHubFetch_Issue_TruncatedComments(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/9", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"title": "Popular issue",
			"body": "Lots of discussion.",
			"state": "open",
			"user": {"login": "someone"},
			"comments": 150,
			"created_at": "2025-01-01T00:00:00Z"
		}`))
	})
	mux.HandleFunc("/repos/owner/repo/issues/9/comments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"user": {"login": "a"}, "body": "first!", "created_at": "2025-01-02T00:00:00Z"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestGitHub(t, srv)
	result, err := g.Fetch(t.Context(), "https://github.com/owner/repo/issues/9")
	require.NoError(t, err)

	assert.Contains(t, result.Markdown, "_(149 more comments not shown)_")
}

func TestGitHubFetch_Pull(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/pulls/456", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"title": "Add retry logic to fetcher",
			"body": "Implements exponential backoff.",
			"state": "closed",
			"user": {"login": "contributor"},
			"labels": [{"name": "enhancement"}],
			"comments": 1,
			"created_at": "2025-05-01T12:00:00Z",
			"merged": true,
			"merged_at": "2025-05-03T12:00:00Z",
			"draft": false,
			"base": {"ref": "main"},
			"head": {"ref": "feature/retry"},
			"additions": 120,
			"deletions": 30,
			"changed_files": 4
		}`))
	})
	mux.HandleFunc("/repos/owner/repo/issues/456/comments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"user": {"login": "reviewer"}, "body": "LGTM", "created_at": "2025-05-02T12:00:00Z"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestGitHub(t, srv)
	// Web URL uses /pull/ (singular); the fake asserts the API path /pulls/.
	result, err := g.Fetch(t.Context(), "https://github.com/owner/repo/pull/456")
	require.NoError(t, err)

	assert.Equal(t, "thread", result.ContentType)
	assert.Equal(t, "owner/repo#456: Add retry logic to fetcher", result.Title)
	assert.Equal(t, "contributor", result.Author)
	assert.Equal(t, "https://github.com/owner/repo/pull/456", result.FinalURL)
	assert.Contains(t, result.Markdown, "**State:** merged")
	assert.Contains(t, result.Markdown, "**Branches:** main ← feature/retry")
	assert.Contains(t, result.Markdown, "**Diff:** +120 −30 across 4 files")
	assert.Contains(t, result.Markdown, "### reviewer (2025-05-02)")
	assert.Contains(t, result.Markdown, "LGTM")
	assert.Equal(t, true, result.Meta["is_pull"])
	assert.Equal(t, true, result.Meta["merged"])
	assert.Equal(t, "merged", result.Meta["state"])
}

func TestGitHubFetch_IssueURLThatIsAPull(t *testing.T) {
	// GitHub redirects /issues/N to /pull/N when N is a PR; the issues
	// API marks these with a pull_request key. We should follow suit.
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/77", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"title": "A PR in disguise",
			"state": "open",
			"user": {"login": "author"},
			"comments": 0,
			"created_at": "2025-06-01T00:00:00Z",
			"pull_request": {}
		}`))
	})
	mux.HandleFunc("/repos/owner/repo/pulls/77", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"title": "A PR in disguise",
			"body": "The real PR body.",
			"state": "open",
			"user": {"login": "author"},
			"comments": 0,
			"created_at": "2025-06-01T00:00:00Z",
			"merged": false,
			"draft": true,
			"base": {"ref": "main"},
			"head": {"ref": "fix"},
			"additions": 1,
			"deletions": 1,
			"changed_files": 1
		}`))
	})
	mux.HandleFunc("/repos/owner/repo/issues/77/comments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestGitHub(t, srv)
	result, err := g.Fetch(t.Context(), "https://github.com/owner/repo/issues/77")
	require.NoError(t, err)

	assert.Equal(t, true, result.Meta["is_pull"])
	assert.Equal(t, "draft", result.Meta["state"])
	assert.Equal(t, "https://github.com/owner/repo/pull/77", result.FinalURL)
	assert.Contains(t, result.Markdown, "The real PR body.")
}

func TestGitHubFetch_Wiki(t *testing.T) {
	apiMux := http.NewServeMux()
	apiSrv := httptest.NewServer(apiMux)
	defer apiSrv.Close()

	rawMux := http.NewServeMux()
	rawMux.HandleFunc("/wiki/owner/repo/Getting-Started.md", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Welcome to the project.\n\n## Install\n\nRun `make install`."))
	})
	rawSrv := httptest.NewServer(rawMux)
	defer rawSrv.Close()

	g := newTestGitHub(t, apiSrv)
	g.rawBaseURL = rawSrv.URL
	result, err := g.Fetch(t.Context(), "https://github.com/owner/repo/wiki/Getting-Started")
	require.NoError(t, err)

	assert.Equal(t, "article", result.ContentType)
	assert.Equal(t, "owner/repo wiki: Getting Started", result.Title)
	assert.Equal(t, "https://github.com/owner/repo/wiki/Getting-Started", result.FinalURL)
	assert.Contains(t, result.Markdown, "# Getting Started")
	assert.Contains(t, result.Markdown, "**Repository:** owner/repo (wiki)")
	assert.Contains(t, result.Markdown, "Run `make install`.")
	assert.Equal(t, true, result.Meta["wiki"])
}

func TestGitHubFetch_WikiHome(t *testing.T) {
	rawMux := http.NewServeMux()
	rawMux.HandleFunc("/wiki/owner/repo/Home.md", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("The wiki home page."))
	})
	rawSrv := httptest.NewServer(rawMux)
	defer rawSrv.Close()

	g := newTestGitHub(t, rawSrv) // baseURL unused for wiki
	g.rawBaseURL = rawSrv.URL
	result, err := g.Fetch(t.Context(), "https://github.com/owner/repo/wiki")
	require.NoError(t, err)

	assert.Contains(t, result.Markdown, "The wiki home page.")
	assert.Equal(t, "owner/repo wiki: Home", result.Title)
}

func TestGitHubFetch_WikiMissing(t *testing.T) {
	rawMux := http.NewServeMux() // no handlers: everything 404s
	rawSrv := httptest.NewServer(rawMux)
	defer rawSrv.Close()

	g := newTestGitHub(t, rawSrv)
	g.rawBaseURL = rawSrv.URL
	_, err := g.Fetch(t.Context(), "https://github.com/owner/repo/wiki/Nope")
	require.Error(t, err)

	var pe *PermanentError
	assert.True(t, errors.As(err, &pe), "missing wiki page should be permanent")
	assert.Contains(t, err.Error(), "wiki")
}

func TestFormatIssueMarkdown_NoComments(t *testing.T) {
	info := urlutil.GitHubURLInfo{Owner: "o", Repo: "r", Type: "issue", Number: 5}
	issue := &ghIssue{
		Title: "Quiet issue",
		Body:  "Nobody replied.",
		State: "open",
		User:  ghUser{Login: "lonely"},
	}
	md := formatIssueMarkdown(info, issue, nil)

	assert.True(t, strings.HasPrefix(md, "# Quiet issue (#5)"))
	assert.Contains(t, md, "**State:** open")
	assert.NotContains(t, md, "## Comments")
}

const repoMetaJSON = `{"description": "d", "default_branch": "main", "created_at": "2024-01-15T10:30:00Z"}`

// TestGitHub_RateLimitDelays: each rate-limit shape is detected and waited
// out for the right time, through the fake clock, before the call is
// retried and succeeds.
func TestGitHub_RateLimitDelays(t *testing.T) {
	fc := newFakeClock()
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		want    time.Duration
	}{
		{
			name:    "secondary limit with Retry-After",
			status:  http.StatusForbidden,
			headers: map[string]string{"Retry-After": "5", "X-RateLimit-Remaining": "4999"},
			want:    5 * time.Second,
		},
		{
			name:   "secondary limit by message only",
			status: http.StatusForbidden,
			body:   `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`,
			want:   time.Minute,
		},
		{
			name:   "429 without headers",
			status: http.StatusTooManyRequests,
			want:   time.Minute,
		},
		{
			name:   "primary limit waits for the reset",
			status: http.StatusForbidden,
			headers: map[string]string{
				"X-RateLimit-Remaining": "0",
				"X-RateLimit-Reset":     strconv.FormatInt(fc.now().Add(90*time.Second).Unix(), 10),
			},
			want: 90 * time.Second,
		},
		{
			name:    "primary limit with a past reset still waits a minute",
			status:  http.StatusForbidden,
			headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1700000000"},
			want:    time.Minute,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if hits.Add(1) == 1 {
					for k, v := range tc.headers {
						w.Header().Set(k, v)
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				_, _ = w.Write([]byte(repoMetaJSON))
			}))
			defer srv.Close()

			g := newTestGitHub(t, srv)
			clk := newFakeClock()
			g.clock = clk.clock()
			meta, err := g.repoMeta(t.Context(), "owner", "repo")
			require.NoError(t, err)
			assert.Equal(t, "main", meta.DefaultBranch)
			assert.Equal(t, []time.Duration{tc.want}, clk.slept())
			assert.Equal(t, int32(2), hits.Load())
		})
	}
}

// TestGitHub_LongRetryAfterFailsFast: a Retry-After beyond the inline cap
// isn't slept in the worker. The attempt fails retryably with the hint, and
// every call during the cooldown, from any Fetch, fails the same way
// without reaching GitHub.
func TestGitHub_LongRetryAfterFailsFast(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	g := newTestGitHub(t, srv)
	fc := newFakeClock()
	g.clock = fc.clock()

	for _, target := range []string{"https://github.com/owner/repo", "https://github.com/other/repo/issues/7"} {
		_, err := g.Fetch(t.Context(), target)
		require.Error(t, err)
		var pe *PermanentError
		assert.False(t, errors.As(err, &pe), "rate limit must stay retryable: %v", err)
		var se *HTTPStatusError
		require.ErrorAs(t, err, &se)
		assert.Equal(t, 600*time.Second, se.RetryAfter)
	}
	assert.Equal(t, int32(1), hits.Load(), "calls during the cooldown must not reach GitHub")
	assert.Empty(t, fc.slept(), "a long cooldown must not be slept inline")
}

// TestGitHub_ForbiddenWithoutRateLimitIsPermanent: a 403 with no
// rate-limit signal is a real permission answer.
func TestGitHub_ForbiddenWithoutRateLimitIsPermanent(t *testing.T) {
	msg := `{"message":"Resource not accessible by integration"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(msg + strings.Repeat(" padding", 200)))
	}))
	defer srv.Close()

	_, err := newTestGitHub(t, srv).Fetch(t.Context(), "https://github.com/owner/repo")
	var pe *PermanentError
	require.ErrorAs(t, err, &pe)
	assert.Contains(t, err.Error(), "Resource not accessible by integration")
	assert.Less(t, len(err.Error()), maxErrorBody+256)
}

// TestGitHubFetch_TransientSubrequestFailures: only a missing README or
// comment thread may be stored without that section. Any other failure of
// those calls fails the fetch retryably instead of saving a document that
// is missing its primary content and never retried.
func TestGitHubFetch_TransientSubrequestFailures(t *testing.T) {
	issueJSON := `{"title": "t", "state": "open", "user": {"login": "u"}, "comments": 1, "created_at": "2025-01-01T00:00:00Z"}`
	pullJSON := `{"title": "t", "state": "open", "user": {"login": "u"}, "comments": 1, "created_at": "2025-01-01T00:00:00Z"}`
	cases := []struct {
		name       string
		target     string
		failPath   string
		failStatus int
		wantErr    bool
		absent     string
	}{
		{"readme 502", "https://github.com/owner/repo", "/repos/owner/repo/readme", http.StatusBadGateway, true, ""},
		{"issue comments 500", "https://github.com/owner/repo/issues/3", "/repos/owner/repo/issues/3/comments", http.StatusInternalServerError, true, ""},
		{"pull comments 500", "https://github.com/owner/repo/pull/4", "/repos/owner/repo/issues/4/comments", http.StatusInternalServerError, true, ""},
		{"issue comments 404", "https://github.com/owner/repo/issues/3", "/repos/owner/repo/issues/3/comments", http.StatusNotFound, false, "## Comments"},
		{"pull comments 404", "https://github.com/owner/repo/pull/4", "/repos/owner/repo/issues/4/comments", http.StatusNotFound, false, "## Comments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(repoMetaJSON))
			})
			mux.HandleFunc("/repos/owner/repo/readme", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("# readme"))
			})
			mux.HandleFunc("/repos/owner/repo/issues/3", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(issueJSON))
			})
			mux.HandleFunc("/repos/owner/repo/pulls/4", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(pullJSON))
			})
			for _, p := range []string{"/repos/owner/repo/issues/3/comments", "/repos/owner/repo/issues/4/comments"} {
				mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`[{"user": {"login": "a"}, "body": "hi", "created_at": "2025-01-02T00:00:00Z"}]`))
				})
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.failPath {
					w.WriteHeader(tc.failStatus)
					return
				}
				mux.ServeHTTP(w, r)
			}))
			defer srv.Close()

			res, err := newTestGitHub(t, srv).Fetch(t.Context(), tc.target)
			if !tc.wantErr {
				require.NoError(t, err)
				assert.NotContains(t, res.Markdown, tc.absent)
				return
			}
			require.Error(t, err)
			var pe *PermanentError
			assert.False(t, errors.As(err, &pe), "a transient sub-request failure must be retried: %v", err)
		})
	}
}

// TestGitHubFetch_ReadmeRateLimitCooldownIsRetryable: a README call that
// runs into a long cooldown fails the fetch retryably; it must not be
// mistaken for "no README" and stored.
func TestGitHubFetch_ReadmeRateLimitCooldownIsRetryable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(repoMetaJSON))
	})
	mux.HandleFunc("/repos/owner/repo/readme", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "900")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := newTestGitHub(t, srv).Fetch(t.Context(), "https://github.com/owner/repo")
	require.Error(t, err)
	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "cooldown must stay retryable: %v", err)
	var se *HTTPStatusError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, 900*time.Second, se.RetryAfter)
}

// TestGitHubFetch_FileEscaping: the decoded path and ref from a bookmark
// are escaped on the way out, so a '#' in a file name or '&'/'+' in a ref
// reach GitHub intact, and the canonical URL round-trips.
func TestGitHubFetch_FileEscaping(t *testing.T) {
	cases := []struct {
		name        string
		target      string
		wantPath    string
		wantRef     string
		wantFinal   string
		decodedPath string
	}{
		{
			name:        "hash in file name",
			target:      "https://github.com/owner/myrepo/blob/main/C%23-notes.md",
			wantPath:    "/repos/owner/myrepo/contents/C%23-notes.md",
			wantRef:     "main",
			wantFinal:   "https://github.com/owner/myrepo/blob/main/C%23-notes.md",
			decodedPath: "/owner/myrepo/blob/main/C#-notes.md",
		},
		{
			name:        "reserved characters in ref, space in path",
			target:      "https://github.com/owner/myrepo/blob/v1+2&x/a%20b.md",
			wantPath:    "/repos/owner/myrepo/contents/a%20b.md",
			wantRef:     "v1+2&x",
			wantFinal:   "https://github.com/owner/myrepo/blob/v1+2&x/a%20b.md",
			decodedPath: "/owner/myrepo/blob/v1+2&x/a b.md",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotRef string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/myrepo" {
					_, _ = w.Write([]byte(repoMetaJSON))
					return
				}
				gotPath, gotRef = r.URL.EscapedPath(), r.URL.Query().Get("ref")
				_, _ = w.Write([]byte("file body"))
			}))
			defer srv.Close()

			res, err := newTestGitHub(t, srv).Fetch(t.Context(), tc.target)
			require.NoError(t, err)
			assert.Equal(t, tc.wantPath, gotPath)
			assert.Equal(t, tc.wantRef, gotRef)
			assert.Equal(t, tc.wantFinal, res.FinalURL)
			u, err := url.Parse(res.FinalURL)
			require.NoError(t, err)
			assert.Equal(t, tc.decodedPath, u.Path)
		})
	}
}

// TestGitHubFetch_BodyOverLimit: a README or file larger than the body cap
// fails permanently rather than being read into memory whole.
func TestGitHubFetch_BodyOverLimit(t *testing.T) {
	for _, target := range []string{"https://github.com/owner/repo", "https://github.com/owner/repo/blob/main/big.md"} {
		t.Run(target, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(repoMetaJSON))
			})
			big := func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
			}
			mux.HandleFunc("/repos/owner/repo/readme", big)
			mux.HandleFunc("/repos/owner/repo/contents/big.md", big)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			g := newTestGitHub(t, srv)
			g.maxBody = 1024
			_, err := g.Fetch(t.Context(), target)
			var pe *PermanentError
			require.ErrorAs(t, err, &pe)
			assert.ErrorIs(t, err, ErrTooLarge)
		})
	}
}
