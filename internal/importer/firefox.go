package importer

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3" // sqlite3 driver, for reading places.sqlite

	"github.com/samsar/curio/internal/urlutil"
)

// Firefox stores bookmarks in places.sqlite (a SQLite DB), not a flat file.
// moz_bookmarks holds the tree (type 1 = bookmark, 2 = folder, 3 = separator)
// with parent pointers; moz_places holds the URLs. Root containers are
// identified by stable GUIDs rather than IDs.
const (
	ffTypeBookmark = 1 // moz_bookmarks.type for a URL (2 = folder, 3 = separator)

	ffGUIDRoot    = "root________"
	ffGUIDMenu    = "menu________"
	ffGUIDToolbar = "toolbar_____"
	ffGUIDTags    = "tags________" // tag containers, NOT folders — skipped
	ffGUIDUnfiled = "unfiled_____"
	ffGUIDMobile  = "mobile______"
)

// FirefoxBookmarksPath returns the path to the default profile's
// places.sqlite, discovered via profiles.ini. Returns "" if Firefox isn't
// installed or the file doesn't exist. Honors CURIO_FIREFOX_DIR for tests.
func FirefoxBookmarksPath() string {
	root := firefoxRoot()
	if root == "" {
		return ""
	}
	rel := firefoxDefaultProfileRel(root)
	if rel == "" {
		return ""
	}
	profDir := rel
	if !filepath.IsAbs(profDir) {
		profDir = filepath.Join(root, rel)
	}
	p := filepath.Join(profDir, "places.sqlite")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func firefoxRoot() string {
	if v := os.Getenv("CURIO_FIREFOX_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Firefox")
	case "linux":
		return filepath.Join(home, ".mozilla", "firefox")
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "Mozilla", "Firefox")
		}
	}
	return ""
}

// firefoxDefaultProfileRel reads profiles.ini and returns the relative (or
// absolute) path of the profile to use. Modern Firefox is per-install, so we
// prefer the [Install*] section's Default (what the running browser uses),
// then fall back to a [Profile*] marked Default=1, then the first profile.
func firefoxDefaultProfileRel(root string) string {
	ini := parseINI(filepath.Join(root, "profiles.ini"))
	sections := make([]string, 0, len(ini))
	for s := range ini {
		sections = append(sections, s)
	}
	sort.Strings(sections) // deterministic

	for _, s := range sections {
		if strings.HasPrefix(s, "Install") {
			if d := ini[s]["Default"]; d != "" {
				return d
			}
		}
	}
	for _, s := range sections {
		if strings.HasPrefix(s, "Profile") && ini[s]["Default"] == "1" {
			if p := ini[s]["Path"]; p != "" {
				return p
			}
		}
	}
	for _, s := range sections {
		if strings.HasPrefix(s, "Profile") {
			if p := ini[s]["Path"]; p != "" {
				return p
			}
		}
	}
	return ""
}

