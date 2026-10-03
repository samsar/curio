package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
)

// library is what the report reads from the copy.
type library struct {
	// docs are the fetched documents' vectors, by document ID, without
	// those with a NaN or infinite component.
	docs    []store.DocVector
	dropped int
	read    time.Duration
	// stored is the latest done interests run, nil when the copy has none.
	stored *storedRun
}

// storedRun is a done run as the store holds it.
type storedRun struct {
	run         *store.InterestRun
	groups      []store.InterestGroup
	assignments []store.InterestAssignment
}

// errLiveHome refuses a database beside daemon.pid: the report migrates the
// database it reads, which a home's own daemon must do itself.
var errLiveHome = errors.New("refusing a database in a home's directory (it holds daemon.pid): " +
	"the report migrates what it reads, so point it at a copy")

// openLibrary reads the library from the database at path, a copy of a
// home's: it refuses a home's own (errLiveHome) before opening anything,
// then migrates it, the only write, and reads the vectors and the latest
// done run.
func openLibrary(ctx context.Context, path string, log *slog.Logger) (*library, error) {
	if err := checkCopy(path); err != nil {
		return nil, err
	}
	start := time.Now()
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Warn("close the database", "path", path, "err", err)
		}
	}()
	version, err := sqlite.Migrate(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	width, err := sqlite.VectorIndexWidth(ctx, db)
	if err != nil {
		return nil, err
	}
	log.Info("reading the library", "path", path, "schema", version, "embedding_width", width)
	lib, err := readLibrary(ctx, sqlite.NewChunks(db, width), sqlite.NewInsights(db))
	if err != nil {
		return nil, err
	}
	lib.read = time.Since(start)
	log.Info("read the library", "documents", len(lib.docs), "dropped_non_finite", lib.dropped,
		"stored_run", lib.stored != nil, "ms", lib.read.Milliseconds())
	return lib, nil
}

// checkCopy fails unless path is an existing file outside a home: Open
// would create a missing one, and a directory holding daemon.pid is a
// home, whose daemon may be running.
func checkCopy(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("the database: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("the database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("the database %s is not a file", path)
	}
	switch _, err := os.Stat(filepath.Join(filepath.Dir(resolved), "daemon.pid")); {
	case err == nil:
		return fmt.Errorf("%w; take one with: sqlite3 -readonly %s \".backup <copy>\"", errLiveHome, path)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("look for daemon.pid beside the database: %w", err)
	}
	return nil
}

// readLibrary reads the local tenant's document vectors, dropping those
// with a NaN or infinite component as the engine does, and its latest done
// run.
func readLibrary(ctx context.Context, chunks store.ChunkStore, insights store.InsightStore) (*library, error) {
	dvs, err := chunks.DocumentVectors(ctx, store.LocalTenantID)
	if err != nil {
		return nil, err
	}
	lib := &library{docs: make([]store.DocVector, 0, len(dvs))}
	for _, dv := range dvs {
		if slices.ContainsFunc(dv.Vector, nonFinite) {
			lib.dropped++
			continue
		}
		lib.docs = append(lib.docs, dv)
	}
	// The draws index the library in ID order, the order the store
	// returns, which the engine prepares the points in.
	slices.SortFunc(lib.docs, func(a, b store.DocVector) int { return strings.Compare(a.DocumentID, b.DocumentID) })
	if len(lib.docs) < insight.FirstRebuildAt {
		return nil, fmt.Errorf("%d documents have a usable vector, fewer than the %d a first grouping waits for",
			len(lib.docs), insight.FirstRebuildAt)
	}
	if err := lib.readStoredRun(ctx, insights); err != nil {
		return nil, err
	}
	return lib, nil
}

func nonFinite(x float32) bool {
	f := float64(x)
	return math.IsNaN(f) || math.IsInf(f, 0)
}

// readStoredRun reads the latest done run with its groups (labels joined)
// and assignments, leaving lib.stored nil when there is none.
func (lib *library) readStoredRun(ctx context.Context, insights store.InsightStore) error {
	run, err := insights.LatestRun(ctx, store.LocalTenantID, store.InterestRunDone)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("read the latest done run: %w", err)
	}
	groups, err := insights.RunGroups(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("read run %s: %w", run.ID, err)
	}
	assignments, err := insights.RunAssignments(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("read run %s: %w", run.ID, err)
	}
	// TopGroups' order: size, then cohesion, both descending, then ID.
	slices.SortFunc(groups, func(a, b store.InterestGroup) int {
		return cmp.Or(cmp.Compare(b.Size, a.Size), cmp.Compare(b.Cohesion, a.Cohesion), strings.Compare(a.ID, b.ID))
	})
	lib.stored = &storedRun{run: run, groups: groups, assignments: assignments}
	return nil
}
