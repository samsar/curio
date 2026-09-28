package setup_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/importer"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
)

// Pages the fixtures hold: two YouTube videos, and an article.
const (
	video1  = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	video2  = "https://youtu.be/9bZkp7q19f0"
	article = "https://example.com/an-article"
)

// importAnswers agree to import from a bookmarks file, at full speed, the
// Mac left to sleep, with extra answered in between (yt-dlp, say).
func importAnswers(extra ...setuptest.Answer) []setuptest.Answer {
	return slices.Concat([]setuptest.Answer{setuptest.Yes().About("Import 3 new bookmarks from bookmarks.html?")},
		extra,
		[]setuptest.Answer{setuptest.Pick(0).About("When should curio work through them?"),
			setuptest.Pick(0).About("Keep the Mac awake")})
}

// importRun is `curio up --import html:<file>` on a harness that is up,
// answering with answers; it must succeed.
func importRun(t *testing.T, h *harness, file string, answers ...setuptest.Answer) (*setuptest.UI, setup.Outcome) {
	t.Helper()
	ui := setuptest.NewUI(t, answers...)
	_, out, err := h.up(ui, setup.Options{Import: "html:" + file})
	require.NoError(t, err, "%v", ui.Events())
	require.Zero(t, ui.Unanswered(), "%v", ui.Events())
	return ui, out
}

// indexOf is the index of the first event of kind that contains text, or
// -1.
func indexOf(events []setuptest.Event, kind, text string) int {
	return slices.IndexFunc(events, func(e setuptest.Event) bool {
		return e.Kind == kind && strings.Contains(e.Text, text)
	})
}

// TestImport_LibraryWithBookmarks: a library that has bookmarks is up,
// and no source is even looked for.
func TestImport_LibraryWithBookmarks(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	var looked atomic.Int32
	h.sources = func() []importer.Source { looked.Add(1); return nil }

	r := h.runner(setuptest.NewUI(t), setup.Options{})
	res := checkStep(t, r, "import")
	assert.Equal(t, setup.OK, res.Status)
	assert.Equal(t, "3 documents in the library", res.Detail)
	assert.Contains(t, res.Hint, "`curio import`")
	_, out, err := h.up(setuptest.NewUI(t), setup.Options{})
	require.NoError(t, err)
	assert.False(t, out.Changed)
	assert.Nil(t, out.Imported)
	assert.Zero(t, looked.Load())
}

// TestImport_BadSpec: an --import curio can't read is refused before
// anything is checked.
func TestImport_BadSpec(t *testing.T) {
	h := newHarness(t)
	for _, spec := range []string{"bogus", "html", "safari:Work"} {
		_, err := setup.New(setup.Options{Home: h.home, Import: spec}, h.deps(setuptest.NewUI(t)))
		require.Error(t, err, spec)
		assert.Contains(t, err.Error(), "--import "+spec)
	}
	assert.Zero(t, h.probe.Calls(), "before any check")
}

