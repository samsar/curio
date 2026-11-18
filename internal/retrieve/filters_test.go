package retrieve

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/understand"
)

// TestMergeFilters: each dimension the request sets is its own, the plan's
// of that dimension dropped; a window is one dimension; include_undated
// goes with the request's dates when it sets any.
func TestMergeFilters(t *testing.T) {
	plan := search.FilterRequest{ContentType: []string{"video"}, Source: []string{"safari"},
		SavedAfter: "2024-01-01T00:00:00-05:00", SavedBefore: "2025-01-01T00:00:00-05:00",
		PublishedBefore: "2020-01-01", IncludeUndated: true}
	for _, tc := range []struct {
		name    string
		req     search.FilterRequest
		want    search.FilterRequest
		dropped []string
	}{
		{"nothing set", search.FilterRequest{}, plan, nil},
		{"a type", search.FilterRequest{ContentType: []string{"pdf"}}, search.FilterRequest{
			ContentType: []string{"pdf"}, Source: plan.Source, SavedAfter: plan.SavedAfter,
			SavedBefore: plan.SavedBefore, PublishedBefore: plan.PublishedBefore, IncludeUndated: true},
			[]string{"content_type"}},
		{"one bound of the saved window", search.FilterRequest{SavedBefore: "2023-01-01"}, search.FilterRequest{
			ContentType: plan.ContentType, Source: plan.Source, SavedBefore: "2023-01-01",
			PublishedBefore: plan.PublishedBefore}, []string{"saved", "include_undated"}},
		{"the request's dates, undated kept", search.FilterRequest{PublishedAfter: "2019-01-01",
			IncludeUndated: true}, search.FilterRequest{ContentType: plan.ContentType, Source: plan.Source,
			SavedAfter: plan.SavedAfter, SavedBefore: plan.SavedBefore, PublishedAfter: "2019-01-01",
			IncludeUndated: true}, []string{"published"}},
		{"a site the plan hasn't", search.FilterRequest{Site: []string{"example.com"}}, search.FilterRequest{
			ContentType: plan.ContentType, Source: plan.Source, Site: []string{"example.com"},
			SavedAfter: plan.SavedAfter, SavedBefore: plan.SavedBefore, PublishedBefore: plan.PublishedBefore,
			IncludeUndated: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped := mergeFilters(tc.req, plan)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.dropped, fieldsOf(dropped))
			_, err := got.Resolve()
			assert.NoError(t, err)
		})
	}
}

// TestWithoutDimension: a dimension taken out takes both bounds of a
// window, and the last date filter takes include_undated with it.
func TestWithoutDimension(t *testing.T) {
	f := search.FilterRequest{ContentType: []string{"video"}, Site: []string{"example.com"},
		SavedAfter: "2024-01-01", PublishedBefore: "2020-01-01", IncludeUndated: true}
	assert.Equal(t, search.FilterRequest{Site: []string{"example.com"}, SavedAfter: "2024-01-01",
		PublishedBefore: "2020-01-01", IncludeUndated: true}, WithoutDimension(f, "content_type"))
	saved := WithoutDimension(f, "saved")
	assert.Empty(t, saved.SavedAfter)
	assert.True(t, saved.IncludeUndated, "a published bound is left")
	assert.False(t, WithoutDimension(saved, "published").IncludeUndated, "no date left to keep undated beside")
	assert.Equal(t, f, WithoutDimension(f, "nothing"), "an unknown field takes nothing")
}

// fieldsOf are dropped's fields, none for none.
func fieldsOf(dropped []understand.Dropped) []string {
	if len(dropped) == 0 {
		return nil
	}
	out := make([]string, len(dropped))
	for i, d := range dropped {
		out[i] = d.Field
	}
	return out
}
