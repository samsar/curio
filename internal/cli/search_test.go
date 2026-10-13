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
// it has none of its own; a failed or dead one says its page is gone.
func TestSearch_HitsWithoutMatches(t *testing.T) {
	srv := apitest.Start(t, apitest.DocumentLists)
	srv.AddBookmark(t, "https://example.com/later", store.SourceChrome, "Sourdough hydration", "/Bookmarks Bar/Baking")
	srv.AddDocument(t, "https://example.com/gone", store.DocStateDead)
	srv.AddBookmark(t, "https://example.com/gone", store.SourceSafari, "Sourdough starters", "")

	out := mustRun(t, srv, "search", "sourdough")
	assert.Contains(t, out, `2 results for "sourdough"`)
	assert.Contains(t, out, " 1. Sourdough", "an untitled hit is named by its bookmark")
	assert.Contains(t, out, "    saved as: Sourdough hydration · /Bookmarks Bar/Baking\n")
	assert.Contains(t, out, "    saved as: Sourdough starters\n")
	assert.Contains(t, out, "    page no longer available (dead); its bookmark matched\n")
	assert.Equal(t, 1, strings.Count(out, "page no longer available"), "the pending page is still there")
}

func TestSavedAs(t *testing.T) {
	cases := []struct {
		name      string
		bookmarks []client.SearchHitBookmark
		want      string
	}{
		{"none", nil, ""},
		{"title and folder", []client.SearchHitBookmark{{Title: "T", FolderPath: "/F"}}, "T · /F"},
		{"the newest titled one", []client.SearchHitBookmark{{FolderPath: "/Newest"}, {Title: "Older", FolderPath: "/O"}},
			"Older · /O"},
		{"a folder alone", []client.SearchHitBookmark{{FolderPath: "/F"}}, "/F"},
		{"nothing to say", []client.SearchHitBookmark{{Source: store.SourceChrome}}, ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, savedAs(tc.bookmarks), tc.name)
	}
}
