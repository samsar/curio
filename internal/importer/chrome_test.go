package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleChromeJSON = `{
  "checksum": "abc",
  "version": 1,
  "roots": {
    "bookmark_bar": {
      "type": "folder",
      "name": "Bookmarks bar",
      "date_added": "13077898918859454",
      "children": [
        {
          "type": "url",
          "name": "Feature Toggles",
          "url": "https://martinfowler.com/articles/feature-toggles.html",
          "date_added": "13371638400000000"
        },
        {
          "type": "folder",
          "name": "Tech",
          "children": [
            {
              "type": "url",
              "name": "Postgres MVCC",
              "url": "https://example.com/postgres-mvcc",
              "date_added": "13371638400000000"
            },
            {
              "type": "folder",
              "name": "AI",
              "children": [
                {
                  "type": "url",
                  "name": "Agents",
                  "url": "https://example.com/agents",
                  "date_added": "0"
                }
              ]
            }
          ]
        }
      ]
    },
    "other": {
      "type": "folder",
      "name": "Other bookmarks",
      "children": [
        {
          "type": "url",
          "name": "Chrome internal",
          "url": "chrome://bookmarks/",
          "date_added": "0"
        }
      ]
    },
    "synced": {
      "type": "folder",
      "name": "Mobile",
      "children": []
    }
  }
}`

func TestParseChrome_Basic(t *testing.T) {
	got, err := ParseChrome(strings.NewReader(sampleChromeJSON))
	require.NoError(t, err)
	require.Len(t, got, 4)

	byURL := map[string]ParsedBookmark{}
	for _, b := range got {
		byURL[b.URL] = b
	}

	fts, ok := byURL["https://martinfowler.com/articles/feature-toggles.html"]
	require.True(t, ok)
	assert.Equal(t, "/Bookmarks Bar", fts.FolderPath)
	assert.Equal(t, "Feature Toggles", fts.Title)
	assert.False(t, fts.SavedAt.IsZero())
	// Sanity: 13371638400000000 micros since 1601 is roughly year 2024.
	assert.Equal(t, 2024, fts.SavedAt.Year())

	mvcc, ok := byURL["https://example.com/postgres-mvcc"]
	require.True(t, ok)
	assert.Equal(t, "/Bookmarks Bar/Tech", mvcc.FolderPath)

	agents, ok := byURL["https://example.com/agents"]
	require.True(t, ok)
	assert.Equal(t, "/Bookmarks Bar/Tech/AI", agents.FolderPath)
	assert.True(t, agents.SavedAt.IsZero(), "date_added=0 should produce zero time")

	internal, ok := byURL["chrome://bookmarks/"]
	require.True(t, ok)
	assert.Equal(t, "/Other Bookmarks", internal.FolderPath)
}

func TestParseChrome_EmptyRoots(t *testing.T) {
	_, err := ParseChrome(strings.NewReader(`{"roots":{}}`))
	assert.ErrorIs(t, err, ErrEmpty)
}

func TestParseChrome_MalformedJSON(t *testing.T) {
	_, err := ParseChrome(strings.NewReader(`{not json`))
	assert.Error(t, err)
}

// Profile discovery is filesystem-driven; test via CURIO_CHROME_DIR
// pointing at a synthetic dir tree.
func TestDiscoverChromeProfiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CURIO_CHROME_DIR", root)

	// Default profile.
	mustMkdir(t, filepath.Join(root, "Default"))
	mustWrite(t, filepath.Join(root, "Default", "Bookmarks"), `{"roots":{}}`)

	// Profile 1 with a display name in Local State.
	mustMkdir(t, filepath.Join(root, "Profile 1"))
	mustWrite(t, filepath.Join(root, "Profile 1", "Bookmarks"), `{"roots":{}}`)

	// Profile 2 with no Bookmarks file — should be skipped.
	mustMkdir(t, filepath.Join(root, "Profile 2"))

	mustWrite(t, filepath.Join(root, "Local State"), `{
		"profile": {
			"info_cache": {
				"Default":   {"name": "Personal"},
				"Profile 1": {"name": "Work"}
			}
		}
	}`)

	got, err := DiscoverChromeProfiles()
	require.NoError(t, err)
	require.Len(t, got, 2, "Profile 2 has no Bookmarks file and should be skipped")
	assert.Equal(t, "Default", got[0].Dir)
	assert.Equal(t, "Personal", got[0].Name)
	assert.Equal(t, "Profile 1", got[1].Dir)
	assert.Equal(t, "Work", got[1].Name)
}

func TestCompareChromeProfiles(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"Default", "Profile 1", -1},
		{"Profile 1", "Default", 1},
		{"Profile 2", "Profile 10", -1},
		{"Profile 10", "Profile 2", 1},
		{"Profile 3", "Profile 3", 0},
		{"Profile 99", "Guest Profile", -1},
		{"Profile abc", "Profile 1", 1},
		{"Guest Profile", "System Profile", -1},
		{"Default", "Guest Profile", -1},
	}
	for _, tc := range cases {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			assert.Equal(t, tc.want, compareChromeProfiles(tc.a, tc.b))
		})
	}
}

func TestDiscoverChromeProfiles_NumericOrder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CURIO_CHROME_DIR", root)
	for _, dir := range []string{"Profile 10", "Profile 2", "Default"} {
		mustMkdir(t, filepath.Join(root, dir))
		mustWrite(t, filepath.Join(root, dir, "Bookmarks"), `{"roots":{}}`)
	}

	got, err := DiscoverChromeProfiles()
	require.NoError(t, err)
	dirs := make([]string, len(got))
	for i, p := range got {
		dirs[i] = p.Dir
	}
	assert.Equal(t, []string{"Default", "Profile 2", "Profile 10"}, dirs)
}

func TestPushFolder(t *testing.T) {
	base := make([]string, 1, 4) // spare capacity: a plain append would share it
	base[0] = "Root"
	a := pushFolder(base, " A ")
	b := pushFolder(base, "B")
	assert.Equal(t, []string{"Root", "A"}, a)
	assert.Equal(t, []string{"Root", "B"}, b, "siblings don't overwrite each other")
	assert.Equal(t, base, pushFolder(base, "  "), "a blank name adds nothing")
}

func TestDiscoverChromeProfiles_NoChromeInstalled(t *testing.T) {
	t.Setenv("CURIO_CHROME_DIR", filepath.Join(t.TempDir(), "nonexistent"))
	got, err := DiscoverChromeProfiles()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(p, 0o700))
}
func mustWrite(t *testing.T, p, contents string) {
	t.Helper()
	require.NoError(t, os.WriteFile(p, []byte(contents), 0o600))
}