// TestImport_YTDLP: new YouTube videos get yt-dlp offered, installed,
// and the daemon restarted to use it, all before the queue is set and the
// bookmarks are sent.
func TestImport_YTDLP(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	h.installer.OnRun = func(argv []string) error {
		if slices.Equal(argv, []string{brew, "install", "yt-dlp"}) {
			h.installYTDLP()
		}
		return nil
	}
	restarts := h.agent.Count("Restart")
	ui, out := importRun(t, h, bookmarksFile(t, video1, video2, article),
		importAnswers(setuptest.Yes().About("2 of your bookmarks are YouTube videos. Install yt-dlp"))...)

	assert.Equal(t, [][]string{{brew, "install", "yt-dlp"}}, h.installer.Runs())
	assert.Equal(t, restarts+1, h.agent.Count("Restart"))
	events := ui.Events()
	restarted, progress := indexOf(events, "info", "restarted curio-daemon"), indexOf(events, "progress", "importing")
	require.NotEqual(t, -1, restarted, "%v", events)
	require.NotEqual(t, -1, progress, "%v", events)
	assert.Less(t, restarted, progress, "the daemon routes YouTube to yt-dlp before a bookmark is sent")
	assert.True(t, ui.Said("the 2 YouTube videos get their transcripts through "+setuptest.YTDLPPath))
	assert.Equal(t, setuptest.YTDLPPath, healthz(t, h).YouTubeFetcher)

	require.NotNil(t, out.Imported)
	assert.Equal(t, 3, out.Imported.Created)
	assert.Equal(t, 3, out.Imported.Pages)
	assert.Equal(t, setup.PaceFull, out.Imported.Pace)
	assert.False(t, out.Imported.KeepAwake)
	assert.False(t, out.Imported.CheckBack.IsZero())
	requests := setuptest.DaemonRequests(h.home)
	queue := slices.IndexFunc(requests, func(r string) bool { return strings.HasPrefix(r, "PUT /v1/queue") })
	imported := slices.IndexFunc(requests, func(r string) bool {
		return strings.HasPrefix(r, "POST /v1/bookmarks/import") && strings.Contains(r, `"dry_run":false`)
	})
	require.NotEqual(t, -1, queue, "%v", requests)
	assert.Less(t, queue, imported, "the pace is set before the import: %v", requests)
	assert.Contains(t, requests[queue], `"keep_awake":false`)
}

// TestImport_YTDLPDeclined: a no is remembered in setup.json, and the next
// run doesn't ask again.
func TestImport_YTDLPDeclined(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	file := bookmarksFile(t, video1, video2, article)
	importRun(t, h, file, importAnswers(setuptest.No().About("Install yt-dlp"))...)
	state := setup.LoadState(h.home, setuptest.NewUI(t))
	assert.Contains(t, state.Declined, "yt-dlp")
	assert.Empty(t, h.installer.Runs())

	ui, _ := importRun(t, h, file, importAnswers()...)
	assert.True(t, ui.Said("yt-dlp was declined before"))
	assert.Empty(t, h.installer.Runs())
}

// TestImport_YTDLPDeclineNotSaved: a decline setup.json can't keep warns
// that the question comes back, and the import goes on.
func TestImport_YTDLPDeclineNotSaved(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	// A directory where setup.json goes: it can't be read, nor replaced.
	require.NoError(t, os.Mkdir(filepath.Join(h.home, setup.StateFile), 0o700))
	ui, out := importRun(t, h, bookmarksFile(t, video1, video2, article),
		importAnswers(setuptest.No().About("Install yt-dlp"))...)
	assert.True(t, ui.Said("curio up will ask about yt-dlp again: "), "%v", ui.Events())
	assert.True(t, ui.Said("not installing yt-dlp"))
	require.NotNil(t, out.Imported)
	assert.Equal(t, 3, out.Imported.Created)
}

// TestImport_YTDLPAlreadyRouted: a daemon that already routes YouTube to
// yt-dlp is said so, with nothing detected, asked or restarted.
func TestImport_YTDLPAlreadyRouted(t *testing.T) {
	h := newHarness(t)
	h.installYTDLP()
	h.firstRun()
	detects, restarts := h.installer.Detects(), h.agent.Count("Restart")
	ui, _ := importRun(t, h, bookmarksFile(t, video1, video2, article), importAnswers()...)
	assert.True(t, ui.Said("the 2 YouTube videos get their transcripts through "+setuptest.YTDLPPath))
	assert.Equal(t, detects, h.installer.Detects())
	assert.Equal(t, restarts, h.agent.Count("Restart"))
	assert.Empty(t, h.installer.Runs())
}

