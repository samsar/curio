package search

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/urlutil"
)

// FilterRequest is a search's filters as a client sends them, every value
// as written: the API's filters, and an evaluation query's. Resolve turns
// it into the store's filters, or refuses it.
type FilterRequest struct {
	ContentType []string
	Host        []string
	Source      []string
	// Site names sites by a domain, matching it and its subdomains.
	Site []string
	// Folder names folders by their path as stored, root included,
	// matching their subfolders too.
	Folder []string
	// The date bounds: a date (YYYY-MM-DD) is midnight UTC that day, and
	// a date-time is RFC 3339 with a zone. Windows are [after, before).
	SavedAfter, SavedBefore         string
	PublishedAfter, PublishedBefore string
	IncludeUndated                  bool
}

// FilterError is a filter Resolve refuses: Field names it as the API
// does (saved_after, site, ...), and Reason says what is wrong with it.
type FilterError struct {
	Field  string
	Reason string
}

func (e *FilterError) Error() string { return "filters." + e.Field + ": " + e.Reason }

// filterErrorf builds a *FilterError for field, its reason formatted like
// fmt.Sprintf.
func filterErrorf(field, format string, args ...any) error {
	return &FilterError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// Resolve checks and normalizes r into the store's filters. A refusal is
// a *FilterError naming the field.
//
// Dates are compared as UTC instants: a date alone is midnight UTC, since
// a published date given as a day is stored at UTC midnight and a local
// midnight would move it into a neighbouring day. A client wanting a
// local day sends date-times with its offset.
func (r FilterRequest) Resolve() (store.SearchFilters, error) {
	f := store.SearchFilters{IncludeUndated: r.IncludeUndated}
	var err error
	lists := []struct {
		field  string
		values []string
		to     *[]string
		clean  func(string) (string, error)
	}{
		{"content_type", r.ContentType, &f.ContentType, contentType},
		{"host", r.Host, &f.Host, host},
		{"source", r.Source, &f.Source, source},
		{"site", r.Site, &f.Site, site},
		{"folder", r.Folder, &f.Folder, folder},
	}
	for _, l := range lists {
		if *l.to, err = resolveList(l.field, l.values, l.clean); err != nil {
			return store.SearchFilters{}, err
		}
	}
	bounds := []struct {
		field string
		value string
		to    *time.Time
	}{
		{"saved_after", r.SavedAfter, &f.SavedAfter},
		{"saved_before", r.SavedBefore, &f.SavedBefore},
		{"published_after", r.PublishedAfter, &f.PublishedAfter},
		{"published_before", r.PublishedBefore, &f.PublishedBefore},
	}
	for _, b := range bounds {
		if *b.to, err = bound(b.field, b.value); err != nil {
			return store.SearchFilters{}, err
		}
	}
	if err := checkWindow("saved", f.SavedAfter, f.SavedBefore); err != nil {
		return store.SearchFilters{}, err
	}
	if err := checkWindow("published", f.PublishedAfter, f.PublishedBefore); err != nil {
		return store.SearchFilters{}, err
	}
	if f.IncludeUndated && !f.HasDates() {
		return store.SearchFilters{}, filterErrorf("include_undated",
			"keeps the documents a date filter can't judge, so it takes saved_* or published_*")
	}
	return f, nil
}

// resolveList checks one list dimension: at most store.MaxFilterValues
// values, each one clean takes, in their order.
func resolveList(field string, values []string, clean func(string) (string, error)) ([]string, error) {
	if len(values) > store.MaxFilterValues {
		return nil, filterErrorf(field, "%d values, at most %d", len(values), store.MaxFilterValues)
	}
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		c, err := clean(v)
		if err != nil {
			return nil, &FilterError{Field: field, Reason: err.Error()}
		}
		out = append(out, c)
	}
	return out, nil
}

