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
	// DaemonLogFile, in the logs dir, gets the daemon's structured log,
	// which it writes to stdout: clients that spawn it point its stdout and
	// stderr there, and its launchd agent its stdout.
	DaemonLogFile = "daemon.log"
	// LaunchdErrFile, in the logs dir, gets what a daemon run by its
	// launchd agent writes to stderr: only what the Go runtime prints as
	// the process dies (a panic's trace, a fatal error).
	LaunchdErrFile = "launchd.err"
	// PIDFileName is the daemon's single-instance lock; the running daemon
	// holds an exclusive flock on it and records its PID inside.
	PIDFileName = "daemon.pid"
	// StartLockFileName serializes clients that auto-start the daemon.
	StartLockFileName = "daemon.start.lock"

	// CurrentSchemaVersion is the placeholder schema version Init writes
	// into a new marker. The daemon's first start replaces it with the
	// version its migrations leave the database at (see Meta).
	CurrentSchemaVersion = 1

	// CurrentFormat is the home layout Init writes and this curio serves.
	// Format 2 sizes the vector index from the marker's embedding width and
	// embeds with the prompts of the model it records; a home without a
	// format, or with an older one, is legacy (see ErrLegacyHome).
	CurrentFormat = 2

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

	// ErrLegacyHome: the marker predates CurrentFormat. Its vectors were
	// made under rules this curio no longer follows, so it is never served;
	// nothing converts it. Remediation: start a new home (`curio up
	// --fresh` moves this one aside) and import the bookmarks again.
	ErrLegacyHome = errors.New("curio home from an older curio")

	// ErrNewerHome: the marker names a format past CurrentFormat, written
	// by a newer curio under rules this one doesn't know. Serving it could
	// corrupt it; the fix is to upgrade curio.
	ErrNewerHome = errors.New("curio home from a newer curio")
)

