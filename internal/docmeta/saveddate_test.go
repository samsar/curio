package docmeta

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/store"
)

// savedEpoch is when the synthetic libraries' first import ran.
var savedEpoch = time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)

// at is savedEpoch plus ms milliseconds.
func at(ms int64) time.Time { return savedEpoch.Add(time.Duration(ms) * time.Millisecond) }

// dated is a dated row of source saved ms after savedEpoch, stored at
// savedEpoch, in folder.
func dated(id, source, folder string, ms int64) SavedRow {
	return SavedRow{ID: id, Source: source, Folder: folder, SavedAt: at(ms), CreatedAt: savedEpoch}
}

// dateless is a dateless row of source stored ms after savedEpoch, its
// saved time the time it was stored.
func dateless(id, source string, ms int64) SavedRow {
	return SavedRow{ID: id, Source: source, SavedAt: at(ms), CreatedAt: at(ms), Dateless: true}
}

// run is n dated rows of source, the first saved at ms and each 10 ms
// after the one before, in folders, a folder a row in turn.
func run(prefix, source string, ms int64, folders ...string) []SavedRow {
	out := make([]SavedRow, 0, len(folders))
	for i, f := range folders {
		out = append(out, dated(fmt.Sprintf("%s%02d", prefix, i), source, f, ms+int64(i)*10))
	}
	return out
}

// repeat is folder n times.
func repeat(folder string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = folder
	}
	return out
}

// classes are SavedDates of rows, by ID.
func classes(rows []SavedRow) map[string]SavedDate {
	got := SavedDates(rows)
	out := make(map[string]SavedDate, len(rows))
	for i, r := range rows {
		out[r.ID] = got[i]
	}
	return out
}

// allOf is every row's ID with class.
func allOf(rows []SavedRow, class SavedDate) map[string]SavedDate {
	out := make(map[string]SavedDate, len(rows))
	for _, r := range rows {
		out[r.ID] = class
	}
	return out
}

func TestSavedDates(t *testing.T) {
	chrome := store.SourceChrome
	cases := []struct {
		name string
		rows []SavedRow
		want map[string]SavedDate
	}{
		{
			name: "a bulk run is an import's stamp",
			rows: run("b", chrome, 0, "/A", "/A", "/B", "/B", "/C"),
			want: allOf(run("b", chrome, 0, "/A", "/A", "/B", "/B", "/C"), SavedUnknown),
		},
		{
			name: "a tab save fills one folder",
			rows: run("t", chrome, 0, repeat("/Tabs", 6)...),
			want: allOf(run("t", chrome, 0, repeat("/Tabs", 6)...), SavedExact),
		},
		{
			name: "a largest folder of exactly 80% is a tab save",
			rows: run("e", chrome, 0, append(repeat("/A", 8), "/B", "/C")...),
			want: allOf(run("e", chrome, 0, append(repeat("/A", 8), "/B", "/C")...), SavedExact),
		},
		{
			name: "a largest folder under 80% is an import",
			rows: run("u", chrome, 0, append(repeat("/A", 7), "/B", "/B", "/C")...),
			want: allOf(run("u", chrome, 0, append(repeat("/A", 7), "/B", "/B", "/C")...), SavedUnknown),
		},
		{
			name: "four rows are too few for an import",
			rows: run("f", chrome, 0, "/A", "/B", "/C", "/D"),
			want: allOf(run("f", chrome, 0, "/A", "/B", "/C", "/D"), SavedExact),
		},
		{
			name: "a folder copied whole",
			rows: run("c", chrome, 0, "/A", "/A/x", "/A/x", "/A/y", "/A/y/z"),
			want: allOf(run("c", chrome, 0, "/A", "/A/x", "/A/x", "/A/y", "/A/y/z"), SavedApprox),
		},
		{
			name: "a root alone is no folder a copy makes",
			rows: run("r", chrome, 0, "/Other Bookmarks", "/Other Bookmarks/x", "/Other Bookmarks/x",
				"/Other Bookmarks/y", "/Other Bookmarks/z"),
			want: allOf(run("r", chrome, 0, "/Other Bookmarks", "/Other Bookmarks/x", "/Other Bookmarks/x",
				"/Other Bookmarks/y", "/Other Bookmarks/z"), SavedUnknown),
		},
		{
			name: "a root alone in another case",
			rows: run("l", chrome, 0, "/bookmarks bar", "/bookmarks bar/x", "/bookmarks bar/y",
				"/bookmarks bar/y", "/bookmarks bar/z"),
			want: allOf(run("l", chrome, 0, "/bookmarks bar", "/bookmarks bar/x", "/bookmarks bar/y",
				"/bookmarks bar/y", "/bookmarks bar/z"), SavedUnknown),
		},
		{
			name: "two equally short folders, neither the other's",
			rows: run("s", chrome, 0, "/A", "/B", "/A/x", "/B/y", "/A/z"),
			want: allOf(run("s", chrome, 0, "/A", "/B", "/A/x", "/B/y", "/A/z"), SavedUnknown),
		},
		{
			name: "a folder that only starts like another",
			rows: run("p", chrome, 0, "/Tech", "/Technology", "/Tech/x", "/Tech/x", "/Tech/y"),
			want: allOf(run("p", chrome, 0, "/Tech", "/Technology", "/Tech/x", "/Tech/x", "/Tech/y"), SavedUnknown),
		},
		{
			name: "no folder holds a folder",
			rows: run("n", chrome, 0, "", "/A", "/A", "/B", "/C"),
			want: allOf(run("n", chrome, 0, "", "/A", "/A", "/B", "/C"), SavedUnknown),
		},
		{
			name: "dated manual rows are saves, however close",
			rows: run("m", store.SourceManual, 0, "/A", "/A", "/B", "/B", "/C"),
			want: allOf(run("m", store.SourceManual, 0, "/A", "/A", "/B", "/B", "/C"), SavedExact),
		},
		{
			name: "a dateless first import",
			rows: []SavedRow{dateless("d1", store.SourceSafari, 0), dateless("d2", store.SourceSafari, 1000)},
			want: map[string]SavedDate{"d1": SavedUnknown, "d2": SavedUnknown},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classes(tc.rows))
		})
	}
}