// contentType takes one of the store's content types.
func contentType(v string) (string, error) {
	if !store.ContentType(v).Valid() {
		return "", fmt.Errorf("%q must be one of: %s", v, joinValues(store.ContentTypes()))
	}
	return v, nil
}

// source takes one of the store's bookmark sources.
func source(v string) (string, error) {
	if !store.ValidSource(v) {
		return "", fmt.Errorf("%q must be one of: %s", v, joinValues(store.Sources()))
	}
	return v, nil
}

// host takes a URL host as written, matched literally: an empty one
// would match nothing, and one longer than a DNS name names no host.
func host(v string) (string, error) {
	switch {
	case v == "":
		return "", errors.New("an empty host")
	case len(v) > store.MaxFilterHostBytes:
		return "", fmt.Errorf("a host of %d bytes; a host name is at most %d", len(v), store.MaxFilterHostBytes)
	}
	return v, nil
}

// site normalizes a site to its registrable domain (urlutil.Site), lowercased
// without a trailing dot: www.example.com is example.com, and an IP
// address or a public suffix is its own site. It takes a bare host name
// alone: no scheme, path, port, user, wildcard, space or empty label.
func site(v string) (string, error) {
	s := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".")
	if net.ParseIP(s) != nil {
		return s, nil
	}
	if s == "" {
		return "", errors.New("an empty site")
	}
	if len(s) > store.MaxFilterHostBytes {
		return "", fmt.Errorf("a site of %d bytes; a domain is at most %d", len(s), store.MaxFilterHostBytes)
	}
	if strings.ContainsAny(s, `/:@*?#\`) || strings.ContainsFunc(s, unicode.IsSpace) ||
		slices.Contains(strings.Split(s, "."), "") {
		return "", fmt.Errorf("%q is not a domain: name a site as example.com, without a scheme, path, port or "+
			"wildcard; it matches its subdomains too", v)
	}
	return urlutil.Site(s), nil
}

// folder normalizes a folder path as bookmarks store it: one leading "/",
// none trailing. It takes any path but the root of everything, "" or
// "/", which would filter nothing, up to store.MaxFilterFolderBytes.
func folder(v string) (string, error) {
	f := strings.TrimRight(strings.TrimSpace(v), "/")
	if f == "" {
		return "", fmt.Errorf("%q names no folder: leave folder out to search every folder", v)
	}
	if !strings.HasPrefix(f, "/") {
		f = "/" + f
	}
	if len(f) > store.MaxFilterFolderBytes {
		return "", fmt.Errorf("a folder of %d bytes, at most %d", len(f), store.MaxFilterFolderBytes)
	}
	return f, nil
}

// bound parses one date bound: zero for none, midnight UTC for a date
// alone, a date-time with a zone in UTC, within years 0001 to 9999,
// truncated to the store's whole milliseconds, so that a window Resolve
// takes holds what the store compares.
func bound(field, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.DateOnly, v)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, v); err != nil {
			return time.Time{}, filterErrorf(field, "%q is neither a date (2024-03-01, midnight UTC) nor an RFC 3339 "+
				"date-time with a zone (2024-03-01T09:00:00+02:00)", v)
		}
	}
	t = t.UTC().Truncate(store.FilterTimePrecision)
	if y := t.Year(); y < 1 || y > 9999 {
		return time.Time{}, filterErrorf(field, "%q is outside the years 0001 to 9999", v)
	}
	return t, nil
}

// checkWindow refuses a window of dimension whose start isn't before its
// end: it would hold nothing.
func checkWindow(dimension string, after, before time.Time) error {
	if after.IsZero() || before.IsZero() || after.Before(before) {
		return nil
	}
	return filterErrorf(dimension+"_after", "must be before %s_before: the window [%s, %s) holds nothing, "+
		"times compared to the millisecond", dimension, after.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano))
}

// joinValues lists an enum's values for an error's detail.
func joinValues[T ~string](values []T) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return strings.Join(out, ", ")
}
