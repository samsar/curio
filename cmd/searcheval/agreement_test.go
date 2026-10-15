package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/eval"
	"github.com/samsar/curio/internal/search"
)

// TestAgreement: each dimension either side sets gets a verdict: the
// plan's window, read as days in the local zone (New York here), against
// the file's as written, and the plan's values against the file's; a
// dimension only the file sets is missing, one only the plan sets extra.
func TestAgreement(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	file := &eval.Filters{SavedAfter: "2018-06-01", SavedBefore: "2018-09-23"}
	local := func(day string) string { return day + "T00:00:00-04:00" }
	for _, tc := range []struct {
		name string
		file *eval.Filters
		plan search.FilterRequest
		want map[string]verdict
	}{
		{"the same days", file, search.FilterRequest{SavedAfter: local("2018-06-01"), SavedBefore: local("2018-09-23")},
			map[string]verdict{"saved": agreeSame}},
		{"inside", file, search.FilterRequest{SavedAfter: local("2018-06-20"), SavedBefore: local("2018-09-23")},
			map[string]verdict{"saved": agreeNarrower}},
		{"around", file, search.FilterRequest{SavedAfter: local("2018-05-01"), SavedBefore: local("2018-10-01")},
			map[string]verdict{"saved": agreeWider}},
		{"one bound, open to the past", file, search.FilterRequest{SavedBefore: local("2018-09-23")},
			map[string]verdict{"saved": agreeWider}},
		{"overlapping in part", file, search.FilterRequest{SavedAfter: local("2018-07-01"), SavedBefore: local("2018-12-01")},
			map[string]verdict{"saved": agreeWrong}},
		{"apart", file, search.FilterRequest{SavedAfter: local("2017-06-01"), SavedBefore: local("2017-09-23")},
			map[string]verdict{"saved": agreeWrong}},
		{"missing", file, search.FilterRequest{}, map[string]verdict{"saved": agreeMissing}},
		{"extra, on a query without filters", nil, search.FilterRequest{SavedAfter: local("2018-06-01"),
			ContentType: []string{"video"}, Source: []string{"safari"}},
			map[string]verdict{"saved": agreeExtra, "content_type": agreeExtra, "source": agreeExtra}},
		{"published, as days", &eval.Filters{PublishedAfter: "2019-01-01", PublishedBefore: "2020-01-01"},
			search.FilterRequest{PublishedAfter: "2019-01-01", PublishedBefore: "2020-01-01"},
			map[string]verdict{"published": agreeSame}},
		{"sites", &eval.Filters{Site: []string{"a.example", "b.example"}, Folder: []string{"/X"}},
			search.FilterRequest{Site: []string{"a.example"}, Folder: []string{"/X", "/Y"}},
			map[string]verdict{"site": agreeNarrower, "folder": agreeWider}},
		{"other sites", &eval.Filters{Site: []string{"a.example"}}, search.FilterRequest{Site: []string{"c.example"}},
			map[string]verdict{"site": agreeWrong}},
		{"nothing on either side", nil, search.FilterRequest{}, map[string]verdict{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, agreement(tc.file, tc.plan, ny))
		})
	}
	// A local midnight is its local day, whatever UTC's is: midnight in New
	// York is 04:00 UTC the same day, but Tokyo's is 15:00 UTC the day
	// before.
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	require.NoError(t, err)
	assert.Equal(t, map[string]verdict{"saved": agreeSame}, agreement(file, search.FilterRequest{
		SavedAfter: "2018-06-01T00:00:00+09:00", SavedBefore: "2018-09-23T00:00:00+09:00"}, tokyo))
}
