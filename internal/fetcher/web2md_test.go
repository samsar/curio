package fetcher

import (
	"context"
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

// TestWeb2MD_FetchAgainstFakeBin runs a fake that mimics the Node tool's
// output, proving the exec plumbing without Node or web2md.js installed.
func TestWeb2MD_FetchAgainstFakeBin(t *testing.T) {
	f, err := NewWeb2MD(Web2MDOptions{Bin: fakeTool(t, "web2md"), Timeout: 30 * time.Second})
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
	f, err := NewWeb2MD(Web2MDOptions{Bin: fakeTool(t, "web2md-fail"), Timeout: 30 * time.Second})
	require.NoError(t, err)
	_, err = f.Fetch(context.Background(), "https://example.com")
	require.Error(t, err)
	assert.Equal(t, "web2md: exit status 1: [fake] login wall detected", err.Error())
}

// TestWeb2MD_OutputOverLimit: stdout past the cap fails permanently
// instead of growing without bound.
func TestWeb2MD_OutputOverLimit(t *testing.T) {
	f, err := NewWeb2MD(Web2MDOptions{Bin: fakeTool(t, "flood-stdout"), Timeout: 30 * time.Second})
	require.NoError(t, err)
	f.maxOutput = 64 << 10

	_, err = f.Fetch(context.Background(), "https://example.com")
	var pe *PermanentError
	require.ErrorAs(t, err, &pe)
	assert.ErrorIs(t, err, ErrTooLarge)
}

// TestWeb2MD_StderrCapped: a tool that floods stderr yields an error
// message bounded by the stderr cap, not megabytes of text.
func TestWeb2MD_StderrCapped(t *testing.T) {
	f, err := NewWeb2MD(Web2MDOptions{Bin: fakeTool(t, "flood-stderr"), Timeout: 30 * time.Second})
	require.NoError(t, err)

	_, err = f.Fetch(context.Background(), "https://example.com")
	require.Error(t, err)
	assert.Less(t, len(err.Error()), maxStderrBytes+256)
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
