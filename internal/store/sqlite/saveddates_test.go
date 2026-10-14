package sqlite

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/store"
)

// savedEpoch is when the synthetic libraries' first import ran.
var savedEpoch = time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)

// savedRow is a synthetic bookmark: dated, saved ms after savedEpoch and
// stored at savedEpoch, in folder.
func savedRow(id, source, folder string, ms int64) docmeta.SavedRow {
	return docmeta.SavedRow{ID: id, Source: source, Folder: folder,
		SavedAt: savedEpoch.Add(time.Duration(ms) * time.Millisecond), CreatedAt: savedEpoch}
}

// insertSavedRows inserts rows as the tenant's bookmarks in one
// transaction, each with the class Ingest inserts it with, its folder ""
// stored as NULL as Ingest stores it.
func insertSavedRows(t testing.TB, db *DB, tenantID string, rows []docmeta.SavedRow) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	for chunk := range slices.Chunk(rows, 400) {
		args := make([]any, 0, 9*len(chunk))
		for _, r := range chunk {
			b := &store.Bookmark{Source: r.Source, Dateless: r.Dateless}
			args = append(args, r.ID, tenantID, "https://example.com/"+r.ID, formatTime(r.SavedAt), r.Source,
				store.NullableString(r.Folder), r.Dateless, insertedSavedDate(b), formatTime(r.CreatedAt))
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO bookmarks (id, tenant_id, url, saved_at, source, folder_path,
			dateless, saved_date, created_at) VALUES `+strings.TrimSuffix(strings.Repeat("(?,?,?,?,?,?,?,?,?),",
			len(chunk)), ","), args...)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
}

// storedSavedDates are the tenant's bookmarks' classes, by ID.
func storedSavedDates(t *testing.T, db *DB, tenantID string) map[string]store.SavedDate {
	t.Helper()
	rows, err := db.Query(`SELECT id, saved_date FROM bookmarks WHERE tenant_id = ?`, tenantID)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]store.SavedDate{}
	for rows.Next() {
		var id string
		var class store.SavedDate
		require.NoError(t, rows.Scan(&id, &class))
		out[id] = class
	}
	require.NoError(t, rows.Err())
	return out
}

// derivedSavedDates are the classes one classification of rows gives, by
// ID.
func derivedSavedDates(rows []docmeta.SavedRow) map[string]store.SavedDate {
	out := make(map[string]store.SavedDate, len(rows))
	for i, class := range docmeta.SavedDates(rows) {
		out[rows[i].ID] = savedDateClasses[class]
	}
	return out
}

// TestSavedDateClasses_MatchTheStore: the rule's classes are the store's,
// one for one, and the migration's CHECK takes each.
func TestSavedDateClasses_MatchTheStore(t *testing.T) {
	assert.ElementsMatch(t, store.SavedDates(), slices.Collect(maps.Values(savedDateClasses)))
	for rule, class := range savedDateClasses {
		assert.Equal(t, string(rule), string(class))
		assert.True(t, class.Valid())
	}
	assert.False(t, store.SavedDate("maybe").Valid())
}

// TestClassifySavedDates_WritesWhatChanged: a classification writes the
// bookmarks whose class changed, and sets the updated_at of those alone;
// another, with nothing changed, writes nothing.
func TestClassifySavedDates_WritesWhatChanged(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bulk := []docmeta.SavedRow{
		savedRow("bulk-1", store.SourceChrome, "/A", 0), savedRow("bulk-2", store.SourceChrome, "/A", 10),
		savedRow("bulk-3", store.SourceChrome, "/B", 20), savedRow("bulk-4", store.SourceChrome, "/B", 30),
		savedRow("bulk-5", store.SourceChrome, "/C", 40),
	}
	tabs := make([]docmeta.SavedRow, 0, 6)
	for i := range 6 {
		tabs = append(tabs, savedRow(fmt.Sprint("tab-", i), store.SourceChrome, "/Tabs", 60_000+int64(i)*10))
	}
	manual := savedRow("manual", store.SourceManual, "/A", 5)
	insertSavedRows(t, db, "local", slices.Concat(bulk, tabs, []docmeta.SavedRow{manual}))
	const old = "2025-01-01T00:00:00.000Z"
	_, err := db.Exec(`UPDATE bookmarks SET updated_at = ?`, old)
	require.NoError(t, err)

	// total_changes() counts one connection's writes.
	db.SetMaxOpenConns(1)
	bms := NewBookmarks(db)
	before := totalChanges(t, db)
	done, err := bms.ClassifySavedDates(ctx, "local", store.SourceChrome)
	require.NoError(t, err)
	assert.Equal(t, store.SavedDatesClassified{Changed: 6}, done, "the save of all tabs, inserted unknown, is exact")
	assert.Equal(t, before+6, totalChanges(t, db))
	updated := map[string]bool{}
	for _, row := range dumpRows(t, db, `SELECT id, updated_at FROM bookmarks`) {
		updated[row[0].(string)] = row[1] != old
	}
	for _, r := range tabs {
		assert.True(t, updated[r.ID], r.ID)
	}
	for _, r := range append(bulk, manual) {
		assert.False(t, updated[r.ID], r.ID)
	}
	got := storedSavedDates(t, db, "local")
	assert.Equal(t, store.SavedDateUnknown, got["bulk-1"])
	assert.Equal(t, store.SavedDateExact, got["tab-0"])
	assert.Equal(t, store.SavedDateExact, got["manual"], "a dated manual bookmark is inserted exact")

	before = totalChanges(t, db)
	done, err = bms.ClassifySavedDates(ctx, "local", "")
	require.NoError(t, err)
	assert.Zero(t, done.Changed)
	assert.Equal(t, before, totalChanges(t, db), "nothing changed, nothing written")
}

// TestClassifySavedDates_ClassesRise: a class is the stored rows', so it
// rises when a later batch of the same run brings the evidence that the
// run was a save: rows joining an import-shaped run in one folder make its
// earlier rows exact, and a folder copy's top folder makes its subfolders
// approx.
func TestClassifySavedDates_ClassesRise(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms := NewBookmarks(db)
	batch := func(source string, rows ...docmeta.SavedRow) map[string]store.SavedDate {
		t.Helper()
		insertSavedRows(t, db, "local", rows)
		_, err := bms.ClassifySavedDates(ctx, "local", source)
		require.NoError(t, err)
		return storedSavedDates(t, db, "local")
	}

	// 3 of 5 in one folder is an import's; 13 of 15, a save of all tabs.
	got := batch(store.SourceChrome, savedRow("a1", store.SourceChrome, "/A", 0),
		savedRow("a2", store.SourceChrome, "/A", 50), savedRow("a3", store.SourceChrome, "/A", 100),
		savedRow("b1", store.SourceChrome, "/B", 150), savedRow("c1", store.SourceChrome, "/C", 200))
	assert.Equal(t, store.SavedDateUnknown, got["a1"])
	more := make([]docmeta.SavedRow, 0, 10)
	for i := range 10 {
		more = append(more, savedRow(fmt.Sprint("a-more-", i), store.SourceChrome, "/A", 250+int64(i)*50))
	}
	got = batch(store.SourceChrome, more...)
	for _, id := range []string{"a1", "a2", "a3", "b1", "c1", "a-more-0", "a-more-9"} {
		assert.Equal(t, store.SavedDateExact, got[id], id)
	}

	// Three sibling folders hold no common folder; their parent's own rows,
	// in the next batch, make the run a copy of it.
	got = batch(store.SourceFirefox, savedRow("x1", store.SourceFirefox, "/Top/x", 0),
		savedRow("x2", store.SourceFirefox, "/Top/x", 10), savedRow("y1", store.SourceFirefox, "/Top/y", 20),
		savedRow("y2", store.SourceFirefox, "/Top/y", 30), savedRow("z1", store.SourceFirefox, "/Top/z", 40),
		savedRow("z2", store.SourceFirefox, "/Top/z", 50))
	assert.Equal(t, store.SavedDateUnknown, got["x1"])
	got = batch(store.SourceFirefox, savedRow("top1", store.SourceFirefox, "/Top", 60),
		savedRow("top2", store.SourceFirefox, "/Top", 70))
	for _, id := range []string{"x1", "x2", "y1", "y2", "z1", "z2", "top1", "top2"} {
		assert.Equal(t, store.SavedDateApprox, got[id], id)
	}
	assert.Equal(t, store.SavedDateExact, got["a1"], "another source's classes stand")
}

// syntheticSource is a random source's bookmarks: bulk runs, saves of all
// tabs, folder copies, runs mostly in one folder, lone saves and dateless
// bookmarks of the first import and of a later one.
func syntheticSource(r *rand.Rand, prefix, source string) []docmeta.SavedRow {
	var (
		rows []docmeta.SavedRow
		ms   int64
	)
	add := func(folder string, step int64) {
		rows = append(rows, savedRow(fmt.Sprintf("%s-%03d", prefix, len(rows)), source, folder, ms))
		ms += step
	}
	for range 2 + r.IntN(4) {
		ms += 5_000 + r.Int64N(60_000)
		n := 5 + r.IntN(5)
		switch r.IntN(6) {
		case 0: // a bulk import's stamp
			for i := range n {
				add(fmt.Sprint("/Bulk/", i%(3+r.IntN(2))), 1+r.Int64N(200))
			}
		case 1: // all tabs saved into one folder
			for range n {
				add("/Tabs", 1+r.Int64N(100))
			}
		case 2: // a folder copied whole
			for i := range n {
				add([]string{"/Copy", "/Copy/x", "/Copy/y", "/Copy/y/z"}[i%4], 1+r.Int64N(100))
			}
		case 3: // mostly one folder: a save of all tabs, unless a batch holds too little of it
			for range 9 + r.IntN(4) {
				add("/Most", 10)
			}
			add("/Other", 10)
			add("/Third", 10)
		case 4: // lone saves
			for range n {
				add([]string{"", "/A", "/B"}[r.IntN(3)], 1_500+r.Int64N(10_000))
			}
		case 5: // dateless, from the first import or a later one
			later := time.Duration(r.IntN(2)) * 20 * time.Minute
			for range n {
				add("", 0)
				last := &rows[len(rows)-1]
				last.Dateless = true
				last.CreatedAt = savedEpoch.Add(later)
				last.SavedAt = last.CreatedAt
			}
		}
	}
	return rows
}

// TestClassifySavedDates_AnyBatchesEndAsOneClassification: however an
// import is split into requests, and in whatever order its bookmarks come,
// classifying the source after each request ends with the classes one
// classification of the final bookmarks gives.
func TestClassifySavedDates_AnyBatchesEndAsOneClassification(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms := NewBookmarks(db)
	sources := []string{store.SourceChrome, store.SourceFirefox, store.SourceSafari, store.SourceHTML,
		store.SourceManual}
	for seed := range uint64(120) {
		r := rand.New(rand.NewPCG(seed, 2026))
		tenantID := fmt.Sprint("library-", seed)
		source := sources[r.IntN(len(sources))]
		rows := syntheticSource(r, tenantID, source)
		r.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
		for rest := rows; len(rest) > 0; {
			n := 1 + r.IntN(len(rest))
			insertSavedRows(t, db, tenantID, rest[:n])
			_, err := bms.ClassifySavedDates(ctx, tenantID, source)
			require.NoError(t, err)
			rest = rest[n:]
		}
		assert.Equal(t, derivedSavedDates(rows), storedSavedDates(t, db, tenantID), "seed %d (%s)", seed, source)
	}
}

// TestEnsureSavedDates: the start's classification covers every tenant
// and source, counts the classes it leaves, and, run again, writes nothing.
func TestEnsureSavedDates(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	first := []docmeta.SavedRow{
		savedRow("one-1", store.SourceChrome, "/A", 0), savedRow("one-2", store.SourceChrome, "/B", 10),
		savedRow("one-3", store.SourceChrome, "/C", 20), savedRow("one-4", store.SourceChrome, "/A", 30),
		savedRow("one-5", store.SourceChrome, "/B", 40), savedRow("one-6", store.SourceManual, "", 50),
	}
	second := []docmeta.SavedRow{
		savedRow("two-1", store.SourceFirefox, "/X", 0), savedRow("two-2", store.SourceFirefox, "/X/y", 10),
		savedRow("two-3", store.SourceFirefox, "/X/z", 20), savedRow("two-4", store.SourceFirefox, "/X/z", 30),
		savedRow("two-5", store.SourceFirefox, "/X", 40),
	}
	insertSavedRows(t, db, "one", first)
	insertSavedRows(t, db, "two", second)

	db.SetMaxOpenConns(1)
	done, err := EnsureSavedDates(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, 11, done.Bookmarks)
	assert.Equal(t, 5, done.Changed, "the folder copy, inserted unknown, is approx")
	assert.Equal(t, []int{1, 5, 5}, []int{done.Exact, done.Approx, done.Unknown})
	assert.Equal(t, derivedSavedDates(first), storedSavedDates(t, db, "one"))
	assert.Equal(t, derivedSavedDates(second), storedSavedDates(t, db, "two"))

	before := totalChanges(t, db)
	again, err := EnsureSavedDates(ctx, db)
	require.NoError(t, err)
	assert.Zero(t, again.Changed)
	assert.Equal(t, 11, again.Bookmarks)
	assert.Equal(t, before, totalChanges(t, db), "a second run writes nothing")
}

// TestEnsureSavedDates_ReportsWhatItDidBeforeAnError: a tenant whose
// classes can't be written stops the start's classification, which
// reports the tenants before it, whose classes stay written.
func TestEnsureSavedDates_ReportsWhatItDidBeforeAnError(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	first := []docmeta.SavedRow{
		savedRow("a-1", store.SourceChrome, "/A", 0), savedRow("a-2", store.SourceChrome, "/B", 2_000),
		savedRow("a-3", store.SourceChrome, "/C", 4_000),
	}
	insertSavedRows(t, db, "a", first)
	insertSavedRows(t, db, "b", []docmeta.SavedRow{savedRow("b-1", store.SourceChrome, "/A", 0)})
	_, err := db.Exec(`CREATE TRIGGER refuse_b BEFORE UPDATE OF saved_date ON bookmarks
		WHEN NEW.tenant_id = 'b' BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	require.NoError(t, err)

	done, err := EnsureSavedDates(ctx, db)
	require.ErrorContains(t, err, "classify saved dates of tenant b: write bookmark b-1's class: refused")
	assert.Equal(t, SavedDatesRefresh{Bookmarks: 3, Changed: 3, Exact: 3, Took: done.Took}, done)
	assert.Positive(t, done.Took)
	assert.Equal(t, derivedSavedDates(first), storedSavedDates(t, db, "a"))
}

// TestClassifySavedDates_SkipsTimesThatDontParse: a bookmark whose saved
// or stored time doesn't parse stops no classification: it is unknown,
// reported by ID, and the rule classifies the rest without it.
func TestClassifySavedDates_SkipsTimesThatDontParse(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	rows := []docmeta.SavedRow{
		savedRow("lone-1", store.SourceChrome, "/A", 0), savedRow("bad-saved", store.SourceChrome, "/B", 1_500),
		savedRow("lone-2", store.SourceChrome, "/A", 3_000), savedRow("bad-stored", store.SourceManual, "", 5),
	}
	insertSavedRows(t, db, "local", rows)
	_, err := db.Exec(`UPDATE bookmarks SET saved_at = 'not a time' WHERE id = 'bad-saved';
		UPDATE bookmarks SET created_at = 'yesterday' WHERE id = 'bad-stored'`)
	require.NoError(t, err)

	done, err := NewBookmarks(db).ClassifySavedDates(ctx, "local", store.SourceChrome)
	require.NoError(t, err)
	assert.Equal(t, store.SavedDatesClassified{Changed: 2, Skipped: []string{"bad-saved"}}, done)
	got := storedSavedDates(t, db, "local")
	assert.Equal(t, map[string]store.SavedDate{"lone-1": store.SavedDateExact, "lone-2": store.SavedDateExact,
		"bad-saved": store.SavedDateUnknown, "bad-stored": store.SavedDateExact}, got)

	refresh, err := EnsureSavedDates(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, []string{"bad-stored", "bad-saved"}, refresh.Skipped, "in saved order")
	assert.Equal(t, 1, refresh.Changed, "the manual bookmark, inserted exact, is unknown")
	assert.Equal(t, store.SavedDateUnknown, storedSavedDates(t, db, "local")["bad-stored"])
	assert.Equal(t, []int{2, 0, 2}, []int{refresh.Exact, refresh.Approx, refresh.Unknown})
}

// TestEnsureSavedDates_MarksSafariDateless: the start marks dateless the
// Safari bookmarks an older daemon wrote dated, whose bookmarks file
// holds no dates, and classifies them so; another source's dated rows
// stand, and a second start marks nothing.
func TestEnsureSavedDates_MarksSafariDateless(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	// Saved seconds apart, dated rows are lone saves, exact; the Safari
	// ones, dateless, are the first import's, unknown.
	rows := make([]docmeta.SavedRow, 0, 10)
	for i := range 5 {
		rows = append(rows, savedRow(fmt.Sprint("safari-", i), store.SourceSafari, "/A", int64(i)*2_000),
			savedRow(fmt.Sprint("chrome-", i), store.SourceChrome, "/A", int64(i)*2_000))
	}
	insertSavedRows(t, db, "local", rows)
	const old = "2025-01-01T00:00:00.000Z"
	_, err := db.Exec(`UPDATE bookmarks SET updated_at = ?`, old)
	require.NoError(t, err)

	done, err := EnsureSavedDates(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, 5, done.MarkedDateless)
	got := dumpRows(t, db, `SELECT source, dateless, saved_date, count(*), sum(updated_at != ?) FROM bookmarks
		GROUP BY source ORDER BY source`, old)
	assert.Equal(t, [][]any{
		{store.SourceChrome, int64(0), string(store.SavedDateExact), int64(5), int64(5)},
		{store.SourceSafari, int64(1), string(store.SavedDateUnknown), int64(5), int64(5)},
	}, got, "the chrome rows' classes, and the Safari rows' fact, were written")

	again, err := EnsureSavedDates(ctx, db)
	require.NoError(t, err)
	assert.Zero(t, again.MarkedDateless)
	assert.Zero(t, again.Changed)
}

// TestEnsureSavedDates_AfterADeleteSplitsARun: a deleted bookmark that
// held a run together leaves two runs, which the next start derives again.
func TestEnsureSavedDates_AfterADeleteSplitsARun(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	folders := []string{"/A", "/B", "/C", "/A", "/B", "/C", "/A"}
	rows := make([]docmeta.SavedRow, 0, len(folders))
	for i, f := range folders {
		rows = append(rows, savedRow(fmt.Sprint("r", i), store.SourceChrome, f, int64(i)*900))
	}
	insertSavedRows(t, db, "local", rows)
	_, err := EnsureSavedDates(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, store.SavedDateUnknown, storedSavedDates(t, db, "local")["r0"], "one run of 7 over 3 folders")

	require.NoError(t, NewBookmarks(db).Delete(ctx, "r3"))
	done, err := EnsureSavedDates(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, 6, done.Changed)
	got := storedSavedDates(t, db, "local")
	for _, id := range []string{"r0", "r1", "r2", "r4", "r5", "r6"} {
		assert.Equal(t, store.SavedDateExact, got[id], "%s: two runs of 3, each too short", id)
	}
}

// BenchmarkClassifySavedDates classifies one source of a library, as an
// import of it does: the read of every bookmark of the source and the
// rule. Nothing changes after the first iteration, as in an import that
// changes few classes.
func BenchmarkClassifySavedDates(b *testing.B) {
	for _, size := range []int{5_000, 50_000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			ctx := context.Background()
			db := newTestDB(b)
			r := rand.New(rand.NewPCG(1, uint64(size)))
			var rows []docmeta.SavedRow
			for len(rows) < size {
				rows = append(rows, syntheticSource(r, fmt.Sprint("part-", len(rows)), store.SourceChrome)...)
			}
			insertSavedRows(b, db, "local", rows[:size])
			bms := NewBookmarks(db)
			if _, err := bms.ClassifySavedDates(ctx, "local", store.SourceChrome); err != nil {
				b.Fatal(err)
			}
			for b.Loop() {
				if _, err := bms.ClassifySavedDates(ctx, "local", store.SourceChrome); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
