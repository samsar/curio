package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samsar/curio/internal/urlutil"
)

// ChromeProfile describes one discovered Chrome profile, suitable for
// presenting to the user via `curio import chrome --list-profiles`.
type ChromeProfile struct {
	Dir         string // e.g. "Default", "Profile 1"
	Name        string // display name from Local State; falls back to Dir
	BookmarkFile string // absolute path
}

// DiscoverChromeProfiles enumerates Chrome profiles on the current OS.
// Returns an empty slice (not an error) when Chrome isn't installed —
// callers decide whether absent profiles are an error or just "use a
// different importer."
//
// The user-data directory layout is documented at
// https://chromium.org/user-experience/user-data-directory/. We honor a
// CURIO_CHROME_DIR env override so tests (and the CLI's --file flag)
// don't need to touch the real user dir.
func DiscoverChromeProfiles() ([]ChromeProfile, error) {
	root, err := chromeUserDataDir()
	if err != nil {
		return nil, err
	}
	if root == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	displayNames := readLocalStateProfileNames(filepath.Join(root, "Local State"))

	var profiles []ChromeProfile
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read chrome dir %q: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := e.Name()
		if dir != "Default" && !strings.HasPrefix(dir, "Profile ") {
			continue
		}
		bm := filepath.Join(root, dir, "Bookmarks")
		if _, err := os.Stat(bm); err != nil {
			// Profile exists but has no bookmarks file yet; skip.
			continue
		}
		name := displayNames[dir]
		if name == "" {
			name = dir
		}
		profiles = append(profiles, ChromeProfile{Dir: dir, Name: name, BookmarkFile: bm})
	}
	// Deterministic order: Default first, then Profile N by N, then anything else alpha.
	sort.Slice(profiles, func(i, j int) bool { return chromeProfileLess(profiles[i].Dir, profiles[j].Dir) })
	return profiles, nil
}

func chromeProfileLess(a, b string) bool {
	if a == b {
		return false
	}
	if a == "Default" {
		return true
	}
	if b == "Default" {
		return false
	}
	return a < b
}

// chromeUserDataDir returns the platform-appropriate Chrome user-data dir.
// Honors CURIO_CHROME_DIR for test injection.
func chromeUserDataDir() (string, error) {
	if v := os.Getenv("CURIO_CHROME_DIR"); v != "" {
		return v, nil
	}
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome"), nil
	case "linux":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "google-chrome"), nil
	case "windows":
		if appdata := os.Getenv("LOCALAPPDATA"); appdata != "" {
			return filepath.Join(appdata, "Google", "Chrome", "User Data"), nil
		}
	}
	return "", nil
}

// readLocalStateProfileNames extracts dir→display-name mappings from
// Chrome's Local State JSON. Returns an empty map on any error — the
// caller falls back to dir names, which is always safe.
func readLocalStateProfileNames(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var parsed struct {
		Profile struct {
			InfoCache map[string]struct {
				Name string `json:"name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return out
	}
	for dir, info := range parsed.Profile.InfoCache {
		out[dir] = info.Name
	}
	return out
}

// ParseChrome reads a Chrome `Bookmarks` JSON file (or any io.Reader with
// that shape) and returns ParsedBookmarks across the three roots
// (`bookmark_bar`, `other`, `synced`). The root name is prepended to the
// folder path so you can tell where each came from.
func ParseChrome(r io.Reader) ([]ParsedBookmark, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("chrome: read: %w", err)
	}
	var doc struct {
		Roots map[string]chromeNode `json:"roots"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("chrome: parse json: %w", err)
	}
	if len(doc.Roots) == 0 {
		return nil, ErrEmpty
	}

	// Deterministic root order for readable output and stable tests.
	rootNames := make([]string, 0, len(doc.Roots))
	for k := range doc.Roots {
		rootNames = append(rootNames, k)
	}
	sort.Strings(rootNames)

	var out []ParsedBookmark
	for _, name := range rootNames {
		node := doc.Roots[name]
		label := chromeRootLabel(name)
		// Walk the root's children rather than the root itself, so we
		// don't double-stack the root's own `name` field (which is the
		// localized "Bookmarks bar" / "Other bookmarks" / etc.). The
		// label we choose above is what we want at the top of the path.
		for _, c := range node.Children {
			walkChromeNode(c, []string{label}, &out)
		}
	}
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

func chromeRootLabel(rootKey string) string {
	switch rootKey {
	case "bookmark_bar":
		return "Bookmarks Bar"
	case "other":
		return "Other Bookmarks"
	case "synced":
		return "Mobile Bookmarks"
	default:
		return rootKey
	}
}

type chromeNode struct {
	Type      string        `json:"type"`
	Name      string        `json:"name"`
	URL       string        `json:"url,omitempty"`
	DateAdded string        `json:"date_added,omitempty"`
	Children  []chromeNode  `json:"children,omitempty"`
}

func walkChromeNode(n chromeNode, folderStack []string, out *[]ParsedBookmark) {
	switch n.Type {
	case "url":
		bm := ParsedBookmark{
			URL:        n.URL,
			Title:      strings.TrimSpace(n.Name),
			FolderPath: joinFolderPath(folderStack),
			SavedAt:    chromeMicrosToTime(n.DateAdded),
		}
		if norm, err := urlutil.Normalize(n.URL); err == nil {
			bm.URL = norm
		}
		*out = append(*out, bm)
	case "folder":
		next := folderStack
		if name := strings.TrimSpace(n.Name); name != "" {
			next = append(append([]string{}, folderStack...), name)
		}
		for _, c := range n.Children {
			walkChromeNode(c, next, out)
		}
	default:
		// Unknown node types (Chrome occasionally introduces new ones);
		// recurse into children just in case.
		for _, c := range n.Children {
			walkChromeNode(c, folderStack, out)
		}
	}
}

// chromeMicrosToTime converts Chrome's "microseconds since 1601-01-01 UTC"
// to a Go time.Time. Returns zero time for "" or "0".
//
// Constant: 11644473600000000 microseconds = 1970-01-01 in Chrome's epoch.
func chromeMicrosToTime(s string) time.Time {
	if s == "" || s == "0" {
		return time.Time{}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return time.Time{}
	}
	const epochOffset int64 = 11644473600000000
	unixMicros := v - epochOffset
	if unixMicros < 0 {
		return time.Time{}
	}
	return time.Unix(unixMicros/1_000_000, (unixMicros%1_000_000)*1000).UTC()
}
