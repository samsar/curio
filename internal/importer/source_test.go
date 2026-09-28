package importer

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chromeFixture writes Chrome's user-data directory with two profiles,
// Default ("Person 1") and Profile 1 ("Work"), each with a bookmark, and
// points CURIO_CHROME_DIR at it.
func chromeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CURIO_CHROME_DIR", root)
	for dir, url := range map[string]string{"Default": "https://example.com/personal", "Profile 1": "https://example.com/work"} {
		mustMkdir(t, filepath.Join(root, dir))
		mustWrite(t, filepath.Join(root, dir, "Bookmarks"), `{"roots":{"bookmark_bar":{"type":"folder","children":[`+
			`{"type":"url","name":"A page","url":"`+url+`"}]}}}`)
	}
	mustWrite(t, filepath.Join(root, "Local State"),
		`{"profile":{"info_cache":{"Default":{"name":"Person 1"},"Profile 1":{"name":"Work"}}}}`)
	return root
}

// noBrowsers points every browser's directory at an empty one.
func noBrowsers(t *testing.T) {
	t.Helper()
	empty := t.TempDir()
	for _, v := range []string{"CURIO_CHROME_DIR", "CURIO_SAFARI_DIR", "CURIO_FIREFOX_DIR"} {
		t.Setenv(v, empty)
	}
}

func byName(sources []Source) map[string]Source {
	out := map[string]Source{}
	for _, s := range sources {
		out[s.Name()] = s
	}
	return out
}

// TestDiscover_Chrome: one source per profile, named from Local State,
// each parsing its own bookmarks, and specs that pick them again.
func TestDiscover_Chrome(t *testing.T) {
	noBrowsers(t)
	chromeFixture(t)
	ctx := context.Background()
	found := byName(Discover())
	require.Contains(t, found, "Chrome: Person 1")
	require.Contains(t, found, "Chrome: Work")
	for name, want := range map[string]string{"Chrome: Person 1": "https://example.com/personal",
		"Chrome: Work": "https://example.com/work"} {
		src := found[name]
		assert.Equal(t, LabelChrome, src.Label())
		assert.Equal(t, Available, src.Check(ctx).State, name)
		bms, err := src.Parse(ctx)
		require.NoError(t, err, name)
		require.Len(t, bms, 1)
		assert.Equal(t, want, bms[0].URL)
	}
	assert.Equal(t, "chrome", found["Chrome: Person 1"].Spec(), "the Default profile needs no name")
	assert.Equal(t, "chrome:Profile 1", found["Chrome: Work"].Spec())
}

// TestDiscover_ChromeNamesCollide: two profiles with one display name are
// told apart by their directories.
func TestDiscover_ChromeNamesCollide(t *testing.T) {
	noBrowsers(t)
	root := chromeFixture(t)
	mustWrite(t, filepath.Join(root, "Local State"),
		`{"profile":{"info_cache":{"Default":{"name":"Me"},"Profile 1":{"name":"Me"}}}}`)
	found := byName(Discover())
	assert.Contains(t, found, "Chrome: Me (Default)")
	assert.Contains(t, found, "Chrome: Me (Profile 1)")
}

func TestFindChrome(t *testing.T) {
	noBrowsers(t)
	chromeFixture(t)
	sources := Discover()
	for want, name := range map[string]string{"": "Chrome: Person 1", "Default": "Chrome: Person 1",
		"Profile 1": "Chrome: Work", "work": "Chrome: Work"} {
		src, err := FindChrome(sources, want)
		require.NoError(t, err, want)
		assert.Equal(t, name, src.Name(), want)
	}
	_, err := FindChrome(sources, "Nope")
	require.EqualError(t, err, `no Chrome profile "Nope"; found Default (Person 1), Profile 1 (Work)`)

	_, err = FindChrome([]Source{Safari()}, "")
	require.ErrorContains(t, err, "is Chrome installed?")
}

func TestPickChromeProfile(t *testing.T) {
	profiles := []ChromeProfile{
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
		got, ok := PickChromeProfile(profiles, want)
		require.True(t, ok, want)
		assert.Equal(t, dir, got.Dir, want)
	}
	_, ok := PickChromeProfile(profiles, "default")
	assert.False(t, ok, "directories match exactly")
	_, ok = PickChromeProfile(profiles, "Personal")
	assert.False(t, ok)
}

// TestDiscover_Firefox: the install default's places.sqlite, found through
// profiles.ini.
func TestDiscover_Firefox(t *testing.T) {
	noBrowsers(t)
	places := buildFixturePlaces(t)
	root := filepath.Dir(places)
	mustWrite(t, filepath.Join(root, "profiles.ini"), "[Install1]\nDefault=.\n")
	t.Setenv("CURIO_FIREFOX_DIR", root)
	ctx := context.Background()

	src := byName(Discover())["Firefox"]
	require.NotNil(t, src)
	assert.Equal(t, LabelFirefox, src.Label())
	assert.Equal(t, "firefox", src.Spec())
	assert.Equal(t, Availability{State: Available}, src.Check(ctx))
	bms, err := src.Parse(ctx)
	require.NoError(t, err)
	assert.Len(t, bms, 3)
}

