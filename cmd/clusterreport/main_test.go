package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
)

// runReport runs the command with args and returns its exit code, stdout
// and stderr.
func runReport(ctx context.Context, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(ctx, args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// newCopy writes a database at dir/name holding n fetched documents, each
// with one chunk whose vector vector(i) gives, and returns its path.
func newCopy(t *testing.T, dir, name string, n int, vector func(i int) []float32) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(dir, name)
	db, err := sqlite.Open(ctx, path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	_, err = sqlite.Migrate(ctx, db)
	require.NoError(t, err)
	dim := len(vector(0))
	require.NoError(t, sqlite.EnsureVectorIndex(ctx, db, dim))
	docs, extractions, chunks := sqlite.NewDocuments(db), sqlite.NewExtractions(db), sqlite.NewChunks(db, dim)
	for i := range n {
		doc := &store.Document{ID: fmt.Sprintf("doc-%03d", i), TenantID: store.LocalTenantID,
			URL: fmt.Sprintf("https://example.com/%d", i)}
		require.NoError(t, docs.Create(ctx, doc))
		ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
			FetchedAt: time.Now().UTC()}
		require.NoError(t, extractions.Create(ctx, ext))
		require.NoError(t, docs.SetCurrentExtraction(ctx, doc.ID, ext.ID))
		require.NoError(t, chunks.ReplaceForDocument(ctx, doc.ID, ext.ID, "", nil,
			[]store.ChunkInput{{Text: "text", Embedding: vector(i)}}))
		require.NoError(t, docs.MarkFetched(ctx, doc.ID))
	}
	return path
}

// sameVector is every document's vector: centered, each is zero, so the
// grouping finds no interest.
func sameVector(int) []float32 { return []float32{0.6, 0.8, 0, 0} }

func TestRun_Usage(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
		says string
	}{
		{"no -db", nil, exitUsage, "-db is required"},
		{"an unknown flag", []string{"-db", "x.db", "-frobnicate"}, exitUsage, "flag provided but not defined"},
		{"no draws", []string{"-db", "x.db", "-draws", "0"}, exitUsage, "-draws must be at least 1"},
		{"negative draws", []string{"-db", "x.db", "-draws", "-1"}, exitUsage, "invalid value"},
		{"an argument", []string{"-db", "x.db", "extra"}, exitUsage, `unexpected argument "extra"`},
		{"help", []string{"-h"}, exitOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runReport(context.Background(), tc.args...)
			assert.Equal(t, tc.code, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.says)
			assert.Contains(t, stderr, "usage: clusterreport -db <copy of curio.db>")
		})
	}
}

// snapshot is a directory's files, not its subdirectories, with their
// contents.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		out[e.Name()] = string(data)
	}
	return out
}

func TestRun_RefusesAHome(t *testing.T) {
	home := t.TempDir()
	db := newCopy(t, home, "curio.db", 1, sameVector)
	require.NoError(t, os.WriteFile(filepath.Join(home, "daemon.pid"), []byte("4242\n"), 0o600))
	link := filepath.Join(t.TempDir(), "link.db")
	require.NoError(t, os.Symlink(db, link))
	before := snapshot(t, home)
	stat, err := os.Stat(db)
	require.NoError(t, err)

	for _, path := range []string{db, link} {
		code, stdout, stderr := runReport(context.Background(), "-db", path, "-json", filepath.Join(home, "r.json"))
		assert.Equal(t, exitFailure, code)
		assert.Empty(t, stdout)
		assert.Contains(t, stderr, "daemon.pid")
		assert.Contains(t, stderr, `sqlite3 -readonly `+path+` ".backup <copy>"`)
		assert.Contains(t, stderr, `sqlite3 'file:`+path+`?immutable=1' ".backup <copy>"`, "for a stopped daemon's")
	}
	assert.Equal(t, before, snapshot(t, home), "no database, -wal, -shm or JSON file written beside it")
	after, err := os.Stat(db)
	require.NoError(t, err)
	assert.Equal(t, stat.ModTime(), after.ModTime())
}

func TestRun_RefusesAMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := runReport(context.Background(), "-db", filepath.Join(dir, "typo.db"))
	assert.Equal(t, exitFailure, code)
	assert.Contains(t, stderr, "no such file")
	assert.Empty(t, snapshot(t, dir), "nothing created in its place")
}

// TestRun_RefusesAnUnusableJSONPath: a -json path the report couldn't
// write at the end is refused before the database is opened.
func TestRun_RefusesAnUnusableJSONPath(t *testing.T) {
	dir := t.TempDir()
	db := newCopy(t, dir, "copy.db", 1, sameVector)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "reports"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "readonly"), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "readonly"), 0o700) })
	before := snapshot(t, dir)
	cases := []struct {
		name, json, says string
	}{
		{"a directory", filepath.Join(dir, "reports"), "is a directory"},
		{"in a missing directory", filepath.Join(dir, "missing", "report.json"), "no such file"},
		{"in a directory it can't write to", filepath.Join(dir, "readonly", "report.json"), "permission denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runReport(context.Background(), "-db", db, "-json", tc.json)
			assert.Equal(t, exitFailure, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.says)
			assert.Equal(t, before, snapshot(t, dir), "the database untouched, nothing written")
		})
	}
}

