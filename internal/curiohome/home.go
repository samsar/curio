// Package curiohome manages the $CURIO_HOME directory layout — the root
// under which the daemon stores its database, extracted content, logs, and
// the marker file that identifies the directory as ours.
//
// Defaults to ~/.curio. Overridable via the CURIO_HOME environment variable.
package curiohome

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	DefaultDirName   = ".curio"
	MarkerFile       = ".curio-meta.json"
	ConfigFile       = "config.yaml"
	FetcherRulesFile = "fetcher_rules.yaml"
	DBFile           = "curio.db"
	ContentDirName   = "content"
	LogsDirName      = "logs"
	PIDFileName      = "daemon.pid"

	// CurrentSchemaVersion is bumped when the on-disk layout or marker
	// shape changes in an incompatible way.
	CurrentSchemaVersion = 1

	dirPerm  = 0o700
	filePerm = 0o600
)

// Errors returned by Open and Init. Use errors.Is to discriminate.
var (
	// ErrNotInitialized: the home directory does not exist yet.
	// Remediation: call Init.
	ErrNotInitialized = errors.New("curio home not initialized")

	// ErrNotOurs: the directory exists but lacks our marker file. We refuse
	// to touch directories that weren't created by curio.
	// Remediation: set CURIO_HOME to a different path, or remove the dir.
	ErrNotOurs = errors.New("directory exists but is not a curio home")

	// ErrAlreadyInitialized: Init was called on a directory that already
	// contains our marker.
	ErrAlreadyInitialized = errors.New("curio home already initialized")
)

// Meta mirrors the on-disk .curio-meta.json file. Cross-checked against the
// schema_meta SQL table at daemon startup; a mismatch is a startup error.
type Meta struct {
	SchemaVersion  int       `json:"schema_version"`
	EmbeddingModel string    `json:"embedding_model"`
	EmbeddingDim   int       `json:"embedding_dim"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Home is a verified handle to a curio home directory. Construct via Open or
// Init — never directly.
type Home struct {
	Path string
}

// Resolve returns the configured CURIO_HOME path without touching the
// filesystem. Honors $CURIO_HOME, falling back to ~/.curio.
func Resolve() (string, error) {
	if v := os.Getenv("CURIO_HOME"); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", fmt.Errorf("resolve CURIO_HOME=%q: %w", v, err)
		}
		return abs, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(userHome, DefaultDirName), nil
}

// Init creates a new curio home at path. Fails with ErrAlreadyInitialized if
// a marker file is already present. Creates subdirectories for content and
// logs. The marker captures the embedding model and dimension so later
// startups can detect configuration drift.
func Init(path, embeddingModel string, embeddingDim int) (*Home, error) {
	if err := os.MkdirAll(path, dirPerm); err != nil {
		return nil, fmt.Errorf("create home %q: %w", path, err)
	}
	markerPath := filepath.Join(path, MarkerFile)
	if _, err := os.Stat(markerPath); err == nil {
		return nil, ErrAlreadyInitialized
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("stat marker %q: %w", markerPath, err)
	}

	h := &Home{Path: path}
	now := time.Now().UTC()
	if err := h.WriteMeta(Meta{
		SchemaVersion:  CurrentSchemaVersion,
		EmbeddingModel: embeddingModel,
		EmbeddingDim:   embeddingDim,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		return nil, err
	}
	for _, sub := range []string{ContentDirName, LogsDirName} {
		if err := os.MkdirAll(filepath.Join(path, sub), dirPerm); err != nil {
			return nil, fmt.Errorf("create %s: %w", sub, err)
		}
	}
	return h, nil
}

// Open verifies path exists and is a valid curio home. Returns
// ErrNotInitialized if path is missing, ErrNotOurs if it exists without our
// marker.
func Open(path string) (*Home, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotInitialized, path)
	}
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("curio home %q is not a directory", path)
	}
	markerPath := filepath.Join(path, MarkerFile)
	if _, err := os.Stat(markerPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s exists but %s is missing (set CURIO_HOME to a different path)",
				ErrNotOurs, path, MarkerFile)
		}
		return nil, fmt.Errorf("stat marker %q: %w", markerPath, err)
	}
	return &Home{Path: path}, nil
}

// Path helpers. Pure string operations; no filesystem access.

func (h *Home) MarkerPath() string       { return filepath.Join(h.Path, MarkerFile) }
func (h *Home) ConfigPath() string       { return filepath.Join(h.Path, ConfigFile) }
func (h *Home) FetcherRulesPath() string { return filepath.Join(h.Path, FetcherRulesFile) }
func (h *Home) DBPath() string           { return filepath.Join(h.Path, DBFile) }
func (h *Home) ContentDir() string       { return filepath.Join(h.Path, ContentDirName) }
func (h *Home) LogsDir() string          { return filepath.Join(h.Path, LogsDirName) }
func (h *Home) PIDFile() string          { return filepath.Join(h.Path, PIDFileName) }

// Meta re-reads the marker file each call. Returns the parsed struct.
func (h *Home) Meta() (Meta, error) {
	data, err := os.ReadFile(h.MarkerPath())
	if err != nil {
		return Meta{}, fmt.Errorf("read marker: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("parse marker: %w", err)
	}
	return m, nil
}

// WriteMeta atomically replaces the marker file. Writes to a temp file and
// renames into place so an interrupted write can't leave a half-written marker.
func (h *Home) WriteMeta(m Meta) error {
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = time.Now().UTC()
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode marker: %w", err)
	}
	data = append(data, '\n')

	final := h.MarkerPath()
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, filePerm); err != nil {
		return fmt.Errorf("write marker tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename marker: %w", err)
	}
	return nil
}
