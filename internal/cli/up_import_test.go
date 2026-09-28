package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/importer"
	"github.com/samsar/curio/internal/setup/setuptest"
	"github.com/samsar/curio/internal/store"
)

// htmlFixture is the exported bookmarks file the import tests read: two
// pages, and a bookmarklet.
var htmlFixture = filepath.Join("testdata", "bookmarks.html")

// withSources makes the world's sources srcs, counting how often they are
// looked for.
func (w *world) withSources(srcs ...importer.Source) *atomic.Int32 {
	var looked atomic.Int32
	w.deps.sources = func() []importer.Source {
		looked.Add(1)
		return srcs
	}
	return &looked
}

// paceAnswers answer the pace and keep-awake questions: at full speed,
// keep-awake as it is.
func paceAnswers() []setuptest.Answer {
	return []setuptest.Answer{setuptest.Pick(0).About("When should curio work through them?"),
		setuptest.Pick(0).About("Keep the Mac awake")}
}

// answers are first, then the pace answers.
func answers(first ...setuptest.Answer) []setuptest.Answer { return append(first, paceAnswers()...) }

// chromeWork is a Chrome profile with three bookmarks.
func chromeWork() *setuptest.Source {
	return setuptest.NewSource("Chrome: Work", importer.LabelChrome, "chrome:Profile 1",
		"https://example.com/a", "https://example.com/b", "https://example.com/shared")
}

// TestUp_EmptyLibraryWithoutATerminal: with no terminal to ask on, an
// empty library is a warning naming the --import values to pass, and
// nothing else changes.
func TestUp_EmptyLibraryWithoutATerminal(t *testing.T) {
	w := upWorld(t)
	w.withSources(chromeWork())
	code, stdout, stderr := w.exit(t, "up")
	assert.Equal(t, 0, code, stderr)
	assert.True(t, strings.HasPrefix(stdout, "Nothing to do: curio is up.\n"), stdout)
	assert.Contains(t, stdout, "warning: import: the library is empty, and without a terminal curio up imports "+
		"only what `curio up --import <source>` names\n")
	assert.Contains(t, stdout, "  --import 'chrome:Profile 1'  (Chrome: Work: 3 new)\n")
	assert.Contains(t, stdout, "  --import html:<file>  (an exported bookmarks file)\n")
	assert.Zero(t, count(t, w.srv, `SELECT count(*) FROM bookmarks`))

	code, _, stderr = w.exit(t, "up", "--dry-run")
	assert.Equal(t, 0, code, stderr)
	assert.Empty(t, w.ollama.Embeds(), "a dry run measures indexing only for notes it shows")
}

// TestUp_ImportMenu: the menu lists each source with how many of its
// bookmarks are new, and the pages to fetch when fewer, and the one chosen
// is imported: as many as the menu said.
func TestUp_ImportMenu(t *testing.T) {
	w := upWorld(t)
	w.srv.AddDocument(t, "https://example.com/shared", store.DocStateFetched) // in the library, with no bookmark
	w.withSources(
		setuptest.NewSource("Firefox", importer.LabelFirefox, "firefox", "https://example.org/x"),
		chromeWork(),
		setuptest.NewSource("Safari", importer.LabelSafari, "safari"), // no bookmarks: not offered
	)
	ui := setuptest.NewUI(t, answers(setuptest.Pick(1).About("Import which bookmarks?\nFirefox: 1 new\n"+
		"Chrome: Work: 3 new (2 pages to fetch)\nAn exported bookmarks file (HTML)…\nSkip for now"))...)
	w.scripted(ui)

	code, stdout, stderr := w.exit(t, "up")
	require.Equal(t, 0, code, "%s\n%v", stderr, ui.Events())
	assert.Zero(t, ui.Unanswered())
	assert.Equal(t, 3, count(t, w.srv, `SELECT count(*) FROM bookmarks WHERE source = 'chrome'`))
	assert.Equal(t, 2, count(t, w.srv, `SELECT count(*) FROM jobs WHERE kind = 'fetch'`), "the 2 new pages")
	assert.Contains(t, stdout, "Import started. Check back after ")
	assert.Contains(t, stdout, "  3 new bookmarks from Chrome: Work, 2 pages to fetch and index now, at full speed\n")
}