// safariFixture points CURIO_SAFARI_DIR at a directory holding a
// Bookmarks.plist, and returns it.
func safariFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Bookmarks.plist"), sampleSafariPlist)
	t.Setenv("CURIO_SAFARI_DIR", dir)
	return dir
}

func TestSafari_Available(t *testing.T) {
	noBrowsers(t)
	safariFixture(t)
	ctx := context.Background()
	src := byName(Discover())["Safari"]
	require.NotNil(t, src)
	assert.Equal(t, LabelSafari, src.Label())
	assert.Equal(t, "safari", src.Spec())
	assert.Equal(t, Availability{State: Available}, src.Check(ctx))
	bms, err := src.Parse(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, bms)
}

// TestSafari_NeedsPermission: macOS refusing the plist, at open or already
// at stat, is a permission to ask for, naming Full Disk Access, never a
// Safari that isn't there.
func TestSafari_NeedsPermission(t *testing.T) {
	noBrowsers(t)
	ctx := context.Background()

	t.Run("open", func(t *testing.T) {
		safariFixture(t)
		src := safariSource{open: func(path string) (io.ReadSeekCloser, error) {
			return nil, &fs.PathError{Op: "open", Path: path, Err: syscall.EPERM}
		}}
		av := src.Check(ctx)
		assert.Equal(t, NeedsPermission, av.State)
		assert.Contains(t, av.Reason, "Full Disk Access")
		_, err := src.Parse(ctx)
		require.ErrorIs(t, err, fs.ErrPermission)
	})
	t.Run("stat", func(t *testing.T) {
		dir := safariFixture(t)
		require.NoError(t, os.Chmod(dir, 0))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		av := Safari().Check(ctx)
		assert.Equal(t, NeedsPermission, av.State, av.Reason)
		assert.Contains(t, av.Reason, "Full Disk Access")
	})
}

// TestHTMLFile: "~/" is the user's home, a relative path is made
// absolute, and the file parses.
func TestHTMLFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWrite(t, filepath.Join(home, "b.html"), `<DL><DT><A HREF="https://example.com/a">A</A></DL>`)
	ctx := context.Background()

	src := HTMLFile("~/b.html")
	assert.Equal(t, "b.html", src.Name())
	assert.Equal(t, LabelHTML, src.Label())
	assert.Equal(t, "html:"+filepath.Join(home, "b.html"), src.Spec())
	assert.Equal(t, Availability{State: Available}, src.Check(ctx))
	bms, err := src.Parse(ctx)
	require.NoError(t, err)
	require.Len(t, bms, 1)
	assert.Equal(t, "https://example.com/a", bms[0].URL)

	t.Chdir(home)
	assert.Equal(t, "html:"+filepath.Join(home, "b.html"), HTMLFile("b.html").Spec(), "made absolute")
	assert.Equal(t, Unreadable, HTMLFile(home).Check(ctx).State, "a directory")
}

// TestNotInstalled: a browser that isn't there, and a file that isn't, are
// not installed, never unreadable.
func TestNotInstalled(t *testing.T) {
	noBrowsers(t)
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "gone")
	t.Setenv("CURIO_FIREFOX_DIR", missing)
	t.Setenv("CURIO_SAFARI_DIR", missing)
	for _, src := range []Source{firefoxSource{}, Safari(), HTMLFile(filepath.Join(missing, "b.html"))} {
		assert.Equal(t, NotInstalled, src.Check(ctx).State, src.Name())
	}
	assert.Equal(t, "no file at "+filepath.Join(missing, "b.html"),
		HTMLFile(filepath.Join(missing, "b.html")).Check(ctx).Reason)
}

// TestDiscover_NothingUnderTheTestGuards: with the browsers' directories
// pointed at an empty one, as every TestMain that builds setup.Deps does,
// nothing is available: no test can read real bookmarks.
func TestDiscover_NothingUnderTheTestGuards(t *testing.T) {
	noBrowsers(t)
	for _, src := range Discover() {
		assert.NotEqual(t, Available, src.Check(context.Background()).State, src.Name())
	}
}

// TestParse_Empty: a source with no bookmarks says so with ErrEmpty.
func TestParse_Empty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.html")
	mustWrite(t, path, "<DL></DL>")
	_, err := HTMLFile(path).Parse(context.Background())
	require.ErrorIs(t, err, ErrEmpty)
}