// TestImport_YTDLPInstalledSinceTheDaemonStarted: a yt-dlp the running
// daemon predates gets one restart, and no install.
func TestImport_YTDLPInstalledSinceTheDaemonStarted(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	h.installYTDLP()
	h.installer.Detected.Formula = true
	restarts := h.agent.Count("Restart")
	ui, _ := importRun(t, h, bookmarksFile(t, video1, video2, article), importAnswers()...)
	assert.Equal(t, restarts+1, h.agent.Count("Restart"))
	assert.Empty(t, h.installer.Runs())
	assert.True(t, ui.Said("through "+setuptest.YTDLPPath))

	// Installed where the daemon doesn't look: restarted, and warned.
	h = newHarness(t)
	h.firstRun()
	h.installer.Detected.Binary = "/Users/x/.local/bin/yt-dlp"
	ui, _ = importRun(t, h, bookmarksFile(t, video1, video2, article), importAnswers()...)
	assert.True(t, ui.Said("curio-daemon doesn't find /Users/x/.local/bin/yt-dlp: link it into Homebrew's bin"))
	assert.True(t, ui.Said("fetcher.youtube.bin"))
}

// TestImport_YTDLPNoInstall: --no-install says how, and asks and runs
// nothing.
func TestImport_YTDLPNoInstall(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	ui := setuptest.NewUI(t, importAnswers()...)
	_, _, err := h.up(ui, setup.Options{Import: "html:" + bookmarksFile(t, video1, video2, article), NoInstall: true})
	require.NoError(t, err, "%v", ui.Events())
	assert.True(t, ui.Said("the new bookmarks include 2 YouTube videos: `brew install yt-dlp`, then `curio daemon stop`"))
	assert.Empty(t, h.installer.Runs())
}

// TestImport_NoYouTube: without new YouTube videos yt-dlp is never
// mentioned: the script has no answer for a question about it.
func TestImport_NoYouTube(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	detects := h.installer.Detects()
	ui, out := importRun(t, h, bookmarksFile(t, article, "https://example.org/another", "https://example.net/third"),
		importAnswers()...)
	assert.False(t, ui.Said("yt-dlp"))
	assert.Equal(t, detects, h.installer.Detects())
	require.NotNil(t, out.Imported)
	assert.Equal(t, 3, out.Imported.JobsEnqueued)
}

// safariNeedingAccess is a Safari that macOS withholds, flipped readable
// when System Settings is opened if grant is set.
func safariNeedingAccess(h *harness, grant bool) *setuptest.Source {
	src := setuptest.NewSource("Safari", importer.LabelSafari, "safari", video1, article, "https://example.org/a")
	src.Set(importer.Availability{State: importer.NeedsPermission, Reason: "operation not permitted"})
	h.sources = func() []importer.Source { return []importer.Source{src} }
	h.installer.OnRun = func(argv []string) error {
		if grant && argv[0] == "/usr/bin/open" {
			src.Set(importer.Availability{State: importer.Available})
		}
		return nil
	}
	return src
}

// openFullDiskAccess is the command that opens System Settings' Full Disk
// Access pane.
var openFullDiskAccess = []string{"/usr/bin/open",
	"x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"}

// TestImport_FullDiskAccess: --import safari, withheld, explains Full Disk
// Access, opens its pane with the exact command shown, and checks again.
func TestImport_FullDiskAccess(t *testing.T) {
	h := newHarness(t)
	h.installYTDLP()
	h.firstRun()
	safariNeedingAccess(h, true)
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	ui := setuptest.NewUI(t,
		setuptest.Yes().About("Import bookmarks from Safari, once macOS lets curio read them?"),
		setuptest.Yes().About("Open Full Disk Access in System Settings?"),
		setuptest.Yes().About("Check again whether curio can read Safari?"),
		setuptest.Pick(0).About("When should curio work through them?"),
		setuptest.Pick(0).About("Keep the Mac awake"))
	_, out, err := h.up(ui, setup.Options{Import: "safari"})
	require.NoError(t, err, "%v", ui.Events())
	assert.Equal(t, [][]string{openFullDiskAccess}, h.installer.Runs())
	assert.True(t, ui.Said("grants it to the terminal app curio runs in (Terminal), not to curio"))
	assert.True(t, ui.Said("  $ /usr/bin/open 'x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles'"))
	require.NotNil(t, out.Imported)
	assert.Equal(t, "Safari", out.Imported.Source)
}

