package search

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func TestFilterRequest_Resolve(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name string
		req  FilterRequest
		want store.SearchFilters
	}{
		{"nothing", FilterRequest{}, store.SearchFilters{}},
		{
			name: "a date is midnight UTC that day",
			req:  FilterRequest{SavedAfter: "2024-03-01", PublishedBefore: "1999-12-31"},
			want: store.SearchFilters{SavedAfter: day(2024, 3, 1), PublishedBefore: day(1999, 12, 31)},
		},
		{
			name: "a date-time with an offset is its instant in UTC",
			req:  FilterRequest{SavedAfter: "2024-03-01T09:30:00+02:00", SavedBefore: "2024-03-02T00:00:00.250Z"},
			want: store.SearchFilters{SavedAfter: time.Date(2024, 3, 1, 7, 30, 0, 0, time.UTC),
				SavedBefore: time.Date(2024, 3, 2, 0, 0, 0, 250e6, time.UTC)},
		},
		{
			name: "bounds are truncated to the store's milliseconds",
			req:  FilterRequest{SavedAfter: "2024-03-01T00:00:00.0001Z", SavedBefore: "2024-03-01T00:00:00.0019Z"},
			want: store.SearchFilters{SavedAfter: day(2024, 3, 1), SavedBefore: time.Date(2024, 3, 1, 0, 0, 0, 1e6,
				time.UTC)},
		},
		{
			name: "the first and last years",
			req:  FilterRequest{PublishedAfter: "0001-01-01", PublishedBefore: "9999-12-31T23:59:59Z"},
			want: store.SearchFilters{PublishedAfter: day(1, 1, 1),
				PublishedBefore: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)},
		},
		{
			name: "include_undated with a date",
			req:  FilterRequest{SavedBefore: "2020-01-01", IncludeUndated: true},
			want: store.SearchFilters{SavedBefore: day(2020, 1, 1), IncludeUndated: true},
		},
		{
			name: "sites are registrable domains",
			req: FilterRequest{Site: []string{" WWW.Example.COM. ", "blog.example.co.uk", "192.168.1.10", "::1",
				"github.io", "user.github.io", "localhost", "my_site.example"}},
			want: store.SearchFilters{Site: []string{"example.com", "example.co.uk", "192.168.1.10", "::1", "github.io",
				"user.github.io", "localhost", "my_site.example"}},
		},
		{
			name: "folders have one leading slash and none trailing",
			req:  FilterRequest{Folder: []string{"Tech/", " /Bookmarks Bar/Tech// ", "/Other Bookmarks", "a,b"}},
			want: store.SearchFilters{Folder: []string{"/Tech", "/Bookmarks Bar/Tech", "/Other Bookmarks", "/a,b"}},
		},
		{
			name: "enums and hosts as given",
			req: FilterRequest{ContentType: []string{"article", "pdf"}, Source: []string{"safari"},
				Host: []string{"Example.com"}},
			want: store.SearchFilters{ContentType: []string{"article", "pdf"}, Source: []string{"safari"},
				Host: []string{"Example.com"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.req.Resolve()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.NoError(t, got.Validate(), "what Resolve gives, the store takes")
		})
	}
}

