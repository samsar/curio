package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestHighRanks: a query's line lists its grade-2 judgments' ranks in
// order, then "-" for each search could return but didn't, then "×" for
// each it can't, and past twelve entries how many more.
func TestHighRanks(t *testing.T) {
	rank := func(r int) *int { return &r }
	high := func(r *int, s judgmentStatus) judgmentReport {
		return judgmentReport{Grade: 2, Rank: r, Status: s}
	}
	cases := []struct {
		name string
		in   []judgmentReport
		want string
	}{
		{"none", nil, ""},
		{"grade 1 is left out", []judgmentReport{{Grade: 1, Rank: rank(1), Status: searchable}}, ""},
		{"ranked, absent, unreachable", []judgmentReport{
			high(nil, unsearchable), high(rank(7), searchable), high(nil, searchable),
			high(rank(2), searchable), high(nil, missing),
		}, "2 7 - × ×"},
		{"capped at twelve", func() []judgmentReport {
			var js []judgmentReport
			for r := 20; r >= 11; r-- {
				js = append(js, high(rank(r), searchable))
			}
			return append(js, high(nil, searchable), high(nil, searchable), high(nil, missing), high(nil, missing))
		}(), "11 12 13 14 15 16 17 18 19 20 - - (+2)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, highRanks(tc.in))
		})
	}
}

// TestNearestRank: the percentile is the value at ceil(pct·n/100)−1,
// computed in integers.
func TestNearestRank(t *testing.T) {
	ms := func(n int) []time.Duration {
		out := make([]time.Duration, n)
		for i := range out {
			out[i] = time.Duration(i+1) * time.Millisecond
		}
		return out
	}
	cases := []struct {
		name string
		in   []time.Duration
		pct  int
		want time.Duration
	}{
		{"empty", nil, 50, 0},
		{"one", ms(1), 95, time.Millisecond},
		{"p50 of 20", ms(20), 50, 10 * time.Millisecond},
		{"p95 of 20", ms(20), 95, 19 * time.Millisecond},
		{"p95 of 50", ms(50), 95, 48 * time.Millisecond},
		{"p50 of 3", ms(3), 50, 2 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, nearestRank(tc.in, tc.pct))
		})
	}
}
