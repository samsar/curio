package docmeta

import (
	"strings"

	"github.com/samsar/curio/internal/textutil"
)

// A card is a handful of short parts: each at most maxCardPartBytes, so
// one long title or folder list can't crowd out the rest, joined by
// cardSeparator.
const (
	maxCardPartBytes = 300
	cardSeparator    = " — "
	cardParts        = 5
)

// MaxCardBytes is the longest a card can be: every part full, and the
// separators between them.
const MaxCardBytes = cardParts*maxCardPartBytes + (cardParts-1)*len(cardSeparator)

// Card is the text a document is embedded by, for searching it by meaning
// when its text can't be: in order, its title; the titles it was saved as
// that differ from the title (compared case-insensitively), joined by
// " | "; its site's name, else its host; its author; its folders, joined
// by " | ". Each part has its whitespace collapsed and is cut to 300
// bytes on a rune boundary, and the parts that aren't empty are joined by
// " — ". It reads only f, so the store can make a card again from the
// fields it keeps.
func (f Fields) Card() string {
	parts := make([]string, 0, cardParts)
	for _, p := range [cardParts]string{f.Title, strings.Join(SavedTitles(f.Title, f.BookmarkTitles), " | "),
		SiteOrHost(f.SiteName, f.Host), f.Author, strings.Join(lines(f.Folders), " | ")} {
		if p = strings.TrimSpace(textutil.TruncateBytes(collapse(p), maxCardPartBytes)); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, cardSeparator)
}

// SavedTitles are the titles a document was saved as, bookmarkTitles one a
// line as its fields hold them, that differ from its title and from each
// other, compared case-insensitively, in their order: what its card and a
// reranker show beside the title.
func SavedTitles(title, bookmarkTitles string) []string {
	var saved []string
	seen := map[string]bool{strings.ToLower(title): true}
	for _, t := range lines(bookmarkTitles) {
		if key := strings.ToLower(collapse(t)); !seen[key] {
			seen[key] = true
			saved = append(saved, t)
		}
	}
	return saved
}

// SiteOrHost is how a document's site is named in the text it is embedded
// by, its card and its chunks' context: its site name (Fields.SiteName),
// else its host.
func SiteOrHost(siteName, host string) string {
	if siteName != "" {
		return siteName
	}
	return host
}
