package importer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
)

// fakeDaemon answers import requests with answer, recording each.
type fakeDaemon struct {
	requests []client.ImportRequest
	answer   func(req client.ImportRequest) (*client.ImportResponse, error)
}

func (f *fakeDaemon) ImportBookmarks(_ context.Context, req client.ImportRequest) (*client.ImportResponse, error) {
	f.requests = append(f.requests, req)
	return f.answer(req)
}

func pages(n int) []ParsedBookmark {
	out := make([]ParsedBookmark, n)
	for i := range out {
		out[i] = ParsedBookmark{URL: fmt.Sprintf("https://example.com/%d", i), Title: strconv.Itoa(i)}
	}
	return out
}

// TestSend: batches of BatchSize, their answers summed, progress after
// each; the first batch to fail stops it, naming its range.
func TestSend(t *testing.T) {
	d := &fakeDaemon{answer: func(req client.ImportRequest) (*client.ImportResponse, error) {
		return &client.ImportResponse{Created: len(req.Bookmarks) - 1, Skipped: 1, JobsEnqueued: 2,
			FilteredBy: map[string]int{"javascript": 1}, Filtered: 1, Errors: []string{"x"}}, nil
	}}
	var progress []int
	totals, err := Send(context.Background(), d, LabelChrome, pages(1200), func(n int, so Totals) {
		progress = append(progress, n, so.Created)
	})
	require.NoError(t, err)
	require.Len(t, d.requests, 3)
	assert.Len(t, d.requests[0].Bookmarks, 500)
	assert.Len(t, d.requests[2].Bookmarks, 200)
	assert.Equal(t, "https://example.com/500", d.requests[1].Bookmarks[0].URL)
	assert.Equal(t, "500", d.requests[1].Bookmarks[0].Title)
	assert.False(t, d.requests[0].DryRun)
	assert.Equal(t, []int{500, 499, 1000, 998, 1200, 1197}, progress)
	assert.Equal(t, Totals{Created: 1197, Skipped: 3, Filtered: 3, JobsEnqueued: 6,
		FilteredBy: map[FilterReason]int{ReasonJavaScript: 3}, Errors: []string{"x", "x", "x"}}, totals)

	boom := errors.New("connection reset")
	d = &fakeDaemon{}
	d.answer = func(client.ImportRequest) (*client.ImportResponse, error) {
		if len(d.requests) == 2 {
			return nil, boom
		}
		return &client.ImportResponse{Created: 500}, nil
	}
	totals, err = Send(context.Background(), d, LabelChrome, pages(1200), nil)
	require.ErrorIs(t, err, boom)
	assert.EqualError(t, err, "batch 500-1000: connection reset")
	assert.Equal(t, 500, totals.Created, "what the batches before it did")
	assert.Len(t, d.requests, 2, "nothing after the failed batch")
}

func TestCandidatesOf(t *testing.T) {
	c := CandidatesOf([]ParsedBookmark{
		{URL: "https://example.com/b"},
		{URL: "javascript:void(0)"},
		{URL: "https://example.com/a"},
		{URL: "HTTPS://EXAMPLE.com/b"},
		{URL: "chrome://settings"},
		{URL: "https://example.com/a?utm_source=x"},
	})
	assert.Equal(t, []string{"https://example.com/b", "https://example.com/a"}, c.URLs, "distinct, first seen first")
	assert.Equal(t, 2, c.Filtered)
	assert.Equal(t, map[FilterReason]int{ReasonJavaScript: 1, ReasonBrowserInternal: 1}, c.FilteredBy)
}

// TestCountNew: the distinct candidates alone go in dry runs of at most
// countBatchSize URLs, and the answers are summed.
func TestCountNew(t *testing.T) {
	d := &fakeDaemon{answer: func(req client.ImportRequest) (*client.ImportResponse, error) {
		resp := &client.ImportResponse{DryRun: true, Created: len(req.Bookmarks)}
		resp.NewURLs = append(resp.NewURLs, req.Bookmarks[0].URL)
		return resp, nil
	}}
	bms := append(pages(12_000), pages(10)...) // ten duplicates
	bms = append(bms, ParsedBookmark{URL: "javascript:alert(1)"})

	c, err := CountNew(context.Background(), d, LabelSafari, bms)
	require.NoError(t, err)
	require.Len(t, d.requests, 2)
	for _, req := range d.requests {
		assert.True(t, req.DryRun)
		assert.Equal(t, LabelSafari, req.Source)
		assert.Empty(t, req.Bookmarks[0].Title, "URLs alone")
	}
	assert.Len(t, d.requests[0].Bookmarks, 10_000)
	assert.Len(t, d.requests[1].Bookmarks, 2000)
	assert.True(t, c.Known)
	assert.Equal(t, 12_011, c.Parsed)
	assert.Equal(t, 1, c.Filtered)
	assert.Equal(t, 12_000, c.Created)
	assert.Equal(t, []string{"https://example.com/0", "https://example.com/10000"}, c.NewURLs)
}

// TestCountNew_OlderDaemon: a daemon that refuses dry_run as a field it
// doesn't know leaves the count unknown; any other failure is an error.
func TestCountNew_OlderDaemon(t *testing.T) {
	older := &fakeDaemon{answer: func(client.ImportRequest) (*client.ImportResponse, error) {
		return nil, &client.APIError{Status: http.StatusBadRequest, Problem: client.Problem{
			Detail: `unknown field "dry_run": this curio-daemon (version v1) doesn't support it`}}
	}}
	c, err := CountNew(context.Background(), older, LabelChrome, pages(3))
	require.NoError(t, err)
	assert.False(t, c.Known)
	assert.Equal(t, 3, c.Parsed)
	assert.Len(t, c.URLs, 3)

	broken := &fakeDaemon{answer: func(client.ImportRequest) (*client.ImportResponse, error) {
		return nil, &client.APIError{Status: http.StatusInternalServerError}
	}}
	_, err = CountNew(context.Background(), broken, LabelChrome, pages(3))
	require.Error(t, err)
}
