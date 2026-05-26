package fetcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/samsar/curio/internal/urlutil"
)

type GitHubOptions struct {
	Token   string
	Timeout time.Duration
	Log     *slog.Logger
}

type GitHub struct {
	token   string
	timeout time.Duration
	baseURL string
	client  *http.Client
	limiter *rate.Limiter
	log     *slog.Logger
}

func NewGitHub(opts GitHubOptions) *GitHub {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	token := opts.Token
	if token == "" {
		token = os.Getenv("CURIO_GITHUB_TOKEN")
	}
	return &GitHub{
		token:   token,
		timeout: opts.Timeout,
		baseURL: "https://api.github.com",
		client:  &http.Client{Timeout: opts.Timeout},
		limiter: rate.NewLimiter(1.5, 1), // 1.5 API calls/s, no burst — stays under GitHub's 100 req/min
		log:     opts.Log,
	}
}

func (g *GitHub) Name() string { return "github" }

func (g *GitHub) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("github: invalid url: %w", err)
	}
	info, ok := urlutil.ParseGitHubURL(u)
	if !ok {
		return nil, &PermanentError{Err: fmt.Errorf("github: not a recognized GitHub URL: %s", rawURL)}
	}

	switch info.Type {
	case "repo":
		return g.fetchRepo(ctx, info)
	case "file":
		return g.fetchFile(ctx, info)
	default:
		return nil, &PermanentError{Err: fmt.Errorf("github: unsupported URL type %q for %s (issues/PRs not yet supported)", info.Type, rawURL)}
	}
}

func (g *GitHub) fetchRepo(ctx context.Context, info urlutil.GitHubURLInfo) (*Result, error) {
	meta, err := g.repoMeta(ctx, info.Owner, info.Repo)
	if err != nil {
		return nil, err
	}

	readme, err := g.repoReadme(ctx, info.Owner, info.Repo)
	if err != nil {
		g.log.Warn("github: no README", "repo", info.Owner+"/"+info.Repo, "err", err)
	}

	markdown := formatRepoMarkdown(meta, readme)
	canonicalURL := fmt.Sprintf("https://github.com/%s/%s", info.Owner, info.Repo)

	published := parseGHDate(meta.CreatedAt)

	return &Result{
		Markdown:    markdown,
		FinalURL:    canonicalURL,
		ContentType: "repo",
		Title:       info.Owner + "/" + info.Repo,
		Author:      info.Owner,
		PublishedAt: published,
		Meta: map[string]any{
			"via":            "github-api",
			"owner":          info.Owner,
			"repo":           info.Repo,
			"description":    meta.Description,
			"language":       meta.Language,
			"stars":          meta.Stars,
			"forks":          meta.Forks,
			"license":        meta.License,
			"topics":         meta.Topics,
			"default_branch": meta.DefaultBranch,
			"archived":       meta.Archived,
		},
	}, nil
}

func (g *GitHub) fetchFile(ctx context.Context, info urlutil.GitHubURLInfo) (*Result, error) {
	meta, err := g.repoMeta(ctx, info.Owner, info.Repo)
	if err != nil {
		return nil, err
	}

	ref := info.Ref
	if ref == "" {
		ref = meta.DefaultBranch
	}

	content, err := g.fileContent(ctx, info.Owner, info.Repo, info.Path, ref)
	if err != nil {
		return nil, err
	}

	markdown := formatFileMarkdown(meta, info, content)
	canonicalURL := fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s", info.Owner, info.Repo, ref, info.Path)

	return &Result{
		Markdown:    markdown,
		FinalURL:    canonicalURL,
		ContentType: "article",
		Title:       info.Path,
		Author:      info.Owner + "/" + info.Repo,
		Meta: map[string]any{
			"via":      "github-api",
			"owner":    info.Owner,
			"repo":     info.Repo,
			"ref":      ref,
			"path":     info.Path,
			"language": meta.Language,
			"stars":    meta.Stars,
		},
	}, nil
}

type ghRepoMeta struct {
	Description   string   `json:"description"`
	Language      string   `json:"language"`
	Stars         int      `json:"stargazers_count"`
	Forks         int      `json:"forks_count"`
	Topics        []string `json:"topics"`
	DefaultBranch string   `json:"default_branch"`
	Archived      bool     `json:"archived"`
	CreatedAt     string   `json:"created_at"`
	License       string
}

type ghLicenseField struct {
	Name string `json:"name"`
}