// TestUp_ImportMenuSkip: skipping imports nothing, and isn't a decline.
func TestUp_ImportMenuSkip(t *testing.T) {
	w := upWorld(t)
	w.withSources(chromeWork())
	ui := setuptest.NewUI(t, setuptest.Pick(2).About("Skip for now"))
	w.scripted(ui)
	code, stdout, stderr := w.exit(t, "up")
	require.Equal(t, 0, code, stderr)
	assert.Zero(t, count(t, w.srv, `SELECT count(*) FROM bookmarks`))
	assert.Contains(t, stdout, "warning: import: skipped: nothing imported\n")
	assert.Contains(t, stdout, "Next:\n  curio import chrome")
}

// TestUp_ImportMenuHTMLFile: a path that won't do says why and shows the
// menu again; a good one is imported.
func TestUp_ImportMenuHTMLFile(t *testing.T) {
	w := upWorld(t)
	w.withSources()
	missing := filepath.Join(t.TempDir(), "gone.html")
	ui := setuptest.NewUI(t, answers(
		setuptest.Pick(0).About("An exported bookmarks file (HTML)…"), setuptest.Type(missing).About("Path"),
		setuptest.Pick(0).About("Import which bookmarks?"), setuptest.Type(htmlFixture).About("Path"))...)
	w.scripted(ui)
	code, _, stderr := w.exit(t, "up")
	require.Equal(t, 0, code, "%s\n%v", stderr, ui.Events())
	assert.True(t, ui.Said("gone.html: no file at "+missing))
	assert.Equal(t, 2, count(t, w.srv, `SELECT count(*) FROM bookmarks WHERE source = 'html'`))
}

// TestUp_ImportHandOff: --import with a terminal asks once, imports, and
// ends with the hand-off; the next run, which has bookmarks, has nothing
// to do, and neither has the same --import again.
func TestUp_ImportHandOff(t *testing.T) {
	w := upWorld(t)
	w.withSources()
	ui := setuptest.NewUI(t, answers(setuptest.Yes().About("Import 2 new bookmarks from bookmarks.html?"))...)
	w.scripted(ui)
	code, _, stderr := w.exit(t, "up", "--import", htmlFixture)
	require.Equal(t, 1, code, "an --import value is a kind first")
	assert.Contains(t, stderr, "--import "+htmlFixture+": import from chrome")

	code, stdout, stderr := w.exit(t, "up", "--import", "html:"+htmlFixture)
	require.Equal(t, 0, code, "%s\n%v", stderr, ui.Events())
	lines := strings.Split(stdout, "\n")
	start := slices.Index(lines, "curio is up.")
	require.NotEqual(t, -1, start, stdout)
	handOff := strings.Join(lines[slices.IndexFunc(lines, func(l string) bool {
		return strings.HasPrefix(l, "Import started. Check back after ")
	}):], "\n")
	assert.Regexp(t, `^Import started\. Check back after (tomorrow )?\d\d:\d\d\.\n`+
		`  2 new bookmarks from bookmarks\.html, 2 pages to fetch and index now, at full speed\n`+
		`\nMeanwhile:\n`+
		`  curio status --follow +follow the import until it is done\n`+
		`  curio pause \| resume +stop starting new work, and start again\n`+
		`  curio throttle gentle +fewer jobs at once, to keep the Mac cool\n`+
		`  curio search "\.\.\." +search your library, as it grows\n`+
		`  claude mcp add curio -- .+ +let Claude Code search your library \(MCP\)\n$`, handOff)
	assert.Equal(t, 2, count(t, w.srv, `SELECT count(*) FROM bookmarks`))

	w.deps.newUI = testDeps(t).newUI
	code, stdout, _ = w.exit(t, "up")
	assert.Equal(t, 0, code)
	assert.True(t, strings.HasPrefix(stdout, "Nothing to do: curio is up.\n"), stdout)
	code, stdout, _ = w.exit(t, "up", "--yes", "--import", "html:"+htmlFixture)
	assert.Equal(t, 0, code)
	assert.True(t, strings.HasPrefix(stdout, "Nothing to do: curio is up.\n"), stdout)
}

