// Package importer parses bookmark files from browsers and exports into a
// common ParsedBookmark representation. Importers do NOT touch the
// database — they emit a slice that the daemon (or the CLI, via the API)
// dedups and inserts.
package importer

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/urlutil"
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
	ReasonEmpty             FilterReason = "empty"
	ReasonInvalidURL        FilterReason = "invalid_url"
	ReasonBrowserInternal   FilterReason = "browser_internal" // chrome://, about:, ...
	ReasonJavaScript        FilterReason = "javascript"       // javascript: bookmarklet
	ReasonLocalFile         FilterReason = "local_file"       // file:// path
	ReasonUnsupportedScheme FilterReason = "unsupported_scheme"
)

// Indexable applies the import-time URL filter rules. Returns (true, "")
// if the URL is good to bookmark + fetch, or (false, reason) if not.
//
// urlutil.Normalize already refuses anything but http(s) URLs with a host,
// for every entry point. Indexable filters anyway because bulk imports
// report how many URLs were skipped and why (bookmarklets, local files,
// browser pages), and that breakdown is import-specific.
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
		return false, ReasonUnsupportedScheme
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

// ErrEmpty is returned when a parse produces zero bookmarks.
var ErrEmpty = errors.New("importer: no bookmarks found")

// canonicalURL normalizes raw with urlutil.Normalize, or returns it unchanged
// when it isn't a fetchable URL, so Indexable can still classify (and the
// import report count) why the bookmark is skipped.
func canonicalURL(raw string) string {
	if norm, err := urlutil.Normalize(raw); err == nil {
		return norm
	}
	return raw
}

// pushFolder returns stack with name appended, or stack itself when name is
// blank after trimming. The result never shares a backing array with stack,
// so sibling folders can't overwrite each other's paths.
func pushFolder(stack []string, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return stack
	}
	return append(slices.Clip(stack), name)
}
