// Package devtool is what the developer's tools that measure curio on a
// copy of a home's database share: opening the copy, refusing a home's
// own, migrating it and building its stores, and checking and writing the
// JSON report. It is the one place those tools reach the SQLite
// implementation; nothing shipped imports it (depguard keeps it to
// cmd/clusterreport and cmd/searcheval).
package devtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
)

// ErrLiveHome refuses a database beside daemon.pid: a tool migrates the
// database it reads, which a home's own daemon must do itself.
var ErrLiveHome = errors.New("refusing a database in a home's directory (it holds daemon.pid): " +
	"the tool migrates what it reads, so point it at a copy")

// Copy is an open copy of a home's database, migrated to this curio's
// schema, with its stores.
type Copy struct {
	Path string
	// Schema is the version the migrations left it at.
	Schema int64
	// Width is its vector index's width, which its chunks are read at.
	Width     int
	Documents store.DocumentStore
	Chunks    store.ChunkStore
	Insights  store.InsightStore

	db *sqlite.DB
}

// OpenCopy opens the database at path, a copy of a home's: it refuses a
// home's own (ErrLiveHome), and anything that isn't an existing file,
// before opening anything, then migrates it, the only write.
func OpenCopy(ctx context.Context, path string) (*Copy, error) {
	if err := checkCopy(path); err != nil {
		return nil, err
	}
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	c, err := migrate(ctx, path, db)
	if err != nil {
		if cerr := db.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", path, cerr))
		}
		return nil, err
	}
	return c, nil
}

// migrate brings db, opened from path, to this curio's schema and builds
// its stores.
func migrate(ctx context.Context, path string, db *sqlite.DB) (*Copy, error) {
	schema, err := sqlite.Migrate(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	width, err := sqlite.VectorIndexWidth(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read the vector index's width in %s: %w", path, err)
	}
	return &Copy{Path: path, Schema: schema, Width: width, Documents: sqlite.NewDocuments(db),
		Chunks: sqlite.NewChunks(db, width), Insights: sqlite.NewInsights(db), db: db}, nil
}

// Close closes the database.
func (c *Copy) Close() error { return c.db.Close() }

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
	switch _, err := os.Stat(filepath.Join(filepath.Dir(resolved), curiohome.PIDFileName)); {
	case err == nil:
		return fmt.Errorf("%w; take one with: sqlite3 -readonly %s \".backup <copy>\" (or, if its daemon is "+
			"stopped and that fails, sqlite3 'file:%s?immutable=1' \".backup <copy>\")", ErrLiveHome, path, path)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("look for %s beside the database: %w", curiohome.PIDFileName, err)
	}
	return nil
}

// CheckOutput fails, before minutes of measuring, unless the JSON report
// can take path's place: its directory exists and takes a new file, and
// the path isn't a directory itself, which the final rename can't replace.
func CheckOutput(path string) error {
	switch info, err := os.Stat(path); {
	case err == nil && info.IsDir():
		return fmt.Errorf("the -json file %s is a directory", path)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("the -json file: %w", err)
	}
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("the -json file's directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("the -json file's directory %s is not a directory", dir)
	}
	// A directory the report can't write to would fail only at the end,
	// after the whole measurement: try a temporary file there now.
	probe, err := os.CreateTemp(dir, ".devtool-probe-*")
	if err != nil {
		return fmt.Errorf("the -json file's directory: %w", err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		return errors.Join(fmt.Errorf("the -json file's directory: %w", err), os.Remove(name))
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("the -json file's directory: %w", err)
	}
	return nil
}

// WriteJSON writes v, indented, to path atomically: a temporary file
// beside it, renamed over it once written, so a failure leaves no partial
// report and no temporary file. os.CreateTemp makes it 0600.
func WriteJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the JSON report: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("write the JSON report: %w", err)
	}
	_, werr := f.Write(append(raw, '\n'))
	if err := errors.Join(werr, f.Close()); err != nil {
		return errors.Join(fmt.Errorf("write the JSON report: %w", err), os.Remove(f.Name()))
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return errors.Join(fmt.Errorf("write the JSON report: %w", err), os.Remove(f.Name()))
	}
	return nil
}