// TestImport_FullDiskAccessStillDenied: access macOS still withholds gets
// the restart note, and, once the user stops checking, fails the step with
// the remedy.
func TestImport_FullDiskAccessStillDenied(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	safariNeedingAccess(h, false)
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	ui := setuptest.NewUI(t, setuptest.Yes().About("Import bookmarks from Safari"),
		setuptest.Yes().About("Open Full Disk Access"), setuptest.Yes().About("Check again"),
		setuptest.No().About("Check again"))
	_, _, err := h.up(ui, setup.Options{Import: "safari"})
	var failed *setup.StepError
	require.ErrorAs(t, err, &failed)
	assert.Equal(t, "import", failed.Step)
	assert.Contains(t, err.Error(), "give iTerm2 Full Disk Access")
	assert.True(t, ui.Said("macOS applies the change to iTerm2 only after it restarts"))
}

// TestImport_FullDiskAccessNoInstall: --no-install opens nothing and says
// where the pane is.
func TestImport_FullDiskAccessNoInstall(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	safariNeedingAccess(h, true)
	t.Setenv("TERM_PROGRAM", "")
	ui := setuptest.NewUI(t, setuptest.Yes().About("Import bookmarks from Safari"), setuptest.No().About("Check again"))
	_, _, err := h.up(ui, setup.Options{Import: "safari", NoInstall: true})
	require.Error(t, err)
	assert.Empty(t, h.installer.Runs())
	assert.True(t, ui.Said("Give your terminal app Full Disk Access in System Settings > Privacy & Security > Full Disk Access"))
}

// TestImport_FullDiskAccessNobodyToAsk: a withheld --import safari that
// nobody is there to walk through Full Disk Access blocks the plan with
// the remedy, opening nothing: without a terminal, and with --yes in one,
// which would otherwise answer every "check again" itself while macOS
// withholds the access until the terminal restarts, and never end.
func TestImport_FullDiskAccessNobodyToAsk(t *testing.T) {
	cases := []struct {
		name string
		ui   func(t *testing.T) (setup.UI, *setuptest.UI)
		why  string
	}{
		{"without a terminal", func(t *testing.T) (setup.UI, *setuptest.UI) {
			ui := setuptest.NewUI(t).NonInteractive()
			return ui, ui
		}, "which takes a terminal to walk through"},
		{"--yes in a terminal", func(t *testing.T) (setup.UI, *setuptest.UI) {
			ui := setuptest.NewUI(t)
			return setup.YesUI(ui), ui
		}, "which only you can turn on, so --yes can't answer for it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.firstRun()
			safariNeedingAccess(h, false)
			ui, scripted := tc.ui(t)
			// Bounded, so a run that loops fails the test instead of
			// hanging it: yesUI answers nothing once the context ends.
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			var plan setup.Plan
			_, err := h.runner(ui, setup.Options{Import: "safari", Yes: true}).Run(ctx,
				func(p setup.Plan) { plan = p })
			var blocked *setup.BlockedError
			require.ErrorAs(t, err, &blocked, "%v", scripted.Events())
			res := item(t, plan, "import")
			assert.Equal(t, "--import safari: macOS doesn't let curio read Safari without Full Disk Access, "+tc.why,
				res.Detail)
			assert.Contains(t, res.Hint, "`curio up --import safari` again")
			assert.Empty(t, h.installer.Runs())
			assert.False(t, scripted.Said("Check again"), "%v", scripted.Events())
		})
	}
}

