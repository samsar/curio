package importer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"howett.net/plist"

	"github.com/samsar/curio/internal/urlutil"
)

// SafariBookmarksPath returns the default path to Safari's Bookmarks.plist.
// Returns "" on non-darwin or if the file doesn't exist. Honors
// CURIO_SAFARI_DIR so tests can inject a fixture directory.
func SafariBookmarksPath() string {
	if v := os.Getenv("CURIO_SAFARI_DIR"); v != "" {
		p := filepath.Join(v, "Bookmarks.plist")
		if _, err := os.Stat(p); err != nil {
			return ""
		}
		return p
	}
	if runtime.GOOS != "darwin" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, "Library", "Safari", "Bookmarks.plist")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// ParseSafari reads a Safari Bookmarks.plist (binary or XML) and returns
// the bookmarks it contains.
//
// Safari's plist is a tree of dicts. Each node has a WebBookmarkType:
//
//   - WebBookmarkTypeList  — folder; has Title + Children array
//   - WebBookmarkTypeLeaf  — bookmark; has URLString + URIDictionary.title
//   - WebBookmarkTypeProxy — special (History, Reading List header)
//
// Top-level special folders are identified by WebBookmarkIdentifier:
//   - "BookmarksBar"  → Favorites (confusingly named)
//   - "BookmarksMenu" → Bookmarks Menu
//   - "com.apple.ReadingList" → Reading List (skipped — ephemeral)
func ParseSafari(r io.ReadSeeker) ([]ParsedBookmark, error) {
	var root safariNode
	decoder := plist.NewDecoder(r)
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("safari: decode plist: %w", err)
	}

	var out []ParsedBookmark
	for _, child := range root.Children {
		if child.WebBookmarkIdentifier == "com.apple.ReadingList" {
			continue
		}
		label := safariRootLabel(child)
		stack := []string{label}
		for _, c := range child.Children {
			walkSafariNode(c, stack, &out)
		}
	}
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

type safariNode struct {
	WebBookmarkType       string       `plist:"WebBookmarkType"`
	WebBookmarkIdentifier string       `plist:"WebBookmarkIdentifier,omitempty"`
	Title                 string       `plist:"Title,omitempty"`
	URLString             string       `plist:"URLString,omitempty"`
	URIDictionary         *safariURI   `plist:"URIDictionary,omitempty"`
	Children              []safariNode `plist:"Children,omitempty"`
}

type safariURI struct {
	Title string `plist:"title"`
}

func safariRootLabel(n safariNode) string {
	switch n.WebBookmarkIdentifier {
	case "BookmarksBar":
		return "Favorites"
	case "BookmarksMenu":
		return "Bookmarks Menu"
	default:
		if n.Title != "" {
			return n.Title
		}
		return "Other"
	}
}

func walkSafariNode(n safariNode, folderStack []string, out *[]ParsedBookmark) {
	switch n.WebBookmarkType {
	case "WebBookmarkTypeLeaf":
		if n.URLString == "" {
			return
		}
		title := ""
		if n.URIDictionary != nil {
			title = strings.TrimSpace(n.URIDictionary.Title)
		}
		bm := ParsedBookmark{
			URL:        n.URLString,
			Title:      title,
			FolderPath: joinFolderPath(folderStack),
		}
		if norm, err := urlutil.Normalize(n.URLString); err == nil {
			bm.URL = norm
		}
		*out = append(*out, bm)

	case "WebBookmarkTypeList":
		next := folderStack
		name := strings.TrimSpace(n.Title)
		if name != "" {
			next = append(append([]string{}, folderStack...), name)
		}
		for _, c := range n.Children {
			walkSafariNode(c, next, out)
		}

	case "WebBookmarkTypeProxy":
		// Proxy nodes (History, Reading List header) — skip.

	default:
		for _, c := range n.Children {
			walkSafariNode(c, folderStack, out)
		}
	}
}
