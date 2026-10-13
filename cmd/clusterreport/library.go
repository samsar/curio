package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/devtool"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
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

// openLibrary reads the library from the database at path, a copy of a
// home's: devtool.OpenCopy refuses a home's own before opening anything,
// then migrates it, the only write; the library is the vectors and the
// latest done run.
func openLibrary(ctx context.Context, path string, log *slog.Logger) (*library, error) {
	start := time.Now()
	c, err := devtool.OpenCopy(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := c.Close(); err != nil {
			log.Warn("close the database", "path", path, "err", err)
		}
	}()
	log.Info("reading the library", "path", path, "schema", c.Schema, "embedding_width", c.Width)
	lib, err := readLibrary(ctx, c.Chunks, c.Insights)
	if err != nil {
		return nil, err
	}
	lib.read = time.Since(start)
	log.Info("read the library", "documents", len(lib.docs), "dropped_non_finite", lib.dropped,
		"stored_run", lib.stored != nil, "ms", lib.read.Milliseconds())
	return lib, nil
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