// TestImport_Blockers: an --import that names nothing importable blocks the
// plan before anything is applied, saying why.
func TestImport_Blockers(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.html")
	require.NoError(t, os.WriteFile(empty, []byte("<DL></DL>"), 0o600))
	unreadable := setuptest.NewSource("Firefox", importer.LabelFirefox, "firefox")
	unreadable.Set(importer.Availability{State: importer.Unreadable, Reason: "database disk image is malformed"})
	cases := []struct {
		spec   string
		detail string
	}{
		{"html:" + filepath.Join(t.TempDir(), "gone.html"), "no file at "},
		{"html:" + empty, "has no bookmarks"},
		{"chrome:Nope", "is Chrome installed?"},
		{"safari", "safari isn't installed"},
		{"firefox", "database disk image is malformed"},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			h := newHarness(t)
			h.firstRun()
			h.sources = func() []importer.Source { return []importer.Source{unreadable} }
			plan, _, err := h.up(setuptest.NewUI(t), setup.Options{Import: tc.spec})
			var blocked *setup.BlockedError
			require.ErrorAs(t, err, &blocked)
			res := item(t, plan, "import")
			assert.True(t, res.Blocked())
			assert.Contains(t, res.Detail, tc.detail)
			assert.Empty(t, setuptest.DaemonRequests(h.home), "nothing was sent")
		})
	}
}

// TestImport_NothingNew: an --import whose bookmarks the library already
// has is up.
func TestImport_NothingNew(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	src := setuptest.NewSource("Firefox", importer.LabelFirefox, "firefox", "javascript:void(0)")
	h.sources = func() []importer.Source { return []importer.Source{src} }
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{Import: "firefox"}), "import")
	assert.Equal(t, setup.OK, res.Status, "%+v", res)
	assert.Equal(t, "nothing new in Firefox", res.Detail)
}

// TestImport_OnlyADryRunMeasures: the plan embeds nothing to estimate an
// import, except for a dry run, whose plan is all it shows.
func TestImport_OnlyADryRunMeasures(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	file := bookmarksFile(t, article)
	embeds := len(h.ollama.Embeds())
	h.runner(setuptest.NewUI(t), setup.Options{Import: "html:" + file}).Plan(t.Context())
	assert.Len(t, h.ollama.Embeds(), embeds)

	plan := h.runner(setuptest.NewUI(t), setup.Options{Import: "html:" + file, DryRun: true}).Plan(t.Context())
	assert.Len(t, h.ollama.Embeds(), embeds+2, "a warm-up batch and a timed one")
	assert.Contains(t, item(t, plan, "import").Notes[0], ", indexing ")
}

// TestNoBrowsersInSight: TestMain hides this Mac's browsers: no test here
// can read its bookmarks.
func TestNoBrowsersInSight(t *testing.T) {
	for _, src := range importer.Discover() {
		assert.NotEqual(t, importer.Available, src.Check(t.Context()).State, src.Name())
	}
}

// TestImport_PaceOnASmallMac: under 16 GiB, --yes imports only overnight,
// and leaves keep-awake off.
func TestImport_PaceOnASmallMac(t *testing.T) {
	h := newHarness(t)
	h.probe.Facts = setuptest.AppleSilicon(8)
	h.firstRun()
	_, out, err := h.up(yes(t), setup.Options{Yes: true, Import: "html:" + bookmarksFile(t, article)})
	require.NoError(t, err)
	require.NotNil(t, out.Imported)
	assert.Equal(t, setup.PaceOvernight, out.Imported.Pace)
	assert.False(t, out.Imported.KeepAwake)
	requests := setuptest.DaemonRequests(h.home)
	assert.True(t, slices.ContainsFunc(requests, func(r string) bool {
		return strings.HasPrefix(r, "PUT /v1/queue") && strings.Contains(r, `"schedule":"22:00-07:00"`)
	}), "%v", requests)
}
