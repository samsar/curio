package setup_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/service"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
)

// legacyMarker is a marker from before home formats.
const legacyMarker = `{"schema_version":11,"embedding_model":"nomic-embed-text","embedding_dim":768}` + "\n"

// legacyHome makes the harness's home a legacy one, with a database and
// content, and returns what its files hold.
func legacyHome(t *testing.T, h *harness) map[string]string {
	t.Helper()
	h.writeHome(genModel)
	files := map[string]string{
		curiohome.MarkerFile: legacyMarker,
		curiohome.DBFile:     "SQLite format 3\x00 the library",
		filepath.Join(curiohome.ContentDirName, "d1", "e1.md"): "# a page\n",
	}
	for name, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(h.home, name)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(h.home, name), []byte(body), 0o600))
	}
	return files
}

// assertHolds asserts dir holds files, byte for byte.
func assertHolds(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, name)
		assert.Equal(t, body, string(got), name)
	}
}

// fixedNow is the time the harness's backups are named after.
var fixedNow = time.Date(2026, 9, 27, 22, 14, 3, 0, time.Local)

// freshRun is `curio up --fresh`, agreeing to everything.
func freshRun(t *testing.T, h *harness, answers ...setuptest.Answer) (setup.Plan, error) {
	t.Helper()
	deps := h.deps(setuptest.NewUI(t, answers...))
	deps.Now = func() time.Time { return fixedNow }
	r, err := setup.New(setup.Options{Home: h.home, Fresh: true}, deps)
	require.NoError(t, err)
	var shown setup.Plan
	_, err = r.Run(t.Context(), func(p setup.Plan) { shown = p })
	return shown, err
}

// TestUp_Fresh: --fresh sets a legacy home aside whole, its agent booted
// out and its daemon stopped first, and carries on as a first run.
func TestUp_Fresh(t *testing.T) {
	h := newHarness(t)
	files := legacyHome(t, h)
	h.agent.Set(func(st *service.Status) { st.Installed, st.Loaded, st.Program = true, true, h.exe })
	backup := h.home + ".bak-20260927-221403"

	plan, err := freshRun(t, h, setuptest.Pick(0).About("Use these?"), setuptest.Yes().About(backup),
		setuptest.Yes().About("launchd"))
	require.NoError(t, err)
	home := item(t, plan, "home")
	assert.Equal(t, setup.AskNo, home.Fix.Consent)
	assert.Contains(t, home.Fix.Summary, "move "+h.home+" aside to "+backup)
	assert.Contains(t, home.Hint, "nothing is deleted", "the plan says so before the move is agreed to")

	assertHolds(t, backup, files)
	assert.Equal(t, 1, h.agent.Count("Uninstall"))
	assert.Equal(t, 1, h.agent.Count("Install"), "installed again, for the new home")
	meta, err := h.openHome().Meta()
	require.NoError(t, err)
	assert.Equal(t, curiohome.CurrentFormat, meta.Format)
	assert.Equal(t, 1024, meta.EmbeddingDim)
}

// TestUp_FreshDeclined: --fresh's move defaults to no, and a no changes
// nothing.
func TestUp_FreshDeclined(t *testing.T) {
	h := newHarness(t)
	files := legacyHome(t, h)
	_, err := freshRun(t, h, setuptest.Pick(0).About("Use these?"), setuptest.No().About("aside"))
	var declined *setup.DeclinedError
	require.ErrorAs(t, err, &declined)
	assert.Equal(t, "home", declined.Step)
	assertHolds(t, h.home, files)
	assert.Zero(t, h.agent.Count("Uninstall"))
}

// TestUp_FreshSuffix: a backup name already taken gets -2.
func TestUp_FreshSuffix(t *testing.T) {
	h := newHarness(t)
	files := legacyHome(t, h)
	taken := h.home + ".bak-20260927-221403"
	require.NoError(t, os.Mkdir(taken, 0o700))
	_, err := freshRun(t, h, setuptest.Pick(0), setuptest.Yes().About(taken+"-2"), setuptest.Yes())
	require.NoError(t, err)
	assertHolds(t, taken+"-2", files)
	entries, err := os.ReadDir(taken)
	require.NoError(t, err)
	assert.Empty(t, entries, "the taken name is left as it was")
}

// TestUp_FreshSymlinkedHome: a symlinked home is moved aside at its
// target, and made again there, so the link keeps working.
func TestUp_FreshSymlinkedHome(t *testing.T) {
	h := newHarness(t)
	link := h.home
	h.home = filepath.Join(t.TempDir(), "curio-data")
	files := legacyHome(t, h)
	require.NoError(t, os.Symlink(h.home, link))
	target := h.home
	h.home = link

	_, err := freshRun(t, h, setuptest.Pick(0), setuptest.Yes().About(target+".bak-"), setuptest.Yes())
	require.NoError(t, err)
	assertHolds(t, target+".bak-20260927-221403", files)
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "the link is still a link")
	assert.FileExists(t, filepath.Join(target, curiohome.MarkerFile), "the new home is at its target")
}

// TestUp_FreshNoHome: with no home, there is nothing to set aside, and
// the run is a first run.
func TestUp_FreshNoHome(t *testing.T) {
	h := newHarness(t)
	_, err := freshRun(t, h, setuptest.Pick(0), setuptest.Yes())
	require.NoError(t, err)
	matches, err := filepath.Glob(h.home + ".bak-*")
	require.NoError(t, err)
	assert.Empty(t, matches)
	assert.FileExists(t, filepath.Join(h.home, curiohome.MarkerFile))
}

// TestUp_FreshNotAHome: a directory that isn't a curio home is never
// moved, --fresh or not.
func TestUp_FreshNotAHome(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, os.MkdirAll(h.home, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(h.home, "notes.txt"), []byte("mine"), 0o600))
	_, err := freshRun(t, h)
	var blocked *setup.BlockedError
	require.ErrorAs(t, err, &blocked)
	assert.FileExists(t, filepath.Join(h.home, "notes.txt"))
	matches, err := filepath.Glob(h.home + ".bak-*")
	require.NoError(t, err)
	assert.Empty(t, matches)
}

// TestUp_FreshDryRun: a dry run shows the move and changes nothing.
func TestUp_FreshDryRun(t *testing.T) {
	h := newHarness(t)
	files := legacyHome(t, h)
	deps := h.deps(setuptest.NewUI(t))
	deps.Now = func() time.Time { return fixedNow }
	r, err := setup.New(setup.Options{Home: h.home, Fresh: true, DryRun: true}, deps)
	require.NoError(t, err)
	out, err := r.Run(t.Context(), func(setup.Plan) {})
	require.NoError(t, err)
	assert.True(t, out.DryRun)
	assert.Contains(t, item(t, out.Plan, "home").Fix.Summary, "aside")
	assertHolds(t, h.home, files)
	assert.Zero(t, h.changes())
}
