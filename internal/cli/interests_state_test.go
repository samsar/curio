package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/client"
)

// TestMapText: each map status the daemon reports reads as its words; one
// this CLI doesn't know, from a newer daemon, is named as sent.
func TestMapText(t *testing.T) {
	took := int64(2900)
	for _, tc := range []struct {
		name string
		m    *client.InterestsMap
		want string
	}{
		{"no map state", nil, ""},
		{"none", &client.InterestsMap{Status: client.MapNone}, ""},
		{"built", &client.InterestsMap{Status: client.MapBuilt}, "map built"},
		{"built warm", &client.InterestsMap{Status: client.MapBuilt, Kind: "warm", TookMS: &took}, "map built (warm, in " + tookText(took) + ")"},
		{"failed", &client.InterestsMap{Status: client.MapFailed, Error: "no room"}, "map failed (no room)"},
		{"off", &client.InterestsMap{Status: client.MapOff}, "map off (insight.map: false)"},
		{"a status from a newer daemon", &client.InterestsMap{Status: "redrawing"}, "map redrawing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mapText(tc.m))
		})
	}
}

// TestDueText_WaitingFor: what a due rebuild waits for, as the daemon
// says it; a daemon that says nothing keeps the words from before.
func TestDueText_WaitingFor(t *testing.T) {
	done := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	reindex := client.InterestsState{State: client.StateDue, LastRebuildAt: done, FreshOwed: client.FreshReindex}
	for name, tc := range map[string]struct {
		s    client.InterestsState
		want string
	}{
		"re-embedding":          {withWaiting(reindex, client.WaitingReembedding), "a fresh rebuild is due: waiting for the re-embedding to finish"},
		"re-embedded, settling": {withWaiting(reindex, client.WaitingSettle), "a fresh rebuild is due (after a re-embedding): waiting for the library to settle"},
		"an older daemon":       {reindex, "a fresh rebuild is due: waiting for the re-embedding to finish"},
		"the embedding check": {withWaiting(client.InterestsState{State: client.StateDue, LastRebuildAt: done,
			ChangedDocuments: 271, RebuildAt: 263}, client.WaitingEmbeddingCheck),
			"a rebuild is due (271 changed, threshold 263): waiting for the first embedding check since the daemon started"},
		"the first grouping, the embedding check": {withWaiting(client.InterestsState{State: client.StateDue,
			ChangedDocuments: 25}, client.WaitingEmbeddingCheck),
			"the first grouping is due (25 documents indexed): waiting for the first embedding check since the daemon started"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, dueText(tc.s))
		})
	}
}

func withWaiting(s client.InterestsState, waiting string) client.InterestsState {
	s.WaitingFor = waiting
	return s
}
