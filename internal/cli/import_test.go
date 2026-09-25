package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/importer"
)

// TestFollowProgress_Interrupted: ctrl-c during --follow is a quiet stop;
// the import itself has finished.
func TestFollowProgress_Interrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	// Nothing listens there: an interrupted follow never asks.
	require.NoError(t, followProgress(ctx, &out, client.New("http://127.0.0.1:1")))
	assert.Contains(t, out.String(), "interrupted")
	assert.NotContains(t, out.String(), "stats unavailable")
}

func TestPickChromeProfile(t *testing.T) {
	profiles := []importer.ChromeProfile{
		{Dir: "Default", Name: "Person 1"},
		{Dir: "Profile 1", Name: "Work"},
	}
	cases := map[string]string{
		"Default":   "Default",
		"Profile 1": "Profile 1",
		"Work":      "Profile 1",
		"work":      "Profile 1", // display names ignore case
		"person 1":  "Default",
	}
	for want, dir := range cases {
		got := pickChromeProfile(profiles, want)
		require.NotNil(t, got, want)
		assert.Equal(t, dir, got.Dir, want)
	}
	assert.Nil(t, pickChromeProfile(profiles, "default"), "directories match exactly")
	assert.Nil(t, pickChromeProfile(profiles, "Personal"))
}
