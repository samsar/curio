package cli

import (
	"testing"

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
