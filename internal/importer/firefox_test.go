package importer

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildFixturePlaces writes a minimal places.sqlite with the moz_places /
// moz_bookmarks schema and a small bookmark tree, and returns its path.
func buildFixturePlaces(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "places.sqlite")

	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE moz_places (id INTEGER PRIMARY KEY, url TEXT, title TEXT);
		CREATE TABLE moz_bookmarks (
			id INTEGER PRIMARY KEY, type INTEGER, fk INTEGER, parent INTEGER,
			position INTEGER, title TEXT, dateAdded INTEGER, guid TEXT
		);`)
	require.NoError(t, err)

	places := []struct {
		id  int
		url string
	}{
		{1, "https://go.dev"},
		{2, "https://www.anthropic.com"},
		{3, "https://example.com"},
	}
	for _, p := range places {
		_, err = db.Exec(`INSERT INTO moz_places(id, url) VALUES (?, ?)`, p.id, p.url)
		require.NoError(t, err)
	}

	const goDateMicros = int64(1_700_000_000_000_000) // 2023-11-14T...Z
	// id, type, fk, parent, title, dateAdded, guid
	rows := [][]any{
		{1, 2, nil, 0, "", 0, ffGUIDRoot},
		{2, 2, nil, 1, "menu", 0, ffGUIDMenu},
		{3, 2, nil, 1, "toolbar", 0, ffGUIDToolbar},
		{4, 2, nil, 1, "tags", 0, ffGUIDTags},
		{5, 2, nil, 1, "unfiled", 0, ffGUIDUnfiled},
		{10, 2, nil, 2, "Tech", 0, "folderTech__"},
		{11, 2, nil, 10, "AI", 0, "folderAI____"},
		{20, ffTypeBookmark, 1, 3, "Go", goDateMicros, "bm_go_______"}, // toolbar
		{21, ffTypeBookmark, 2, 11, "Anthropic", 0, "bm_anthropic"},    // menu/Tech/AI
		{22, ffTypeBookmark, 3, 2, "Example", 0, "bm_example__"},       // menu (direct)
		{30, 2, nil, 4, "mytag", 0, "tagcontainer"},                    // under tags
		{31, ffTypeBookmark, 1, 30, "Go (tagged)", 0, "bm_go_tag___"},  // under tags -> skip
		{40, 3, nil, 2, "", 0, "separator___"},                         // separator -> skip
	}
	for _, r := range rows {
		_, err = db.Exec(
			`INSERT INTO moz_bookmarks(id, type, fk, parent, title, dateAdded, guid) VALUES (?,?,?,?,?,?,?)`,
			r[0], r[1], r[2], r[3], r[4], r[5], r[6])
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	return path
}

func TestParseFirefox(t *testing.T) {
	path := buildFixturePlaces(t)

	bms, err := ParseFirefox(path)
	require.NoError(t, err)

	// 3 real bookmarks; the tagged entry and the separator are excluded.
	require.Len(t, bms, 3)

	byTitle := map[string]ParsedBookmark{}
	for _, b := range bms {
		byTitle[b.Title] = b
	}

	assert.Equal(t, "/Bookmarks Toolbar", byTitle["Go"].FolderPath)
	assert.Equal(t, "/Bookmarks Menu/Tech/AI", byTitle["Anthropic"].FolderPath)
	assert.Equal(t, "/Bookmarks Menu", byTitle["Example"].FolderPath)

	// URLs are normalized.
	assert.Contains(t, byTitle["Go"].URL, "go.dev")

	// dateAdded (microseconds since the Unix epoch) is converted.
	assert.Equal(t, time.Unix(1_700_000_000, 0).UTC(), byTitle["Go"].SavedAt)

	// The tagged copy of go.dev must not appear (it lives under the Tags root).
	goCount := 0
	for _, b := range bms {
		if b.Title == "Go (tagged)" {
			goCount++
		}
	}
	assert.Zero(t, goCount, "tag entries should be skipped")
}

func TestParseFirefox_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "places.sqlite")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE moz_places (id INTEGER PRIMARY KEY, url TEXT, title TEXT);
		CREATE TABLE moz_bookmarks (id INTEGER PRIMARY KEY, type INTEGER, fk INTEGER, parent INTEGER, position INTEGER, title TEXT, dateAdded INTEGER, guid TEXT);`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = ParseFirefox(path)
	assert.ErrorIs(t, err, ErrEmpty)
}

// TestFirefoxBookmarksPath_PrefersInstallDefault verifies profile selection:
// the [Install*] default (what the running browser uses) wins over a
// [Profile*] marked Default=1.
func TestFirefoxBookmarksPath_PrefersInstallDefault(t *testing.T) {
	root := t.TempDir()
	// The Profile marked Default=1 is the legacy default; the Install default
	// points at a different, newer profile. We must pick the Install one.
	releaseDir := filepath.Join(root, "Profiles", "aaa.default-release")
	legacyDir := filepath.Join(root, "Profiles", "bbb.default")
	require.NoError(t, os.MkdirAll(releaseDir, 0o755))
	require.NoError(t, os.MkdirAll(legacyDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(releaseDir, "places.sqlite"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "places.sqlite"), []byte("x"), 0o644))

	ini := `[Profile0]
Name=default-release
IsRelative=1
Path=Profiles/aaa.default-release

[Profile1]
Name=default
IsRelative=1
Path=Profiles/bbb.default
Default=1

[Install123ABC]
Default=Profiles/aaa.default-release
Locked=1
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "profiles.ini"), []byte(ini), 0o644))

	t.Setenv("CURIO_FIREFOX_DIR", root)
	got := FirefoxBookmarksPath()
	want := filepath.Join(releaseDir, "places.sqlite")
	assert.Equal(t, want, got)
}
