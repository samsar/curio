package docmeta

// Saved is what a search hit without a passage is shown by: the bookmark
// it was saved as, which is what it matched on.
type Saved struct {
	// Index is the bookmark's position in the list SavedAs was given, for
	// a surface that names what else it knows of it, such as its source.
	Index int
	// Title is its title, whitespace collapsed; "" when it has none.
	Title string
	// Folder is its folder as search reads it (FolderText); "" for none,
	// or for a browser's root alone.
	Folder string
}

// SavedAs is the bookmark, of a document's bookmarks given newest saved
// first, that a search hit without a passage is shown by: the newest with
// a title, since a title is what its owner would know it by, else the
// newest. A title of whitespace alone is none. ok is false when there are
// no bookmarks.
func SavedAs(bookmarks []Bookmark) (s Saved, ok bool) {
	if len(bookmarks) == 0 {
		return Saved{}, false
	}
	for i, b := range bookmarks {
		if title := collapse(b.Title); title != "" {
			return Saved{Index: i, Title: title, Folder: FolderText([]string{b.FolderPath})}, true
		}
	}
	return Saved{Folder: FolderText([]string{bookmarks[0].FolderPath})}, true
}

// Unavailable says why a document in state, as the store names it, has
// no page to read, when what it was saved as is all a search hit has: a
// dead page is gone, and a failed one couldn't be read, though it is often
// still there, behind a sign-in or a bot check, or too slow to answer. ""
// for a page curio has read or may yet.
func Unavailable(state string) string {
	switch state {
	case "dead":
		return "the page is no longer available"
	case "failed":
		return "the page couldn't be read"
	}
	return ""
}
