package fetcher

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/time/rate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/urlutil"
)

func newTestGitHub(t *testing.T, srv *httptest.Server) *GitHub {
	t.Helper()
	return &GitHub{
		baseURL: srv.URL,
		client:  srv.Client(),
		timeout: 5_000_000_000,
		limiter: rate.NewLimiter(rate.Inf, 1),
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

func TestGitHubFetch_RateLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message": "rate limit exceeded"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := newTestGitHub(t, srv)
	_, err := g.Fetch(t.Context(), "https://github.com/owner/repo")
	require.Error(t, err)

	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "rate limit should be transient")
	assert.Contains(t, err.Error(), "rate limited")
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