// TestSavedDates_RunGap: consecutive saves exactly runGap apart join a run,
// a millisecond more splits it, compared as whole milliseconds.
func TestSavedDates_RunGap(t *testing.T) {
	folders := []string{"/A", "/A", "/B", "/B", "/C"}
	spaced := func(gap int64) []SavedRow {
		out := make([]SavedRow, len(folders))
		for i, f := range folders {
			out[i] = dated(strconv.Itoa(i), store.SourceFirefox, f, int64(i)*gap)
		}
		return out
	}
	assert.Equal(t, allOf(spaced(1000), SavedUnknown), classes(spaced(1000)))
	assert.Equal(t, allOf(spaced(1001), SavedExact), classes(spaced(1001)))

	// The store keeps whole milliseconds, so a save at 1000.9 ms is one at
	// 1000 ms, 1000 ms after one at 0.
	sub := spaced(1000)
	for i := 1; i < len(sub); i += 2 {
		sub[i].SavedAt = sub[i].SavedAt.Add(900 * time.Microsecond)
	}
	assert.Equal(t, allOf(sub, SavedUnknown), classes(sub))
}

// TestSavedDates_Dateless: a dateless row first seen by a later import of a
// browser's collection is approx; one in the source's first import, or
// from an HTML file or a manual import, is unknown.
func TestSavedDates_Dateless(t *testing.T) {
	const limit = int64(10 * 60 * 1000)
	for _, source := range []string{store.SourceChrome, store.SourceFirefox, store.SourceSafari, store.SourceHTML,
		store.SourceManual} {
		t.Run(source, func(t *testing.T) {
			rows := []SavedRow{
				dateless("first", source, 0),
				dateless("same-import", source, limit),
				dateless("later", source, limit+1),
			}
			later := SavedApprox
			if source == store.SourceHTML || source == store.SourceManual {
				later = SavedUnknown
			}
			assert.Equal(t, map[string]SavedDate{"first": SavedUnknown, "same-import": SavedUnknown, "later": later},
				classes(rows))
		})
	}
	// The group's first row is the first stored, dated or not.
	rows := []SavedRow{dated("old", store.SourceChrome, "/A", 0), dateless("new", store.SourceChrome, limit+1)}
	assert.Equal(t, map[string]SavedDate{"old": SavedExact, "new": SavedApprox}, classes(rows))
}

