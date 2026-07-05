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
	token      string
	timeout    time.Duration
	baseURL    string
	rawBaseURL string // raw.githubusercontent.com, used for wiki pages
	client     *http.Client
	limiter    *rate.Limiter
	log        *slog.Logger
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
		token:      token,
		timeout:    opts.Timeout,
		baseURL:    "https://api.github.com",
		rawBaseURL: "https://raw.githubusercontent.com",
		client:     &http.Client{Timeout: opts.Timeout},
		limiter:    rate.NewLimiter(1.5, 1), // 1.5 API calls/s, no burst — stays under GitHub's 100 req/min
		log:        opts.Log,
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
	case "issue":
		return g.fetchIssue(ctx, info)
	case "pull":
		return g.fetchPull(ctx, info)
	case "wiki":
		return g.fetchWiki(ctx, info)
	default:
		return nil, &PermanentError{Err: fmt.Errorf("github: unsupported URL type %q for %s", info.Type, rawURL)}
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

// maxIssueComments caps how many comments are fetched per issue/PR.
// One page at the API maximum: staying at a single API call per comment
// thread keeps the per-fetch call count low (the limiter paces individual
// API calls; see the rate-limiting decision in docs/decisions.md).
const maxIssueComments = 100

func (g *GitHub) fetchIssue(ctx context.Context, info urlutil.GitHubURLInfo) (*Result, error) {
	issue, err := g.issueMeta(ctx, info.Owner, info.Repo, info.Number)
	if err != nil {
		return nil, err
	}

	// The issues endpoint returns pull requests too (a PR is an issue).
	// GitHub redirects /issues/N to /pull/N in the browser; do the same.
	if issue.PullRequest != nil {
		return g.fetchPull(ctx, info)
	}

	comments, err := g.issueComments(ctx, info.Owner, info.Repo, info.Number)
	if err != nil {
		g.log.Warn("github: could not fetch comments", "issue", fmt.Sprintf("%s/%s#%d", info.Owner, info.Repo, info.Number), "err", err)
	}

	markdown := formatIssueMarkdown(info, issue, comments)
	canonicalURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", info.Owner, info.Repo, info.Number)

	return &Result{
		Markdown:    markdown,
		FinalURL:    canonicalURL,
		ContentType: "thread",
		Title:       fmt.Sprintf("%s/%s#%d: %s", info.Owner, info.Repo, info.Number, issue.Title),
		Author:      issue.User.Login,
		PublishedAt: parseGHDate(issue.CreatedAt),
		Meta: map[string]any{
			"via":           "github-api",
			"owner":         info.Owner,
			"repo":          info.Repo,
			"number":        info.Number,
			"state":         issue.State,
			"labels":        issue.labelNames(),
			"comment_count": issue.Comments,
			"is_pull":       false,
		},
	}, nil
}

func (g *GitHub) fetchPull(ctx context.Context, info urlutil.GitHubURLInfo) (*Result, error) {
	pr, err := g.pullMeta(ctx, info.Owner, info.Repo, info.Number)
	if err != nil {
		return nil, err
	}

	// Conversation comments live on the issues endpoint for PRs too
	// (/pulls/{n}/comments is diff review comments — a different thing).
	comments, err := g.issueComments(ctx, info.Owner, info.Repo, info.Number)
	if err != nil {
		g.log.Warn("github: could not fetch comments", "pull", fmt.Sprintf("%s/%s#%d", info.Owner, info.Repo, info.Number), "err", err)
	}

	markdown := formatPullMarkdown(info, pr, comments)
	canonicalURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", info.Owner, info.Repo, info.Number)

	return &Result{
		Markdown:    markdown,
		FinalURL:    canonicalURL,
		ContentType: "thread",
		Title:       fmt.Sprintf("%s/%s#%d: %s", info.Owner, info.Repo, info.Number, pr.Title),
		Author:      pr.User.Login,
		PublishedAt: parseGHDate(pr.CreatedAt),
		Meta: map[string]any{
			"via":           "github-api",
			"owner":         info.Owner,
			"repo":          info.Repo,
			"number":        info.Number,
			"state":         pr.displayState(),
			"labels":        pr.labelNames(),
			"comment_count": pr.Comments,
			"is_pull":       true,
			"merged":        pr.Merged,
			"base_ref":      pr.Base.Ref,
			"head_ref":      pr.Head.Ref,
			"additions":     pr.Additions,
			"deletions":     pr.Deletions,
			"changed_files": pr.ChangedFiles,
		},
	}, nil
}