func TestFilterRequest_ResolveRefuses(t *testing.T) {
	values := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("v%d.example.com", i)
		}
		return out
	}
	cases := []struct {
		name  string
		req   FilterRequest
		field string
	}{
		{"a month 13", FilterRequest{SavedAfter: "2024-13-01"}, "saved_after"},
		{"a day of no month", FilterRequest{SavedBefore: "2023-02-29"}, "saved_before"},
		{"words", FilterRequest{PublishedAfter: "last week"}, "published_after"},
		{"a date-time without a zone", FilterRequest{PublishedBefore: "2024-03-01T09:00:00"}, "published_before"},
		{"a date-time with a space", FilterRequest{SavedAfter: "2024-03-01 09:00:00Z"}, "saved_after"},
		{"year 0", FilterRequest{SavedAfter: "0000-12-31"}, "saved_after"},
		{"year 0 in UTC", FilterRequest{SavedBefore: "0001-01-01T00:30:00+01:00"}, "saved_before"},
		{"after equal to before", FilterRequest{SavedAfter: "2024-01-01", SavedBefore: "2024-01-01T00:00:00Z"},
			"saved_after"},
		{"after past before", FilterRequest{PublishedAfter: "2024-02-01", PublishedBefore: "2024-01-01"},
			"published_after"},
		{"a window within one millisecond", FilterRequest{SavedAfter: "2024-01-01T00:00:00.0001Z",
			SavedBefore: "2024-01-01T00:00:00.0009Z"}, "saved_after"},
		{"a host one past a DNS name", FilterRequest{Host: []string{strings.Repeat("h", 254)}}, "host"},
		{"a site one past a DNS name", FilterRequest{Site: []string{strings.Repeat("s", 250) + ".com"}}, "site"},
		{"a folder one past the most", FilterRequest{Folder: []string{"/" + strings.Repeat("f", 1024)}}, "folder"},
		{"include_undated alone", FilterRequest{IncludeUndated: true}, "include_undated"},
		{"include_undated without a date", FilterRequest{IncludeUndated: true, Site: []string{"example.com"}},
			"include_undated"},
		{"an empty site", FilterRequest{Site: []string{" "}}, "site"},
		{"a site with a scheme", FilterRequest{Site: []string{"https://example.com"}}, "site"},
		{"a site with a path", FilterRequest{Site: []string{"example.com/blog"}}, "site"},
		{"a site with a port", FilterRequest{Site: []string{"example.com:443"}}, "site"},
		{"a site with a user", FilterRequest{Site: []string{"me@example.com"}}, "site"},
		{"a wildcard site", FilterRequest{Site: []string{"*.example.com"}}, "site"},
		{"a site with a space", FilterRequest{Site: []string{"exa mple.com"}}, "site"},
		{"a site with an empty label", FilterRequest{Site: []string{".example.com"}}, "site"},
		{"an empty folder", FilterRequest{Folder: []string{""}}, "folder"},
		{"the root of every folder", FilterRequest{Folder: []string{" // "}}, "folder"},
		{"an unknown content type", FilterRequest{ContentType: []string{"articles"}}, "content_type"},
		{"an unknown source", FilterRequest{Source: []string{"opera"}}, "source"},
		{"an empty host", FilterRequest{Host: []string{""}}, "host"},
		{"33 hosts", FilterRequest{Host: values(33)}, "host"},
		{"33 sites", FilterRequest{Site: values(33)}, "site"},
		{"33 folders", FilterRequest{Folder: values(33)}, "folder"},
		{"33 sources", FilterRequest{Source: make([]string, 33)}, "source"},
		{"33 content types", FilterRequest{ContentType: make([]string, 33)}, "content_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.req.Resolve()
			var fe *FilterError
			require.True(t, errors.As(err, &fe), "%v", err)
			assert.Equal(t, tc.field, fe.Field)
			assert.True(t, strings.HasPrefix(err.Error(), "filters."+tc.field+": "), err.Error())
		})
	}
}

// TestFilterRequest_ResolveTakesTheMost: every list at its most values
// resolves.
func TestFilterRequest_ResolveTakesTheMost(t *testing.T) {
	n := store.MaxFilterValues
	req := FilterRequest{Host: make([]string, n), Site: make([]string, n), Folder: make([]string, n)}
	for i := range n {
		req.Host[i] = fmt.Sprintf("h%d.example.com", i)
		req.Site[i] = fmt.Sprintf("s%d.example.com", i)
		req.Folder[i] = fmt.Sprintf("/F%d", i)
	}
	f, err := req.Resolve()
	require.NoError(t, err)
	assert.Len(t, f.Site, n)

	long := FilterRequest{Host: []string{strings.Repeat("h", 253)}, Site: []string{strings.Repeat("s", 249) + ".com"},
		Folder: []string{strings.Repeat("f", 1023) + "/"}}
	f, err = long.Resolve()
	require.NoError(t, err, "the longest values")
	assert.NoError(t, f.Validate())
	assert.Len(t, f.Folder[0], store.MaxFilterFolderBytes, "a leading slash added, the trailing one dropped")
}