// parseINI parses a flat INI file into section→key→value. Returns an empty
// map on any read error (caller treats that as "no Firefox").
func parseINI(path string) map[string]map[string]string {
	out := map[string]map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	section := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			out[section] = map[string]string{}
			continue
		}
		if section == "" {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[section][strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// ParseFirefox reads a Firefox places.sqlite and returns its bookmarks.
//
// places.sqlite is usually open and in WAL mode while Firefox runs, so we
// copy it (plus its -wal/-shm sidecars, which carry un-checkpointed writes
// like a bookmark added seconds ago) to a temp file and read that. Tag
// entries (under the Tags root) and separators are skipped.
func ParseFirefox(placesPath string) ([]ParsedBookmark, error) {
	tmp, cleanup, err := copyDBForRead(placesPath)
	if err != nil {
		return nil, fmt.Errorf("firefox: copy places.sqlite: %w", err)
	}
	defer cleanup()

	db, err := sql.Open("sqlite3", "file:"+tmp+"?_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("firefox: open places.sqlite: %w", err)
	}
	defer db.Close() //nolint:errcheck

	rows, err := db.Query(`
		SELECT b.id, b.type, COALESCE(b.parent, 0), COALESCE(b.title, ''),
		       COALESCE(b.dateAdded, 0), COALESCE(b.guid, ''), COALESCE(p.url, '')
		FROM moz_bookmarks b
		LEFT JOIN moz_places p ON p.id = b.fk`)
	if err != nil {
		return nil, fmt.Errorf("firefox: query bookmarks: %w", err)
	}
	defer rows.Close()

	byID := map[int64]*ffNode{}
	var order []*ffNode
	var tagsRootID int64
	for rows.Next() {
		var n ffNode
		if err := rows.Scan(&n.id, &n.typ, &n.parent, &n.title, &n.dateMicros, &n.guid, &n.url); err != nil {
			return nil, fmt.Errorf("firefox: scan: %w", err)
		}
		node := n
		byID[node.id] = &node
		order = append(order, &node)
		if node.guid == ffGUIDTags {
			tagsRootID = node.id
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("firefox: rows: %w", err)
	}

	var out []ParsedBookmark
	for _, n := range order {
		if n.typ != ffTypeBookmark || n.url == "" {
			continue
		}
		path, underTags := firefoxFolderPath(byID, n.parent, tagsRootID)
		if underTags {
			continue
		}
		bm := ParsedBookmark{
			URL:        n.url,
			Title:      strings.TrimSpace(n.title),
			FolderPath: path,
			SavedAt:    firefoxMicrosToTime(n.dateMicros),
		}
		if norm, err := urlutil.Normalize(n.url); err == nil {
			bm.URL = norm
		}
		out = append(out, bm)
	}
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

type ffNode struct {
	id         int64
	typ        int
	parent     int64
	title      string
	url        string
	guid       string
	dateMicros int64
}

// firefoxFolderPath walks parent pointers up to a root container, building
// the folder path. Returns underTags=true if any ancestor is the Tags root,
// so the caller can skip tag pseudo-bookmarks.
func firefoxFolderPath(byID map[int64]*ffNode, parentID, tagsRootID int64) (string, bool) {
	var parts []string
	seen := map[int64]bool{} // guard against cycles in a corrupt DB
	for cur := parentID; cur != 0; {
		if seen[cur] {
			break
		}
		seen[cur] = true
		node, ok := byID[cur]
		if !ok {
			break
		}
		if (tagsRootID != 0 && cur == tagsRootID) || node.guid == ffGUIDTags {
			return "", true
		}
		if node.guid == ffGUIDRoot {
			break
		}
		if label, isRoot := firefoxRootLabel(node.guid); isRoot {
			parts = append([]string{label}, parts...)
			break
		}
		if title := strings.TrimSpace(node.title); title != "" {
			parts = append([]string{title}, parts...)
		}
		cur = node.parent
	}
	return joinFolderPath(parts), false
}

func firefoxRootLabel(guid string) (string, bool) {
	switch guid {
	case ffGUIDMenu:
		return "Bookmarks Menu", true
	case ffGUIDToolbar:
		return "Bookmarks Toolbar", true
	case ffGUIDUnfiled:
		return "Other Bookmarks", true
	case ffGUIDMobile:
		return "Mobile Bookmarks", true
	}
	return "", false
}

// firefoxMicrosToTime converts Firefox's dateAdded (microseconds since the
// Unix epoch) to a time.Time. Returns zero for non-positive values.
func firefoxMicrosToTime(micros int64) time.Time {
	if micros <= 0 {
		return time.Time{}
	}
	return time.Unix(micros/1_000_000, (micros%1_000_000)*1000).UTC()
}

// copyDBForRead copies a SQLite DB (and its -wal/-shm sidecars) into a temp
// directory so we can read it while another process holds it open. Returns
// the copy's path and a cleanup func.
func copyDBForRead(src string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "curio-firefox-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dst := filepath.Join(dir, "places.sqlite")
	if err := copyFile(src, dst); err != nil {
		cleanup()
		return "", nil, err
	}
	// Sidecars are best-effort: absence is fine (DB not in WAL mode, or
	// already checkpointed).
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = copyFile(src+suffix, dst+suffix)
	}
	return dst, cleanup, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
