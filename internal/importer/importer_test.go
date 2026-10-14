package importer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/docmeta"
)

// TestRootLabels_AreBrowserRoots: every root label the browser importers
// start a folder path with is one docmeta reads as a browser's root, so a
// document's folders are searched by the folders the user made, not by
// the tree they live in.
func TestRootLabels_AreBrowserRoots(t *testing.T) {
	labels := make([]string, 0, 9)
	for _, key := range []string{"bookmark_bar", "other", "synced"} {
		labels = append(labels, chromeRootLabel(key))
	}
	for _, id := range []string{"BookmarksBar", "BookmarksMenu"} {
		labels = append(labels, safariRootLabel(safariNode{WebBookmarkIdentifier: id}))
	}
	for _, guid := range []string{ffGUIDMenu, ffGUIDToolbar, ffGUIDUnfiled, ffGUIDMobile} {
		label, ok := firefoxRootLabel(guid)
		assert.True(t, ok, guid)
		labels = append(labels, label)
	}
	for _, label := range labels {
		path := joinFolderPath([]string{label, "Reading"})
		assert.Equal(t, "Reading", docmeta.FolderText([]string{path}), "%s is a browser's root", label)
		assert.Empty(t, docmeta.FolderText([]string{joinFolderPath([]string{label})}), label)
	}
}