// TestUp_ImportYes: --yes --import imports without a terminal, at full
// speed, leaving keep-awake as the queue has it: never switched on, nor
// off.
func TestUp_ImportYes(t *testing.T) {
	cases := []struct {
		name      string
		keepAwake bool
		answer    string
	}{
		{"keep-awake off", false, "no"},
		{"keep-awake on", true, "yes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := upWorld(t)
			w.withSources()
			daemon := client.New(w.daemonURL)
			_, err := daemon.UpdateQueue(context.Background(), client.QueueUpdate{KeepAwake: new(tc.keepAwake)})
			require.NoError(t, err)
			code, stdout, stderr := w.exit(t, "up", "--yes", "--import", "html:"+htmlFixture)
			require.Equal(t, 0, code, stderr)
			assert.Contains(t, stdout, "Import started. Check back after ")
			assert.Contains(t, stderr, "Keep the Mac awake while it imports? "+tc.answer+" (--yes)")
			q, err := daemon.Queue(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.keepAwake, q.KeepAwake)
			assert.Equal(t, client.ThrottleNormal, q.Throttle)
			assert.Empty(t, q.Schedule)

			code, stdout, _ = w.exit(t, "up", "--yes", "--import", "html:"+htmlFixture)
			assert.Equal(t, 0, code)
			assert.True(t, strings.HasPrefix(stdout, "Nothing to do: curio is up.\n"), stdout)
		})
	}
}

// TestUp_ImportBlockers: an --import that names nothing importable exits
// 1 before anything is applied; one that isn't a source fails before
// anything is checked.
func TestUp_ImportBlockers(t *testing.T) {
	for _, spec := range []string{"html:" + filepath.Join(t.TempDir(), "gone.html"), "chrome:Nope"} {
		t.Run(spec, func(t *testing.T) {
			w := upWorld(t)
			w.withSources()
			code, stdout, stderr := w.exit(t, "up", "--yes", "--import", spec)
			assert.Equal(t, 1, code)
			assert.Contains(t, stdout, "To fix by hand first:\n  ✗ import: --import "+spec)
			assert.Equal(t, "Error: nothing was changed: import must be fixed by hand first (see above)\n", stderr)
			assert.Zero(t, count(t, w.srv, `SELECT count(*) FROM bookmarks`))
		})
	}
	w := freshWorld(t)
	probe := setuptest.NewProbe()
	w.deps.probe = probe
	code, _, stderr := w.exit(t, "up", "--import", "bogus")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "--import bogus: import from chrome, chrome:<profile>, safari, firefox or html:<path>")
	assert.Zero(t, probe.Calls(), "before any check")
}

// TestUp_ImportFreshCountsAllNew: with --fresh, the library the old home's
// daemon serves is never asked about: every bookmark is new to the home
// that replaces it.
func TestUp_ImportFreshCountsAllNew(t *testing.T) {
	w := upWorld(t)
	w.withSources()
	_, err := w.run(t, "import", "html", htmlFixture)
	require.NoError(t, err)

	_, stdout, _ := w.exit(t, "up", "--dry-run", "--import", "html:"+htmlFixture)
	assert.NotContains(t, stdout, "import: import", "the old home has them all")

	_, stdout, _ = w.exit(t, "up", "--fresh", "--dry-run", "--import", "html:"+htmlFixture)
	assert.Contains(t, stdout, ". import: import 2 new bookmarks from bookmarks.html\n", stdout)
	assert.Regexp(t, `bookmarks\.html: 2 new, fetching about 1m at full speed, indexing about 1m\n`, stdout,
		"a dry run's plan measures how fast this Mac indexes")
}

// cancelingIngest cancels the run the import comes from as the first
// bookmark arrives, and saves nothing.
type cancelingIngest struct {
	store.BookmarkStore
	cancel context.CancelFunc
}