func TestRun_WritesNoJSONOnFailure(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		ctx  context.Context
		docs int
		says string
	}{
		{"too few documents", context.Background(), 5, "fewer than the 20 a first grouping waits for"},
		{"interrupted", cancelled, 30, "context canceled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db := newCopy(t, dir, "copy.db", tc.docs, sameVector)
			out := filepath.Join(dir, "report.json")
			code, stdout, stderr := runReport(tc.ctx, "-db", db, "-json", out, "-draws", "1")
			assert.Equal(t, exitFailure, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.says)
			assert.NoFileExists(t, out)
			for name := range snapshot(t, dir) {
				assert.Contains(t, []string{"copy.db", "copy.db-wal", "copy.db-shm"}, name, "no temporary file left")
			}
		})
	}
}

// TestRun_DegenerateLibrary: a library the grouping finds nothing in
// reports every share and cohesion as n/a, and its JSON says null.
func TestRun_DegenerateLibrary(t *testing.T) {
	dir := t.TempDir()
	db := newCopy(t, dir, "copy.db", 25, sameVector)
	out := filepath.Join(dir, "report.json")
	require.NoError(t, os.WriteFile(out, []byte("an earlier report"), 0o644))

	code, stdout, stderr := runReport(context.Background(), "-db", db, "-json", out, "-draws", "1")
	require.Equal(t, exitOK, code, stderr)
	assert.Contains(t, stdout, "25 documents with a usable vector (0 dropped: NaN or infinite)")
	assert.Contains(t, stdout, "Stored run: none")

	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var rep map[string]any
	require.NoError(t, json.Unmarshal(raw, &rep))
	assert.InDelta(t, 25, rep["documents"], 0)
	fresh := rep["fresh"].(map[string]any)
	assert.Equal(t, "flat", fresh["shape"])
	assert.Nil(t, fresh["areas"])
	interests := fresh["interests"].(map[string]any)
	assert.InDelta(t, 0, interests["groups"], 0)
	assert.Nil(t, interests["cohesion"])
	assert.Nil(t, interests["silhouette"])
	added := rep["warm"].(map[string]any)["added"].(map[string]any)
	assert.Equal(t, map[string]any{"interests": nil, "areas": nil}, added["mean"])
	assert.Nil(t, rep["chain"].(map[string]any)["worst_gap"])
	assert.Nil(t, rep["stored_run"])
	for name := range snapshot(t, dir) {
		assert.True(t, slices.Contains([]string{"copy.db", "copy.db-wal", "copy.db-shm", "report.json"}, name),
			"no temporary file left: %s", name)
	}
}

// TestRun_FlatLibrary runs the whole report on a library small enough for
// the flat shape, whose latest run the engine made: every section measures
// interests, every area is n/a, and the stored run is the report's fresh
// grouping.
func TestRun_FlatLibrary(t *testing.T) {
	dir := t.TempDir()
	lib := newSynthetic(300, 1)
	db := newCopy(t, dir, "copy.db", len(lib.docs), func(i int) []float32 { return lib.docs[i].Vector })
	runID := firstRebuild(t, db)
	out := filepath.Join(dir, "report.json")

	code, stdout, stderr := runReport(context.Background(), "-db", db, "-json", out, "-draws", "1")
	require.Equal(t, exitOK, code, stderr)
	assert.Contains(t, stdout, "Fresh grouping of the whole library: flat shape")
	assert.Contains(t, stdout, "Stored run "+runID+": trigger first, fresh, flat shape")
	assert.Contains(t, stdout, "identical: true")

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var rep report
	require.NoError(t, json.Unmarshal(raw, &rep))
	assert.Equal(t, insight.ShapeFlat, rep.Fresh.Shape)
	assert.Nil(t, rep.Fresh.Areas)
	assert.Positive(t, rep.Fresh.Interests.Groups)
	assert.Positive(t, rep.Baseline.Interests.Groups)
	for name, k := range map[string]kept{
		"warm, added": rep.Warm.Added.Mean, "warm, mixed": rep.Warm.Mixed.Mean, "fresh rebuild": rep.FreshRebuild.Mean,
		"chain, split checks": rep.Chain.SplitSteps.Mean, "chain, other steps": rep.Chain.OtherSteps.Mean,
	} {
		assert.NotNil(t, k.Interests, name)
		assert.Nil(t, k.Areas, "%s: no areas to keep in the flat shape", name)
	}
	assert.Len(t, rep.Chain.Steps, chainSteps+1)
	assert.NotNil(t, rep.Chain.WorstGap)
	require.NotNil(t, rep.StoredRun)
	ag := rep.StoredRun.Agreement
	assert.Equal(t, len(lib.docs), ag.Shared)
	assert.Nil(t, ag.AreasARI)
	assert.True(t, ag.Identical, "the engine's run is the report's fresh grouping")
}

// firstRebuild runs the engine's first rebuild, with term labels, on the
// database at path, and returns the run's ID.
func firstRebuild(t *testing.T, path string) string {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	width, err := sqlite.VectorIndexWidth(ctx, db)
	require.NoError(t, err)
	quiet := slog.New(slog.DiscardHandler)
	engine := insight.New(sqlite.NewDocuments(db), sqlite.NewChunks(db, width), sqlite.NewInsights(db),
		insight.NewLouvainGrouper(quiet), nil, insight.Config{Labeling: insight.LabelingTerms, Center: true}, quiet)
	id, err := engine.Rebuild(ctx, store.LocalTenantID, store.RunTriggerFirst)
	require.NoError(t, err)
	return id
}
