package docmeta

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"
)

// SavedDate is how far a bookmark's saved time can be trusted as the time
// its owner saved it.
//
// The rule is a function of a (tenant, source) group's stored rows alone,
// so the store derives every class again whenever it likes, at every
// start and after every import, and the classes of a library don't depend
// on how its imports were split into requests. No derived row is keyed by
// it, so it is outside RulesVersion.
type SavedDate string

const (
	// SavedExact: the time is when the bookmark was saved, as its browser
	// or curio recorded it.
	SavedExact SavedDate = "exact"
	// SavedApprox: the bookmark was saved at or before the time: a folder
	// copied whole, or a save first seen by a later import of the same
	// collection.
	SavedApprox SavedDate = "approx"
	// SavedUnknown: the time says nothing about when the bookmark was
	// saved: a browser's bulk import or sync stamped it, or the source kept
	// no date and the import stored its own time.
	SavedUnknown SavedDate = "unknown"
)

// The constants of the saved-date rule (SavedDates).
const (
	// runGap is the longest gap between two consecutive saved times of one
	// run: a browser that imports or merges a bookmark tree stamps it
	// within milliseconds, and a person saves a page at most once a
	// second, a "Bookmark all tabs" aside.
	runGap = time.Second
	// importMinRows and importMinFolders are the fewest bookmarks and
	// distinct folders a run spans to be an import's stamp.
	importMinRows    = 5
	importMinFolders = 3
	// importLargestNum/importLargestDen: a run whose largest folder holds
	// at least this share of it was saved into that folder, all tabs at
	// once, not stamped by an import.
	importLargestNum = 4
	importLargestDen = 5
	// sameImport is how long one import of a source may run: a dateless
	// bookmark first stored more than this after the source's first
	// bookmark was seen by a later import of the collection.
	sameImport = 10 * time.Minute
)

// Bookmark sources as the store names them (bookmarks.source); a test
// holds them to store's.
const (
	sourceChrome  = "chrome"
	sourceFirefox = "firefox"
	sourceSafari  = "safari"
	sourceManual  = "manual"
)

// SavedRow is one bookmark as the saved-date rule reads it.
type SavedRow struct {
	ID     string
	Source string
	// Folder is its folder path as stored ("/Bookmarks Bar/Tech"), "" for
	// none.
	Folder string
	// SavedAt is when it was saved, as stored: from its source, or the
	// time the import stored it when Dateless.
	SavedAt time.Time
	// CreatedAt is when curio stored it.
	CreatedAt time.Time
	// Dateless: its source gave no save date, so SavedAt is the time the
	// import stored it.
	Dateless bool
}

// SavedDates classifies each of rows, in any order and of any sources, by
// the group of rows of its source; the result is parallel to rows. The
// caller passes every row of a tenant's source, since a class depends on
// the rows around it:
//
//   - A dateless row is unknown when its source is an HTML file or a
//     manual import, each a collection of its own, so a later one says
//     nothing about when its rows were saved; unknown when it was stored
//     within sameImport of the group's first row, the import that first
//     read the collection; and approx otherwise, saved between the
//     collection's previous import and the one that found it.
//   - A dated manual row is exact: curio, or the person scripting curio
//     add, recorded it.
//   - The other dated rows form runs, chains in (SavedAt, ID) order whose
//     consecutive saved times are at most runGap apart; dateless rows
//     never join or split one. A run of importMinRows or more over
//     importMinFolders or more folders, no folder holding 4/5 of it, is an
//     import's stamp: approx when it is a folder copied whole (a folder
//     that is neither none nor a browser's root alone holds every folder
//     of the run, itself or under it), unknown otherwise.
//   - Every other row is exact.
//
// Times are compared as whole milliseconds, as the store keeps them.
func SavedDates(rows []SavedRow) []SavedDate {
	out := make([]SavedDate, len(rows))
	bySource := map[string][]int{}
	for i, r := range rows {
		bySource[r.Source] = append(bySource[r.Source], i)
	}
	for source, group := range bySource {
		classifyGroup(source, rows, group, out)
	}
	return out
}

// classifyGroup writes the classes of rows[group], the rows of source,
// into out.
func classifyGroup(source string, rows []SavedRow, group []int, out []SavedDate) {
	first := rows[group[0]].CreatedAt.UnixMilli()
	for _, i := range group {
		first = min(first, rows[i].CreatedAt.UnixMilli())
	}
	var dated []int
	for _, i := range group {
		r := rows[i]
		switch {
		case r.Dateless:
			out[i] = datelessClass(source, r.CreatedAt.UnixMilli()-first)
		case source == sourceManual:
			out[i] = SavedExact
		default:
			dated = append(dated, i)
		}
	}
	slices.SortFunc(dated, func(a, b int) int {
		return cmp.Or(cmp.Compare(rows[a].SavedAt.UnixMilli(), rows[b].SavedAt.UnixMilli()),
			strings.Compare(rows[a].ID, rows[b].ID))
	})
	for len(dated) > 0 {
		n := runLength(rows, dated)
		class := runClass(rows, dated[:n])
		for _, i := range dated[:n] {
			out[i] = class
		}
		dated = dated[n:]
	}
}

// datelessClass is the class of a dateless row of source stored
// sinceFirst milliseconds after the group's first row.
func datelessClass(source string, sinceFirst int64) SavedDate {
	if !rereadsOneCollection(source) || sinceFirst <= sameImport.Milliseconds() {
		return SavedUnknown
	}
	return SavedApprox
}

// rereadsOneCollection reports whether every import of source reads the
// same collection again: a browser's bookmarks, as opposed to an HTML
// file or a manual import, each a collection of its own.
func rereadsOneCollection(source string) bool {
	switch source {
	case sourceChrome, sourceFirefox, sourceSafari:
		return true
	}
	return false
}

// runLength is how many of sorted, dated rows in (SavedAt, ID) order, the
// first run takes.
func runLength(rows []SavedRow, sorted []int) int {
	n := 1
	for n < len(sorted) &&
		rows[sorted[n]].SavedAt.UnixMilli()-rows[sorted[n-1]].SavedAt.UnixMilli() <= runGap.Milliseconds() {
		n++
	}
	return n
}

// runClass is the class of every row of run.
func runClass(rows []SavedRow, run []int) SavedDate {
	counts := map[string]int{}
	for _, i := range run {
		counts[rows[i].Folder]++
	}
	largest := 0
	for _, n := range counts {
		largest = max(largest, n)
	}
	importShaped := len(run) >= importMinRows && len(counts) >= importMinFolders &&
		importLargestDen*largest < importLargestNum*len(run)
	switch {
	case !importShaped:
		return SavedExact
	case isFolderCopy(counts):
		return SavedApprox
	}
	return SavedUnknown
}

// isFolderCopy reports whether one of a run's folders holds every other:
// a folder that isn't none or a browser's root alone, which every folder
// of the run is, or is under. Only the shortest can.
func isFolderCopy(folders map[string]int) bool {
	top := slices.MinFunc(slices.Collect(maps.Keys(folders)), func(a, b string) int {
		return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b))
	})
	if top == "" || isRootAlone(top) {
		return false
	}
	for f := range folders {
		if f != top && !strings.HasPrefix(f, top+"/") {
			return false
		}
	}
	return true
}

// isRootAlone reports whether path names a browser's root and nothing
// under it ("/Other Bookmarks"), compared case-insensitively as
// FolderText compares a root.
func isRootAlone(path string) bool {
	segments := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	return len(segments) == 1 && isRootLabel(segments[0])
}
