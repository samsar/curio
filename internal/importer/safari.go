package importer

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"howett.net/plist"
)

// SafariPlist returns the path of Safari's Bookmarks.plist and the error
// of looking at it: one matching fs.ErrNotExist off macOS or when it isn't
// there, and fs.ErrPermission when macOS withholds it (Full Disk Access).
// Honors CURIO_SAFARI_DIR so tests can inject a fixture directory.
func SafariPlist() (string, error) {
	if dir := os.Getenv("CURIO_SAFARI_DIR"); dir != "" {
		p := filepath.Join(dir, "Bookmarks.plist")
		_, err := os.Stat(p)
		return p, err
	}
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("safari's bookmarks are macOS-only: %w", fs.ErrNotExist)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find safari's bookmarks: %w", err)
	}
	p := filepath.Join(home, "Library", "Safari", "Bookmarks.plist")
	_, err = os.Stat(p)
	return p, err
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
//
// A bookmark directly under the root is emitted with an empty FolderPath.
func ParseSafari(r io.ReadSeeker) ([]ParsedBookmark, error) {
	var root safariNode
	decoder := plist.NewDecoder(r)
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("safari: decode plist: %w", err)
	}

	var out []ParsedBookmark
	for _, child := range root.Children {
		switch {
		case child.WebBookmarkIdentifier == "com.apple.ReadingList":
		case child.WebBookmarkType == "WebBookmarkTypeLeaf", child.WebBookmarkType == "WebBookmarkTypeProxy":
			// A bookmark saved at the top level belongs to no folder;
			// walkSafariNode skips proxies (History).
			walkSafariNode(child, nil, &out)
		default:
			stack := []string{safariRootLabel(child)}
			for _, c := range child.Children {
				walkSafariNode(c, stack, &out)
			}
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
		*out = append(*out, ParsedBookmark{
			URL:        canonicalURL(n.URLString),
			Title:      title,
			FolderPath: joinFolderPath(folderStack),
		})

	case "WebBookmarkTypeList":
		next := pushFolder(folderStack, n.Title)
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