func (c cancelingIngest) Ingest(ctx context.Context, _ *store.Bookmark) (store.IngestResult, error) {
	c.cancel()
	<-ctx.Done() // the client going away
	return store.IngestResult{}, ctx.Err()
}

// TestUp_ImportInterrupted: an import cut short says how to finish it
// before the run ends.
func TestUp_ImportInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := upWorld(t, func(d *api.Deps) { d.Bookmarks = cancelingIngest{BookmarkStore: d.Bookmarks, cancel: cancel} })
	w.withSources()
	ui := setuptest.NewUI(t, answers(setuptest.Yes().About("Import 2 new bookmarks"))...)
	w.scripted(ui)
	root := newRootCmdWith(w.deps)
	root.SetIn(strings.NewReader(""))
	var stdout, stderr bytes.Buffer
	code := run(ctx, root, []string{"--curio-home", w.home, "--daemon-url", w.daemonURL, "up", "--import",
		"html:" + htmlFixture}, &stdout, &stderr)
	assert.Equal(t, 130, code)
	abs, err := filepath.Abs(htmlFixture)
	require.NoError(t, err)
	assert.True(t, ui.Said("the import stopped after 0 of 3 bookmarks from bookmarks.html; `curio up --import "+
		"html:"+abs+"` finishes it (bookmarks already saved are skipped)"), "%v", ui.Events())
}

// TestUp_ImportDaemonStarting: a daemon still starting leaves the library
// unknown: the import is not checked, and no source is looked for.
func TestUp_ImportDaemonStarting(t *testing.T) {
	w := upWorldFrom(t, apitest.StartNotReady)
	looked := w.withSources(chromeWork())
	code, stdout, stderr := w.exit(t, "up")
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "warning: import: not checked: the daemon is starting; "+
		"checked again once the daemon serves\n")
	assert.Zero(t, looked.Load())
}

// TestUp_ImportFullDiskAccessDeclined: picking a source macOS withholds
// explains Full Disk Access; a no to opening System Settings runs nothing
// and shows the menu again.
func TestUp_ImportFullDiskAccessDeclined(t *testing.T) {
	w := upWorld(t)
	safari := setuptest.NewSource("Safari", importer.LabelSafari, "safari", "https://example.com/a")
	safari.Set(importer.Availability{State: importer.NeedsPermission, Reason: "operation not permitted"})
	w.withSources(safari)
	installer := setuptest.NewInstaller()
	w.deps.installer = installer
	ui := setuptest.NewUI(t, setuptest.Pick(0).About("Safari: needs Full Disk Access"),
		setuptest.No().About("Open Full Disk Access in System Settings?"),
		setuptest.Pick(2).About("Import which bookmarks?"))
	w.scripted(ui)
	code, _, stderr := w.exit(t, "up")
	require.Equal(t, 0, code, "%s\n%v", stderr, ui.Events())
	assert.Zero(t, ui.Unanswered())
	assert.Empty(t, installer.Runs())
	assert.True(t, ui.Said("macOS lets an app read Safari's bookmarks only with Full Disk Access"))
	assert.Zero(t, count(t, w.srv, `SELECT count(*) FROM bookmarks`))
}

// TestUp_ImportPace: the pace and keep-awake answers reach the daemon's
// queue gate in one update, before the import.
func TestUp_ImportPace(t *testing.T) {
	cases := []struct {
		name      string
		pace      int
		keepAwake int // the answer's index: "no" is the default
		want      client.Queue
	}{
		{"gently, kept awake", 1, 1, client.Queue{Throttle: client.ThrottleGentle, KeepAwake: true}},
		{"only overnight", 2, 0, client.Queue{Throttle: client.ThrottleNormal, Schedule: "22:00-07:00"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := upWorld(t)
			w.withSources()
			_, err := w.run(t, "pause")
			require.NoError(t, err)
			ui := setuptest.NewUI(t, setuptest.Yes().About("Import 2 new bookmarks"),
				setuptest.Pick(tc.pace).About("When should curio work through them?"),
				setuptest.Pick(tc.keepAwake).About("Keep the Mac awake while it imports? [no yes]"))
			w.scripted(ui)
			code, _, stderr := w.exit(t, "up", "--import", "html:"+htmlFixture)
			require.Equal(t, 0, code, "%s\n%v", stderr, ui.Events())
			q, err := client.New(w.daemonURL).Queue(context.Background())
			require.NoError(t, err)
			assert.False(t, q.Paused, "the import opens the queue")
			assert.Equal(t, tc.want.Throttle, q.Throttle)
			assert.Equal(t, tc.want.Schedule, q.Schedule)
			assert.Equal(t, tc.want.KeepAwake, q.KeepAwake)
		})
	}
}