func (g *GitHub) fetchWiki(ctx context.Context, info urlutil.GitHubURLInfo) (*Result, error) {
	page := info.Path
	if page == "" {
		page = "Home"
	}

	// Wikis have no REST API (they're separate git repos); public wiki
	// pages are served raw at raw.githubusercontent.com/wiki/o/r/Page.md.
	rawURL := fmt.Sprintf("%s/wiki/%s/%s/%s.md", g.rawBaseURL, info.Owner, info.Repo, url.PathEscape(page))
	body, err := g.apiGet(ctx, rawURL, "text/plain")
	if err != nil {
		var pe *PermanentError
		if errors.As(err, &pe) {
			return nil, &PermanentError{Err: fmt.Errorf("github: wiki page %q not available (wiki disabled, private, or page missing): %v", page, pe.Err)}
		}
		return nil, err
	}

	markdown := formatWikiMarkdown(info, page, string(body))
	canonicalURL := fmt.Sprintf("https://github.com/%s/%s/wiki/%s", info.Owner, info.Repo, url.PathEscape(page))

	return &Result{
		Markdown:    markdown,
		FinalURL:    canonicalURL,
		ContentType: "article",
		Title:       fmt.Sprintf("%s/%s wiki: %s", info.Owner, info.Repo, wikiPageTitle(page)),
		Author:      info.Owner + "/" + info.Repo,
		Meta: map[string]any{
			"via":   "github-api",
			"owner": info.Owner,
			"repo":  info.Repo,
			"wiki":  true,
			"page":  page,
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

type ghUser struct {
	Login string `json:"login"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghIssue struct {
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	StateReason string    `json:"state_reason"`
	User        ghUser    `json:"user"`
	Labels      []ghLabel `json:"labels"`
	Comments    int       `json:"comments"`
	CreatedAt   string    `json:"created_at"`
	ClosedAt    string    `json:"closed_at"`
	// Present (possibly empty) when the "issue" is actually a pull request.
	PullRequest *struct{} `json:"pull_request"`
}

func (i *ghIssue) labelNames() []string {
	names := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		names = append(names, l.Name)
	}
	return names
}

type ghPull struct {
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	State        string    `json:"state"`
	User         ghUser    `json:"user"`
	Labels       []ghLabel `json:"labels"`
	Comments     int       `json:"comments"`
	CreatedAt    string    `json:"created_at"`
	MergedAt     string    `json:"merged_at"`
	Merged       bool      `json:"merged"`
	Draft        bool      `json:"draft"`
	Base         ghBranch  `json:"base"`
	Head         ghBranch  `json:"head"`
	Additions    int       `json:"additions"`
	Deletions    int       `json:"deletions"`
	ChangedFiles int       `json:"changed_files"`
}

type ghBranch struct {
	Ref string `json:"ref"`
}

func (p *ghPull) labelNames() []string {
	names := make([]string, 0, len(p.Labels))
	for _, l := range p.Labels {
		names = append(names, l.Name)
	}
	return names
}

// displayState folds merged/draft into the open/closed state for display.
func (p *ghPull) displayState() string {
	switch {
	case p.Merged:
		return "merged"
	case p.Draft && p.State == "open":
		return "draft"
	default:
		return p.State
	}
}

type ghComment struct {
	User      ghUser `json:"user"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

func (g *GitHub) issueMeta(ctx context.Context, owner, repo string, number int) (*ghIssue, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d", g.baseURL, owner, repo, number)
	body, err := g.apiGet(ctx, url, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var issue ghIssue
	if err := json.Unmarshal(body, &issue); err != nil {
		return nil, fmt.Errorf("github: parse issue json: %w", err)
	}
	return &issue, nil
}

// pullMeta fetches PR metadata. Note the REST path is /pulls/{n} (plural)
// even though web URLs use /pull/{n}.
func (g *GitHub) pullMeta(ctx context.Context, owner, repo string, number int) (*ghPull, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", g.baseURL, owner, repo, number)
	body, err := g.apiGet(ctx, url, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var pr ghPull
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("github: parse pull json: %w", err)
	}
	return &pr, nil
}

// issueComments fetches the first page of conversation comments (works for
// both issues and PRs). Capped at maxIssueComments; callers detect
// truncation by comparing against the issue's comment count.
func (g *GitHub) issueComments(ctx context.Context, owner, repo string, number int) ([]ghComment, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=%d", g.baseURL, owner, repo, number, maxIssueComments)
	body, err := g.apiGet(ctx, url, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var comments []ghComment
	if err := json.Unmarshal(body, &comments); err != nil {
		return nil, fmt.Errorf("github: parse comments json: %w", err)
	}
	return comments, nil
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

func formatIssueMarkdown(info urlutil.GitHubURLInfo, issue *ghIssue, comments []ghComment) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s (#%d)\n\n", issue.Title, info.Number)
	fmt.Fprintf(&b, "**Repository:** %s/%s\n", info.Owner, info.Repo)
	state := issue.State
	if issue.StateReason != "" {
		state += " (" + issue.StateReason + ")"
	}
	fmt.Fprintf(&b, "**State:** %s\n", state)
	if issue.User.Login != "" {
		fmt.Fprintf(&b, "**Author:** %s\n", issue.User.Login)
	}
	if labels := issue.labelNames(); len(labels) > 0 {
		fmt.Fprintf(&b, "**Labels:** %s\n", strings.Join(labels, ", "))
	}
	fmt.Fprintf(&b, "**Comments:** %d\n", issue.Comments)

	if body := strings.TrimSpace(issue.Body); body != "" {
		fmt.Fprintf(&b, "\n%s\n", body)
	}

	writeCommentsSection(&b, comments, issue.Comments)

	return b.String()
}

func formatPullMarkdown(info urlutil.GitHubURLInfo, pr *ghPull, comments []ghComment) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s (#%d)\n\n", pr.Title, info.Number)
	fmt.Fprintf(&b, "**Repository:** %s/%s\n", info.Owner, info.Repo)
	fmt.Fprintf(&b, "**State:** %s\n", pr.displayState())
	if pr.User.Login != "" {
		fmt.Fprintf(&b, "**Author:** %s\n", pr.User.Login)
	}
	if pr.Base.Ref != "" || pr.Head.Ref != "" {
		fmt.Fprintf(&b, "**Branches:** %s ← %s\n", pr.Base.Ref, pr.Head.Ref)
	}
	fmt.Fprintf(&b, "**Diff:** +%d −%d across %d files\n", pr.Additions, pr.Deletions, pr.ChangedFiles)
	if labels := pr.labelNames(); len(labels) > 0 {
		fmt.Fprintf(&b, "**Labels:** %s\n", strings.Join(labels, ", "))
	}
	fmt.Fprintf(&b, "**Comments:** %d\n", pr.Comments)

	if body := strings.TrimSpace(pr.Body); body != "" {
		fmt.Fprintf(&b, "\n%s\n", body)
	}

	writeCommentsSection(&b, comments, pr.Comments)

	return b.String()
}

// writeCommentsSection appends a ## Comments section. total is the
// server-reported comment count, used to flag truncation when more
// comments exist than were fetched.
func writeCommentsSection(b *strings.Builder, comments []ghComment, total int) {
	if len(comments) == 0 {
		return
	}
	b.WriteString("\n## Comments\n")
	for _, c := range comments {
		date := c.CreatedAt
		if t := parseGHDate(c.CreatedAt); t != nil {
			date = t.Format("2006-01-02")
		}
		fmt.Fprintf(b, "\n### %s (%s)\n\n%s\n", c.User.Login, date, strings.TrimSpace(c.Body))
	}
	if total > len(comments) {
		fmt.Fprintf(b, "\n_(%d more comments not shown)_\n", total-len(comments))
	}
}

func formatWikiMarkdown(info urlutil.GitHubURLInfo, page, content string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", wikiPageTitle(page))
	fmt.Fprintf(&b, "**Repository:** %s/%s (wiki)\n", info.Owner, info.Repo)
	fmt.Fprintf(&b, "\n## Content\n\n%s\n", strings.TrimSpace(content))

	return b.String()
}

// wikiPageTitle converts a wiki page slug to a display title, matching
// GitHub's rendering (hyphens become spaces).
func wikiPageTitle(page string) string {
	return strings.ReplaceAll(page, "-", " ")
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
