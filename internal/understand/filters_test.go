package understand

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/search"
)

// TestGroundFilters: each dimension the question's words state is kept,
// saved days as local midnights and published days as days; each it
// doesn't state, or that fails a sanity bound or Resolve, is dropped
// alone, with why. "Today" is Thursday 2025-04-17 in New York.
func TestGroundFilters(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	now := time.Date(2025, 4, 17, 15, 0, 0, 0, ny)
	for _, tc := range []struct {
		name     string
		question string
		in       answerFilters
		want     search.FilterRequest
		dropped  []string // the fields dropped
	}{
		{"a year, in winter time", "the queue articles I saved in 2017", answerFilters{SavedAfter: "2017-01-01",
			SavedBefore: "2018-01-01"}, search.FilterRequest{SavedAfter: "2017-01-01T00:00:00-05:00",
			SavedBefore: "2018-01-01T00:00:00-05:00"}, nil},
		{"summer time", "what I read in July 2018", answerFilters{SavedAfter: "2018-07-01", SavedBefore: "2018-08-01"},
			search.FilterRequest{SavedAfter: "2018-07-01T00:00:00-04:00", SavedBefore: "2018-08-01T00:00:00-04:00"}, nil},
		{"one bound", "notes saved 3 or more years ago", answerFilters{SavedBefore: "2022-04-18"},
			search.FilterRequest{SavedBefore: "2022-04-18T00:00:00-04:00"}, nil},
		{"a season word", "talks from last winter", answerFilters{SavedAfter: "2024-12-01",
			SavedBefore: "2025-03-21", ContentType: []string{"video"}}, search.FilterRequest{
			SavedAfter: "2024-12-01T00:00:00-05:00", SavedBefore: "2025-03-21T00:00:00-04:00",
			ContentType: []string{"video"}}, nil},
		{"no time stated", "postgres vacuum tuning", answerFilters{SavedAfter: "2025-04-10"},
			search.FilterRequest{}, []string{"saved"}},
		{"published, stated", "papers published in 2019", answerFilters{PublishedAfter: "2019-01-01",
			PublishedBefore: "2020-01-01"}, search.FilterRequest{PublishedAfter: "2019-01-01",
			PublishedBefore: "2020-01-01"}, nil},
		{"published, not stated", "papers I saved in 2019", answerFilters{SavedAfter: "2019-01-01",
			SavedBefore: "2020-01-01", PublishedAfter: "2019-01-01", PublishedBefore: "2020-01-01"},
			search.FilterRequest{SavedAfter: "2019-01-01T00:00:00-05:00", SavedBefore: "2020-01-01T00:00:00-05:00"},
			[]string{"published"}},
		{"a type stated, another not", "videos about raft", answerFilters{ContentType: []string{"video", "pdf",
			"video"}}, search.FilterRequest{ContentType: []string{"video"}}, []string{"content_type"}},
		{"a type outside the schema", "an article about raft", answerFilters{ContentType: []string{"article"}},
			search.FilterRequest{}, []string{"content_type"}},
		{"a browser stated", "what I saved from safari", answerFilters{Source: []string{"safari"}},
			search.FilterRequest{Source: []string{"safari"}}, nil},
		{"a browser not stated", "rust ownership", answerFilters{Source: []string{"chrome", "firefox"}},
			search.FilterRequest{}, []string{"source"}},
		{"starts after tomorrow", "plans for next year", answerFilters{SavedAfter: "2026-01-01",
			SavedBefore: "2027-01-01"}, search.FilterRequest{}, []string{"saved"}},
		{"starts tomorrow", "anything from today", answerFilters{SavedAfter: "2025-04-18"},
			search.FilterRequest{SavedAfter: "2025-04-18T00:00:00-04:00"}, nil},
		{"ends by 1990", "articles from 1989", answerFilters{SavedAfter: "1989-01-01", SavedBefore: "1990-01-01"},
			search.FilterRequest{}, []string{"saved"}},
		{"not a day", "saved in 2017", answerFilters{SavedAfter: "2017-13-01"}, search.FilterRequest{},
			[]string{"saved"}},
		{"a window that holds nothing", "saved in 2017", answerFilters{SavedAfter: "2018-01-01",
			SavedBefore: "2017-01-01"}, search.FilterRequest{}, []string{"saved"}},
		{"another language", "vídeos sobre Rust que guardé el mes pasado", answerFilters{SavedAfter: "2025-03-01",
			SavedBefore: "2025-04-01", ContentType: []string{"video"}}, search.FilterRequest{},
			[]string{"saved", "content_type"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped := groundFilters(tc.in, tc.question, now, ny)
			assert.Equal(t, tc.want, got)
			var fields []string
			for _, d := range dropped {
				fields = append(fields, d.Field)
				assert.NotEmpty(t, d.Reason, d.Field)
			}
			assert.Equal(t, tc.dropped, fields)
			assert.False(t, got.IncludeUndated, "never from an answer")
			_, err := got.Resolve()
			assert.NoError(t, err, "what is kept resolves")
		})
	}
}

// TestGroundFilters_Reasons: a dropped filter says which guard or which
// refusal dropped it.
func TestGroundFilters_Reasons(t *testing.T) {
	now := time.Date(2025, 4, 17, 12, 0, 0, 0, time.UTC)
	reason := func(question string, f answerFilters) string {
		_, dropped := groundFilters(f, question, now, time.UTC)
		require.Len(t, dropped, 1)
		return dropped[0].Reason
	}
	assert.Equal(t, "the question names no time: no year, month, season or word like week or ago",
		reason("postgres", answerFilters{SavedAfter: "2025-01-01"}))
	assert.Equal(t, `the question doesn't name the type "pdf"`,
		reason("videos", answerFilters{ContentType: []string{"video", "pdf"}}))
	assert.Equal(t, `the question doesn't name the browser "chrome"`,
		reason("rust", answerFilters{Source: []string{"chrome"}}))
	assert.Equal(t, "the window starts after tomorrow, 2025-04-18",
		reason("next year", answerFilters{SavedAfter: "2026-01-01"}))
	assert.Equal(t, `"2017-02-30" is not a day YYYY-MM-DD`, reason("in 2017", answerFilters{SavedAfter: "2017-02-30"}))
	assert.Contains(t, reason("in 2017", answerFilters{SavedAfter: "2018-01-01", SavedBefore: "2017-01-01"}),
		"must be before saved_before", "Resolve's reason")
}