func (g *GitHub) repoMeta(ctx context.Context, owner, repo string) (*ghRepoMeta, error) {
	url := fmt.Sprintf("%s/repos/%s/%s", g.baseURL, owner, repo)
	body, err := g.apiGet(ctx, url, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}

	var raw struct {
		ghRepoMeta
		LicenseField *ghLicenseField `json:"license"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("github: parse repo json: %w", err)
	}
	meta := &raw.ghRepoMeta
	if raw.LicenseField != nil {
		meta.License = raw.LicenseField.Name
	}
	return meta, nil
}

func (g *GitHub) repoReadme(ctx context.Context, owner, repo string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/readme", g.baseURL, owner, repo)
	body, err := g.apiGet(ctx, url, "application/vnd.github.raw+json")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (g *GitHub) fileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", g.baseURL, owner, repo, path, ref)
	body, err := g.apiGet(ctx, url, "application/vnd.github.raw+json")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

const maxAPIRetries = 3

func (g *GitHub) apiGet(ctx context.Context, url, accept string) ([]byte, error) {
	var lastErr error
	for attempt := range maxAPIRetries {
		if err := g.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("github: rate limiter: %w", err)
		}

		body, err := g.doRequest(ctx, url, accept)
		if err == nil {
			return body, nil
		}

		var re *retryableError
		if !errors.As(err, &re) {
			return nil, err
		}

		lastErr = re.Err
		if attempt == maxAPIRetries-1 {
			break
		}

		delay := re.RetryAfter
		if delay <= 0 {
			delay = 60 * time.Second
		}
		if delay > 2*time.Minute {
			delay = 2 * time.Minute
		}
		g.log.Info("github: rate limited, waiting", "delay_s", int(delay.Seconds()), "url", url, "attempt", attempt+1)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	return nil, lastErr
}

type retryableError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *retryableError) Error() string { return e.Err.Error() }

func (g *GitHub) doRequest(ctx context.Context, url, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("github: read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusNotFound:
		return nil, &PermanentError{Err: fmt.Errorf("github: 404 not found: %s", url)}
	case http.StatusTooManyRequests, http.StatusForbidden:
		if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") != "0" {
			return nil, &PermanentError{Err: fmt.Errorf("github: 403 forbidden: %s", string(body))}
		}
		return nil, &retryableError{
			Err:        fmt.Errorf("github: rate limited: %s", url),
			RetryAfter: parseRetryAfter(resp.Header),
		}
	case http.StatusUnauthorized:
		return nil, &PermanentError{Err: fmt.Errorf("github: 401 unauthorized")}
	default:
		return nil, fmt.Errorf("github: HTTP %d: %s", resp.StatusCode, string(body))
	}
}

func parseRetryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil {
			d := time.Until(time.Unix(epoch, 0))
			if d > 0 {
				return d
			}
		}
	}
	return 0
}

func formatRepoMarkdown(meta *ghRepoMeta, readme string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", meta.Description)
	if meta.Description == "" {
		b.Reset()
		b.WriteString("# (no description)\n\n")
	}

	if meta.Language != "" {
		fmt.Fprintf(&b, "**Language:** %s\n", meta.Language)
	}
	fmt.Fprintf(&b, "**Stars:** %d\n", meta.Stars)
	if meta.License != "" {
		fmt.Fprintf(&b, "**License:** %s\n", meta.License)
	}
	if len(meta.Topics) > 0 {
		fmt.Fprintf(&b, "**Topics:** %s\n", strings.Join(meta.Topics, ", "))
	}
	if meta.Archived {
		b.WriteString("**Status:** archived\n")
	}

	if readme != "" {
		fmt.Fprintf(&b, "\n## README\n\n%s\n", strings.TrimSpace(readme))
	}

	return b.String()
}

func formatFileMarkdown(meta *ghRepoMeta, info urlutil.GitHubURLInfo, content string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", info.Path)
	fmt.Fprintf(&b, "**Repository:** %s/%s", info.Owner, info.Repo)
	if meta.Description != "" {
		fmt.Fprintf(&b, " — %s", meta.Description)
	}
	b.WriteString("\n")
	if meta.Language != "" {
		fmt.Fprintf(&b, "**Language:** %s | ", meta.Language)
	}
	fmt.Fprintf(&b, "**Stars:** %d\n", meta.Stars)

	fmt.Fprintf(&b, "\n## Content\n\n%s\n", strings.TrimSpace(content))

	return b.String()
}

func parseGHDate(iso string) *time.Time {
	if iso == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return nil
	}
	return &t
}

// GitHubHosts lists the hostnames the PatternDispatcher should route
// to the GitHub fetcher.
var GitHubHosts = []string{"github.com"}