// TestUpCards_NameRealCommands: every curio command either card names is
// one, with flags it takes.
func TestUpCards_NameRealCommands(t *testing.T) {
	root := newRootCmdWith(testDeps(t))
	for _, line := range slices.Concat(nextCard, handOffCard) {
		for _, argv := range cardCommands(t, line.command) {
			cmd, rest, err := root.Find(argv)
			require.NoError(t, err, line.command)
			require.NotSame(t, root, cmd, "%q names no command", line.command)
			require.NoError(t, cmd.ParseFlags(rest), line.command)
		}
	}
}

// cardCommands are the curio command lines a card's command stands for,
// without "curio": "curio pause | resume" is two.
func cardCommands(t *testing.T, command string) [][]string {
	t.Helper()
	words := strings.Fields(command)
	require.Equal(t, "curio", words[0], command)
	if i := slices.Index(words, "|"); i >= 0 {
		return [][]string{words[1:i], append(slices.Clone(words[1:i-1]), words[i+1:]...)}
	}
	return [][]string{words[1:]}
}

// TestStatus_Follow: --follow prints the status, then follows the queue
// until it has drained.
func TestStatus_Follow(t *testing.T) {
	followEvery = 10 * time.Millisecond
	t.Cleanup(func() { followEvery = 2 * time.Second })
	srv := apitest.Start(t)
	root := newRootCmdWith(testDeps(t))
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1) // the goroutine touches nothing of t, should it outlive the test
	go func() { done <- runCLIStreams(root, srv.Home.Path, srv.URL, &stdout, &stderr, "status", "--follow") }()
	select {
	case err := <-done:
		require.NoError(t, err, stderr.String())
		out := stdout.String()
		assert.Contains(t, out, "daemon:  running")
		assert.Contains(t, out, "watching queue drain")
		assert.Contains(t, out, "queue drained after ")
	case <-time.After(10 * time.Second):
		t.Fatal("status --follow didn't return once the queue was drained")
	}
	out := runArgs(t, "status", "--help")
	assert.Contains(t, out, "--follow")
	assert.Contains(t, out, "until the queue has drained")
}

// TestNoBrowsersInSight: TestMain hides this Mac's browsers: no test here
// can read its bookmarks.
func TestNoBrowsersInSight(t *testing.T) {
	for _, src := range importer.Discover() {
		assert.NotEqual(t, importer.Available, src.Check(t.Context()).State, src.Name())
	}
}

// TestUp_ImportNothingToFetch: bookmarks whose pages the library already
// has are saved without asking about the pace: there is nothing to fetch.
func TestUp_ImportNothingToFetch(t *testing.T) {
	w := upWorld(t)
	w.withSources()
	w.srv.AddDocument(t, "https://example.com/html/one", store.DocStateFetched)
	w.srv.AddDocument(t, "https://example.com/html/two", store.DocStateFetched)
	ui := setuptest.NewUI(t, setuptest.Yes().About("Import 2 new bookmarks from bookmarks.html?"))
	w.scripted(ui)
	code, stdout, stderr := w.exit(t, "up", "--import", "html:"+htmlFixture)
	require.Equal(t, 0, code, "%s\n%v", stderr, ui.Events())
	assert.Contains(t, stdout, "\nImported 2 new bookmarks from bookmarks.html; their pages were in the library already.\n")
	assert.Zero(t, count(t, w.srv, `SELECT count(*) FROM jobs`))
}