// TestSavedDates_DatelessRowsLeaveRunsAlone: dateless rows saved inside a
// dated burst neither join it nor split it.
func TestSavedDates_DatelessRowsLeaveRunsAlone(t *testing.T) {
	// withGaps is rows with a dateless row of the same import saved
	// halfway between each two, in a folder of its own.
	withGaps := func(rows []SavedRow) []SavedRow {
		out := slices.Clone(rows)
		for i := 1; i < len(rows); i++ {
			mid := rows[i-1].SavedAt.Add(rows[i].SavedAt.Sub(rows[i-1].SavedAt) / 2)
			out = append(out, SavedRow{ID: fmt.Sprint("gap", i), Source: rows[i].Source, Folder: fmt.Sprint("/Z", i),
				SavedAt: mid, CreatedAt: savedEpoch, Dateless: true})
		}
		return out
	}
	// Four saves over three folders are too few for an import; joined by
	// the dateless rows they would be one.
	short := withGaps(run("s", store.SourceChrome, 0, "/A", "/A", "/B", "/C"))
	got := classes(short)
	for _, id := range []string{"s00", "s01", "s02", "s03"} {
		assert.Equal(t, SavedExact, got[id], id)
	}
	// Five saves 900 ms apart over three folders are an import; split by
	// the dateless rows they would be five saves.
	spread := make([]SavedRow, 0, 5)
	for i, f := range []string{"/A", "/A", "/B", "/B", "/C"} {
		spread = append(spread, dated(fmt.Sprint("w", i), store.SourceChrome, f, int64(i)*900))
	}
	got = classes(withGaps(spread))
	for _, r := range spread {
		assert.Equal(t, SavedUnknown, got[r.ID], r.ID)
	}
	for i := 1; i < 5; i++ {
		assert.Equal(t, SavedUnknown, got[fmt.Sprint("gap", i)], "a dateless row of the first import")
	}
}

// TestSavedDates_SourcesApart: each source's rows are a group of their
// own: runs never mix sources, and a source's first import is its own.
func TestSavedDates_SourcesApart(t *testing.T) {
	rows := slices.Concat(run("c", store.SourceChrome, 0, "/A", "/B", "/C"), run("f", store.SourceFirefox, 30, "/D", "/E"))
	assert.Equal(t, allOf(rows, SavedExact), classes(rows), "five rows of two sources are two runs too short")

	rows = []SavedRow{dated("old", store.SourceChrome, "/A", 0), dateless("new", store.SourceSafari, 11*60*1000)}
	assert.Equal(t, map[string]SavedDate{"old": SavedExact, "new": SavedUnknown}, classes(rows),
		"safari's first import, whatever chrome's")
}

// TestSavedDates_AnyOrder: the classes are the rows', whatever order they
// come in.
func TestSavedDates_AnyOrder(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	rows := slices.Concat(
		run("bulk", store.SourceChrome, 0, "/A", "/A", "/B", "/B", "/C"),
		run("copy", store.SourceChrome, 60_000, "/F", "/F/x", "/F/x", "/F/y", "/F/y/z"),
		run("tabs", store.SourceChrome, 120_000, repeat("/T", 6)...),
		[]SavedRow{dateless("first", store.SourceSafari, 0), dateless("later", store.SourceSafari, 11*60*1000)},
		// Equal saved times, ordered by ID.
		run("tie", store.SourceFirefox, 0, "/A", "/B", "/C", "/D", "/E"))
	for i := range rows {
		if rows[i].Source == store.SourceFirefox {
			rows[i].SavedAt = savedEpoch
		}
	}
	want := classes(rows)
	for range 20 {
		rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
		assert.Equal(t, want, classes(rows))
	}
}

// TestSavedDates_SourcesMatchTheStore: the rule names the store's sources.
func TestSavedDates_SourcesMatchTheStore(t *testing.T) {
	assert.Equal(t, store.SourceChrome, sourceChrome)
	assert.Equal(t, store.SourceFirefox, sourceFirefox)
	assert.Equal(t, store.SourceSafari, sourceSafari)
	assert.Equal(t, store.SourceManual, sourceManual)
}

func TestSavedDates_Empty(t *testing.T) {
	assert.Empty(t, SavedDates(nil))
}
