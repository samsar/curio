package fetcher

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestGitHub(t *testing.T, srv *httptest.Server) *GitHub {
	t.Helper()
	return &GitHub{
		baseURL: srv.URL,
		client:  srv.Client(),
		timeout: 5_000_000_000,
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
	g := &GitHub{baseURL: "https://api.github.com", client: http.DefaultClient, log: slog.Default()}
	_, err := g.Fetch(t.Context(), "https://github.com/owner/repo/issues/123")
	require.Error(t, err)

	var pe *PermanentError
	assert.True(t, errors.As(err, &pe), "unsupported type should be permanent")
}
