package docmeta

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/store"
)

// TestSavedAs: a hit without a passage is shown by its newest titled
// bookmark, else its newest, with its folder as search reads it.
func TestSavedAs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bookmarks []Bookmark
		want      Saved
		ok        bool
	}{
		{name: "no bookmarks"},
		{"title and folder", []Bookmark{{Title: "Queues", FolderPath: "/Bookmarks Bar/Tech/Streams"}},
			Saved{Title: "Queues", Folder: "Tech / Streams"}, true},
		{"an untitled newest and a titled older", []Bookmark{{FolderPath: "/Newest"}, {Title: "Older", FolderPath: "/Old"}},
			Saved{Index: 1, Title: "Older", Folder: "Old"}, true},
		{"none titled: the newest", []Bookmark{{FolderPath: "/Favorites/Later"}, {FolderPath: "/Other"}},
			Saved{Folder: "Later"}, true},
		{"a title of whitespace is none", []Bookmark{{Title: " \t\n", FolderPath: "/Newest"}, {Title: "  Older  "}},
			Saved{Index: 1, Title: "Older"}, true},
		{"whitespace alone everywhere", []Bookmark{{Title: "  ", FolderPath: "/Newest"}, {Title: "\t"}},
			Saved{Folder: "Newest"}, true},
		{"a browser's root alone", []Bookmark{{Title: "Saved", FolderPath: "/Bookmarks Bar"}},
			Saved{Title: "Saved"}, true},
		{"nothing but the bookmark", []Bookmark{{}}, Saved{}, true},
	} {
		got, ok := SavedAs(tc.bookmarks)
		assert.Equal(t, tc.ok, ok, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}
}

// TestUnavailable: a dead page is gone and a failed one couldn't be read,
// in the store's own names for those states; any other has nothing to say.
func TestUnavailable(t *testing.T) {
	for state, want := range map[string]string{
		string(store.DocStateDead):    "the page is no longer available",
		string(store.DocStateFailed):  "the page couldn't be read",
		string(store.DocStatePending): "",
		string(store.DocStateFetched): "",
		"archived":                    "",
	} {
		assert.Equal(t, want, Unavailable(state), state)
	}
}
