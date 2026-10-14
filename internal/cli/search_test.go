package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/store"
)

// TestSearch_HitsWithoutMatches: a hit found by what it was saved as, with
// no passage, says what it was saved as, named by its bookmark's title when
// it has none of its own; a dead one says its page is gone, a failed one
// that it couldn't be read.
func TestSearch_HitsWithoutMatches(t *testing.T) {
	srv := apitest.Start(t, apitest.DocumentLists)
	srv.AddBookmark(t, "https://example.com/later", store.SourceChrome, "Sourdough hydration", "/Bookmarks Bar/Baking")
	srv.AddDocument(t, "https://example.com/gone", store.DocStateDead)
	srv.AddBookmark(t, "https://example.com/gone", store.SourceSafari, "Sourdough starters", "")
	srv.AddDocument(t, "https://example.com/walled", store.DocStateFailed)
	srv.AddBookmark(t, "https://example.com/walled", store.SourceSafari, "Sourdough levain", "")

	out := mustRun(t, srv, "search", "sourdough")
	assert.Contains(t, out, `3 results for "sourdough"`)
	assert.Contains(t, out, " 1. Sourdough", "an untitled hit is named by its bookmark")
	assert.Contains(t, out, "    saved as: Sourdough hydration · Baking\n", "its folder without the browser's root")
	assert.Contains(t, out, "    saved as: Sourdough starters\n")
	assert.Contains(t, out, "    the page is no longer available (dead); its bookmark matched\n")
	assert.Contains(t, out, "    the page couldn't be read (failed); its bookmark matched\n")
	assert.Equal(t, 2, strings.Count(out, "; its bookmark matched"), "the pending page is still there")
}

// TestSavedAs: the line names the parts the bookmark docmeta.SavedAs
// picks has, joined by " · ".
func TestSavedAs(t *testing.T) {
	cases := []struct {
		name      string
		bookmarks []client.SearchHitBookmark
		want      string
	}{
		{"none", nil, ""},
		{"title and folder", []client.SearchHitBookmark{{Title: "T", FolderPath: "/F/G"}}, "T · F / G"},
		{"a folder alone", []client.SearchHitBookmark{{FolderPath: "/Favorites/F"}}, "F"},
		{"a root alone", []client.SearchHitBookmark{{Title: "T", FolderPath: "/Bookmarks Bar"}}, "T"},
		{"nothing to say", []client.SearchHitBookmark{{Source: store.SourceChrome}}, ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, savedAs(tc.bookmarks), tc.name)
	}
}

// TestSearch_Filters: each filter flag reaches the daemon under its own
// field, which refuses a value it can't take by that name; a folder whose
// name holds a comma is one folder; and a date-filtered search says how
// many documents it couldn't judge, which a search without one doesn't.
func TestSearch_Filters(t *testing.T) {
	srv := apitest.Start(t, apitest.DocumentLists)
	srv.AddBookmark(t, "https://example.com/comma", store.SourceChrome, "Sourdough comma", "/Bookmarks Bar/A,B")
	srv.AddBookmark(t, "https://example.com/plain", store.SourceChrome, "Sourdough plain", "/Bookmarks Bar/A")
	srv.AddDocument(t, "https://example.com/undated", store.DocStatePending)
	srv.AddBookmark(t, "https://example.com/undated", store.SourceSafari, "Sourdough undated", "")
	_, err := srv.DB.Exec(`UPDATE bookmarks SET saved_date = 'unknown' WHERE source = 'safari'`)
	require.NoError(t, err)

	for flag, field := range map[string]string{
		"--saved-after=last week":                                  "saved_after",
		"--saved-before=2024-13-01":                                "saved_before",
		"--published-after=2024-01-01T10:00":                       "published_after",
		"--published-before=yesterday":                             "published_before",
		"--site=https://example.com":                               "site",
		"--folder=/":                                               "folder",
		"--host=" + strings.Repeat("a.example,", 32) + "b.example": "host",
		"--source=opera":                                           "source",
		"--type=essay":                                             "content_type",
		"--include-undated":                                        "include_undated",
	} {
		_, err := runCLI(t, srv, "search", "sourdough", flag)
		require.Error(t, err, flag)
		assert.Contains(t, err.Error(), "filters."+field+": ", flag)
	}

	out := mustRun(t, srv, "search", "sourdough", "--folder", "/Bookmarks Bar/A,B")
	assert.Contains(t, out, "1 results")
	assert.Contains(t, out, "https://example.com/comma")
	assert.NotContains(t, out, "no date")

	out = mustRun(t, srv, "search", "sourdough", "--saved-after", "2000-01-01", "--site", "www.example.com")
	assert.Contains(t, out, "2 results")
	assert.Contains(t, out, "1 document has no date the date filters can judge; left out (--include-undated keeps them)\n")
	out = mustRun(t, srv, "search", "sourdough", "--saved-after", "2000-01-01", "--include-undated")
	assert.Contains(t, out, "3 results")
	assert.Contains(t, out, "1 document has no date the date filters can judge; kept (--include-undated)\n")
}
