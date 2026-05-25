package importer

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// XML plist representation of a minimal Safari Bookmarks.plist. The real
// file is binary plist, but howett.net/plist handles both transparently.
const sampleSafariPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>WebBookmarkType</key>
	<string>WebBookmarkTypeList</string>
	<key>Title</key>
	<string></string>
	<key>Children</key>
	<array>
		<!-- Favorites / BookmarksBar -->
		<dict>
			<key>WebBookmarkType</key>
			<string>WebBookmarkTypeList</string>
			<key>WebBookmarkIdentifier</key>
			<string>BookmarksBar</string>
			<key>Title</key>
			<string>BookmarksBar</string>
			<key>Children</key>
			<array>
				<dict>
					<key>WebBookmarkType</key>
					<string>WebBookmarkTypeLeaf</string>
					<key>URLString</key>
					<string>https://martinfowler.com/articles/feature-toggles.html</string>
					<key>URIDictionary</key>
					<dict>
						<key>title</key>
						<string>Feature Toggles</string>
					</dict>
				</dict>
				<dict>
					<key>WebBookmarkType</key>
					<string>WebBookmarkTypeList</string>
					<key>Title</key>
					<string>Tech</string>
					<key>Children</key>
					<array>
						<dict>
							<key>WebBookmarkType</key>
							<string>WebBookmarkTypeLeaf</string>
							<key>URLString</key>
							<string>https://example.com/postgres-mvcc</string>
							<key>URIDictionary</key>
							<dict>
								<key>title</key>
								<string>Postgres MVCC</string>
							</dict>
						</dict>
						<dict>
							<key>WebBookmarkType</key>
							<string>WebBookmarkTypeList</string>
							<key>Title</key>
							<string>AI</string>
							<key>Children</key>
							<array>
								<dict>
									<key>WebBookmarkType</key>
									<string>WebBookmarkTypeLeaf</string>
									<key>URLString</key>
									<string>https://example.com/agents</string>
									<key>URIDictionary</key>
									<dict>
										<key>title</key>
										<string>Agents</string>
									</dict>
								</dict>
							</array>
						</dict>
					</array>
				</dict>
			</array>
		</dict>
		<!-- Bookmarks Menu -->
		<dict>
			<key>WebBookmarkType</key>
			<string>WebBookmarkTypeList</string>
			<key>WebBookmarkIdentifier</key>
			<string>BookmarksMenu</string>
			<key>Title</key>
			<string>BookmarksMenu</string>
			<key>Children</key>
			<array>
				<dict>
					<key>WebBookmarkType</key>
					<string>WebBookmarkTypeLeaf</string>
					<key>URLString</key>
					<string>https://example.com/menu-bookmark</string>
					<key>URIDictionary</key>
					<dict>
						<key>title</key>
						<string>Menu Bookmark</string>
					</dict>
				</dict>
			</array>
		</dict>
		<!-- Reading List — should be skipped -->
		<dict>
			<key>WebBookmarkType</key>
			<string>WebBookmarkTypeList</string>
			<key>WebBookmarkIdentifier</key>
			<string>com.apple.ReadingList</string>
			<key>Title</key>
			<string>com.apple.ReadingList</string>
			<key>Children</key>
			<array>
				<dict>
					<key>WebBookmarkType</key>
					<string>WebBookmarkTypeLeaf</string>
					<key>URLString</key>
					<string>https://example.com/reading-list-item</string>
					<key>URIDictionary</key>
					<dict>
						<key>title</key>
						<string>Should Not Appear</string>
					</dict>
				</dict>
			</array>
		</dict>
	</array>
</dict>
</plist>`

func TestParseSafari_Basic(t *testing.T) {
	got, err := ParseSafari(strings.NewReader(sampleSafariPlist))
	require.NoError(t, err)
	require.Len(t, got, 4)

	byURL := map[string]ParsedBookmark{}
	for _, b := range got {
		byURL[b.URL] = b
	}

	fts, ok := byURL["https://martinfowler.com/articles/feature-toggles.html"]
	require.True(t, ok)
	assert.Equal(t, "/Favorites", fts.FolderPath)
	assert.Equal(t, "Feature Toggles", fts.Title)

	mvcc, ok := byURL["https://example.com/postgres-mvcc"]
	require.True(t, ok)
	assert.Equal(t, "/Favorites/Tech", mvcc.FolderPath)

	agents, ok := byURL["https://example.com/agents"]
	require.True(t, ok)
	assert.Equal(t, "/Favorites/Tech/AI", agents.FolderPath)

	menu, ok := byURL["https://example.com/menu-bookmark"]
	require.True(t, ok)
	assert.Equal(t, "/Bookmarks Menu", menu.FolderPath)
}

func TestParseSafari_SkipsReadingList(t *testing.T) {
	got, err := ParseSafari(strings.NewReader(sampleSafariPlist))
	require.NoError(t, err)
	for _, b := range got {
		assert.NotEqual(t, "https://example.com/reading-list-item", b.URL,
			"reading list items should be excluded")
	}
}

func TestParseSafari_Empty(t *testing.T) {
	empty := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>WebBookmarkType</key>
	<string>WebBookmarkTypeList</string>
	<key>Children</key>
	<array></array>
</dict>
</plist>`
	_, err := ParseSafari(strings.NewReader(empty))
	assert.ErrorIs(t, err, ErrEmpty)
}

func TestParseSafari_NoURIDictionary(t *testing.T) {
	p := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>WebBookmarkType</key>
	<string>WebBookmarkTypeList</string>
	<key>Children</key>
	<array>
		<dict>
			<key>WebBookmarkType</key>
			<string>WebBookmarkTypeList</string>
			<key>WebBookmarkIdentifier</key>
			<string>BookmarksBar</string>
			<key>Title</key>
			<string>BookmarksBar</string>
			<key>Children</key>
			<array>
				<dict>
					<key>WebBookmarkType</key>
					<string>WebBookmarkTypeLeaf</string>
					<key>URLString</key>
					<string>https://example.com/no-title</string>
				</dict>
			</array>
		</dict>
	</array>
</dict>
</plist>`
	got, err := ParseSafari(strings.NewReader(p))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "", got[0].Title)
	assert.Equal(t, "https://example.com/no-title", got[0].URL)
}

func TestParseSafari_MalformedPlist(t *testing.T) {
	_, err := ParseSafari(strings.NewReader(`not a plist`))
	assert.Error(t, err)
}

func TestSafariBookmarksPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CURIO_SAFARI_DIR", dir)

	// No file yet — should return empty.
	assert.Equal(t, "", SafariBookmarksPath())

	// Create the file.
	mustWrite(t, filepath.Join(dir, "Bookmarks.plist"), "placeholder")
	assert.Equal(t, filepath.Join(dir, "Bookmarks.plist"), SafariBookmarksPath())
}
