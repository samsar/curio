package fetcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/samsar/curio/internal/store"
)

// Web2MD is a Fetcher that shells out to the existing `web2md` Node tool.
//
// The Node tool emits YAML frontmatter (title, author, source, site,
// published, fetched_at, via) followed by the markdown body. We parse the
// frontmatter into Result fields and keep the body as Markdown verbatim.
//
// Bin is configurable so the daemon can run either a local checkout of
// web2md.js (under node) or a globally-installed `web2md` shim.
type Web2MD struct {
	bin       string        // path to web2md executable (or "web2md" if in PATH)
	nodeBin   string        // optional explicit node binary; empty = "node"
	timeout   time.Duration // per-fetch wall clock
	maxOutput int64         // stdout cap, maxResponseBytes outside tests
}

// Web2MDOptions configures the fetcher.
type Web2MDOptions struct {
	// Bin is either:
	//   - an absolute path to web2md.js (we invoke "node <bin> <url>"), or
	//   - the name of an executable in PATH (we invoke "<bin> <url>" directly).
	// We detect by checking whether Bin ends in ".js".
	Bin     string
	NodeBin string        // override "node" path; rarely needed
	Timeout time.Duration // default 30s
}

func NewWeb2MD(opts Web2MDOptions) (*Web2MD, error) {
	if opts.Bin == "" {
		return nil, errors.New("web2md: bin is required")
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Web2MD{
		bin:       opts.Bin,
		nodeBin:   opts.NodeBin,
		timeout:   timeout,
		maxOutput: maxResponseBytes,
	}, nil
}

func (*Web2MD) Name() string { return "web2md" }

func (w *Web2MD) Fetch(ctx context.Context, target string) (*Result, error) {
	if strings.TrimSpace(target) == "" {
		return nil, errors.New("web2md: url is empty")
	}

	stdout := &cappedBuffer{max: w.maxOutput, strict: true}
	name, args := w.command(target)
	stderr, err := runCapped(ctx, w.timeout, stdout, name, args...)
	if stdout.overflowed {
		return nil, &PermanentError{Err: fmt.Errorf("web2md: output: %w", stdout.tooLarge())}
	}
	if err != nil {
		return nil, toolError("web2md", err, stderr)
	}
	return parseWeb2MDOutput(stdout.Bytes(), target)
}

// command returns what to run for target. Direct executables run as-is;
// .js paths run under node.
func (w *Web2MD) command(target string) (name string, args []string) {
	if strings.HasSuffix(w.bin, ".js") {
		node := w.nodeBin
		if node == "" {
			node = "node"
		}
		return node, []string{w.bin, target, "--stdout"}
	}
	return w.bin, []string{target, "--stdout"}
}

// web2mdFrontmatter mirrors what the Node tool writes. Field names match the
// YAML keys exactly. Extra keys in the YAML are ignored.
type web2mdFrontmatter struct {
	Title     string `yaml:"title"`
	Author    string `yaml:"author"`
	Source    string `yaml:"source"`
	Site      string `yaml:"site"`
	Published string `yaml:"published"`
	FetchedAt string `yaml:"fetched_at"`
	Via       string `yaml:"via"`
}

// parseWeb2MDOutput separates the YAML frontmatter from the markdown body.
// The Node tool always emits "---\n<yaml>\n---\n\n<body>". If frontmatter
// is missing or unparseable, the entire output is treated as markdown body
// (resilient — never reject a successful fetch over metadata trouble).
func parseWeb2MDOutput(raw []byte, sourceURL string) (*Result, error) {
	text := string(raw)
	trimmed := strings.TrimPrefix(text, "\uFEFF") // strip UTF-8 BOM if present

	r := &Result{
		FinalURL:    sourceURL,
		ContentType: store.ContentTypeArticle,
		Meta:        map[string]any{},
	}

	const sep = "---\n"
	if !strings.HasPrefix(trimmed, sep) {
		// No frontmatter; treat whole thing as body.
		r.Markdown = strings.TrimSpace(trimmed)
		if r.Markdown == "" {
			return nil, errors.New("web2md: empty output")
		}
		return r, nil
	}

	rest := trimmed[len(sep):]
	end := strings.Index(rest, "\n"+sep[:len(sep)-1]) // look for "\n---"
	if end < 0 {
		// Unclosed frontmatter; body is whatever's there.
		r.Markdown = strings.TrimSpace(trimmed)
		return r, nil
	}
	yamlBlock := rest[:end]
	body := strings.TrimLeft(rest[end+len("\n---"):], "\r\n")

	var fm web2mdFrontmatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err == nil {
		r.Title = fm.Title
		r.Author = fm.Author
		if fm.Published != "" {
			if t, err := time.Parse(time.RFC3339, fm.Published); err == nil {
				r.PublishedAt = &t
			}
		}
		if fm.Via != "" {
			r.Meta["via"] = fm.Via
		}
		if fm.Site != "" {
			r.Meta["site"] = fm.Site
		}
		if fm.Source != "" && fm.Source != sourceURL {
			r.FinalURL = fm.Source
		}
	}

	r.Markdown = strings.TrimSpace(body)
	if r.Markdown == "" {
		return nil, errors.New("web2md: extracted body is empty")
	}
	return r, nil
}
