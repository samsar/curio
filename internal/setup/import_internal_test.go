package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/importer"
)

// withheld is a source macOS withholds: it can be named, and read never.
type withheld string

func (s withheld) Name() string { return string(s) }
func (withheld) Label() string  { return importer.LabelSafari }
func (s withheld) Spec() string { return strings.ToLower(string(s)) }
func (withheld) Check(context.Context) importer.Availability {
	return importer.Availability{State: importer.NeedsPermission, Reason: "operation not permitted"}
}
func (withheld) Parse(context.Context) ([]importer.ParsedBookmark, error) {
	return nil, importer.ErrEmpty
}

// refusingInstaller fails the test on any command it is asked to run.
type refusingInstaller struct{ t *testing.T }

func (i refusingInstaller) Detect(context.Context, string, string) (Detection, error) {
	i.t.Error("detected an install")
	return Detection{}, errors.New("no installs here")
}

func (i refusingInstaller) Run(_ context.Context, _ UI, argv []string) error {
	i.t.Errorf("ran %v", argv)
	return errors.New("no commands here")
}

// TestMenu: the menu offers the readable sources and those that need
// permission, each counted in new bookmarks, with the pages to fetch when
// fewer; the default is the one with the most new bookmarks, not pages.
func TestMenu(t *testing.T) {
	available := importer.Availability{State: importer.Available}
	counted := func(name string, created int, pages ...string) sourceCount {
		return sourceCount{src: withheld(name), av: available,
			count: importer.Count{Known: true, Created: created, NewURLs: pages}}
	}
	survey := []sourceCount{
		counted("Firefox", 2, "https://example.org/x", "https://example.org/y"),
		counted("Chrome: Work", 3, "https://example.com/a"),
		{src: withheld("Chrome: Old"), av: available,
			count: importer.Count{Candidates: importer.Candidates{URLs: []string{"https://example.net/z"}}}},
		{src: withheld("Safari"), av: withheld("Safari").Check(t.Context())},
		{src: withheld("Chrome: Empty"), av: available, err: importer.ErrEmpty},
		{src: withheld("Brave"), av: importer.Availability{State: importer.NotInstalled, Reason: "not installed"}},
	}
	options, choices, def := menu(survey)
	assert.Equal(t, []string{
		"Firefox: 2 new",
		"Chrome: Work: 3 new (1 page to fetch)",
		"Chrome: Old: 1 bookmark",
		"Safari: needs Full Disk Access",
		"An exported bookmarks file (HTML)…",
		"Skip for now",
	}, options)
	assert.Equal(t, 1, def, "Chrome: Work saves the most bookmarks, though Firefox fetches more pages")
	assert.True(t, choices[4].html)
	assert.True(t, choices[5].skip)
}

// TestAllowed_NobodyToAsk: Apply's guard, for a source withheld only after
// the plan found it readable: in a run that can't walk the user through
// Full Disk Access (--yes here) it fails at once with the remedy, asking
// and opening nothing.
func TestAllowed_NobodyToAsk(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	var out bytes.Buffer
	ui := yesUI{newLineUI(strings.NewReader(""), &out)}
	w := &world{opts: Options{Yes: true}, deps: Deps{UI: ui, Installer: refusingInstaller{t}}}
	err := w.allowed(t.Context(), ui, withheld("Safari"))
	require.EqualError(t, err, "Safari: macOS doesn't let curio read it without Full Disk Access, which only you "+
		"can turn on, so --yes can't answer for it (give Terminal Full Disk Access in System Settings > Privacy & "+
		"Security > Full Disk Access, quit and reopen it, then run `curio up --import safari` again)")
	assert.Empty(t, out.String(), "nothing said, asked or run")
}

// TestTypedPath: a path typed at a prompt is taken as the shell takes one
// word, as a file dragged into the terminal arrives.
func TestTypedPath(t *testing.T) {
	cases := []struct{ typed, want string }{
		{`/Users/x/Downloads/Safari\ Bookmarks.html `, "/Users/x/Downloads/Safari Bookmarks.html"},
		{`'/Users/x/My Bookmarks.html'`, "/Users/x/My Bookmarks.html"},
		{`"/Users/x/My Bookmarks.html"`, "/Users/x/My Bookmarks.html"},
		{`/Users/x/it\'s.html`, "/Users/x/it's.html"},
		{`/Users/x/back\\slash.html`, `/Users/x/back\slash.html`},
		{`~/bookmarks.html`, "~/bookmarks.html"},
		{`'unbalanced.html`, "'unbalanced.html"},
		{"  ", ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, typedPath(tc.typed), tc.typed)
	}
}

// TestAskHTMLFile_ReadsAgain: a file named again is read again, so one
// fixed after its first try is imported.
func TestAskHTMLFile_ReadsAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bookmarks.html")
	require.NoError(t, os.WriteFile(path, []byte("<html></html>"), 0o600))
	var out bytes.Buffer
	ui := newLineUI(strings.NewReader(path+"\n"+path+"\n"), &out)
	w := &world{imp: newImportState(nil)}

	_, ok, err := w.askHTMLFile(t.Context(), ui)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Contains(t, out.String(), "bookmarks.html: "+importer.ErrEmpty.Error())

	require.NoError(t, os.WriteFile(path, []byte(`<DL><DT><A HREF="https://example.com/a">A</A></DL>`), 0o600))
	src, ok, err := w.askHTMLFile(t.Context(), ui)
	require.NoError(t, err)
	assert.True(t, ok, out.String())
	assert.Equal(t, "html:"+path, src.Spec())
}