// Meta mirrors the on-disk .curio-meta.json file.
//
// SchemaVersion is a cache of the database's schema version, whose source
// of truth is goose's goose_db_version table. The daemon rewrites it after
// migrating, so commands that don't reach the daemon can still show it.
//
// EmbeddingModel and EmbeddingDim say what made the home's vectors, and
// EmbeddingDim is the width of its vector index; both are fixed at Init.
// CheckEmbedding holds config.yaml to them. EmbeddingModelDigest and
// OllamaVersion fingerprint the build that made them: the daemon records
// them at its first successful check. When either changes it re-embeds a
// sample of the library, records the new build if the vectors come back
// the same, and reports a drift if they don't or can't be checked
// (internal/drift).
type Meta struct {
	Format               int       `json:"format"`
	SchemaVersion        int       `json:"schema_version"`
	EmbeddingModel       string    `json:"embedding_model"`
	EmbeddingDim         int       `json:"embedding_dim"`
	EmbeddingModelDigest string    `json:"embedding_model_digest,omitempty"`
	OllamaVersion        string    `json:"ollama_version,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Embedding names an embedding model and the width of its vectors.
type Embedding struct {
	Model string
	Dim   int
}

func (e Embedding) String() string { return fmt.Sprintf("%q (dim %d)", e.Model, e.Dim) }

// EmbeddingMismatchError: config.yaml asks for an embedding model or width
// other than the one the home's vectors were made with. Searching them
// with another model's query vectors returns noise, and the vector index
// takes only its own width.
type EmbeddingMismatchError struct {
	Recorded   Embedding // the marker's: what made the home's vectors
	Configured Embedding // config.yaml's
}

func (e *EmbeddingMismatchError) Error() string {
	return fmt.Sprintf("embedding model mismatch: configured %v, but this home's vectors were made with %v",
		e.Configured, e.Recorded)
}

// CheckEmbedding reports whether this home can serve embeddings from model
// at dim: ErrLegacyHome for a marker older than CurrentFormat and
// ErrNewerHome for one past it, checked first, or an
// *EmbeddingMismatchError when model or dim differ from the recorded ones.
func (m Meta) CheckEmbedding(model string, dim int) error {
	switch {
	case m.Format < CurrentFormat:
		return ErrLegacyHome
	case m.Format > CurrentFormat:
		return ErrNewerHome
	}
	configured := Embedding{Model: model, Dim: dim}
	if recorded := (Embedding{Model: m.EmbeddingModel, Dim: m.EmbeddingDim}); recorded != configured {
		return &EmbeddingMismatchError{Recorded: recorded, Configured: configured}
	}
	return nil
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
	return DefaultPath()
}

// DefaultPath is ~/.curio, the home used when $CURIO_HOME is unset.
func DefaultPath() (string, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(userHome, DefaultDirName), nil
}

// CanonicalPath resolves symlinks in path where it can (on macOS /tmp is
// /private/tmp), so two spellings of one directory compare equal. A path
// that doesn't exist here is cleaned and compared as written.
func CanonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// Init creates a new curio home at path, in CurrentFormat. Fails with
// ErrAlreadyInitialized if a marker file is already present. Creates
// subdirectories for content and logs. The marker records the embedding
// model and the width of its vectors, which is the home's vector width for
// good: later startups hold config.yaml to them.
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
	if err := h.WriteMeta(Meta{
		Format:         CurrentFormat,
		SchemaVersion:  CurrentSchemaVersion,
		EmbeddingModel: embeddingModel,
		EmbeddingDim:   embeddingDim,
		CreatedAt:      time.Now().UTC(),
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
// marker. A legacy home opens: whether it can be served is CheckEmbedding's
// call, and commands such as `curio doctor` still report on it.
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
func (h *Home) DaemonLogPath() string    { return filepath.Join(h.LogsDir(), DaemonLogFile) }
func (h *Home) LaunchdErrPath() string   { return filepath.Join(h.LogsDir(), LaunchdErrFile) }
func (h *Home) PIDFile() string          { return filepath.Join(h.Path, PIDFileName) }
func (h *Home) StartLockFile() string    { return filepath.Join(h.Path, StartLockFileName) }

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

// CheckEmbedding reads the marker and holds it to config.yaml's embedding
// model and dim (Meta.CheckEmbedding), returning the marker when the home
// can serve them. A refusal still matches ErrLegacyHome, ErrNewerHome or
// *EmbeddingMismatchError, and says which files disagree, what they record
// and what to do: the daemon refuses to start with it, and `curio doctor`
// reports it.
func (h *Home) CheckEmbedding(model string, dim int) (Meta, error) {
	meta, err := h.Meta()
	if err != nil {
		return Meta{}, err
	}
	err = meta.CheckEmbedding(model, dim)
	var mismatch *EmbeddingMismatchError
	switch {
	case err == nil:
		return meta, nil
	case errors.Is(err, ErrLegacyHome):
		return Meta{}, fmt.Errorf("%w: %s was made before home format %d, which this curio needs; "+
			"its marker %s records vectors from %v, and nothing converts them. To go on, %s, "+
			"or move it aside yourself; then import your bookmarks again",
			err, h.Path, CurrentFormat, h.MarkerPath(),
			Embedding{Model: meta.EmbeddingModel, Dim: meta.EmbeddingDim}, h.freshHint())
	case errors.Is(err, ErrNewerHome):
		return Meta{}, fmt.Errorf("%w: %s is home format %d, which a newer curio wrote; this one serves format %d. "+
			"Upgrade curio (brew upgrade curio) and restart the daemon", err, h.Path, meta.Format, CurrentFormat)
	case errors.As(err, &mismatch):
		return Meta{}, fmt.Errorf("%w; %s sets the first and the marker %s records the second. "+
			"Set embedding.model and embedding.dim back to the recorded values, or, to embed with the new model, %s",
			err, h.ConfigPath(), h.MarkerPath(), h.freshHint())
	default:
		return Meta{}, err
	}
}

// freshHint is how to start over in a new home, keeping this one.
func (h *Home) freshHint() string {
	return fmt.Sprintf("start a new home with `curio up --fresh`, which moves this one aside to "+
		"%s.bak-<YYYYMMDD-HHMMSS> and deletes nothing", h.Path)
}

// WriteMeta replaces the marker file with m, stamping UpdatedAt with the
// current time. It writes a temp file, syncs it to disk and renames it into
// place, so neither an interrupted write nor a power loss leaves a
// half-written or empty marker, which would fail every command with "parse
// marker".
func (h *Home) WriteMeta(m Meta) error {
	m.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode marker: %w", err)
	}
	data = append(data, '\n')

	final := h.MarkerPath()
	tmp := final + ".tmp"
	if err := writeSynced(tmp, data); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write marker tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename marker: %w", err)
	}
	return nil
}

// writeSynced writes data to path and flushes it to disk before closing.
func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
