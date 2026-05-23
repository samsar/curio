package fetcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWeb2MDOutput_FullFrontmatter(t *testing.T) {
	raw := `---
title: "Feature Toggles"
author: "Pete Hodgson"
source: "https://martinfowler.com/articles/feature-toggles.html"
site: "martinfowler.com"
published: "2017-10-09T00:00:00.000Z"
fetched_at: "2026-05-23T20:00:00.000Z"
via: "readability"
---

# Feature Toggles

A long article about feature flags...
`
	r, err := parseWeb2MDOutput([]byte(raw), "https://martinfowler.com/articles/feature-toggles.html")
	require.NoError(t, err)
	assert.Equal(t, "Feature Toggles", r.Title)
	assert.Equal(t, "Pete Hodgson", r.Author)
	require.NotNil(t, r.PublishedAt)
	assert.Equal(t, 2017, r.PublishedAt.Year())
	assert.Equal(t, "readability", r.Meta["via"])
	assert.Equal(t, "martinfowler.com", r.Meta["site"])
	assert.Contains(t, r.Markdown, "# Feature Toggles")
	assert.NotContains(t, r.Markdown, "---") // frontmatter stripped
}

func TestParseWeb2MDOutput_PartialFrontmatter(t *testing.T) {
	raw := `---
title: "X"
---

body only`
	r, err := parseWeb2MDOutput([]byte(raw), "https://example.com")
	require.NoError(t, err)
	assert.Equal(t, "X", r.Title)
	assert.Empty(t, r.Author)
	assert.Nil(t, r.PublishedAt)
	assert.Equal(t, "body only", r.Markdown)
}

func TestParseWeb2MDOutput_NoFrontmatter(t *testing.T) {
	r, err := parseWeb2MDOutput([]byte("just markdown\n\nmore"), "https://example.com")
	require.NoError(t, err)
	assert.Equal(t, "https://example.com", r.FinalURL)
	assert.Equal(t, "just markdown\n\nmore", r.Markdown)
}

func TestParseWeb2MDOutput_EmptyBody(t *testing.T) {
	raw := `---
title: "x"
---

`
	_, err := parseWeb2MDOutput([]byte(raw), "https://x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestParseWeb2MDOutput_EmptyInput(t *testing.T) {
	_, err := parseWeb2MDOutput(nil, "https://x")
	require.Error(t, err)
}

// TestWeb2MD_FetchAgainstFakeBin uses a tiny shell script that mimics the
// Node tool's output. This proves the exec.Cmd plumbing without depending
// on Node + the real web2md.js being installed.
func TestWeb2MD_FetchAgainstFakeBin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-web2md")
	script := `#!/usr/bin/env bash
set -e
URL="$1"
cat <<EOF
---
title: "Fake Title"
source: "$URL"
via: "test"
---

# Fake Title

This is the body.
EOF
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	f, err := NewWeb2MD(Web2MDOptions{Bin: bin, Timeout: 5 * time.Second})
	require.NoError(t, err)
	assert.Equal(t, "web2md", f.Name())

	res, err := f.Fetch(context.Background(), "https://example.com/article")
	require.NoError(t, err)
	assert.Equal(t, "Fake Title", res.Title)
	assert.Contains(t, res.Markdown, "Fake Title")
	assert.Contains(t, res.Markdown, "body.")
	assert.Equal(t, "test", res.Meta["via"])
}

func TestWeb2MD_FetchPropagatesStderrOnFailure(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fail-web2md")
	script := `#!/usr/bin/env bash
echo "[fake] login wall detected" >&2
exit 1
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	f, _ := NewWeb2MD(Web2MDOptions{Bin: bin, Timeout: 5 * time.Second})
	_, err := f.Fetch(context.Background(), "https://example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "login wall detected")
}

func TestWeb2MD_FetchEmptyURL(t *testing.T) {
	f, _ := NewWeb2MD(Web2MDOptions{Bin: "anything", Timeout: time.Second})
	_, err := f.Fetch(context.Background(), "  ")
	require.Error(t, err)
}

func TestNewWeb2MD_RequiresBin(t *testing.T) {
	_, err := NewWeb2MD(Web2MDOptions{})
	require.Error(t, err)
}

func TestSingleDispatcher(t *testing.T) {
	d := &Single{}
	_, err := d.For("https://example.com")
	require.ErrorIs(t, err, ErrFetcherNotFound)

	f, _ := NewWeb2MD(Web2MDOptions{Bin: "x"})
	d.F = f
	got, err := d.For("https://example.com")
	require.NoError(t, err)
	assert.Equal(t, "web2md", got.Name())
}
