package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

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
	assert.Contains(t, out, "    page no longer available (dead); its bookmark matched\n")
	assert.Contains(t, out, "    page couldn't be read (failed); its bookmark matched\n")
	assert.Equal(t, 2, strings.Count(out, "; its bookmark matched"), "the pending page is still there")
}

func TestSavedAs(t *testing.T) {
	cases := []struct {
		name      string
		bookmarks []client.SearchHitBookmark
		want      string
	}{
		{"none", nil, ""},
		{"title and folder", []client.SearchHitBookmark{{Title: "T", FolderPath: "/F/G"}}, "T · F / G"},
		{"the newest titled one", []client.SearchHitBookmark{{FolderPath: "/Newest"}, {Title: "Older", FolderPath: "/O"}},
			"Older · O"},
		{"a folder alone", []client.SearchHitBookmark{{FolderPath: "/Favorites/F"}}, "F"},
		{"a root alone", []client.SearchHitBookmark{{Title: "T", FolderPath: "/Bookmarks Bar"}}, "T"},
		{"nothing to say", []client.SearchHitBookmark{{Source: store.SourceChrome}}, ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, savedAs(tc.bookmarks), tc.name)
	}
}
