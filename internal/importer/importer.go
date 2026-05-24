// Package importer parses bookmark files from browsers and exports into a
// common ParsedBookmark representation. Importers do NOT touch the
// database — they emit a slice that the daemon (or the CLI, via the API)
// dedups and inserts.
package importer

import (
	"errors"
	"strings"
	"time"

	"github.com/samansartipi/curio/internal/urlutil"
)

// ParsedBookmark is the common shape every parser emits.
type ParsedBookmark struct {
	URL        string    // post-normalization (urlutil.Normalize)
	Title      string    // empty if the source had none
	FolderPath string    // "/Tech/AI"; root bookmarks have empty path
	Tags       []string  // empty unless the source carries native tags
	SavedAt    time.Time // zero if the source didn't supply one
}

// Source labels the bookmark provenance for the store. Mirrors the
// store.Source* constants but listed here so importer-level code doesn't
// import the store package transitively.
type Source string

const (
	SourceChrome  Source = "chrome"
	SourceSafari  Source = "safari"
	SourceFirefox Source = "firefox"
	SourceHTML    Source = "html"
)

// FilterReason explains why a URL was skipped. Returned by Indexable so
// callers (or future status endpoints) can report aggregate counts.
type FilterReason string

const (
	ReasonEmpty            FilterReason = "empty"
	ReasonInvalidURL       FilterReason = "invalid_url"
	ReasonBrowserInternal  FilterReason = "browser_internal" // chrome://, about:, ...
	ReasonJavaScript       FilterReason = "javascript"       // javascript: bookmarklet
	ReasonLocalFile        FilterReason = "local_file"       // file:// path
	ReasonUnsupportedSchem FilterReason = "unsupported_scheme"
)

// Indexable applies the import-time URL filter rules. Returns (true, "")
// if the URL is good to bookmark + fetch, or (false, reason) if not.
//
// Lives here rather than urlutil because the rules are import-specific:
// a manually-added bookmark via API could still in principle reference a
// file:// path (debatable), but bulk-importing them from browser data
// would surface thousands of local file references that aren't useful
// to fetch and index.
func Indexable(rawURL string) (bool, FilterReason) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false, ReasonEmpty
	}
	low := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(low, "javascript:"):
		return false, ReasonJavaScript
	case strings.HasPrefix(low, "file:"):
		return false, ReasonLocalFile
	}
	for _, prefix := range browserInternalPrefixes {
		if strings.HasPrefix(low, prefix) {
			return false, ReasonBrowserInternal
		}
	}
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return false, ReasonUnsupportedSchem
	}
	if _, err := urlutil.Normalize(trimmed); err != nil {
		return false, ReasonInvalidURL
	}
	return true, ""
}

var browserInternalPrefixes = []string{
	"chrome://",
	"chrome-extension://",
	"about:",
	"edge://",
	"brave://",
	"safari-resource:",
	"opera://",
	"vivaldi://",
	"arc://",
}

// Result is the aggregate output of an import-and-insert flow. Used by
// the daemon endpoint and the CLI to report counts.
type Result struct {
	Source    Source
	Created   int      // bookmarks newly inserted; fetch job enqueued for each
	Skipped   int      // bookmarks that already existed (UNIQUE conflict)
	Filtered  int      // URLs dropped by Indexable
	FilterBy  map[FilterReason]int
	JobsAdded int      // count of fetch jobs enqueued (==Created in v1)
}

// ErrEmpty is returned when a parse produces zero bookmarks.
var ErrEmpty = errors.New("importer: no bookmarks found")
