package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Source is somewhere bookmarks can be imported from: a Chrome profile,
// Firefox's default profile, Safari, or an exported HTML file. `curio up`
// offers the ones Discover finds, and an HTML file the user names.
type Source interface {
	// Name is how a menu names it: "Chrome: Person 1", "Firefox",
	// "Safari", "bookmarks.html".
	Name() string
	// Label is the source its bookmarks are saved under, the API's:
	// chrome, safari, firefox or html.
	Label() string
	// Spec is how `curio up --import` names it: chrome (the Default
	// profile), chrome:<profile directory>, safari, firefox or
	// html:<path>.
	Spec() string
	// Check says whether it can be read now, reading no more than it takes
	// to find out.
	Check(ctx context.Context) Availability
	// Parse reads its bookmarks; ErrEmpty when it has none.
	Parse(ctx context.Context) ([]ParsedBookmark, error)
}

// State is whether a source can be read.
type State int

const (
	// Available: it can be read.
	Available State = iota
	// NotInstalled: the browser, or the file, isn't there.
	NotInstalled
	// NeedsPermission: macOS withholds it from the terminal app curio
	// runs in, which Full Disk Access lifts: Safari's bookmarks, say.
	NeedsPermission
	// Unreadable: it is there and can't be read, for another reason.
	Unreadable
)

func (s State) String() string {
	switch s {
	case Available:
		return "available"
	case NotInstalled:
		return "not installed"
	case NeedsPermission:
		return "needs permission"
	case Unreadable:
		return "unreadable"
	default:
		return fmt.Sprintf("state(%d)", int(s))
	}
}

// Availability is whether a source can be read now, and why not.
type Availability struct {
	State State
	// Reason says why it can't be read; empty when it can.
	Reason string
}

// fullDiskAccess is what lifts a permission error on macOS.
const fullDiskAccess = "macOS lets an app read it only with Full Disk Access " +
	"(System Settings > Privacy & Security > Full Disk Access)"

// availabilityOf judges the error of looking at or opening a source's
// file: none is Available; a missing file NotInstalled, saying missing; a
// permission error NeedsPermission; anything else Unreadable.
func availabilityOf(err error, missing string) Availability {
	switch {
	case err == nil:
		return Availability{State: Available}
	case errors.Is(err, fs.ErrNotExist):
		return Availability{State: NotInstalled, Reason: missing}
	case errors.Is(err, fs.ErrPermission):
		return Availability{State: NeedsPermission, Reason: err.Error() + ": " + fullDiskAccess}
	default:
		return Availability{State: Unreadable, Reason: err.Error()}
	}
}

// Discover finds the sources on this machine: each Chrome profile with
// bookmarks, Firefox's default profile, and Safari. It never fails, and
// reads no bookmarks: a source it couldn't look for is Unreadable, and
// one that isn't there says so when checked.
func Discover() []Source {
	return append(chromeSources(), firefoxSource{}, Safari())
}

// chromeSources is one source per Chrome profile, named by its display
// name, and by its directory too when two share a name.
func chromeSources() []Source {
	profiles, err := DiscoverChromeProfiles()
	if err != nil {
		return []Source{unavailable{name: "Chrome", label: LabelChrome, spec: "chrome",
			av: Availability{State: Unreadable, Reason: err.Error()}}}
	}
	named := map[string]int{}
	for _, p := range profiles {
		named[p.Name]++
	}
	out := make([]Source, 0, len(profiles))
	for _, p := range profiles {
		name := "Chrome: " + p.Name
		if named[p.Name] > 1 {
			name += " (" + p.Dir + ")"
		}
		out = append(out, chromeSource{profile: p, name: name})
	}
	return out
}

// defaultChromeProfile is the profile `chrome` names without one.
const defaultChromeProfile = "Default"

// PickChromeProfile finds the profile want names: its directory exactly,
// then its display name, ignoring case.
func PickChromeProfile(profiles []ChromeProfile, want string) (ChromeProfile, bool) {
	for _, p := range profiles {
		if p.Dir == want {
			return p, true
		}
	}
	for _, p := range profiles {
		if strings.EqualFold(p.Name, want) {
			return p, true
		}
	}
	return ChromeProfile{}, false
}

// FindChrome is the source among sources of the Chrome profile want names
// (the Default profile when want is empty), matched as PickChromeProfile
// matches. With no Chrome profile among them it is the Chrome source that
// says why, if there is one, and otherwise an error saying Chrome isn't
// installed; a profile none matches is an error listing those found.
func FindChrome(sources []Source, want string) (Source, error) {
	if want == "" {
		want = defaultChromeProfile
	}
	var profiles []ChromeProfile
	byDir := map[string]Source{}
	var placeholder Source
	for _, s := range sources {
		switch c := s.(type) {
		case chromeSource:
			profiles = append(profiles, c.profile)
			byDir[c.profile.Dir] = s
		case unavailable:
			if c.label == LabelChrome {
				placeholder = s
			}
		}
	}
	if len(profiles) == 0 {
		if placeholder != nil {
			return placeholder, nil
		}
		return nil, errors.New("no Chrome profile with bookmarks was found: is Chrome installed?")
	}
	p, ok := PickChromeProfile(profiles, want)
	if !ok {
		found := make([]string, len(profiles))
		for i, p := range profiles {
			found[i] = fmt.Sprintf("%s (%s)", p.Dir, p.Name)
		}
		return nil, fmt.Errorf("no Chrome profile %q; found %s", want, strings.Join(found, ", "))
	}
	return byDir[p.Dir], nil
}

