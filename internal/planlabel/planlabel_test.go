package planlabel

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFilter: a filter is said in a person's words, a window's days in the
// local zone with its end shown inclusive: a whole year or month by name,
// one bound as before or since.
func TestFilter(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	for _, tc := range []struct {
		field         string
		values        []string
		after, before string
		want          string
	}{
		{"saved", nil, "2017-01-01T00:00:00-05:00", "2018-01-01T00:00:00-05:00", "Saved in 2017"},
		{"saved", nil, "2016-05-01T00:00:00-04:00", "2016-06-01T00:00:00-04:00", "Saved in May 2016"},
		{"saved", nil, "2018-06-01T00:00:00-04:00", "2018-09-23T00:00:00-04:00", "Saved Jun 1 – Sep 22, 2018"},
		{"saved", nil, "2023-12-20T00:00:00-05:00", "2024-01-04T00:00:00-05:00", "Saved Dec 20, 2023 – Jan 3, 2024"},
		{"saved", nil, "2025-04-17T00:00:00-04:00", "2025-04-18T00:00:00-04:00", "Saved on Apr 17, 2025"},
		{"saved", nil, "", "2022-04-18T00:00:00-04:00", "Saved before Apr 18, 2022"},
		{"saved", nil, "2024-03-01T00:00:00-05:00", "", "Saved since Mar 1, 2024"},
		// A day as written, whatever the zone: a published day is stored at
		// midnight UTC.
		{"published", nil, "2019-01-01", "2020-01-01", "Published in 2019"},
		{"published", nil, "", "2020-03-01", "Published before Mar 1, 2020"},
		{"content_type", []string{"video"}, "", "", "Videos"},
		{"content_type", []string{"repo", "pdf", "thread"}, "", "", "Repos, PDFs or Threads"},
		{"content_type", []string{"article", "unknown", "zine"}, "", "", "Articles, Pages of no known type or zine"},
		{"source", []string{"safari"}, "", "", "From Safari"},
		{"source", []string{"chrome", "firefox"}, "", "", "From Chrome or Firefox"},
		{"source", []string{"html", "manual"}, "", "", "From a bookmarks file or curio"},
		{"site", []string{"example.com"}, "", "", "On example.com"},
		{"host", []string{"www.example.com"}, "", "", "At www.example.com"},
		{"folder", []string{"/Bookmarks Bar/Tech"}, "", "", "In /Bookmarks Bar/Tech"},
	} {
		assert.Equal(t, tc.want, Filter(tc.field, tc.values, tc.after, tc.before, ny), "%+v", tc)
	}
}