// chromeSource is one Chrome profile's Bookmarks file.
type chromeSource struct {
	profile ChromeProfile
	name    string
}

func (s chromeSource) Name() string { return s.name }
func (chromeSource) Label() string  { return LabelChrome }

func (s chromeSource) Spec() string {
	if s.profile.Dir == defaultChromeProfile {
		return "chrome"
	}
	return "chrome:" + s.profile.Dir
}

func (s chromeSource) Check(context.Context) Availability {
	_, err := os.Stat(s.profile.BookmarkFile)
	return availabilityOf(err, "the profile has no Bookmarks file")
}

func (s chromeSource) Parse(context.Context) ([]ParsedBookmark, error) {
	return parseFile(s.profile.BookmarkFile, openFile, func(r io.ReadSeeker) ([]ParsedBookmark, error) {
		return ParseChrome(r)
	})
}

// firefoxSource is the places.sqlite of Firefox's default profile.
type firefoxSource struct{}

func (firefoxSource) Name() string  { return "Firefox" }
func (firefoxSource) Label() string { return LabelFirefox }
func (firefoxSource) Spec() string  { return "firefox" }

func (firefoxSource) Check(context.Context) Availability {
	_, err := firefoxPlaces()
	return availabilityOf(err, "not installed")
}

func (firefoxSource) Parse(ctx context.Context) ([]ParsedBookmark, error) {
	path, err := firefoxPlaces()
	if err != nil {
		return nil, err
	}
	return ParseFirefox(ctx, path)
}

// opener opens a file to parse; os.Open, or a test's.
type opener func(path string) (io.ReadSeekCloser, error)

func openFile(path string) (io.ReadSeekCloser, error) { return os.Open(path) }

// Safari is Safari's Bookmarks.plist, which macOS lets the terminal app
// curio runs in read only with Full Disk Access.
func Safari() Source { return safariSource{open: openFile} }

// safariSource opens the plist through open, which tests stand a
// permission error in for.
type safariSource struct{ open opener }

func (safariSource) Name() string  { return "Safari" }
func (safariSource) Label() string { return LabelSafari }
func (safariSource) Spec() string  { return "safari" }

// Check looks at the plist and opens it: macOS may refuse either without
// Full Disk Access.
func (s safariSource) Check(context.Context) Availability {
	path, err := SafariPlist()
	if err == nil {
		var f io.ReadSeekCloser
		if f, err = s.open(path); err == nil {
			err = f.Close()
		}
	}
	return availabilityOf(err, "not installed")
}

func (s safariSource) Parse(context.Context) ([]ParsedBookmark, error) {
	path, err := SafariPlist()
	if err != nil {
		return nil, err
	}
	return parseFile(path, s.open, ParseSafari)
}

// HTMLFile is an exported bookmarks file (the Netscape format every
// browser exports) at path: "~" and "~/..." are the user's home, and a
// relative path is made absolute.
func HTMLFile(path string) Source {
	abs, err := expandPath(path)
	if err != nil {
		return htmlSource{path: path, err: err}
	}
	return htmlSource{path: abs}
}

// expandPath is path with a leading "~" made the user's home, made
// absolute.
func expandPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %s: %w", path, err)
		}
		path = filepath.Join(home, path[1:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("expand %s: %w", path, err)
	}
	return abs, nil
}

// htmlSource is an exported bookmarks file; err is why its path couldn't
// be made absolute.
type htmlSource struct {
	path string
	err  error
}

func (s htmlSource) Name() string { return filepath.Base(s.path) }
func (htmlSource) Label() string  { return LabelHTML }
func (s htmlSource) Spec() string { return "html:" + s.path }

func (s htmlSource) Check(context.Context) Availability {
	if s.err != nil {
		return Availability{State: Unreadable, Reason: s.err.Error()}
	}
	info, err := os.Stat(s.path)
	if err == nil && info.IsDir() {
		return Availability{State: Unreadable, Reason: s.path + " is a directory"}
	}
	return availabilityOf(err, "no file at "+s.path)
}

func (s htmlSource) Parse(context.Context) ([]ParsedBookmark, error) {
	if s.err != nil {
		return nil, s.err
	}
	return parseFile(s.path, openFile, func(r io.ReadSeeker) ([]ParsedBookmark, error) {
		return ParseHTML(r)
	})
}

// parseFile opens path with open and parses it. ErrEmpty stays
// recognizable; any other parse failure names the file.
func parseFile(path string, open opener, parse func(io.ReadSeeker) ([]ParsedBookmark, error)) (
	[]ParsedBookmark, error) {
	f, err := open(path)
	if err != nil {
		return nil, err // an *fs.PathError already names the file
	}
	defer f.Close()
	bms, err := parse(f)
	if err != nil && !errors.Is(err, ErrEmpty) {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return bms, err
}

// unavailable is a source that couldn't be looked for: Chrome whose
// profiles couldn't be listed.
type unavailable struct {
	name, label, spec string
	av                Availability
}

func (u unavailable) Name() string                       { return u.name }
func (u unavailable) Label() string                      { return u.label }
func (u unavailable) Spec() string                       { return u.spec }
func (u unavailable) Check(context.Context) Availability { return u.av }

func (u unavailable) Parse(context.Context) ([]ParsedBookmark, error) {
	return nil, fmt.Errorf("%s: %s", u.name, u.av.Reason)
}
