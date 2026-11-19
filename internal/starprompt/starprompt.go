// Package starprompt asks a person, once per curio home, to star curio's
// repository on GitHub, after the fifth search of theirs that found
// something. Only searches a person ran and reads the answer of count:
// curio search on a terminal and the dashboard's submitted searches, never
// curio-mcp's, whose caller is a model. Nothing here reaches the network:
// the ask is a line with a link, and curio never learns whether anyone
// followed it.
//
// The daemon is the one writer of the home's star.json (one daemon per
// home), which records the count until the ask and then that it was made,
// so that the CLI and the dashboard share one count and one ask.
package starprompt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// Threshold is the search, counting from 1, after which the ask is
	// shown: the fifth that found something.
	Threshold = 5
	// RepoURL is the repository the ask links to.
	RepoURL = "https://github.com/samsar/curio"
	// OptOutEnv, set to anything but empty, turns the ask off: in the
	// daemon's environment it never counts or asks, and in the CLI's,
	// curio search never asks the daemon to count.
	OptOutEnv = "CURIO_NO_STAR_PROMPT"
)

// Surface is where a counted search was run, and so where an ask shows.
type Surface string

const (
	SurfaceCLI       Surface = "cli"
	SurfaceDashboard Surface = "dashboard"
)

// Message is the ask, as both surfaces word it: one sentence, the link
// after it.
const Message = "Finding things in curio? A star on GitHub helps others find it too:"

// Once is what follows the ask, so nobody waits for it to come back.
const Once = "This is asked once and won't show again."

// record is star.json.
type record struct {
	// Searches counts the searches that found something, up to Threshold.
	Searches int `json:"searches"`
	// ShownAt is when the ask was shown, and ShownIn where; nil until then.
	ShownAt *time.Time `json:"shown_at,omitempty"`
	ShownIn Surface    `json:"shown_in,omitempty"`
}

// Prompt counts a home's searches and says when to ask, once. Its methods
// are safe for concurrent use; a nil Prompt never asks.
type Prompt struct {
	path string
	log  *slog.Logger
	now  func() time.Time

	mu     sync.Mutex
	rec    record
	done   bool // asked, turned off, or star.json unreadable: never asks again
	warned bool // a failed write was logged
}

// Open reads the count kept at path. A missing file starts it at 0. With
// off, or when the file can't be read or isn't star.json, the Prompt never
// asks: an ask is nothing to risk making twice. log may be nil.
func Open(path string, off bool, log *slog.Logger) *Prompt {
	if log == nil {
		log = slog.Default()
	}
	p := &Prompt{path: path, log: log, now: time.Now, done: off}
	if off {
		return p
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return p
	case err != nil:
		p.done = true
		log.Warn("star prompt: can't read its count; it won't ask", "path", path, "error", err)
		return p
	}
	if err := json.Unmarshal(data, &p.rec); err != nil {
		p.done = true
		log.Warn("star prompt: its count isn't valid; it won't ask", "path", path, "error", err)
		return p
	}
	p.done = p.rec.ShownAt != nil
	return p
}

// Searched counts one search a person ran on surface that found something,
// and reports whether to show the ask with its answer: true exactly once
// per home, for the search that reaches Threshold, and only once the ask
// is recorded, so a home whose star.json can't be written is never asked
// at all rather than asked again after a restart.
func (p *Prompt) Searched(surface Surface) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return false
	}
	next := p.rec
	next.Searches = min(next.Searches+1, Threshold)
	if next.Searches < Threshold {
		// The count is kept in memory even when the write fails: a restart
		// only starts it again from what was written.
		p.rec = next
		if err := write(p.path, next); err != nil {
			p.warnOnce(err)
		}
		return false
	}
	shown := p.now().UTC().Truncate(time.Second)
	next.ShownAt, next.ShownIn = &shown, surface
	if err := write(p.path, next); err != nil {
		p.done = true
		p.warnOnce(err)
		return false
	}
	p.rec, p.done = next, true
	p.log.Info("star prompt: asked", "surface", string(surface))
	return true
}

// warnOnce logs a failed write once a process.
func (p *Prompt) warnOnce(err error) {
	if p.warned {
		return
	}
	p.warned = true
	p.log.Warn("star prompt: can't write its count", "path", p.path, "error", err)
}

// write replaces the file at path with rec, atomically: a temp file in the
// same directory, synced, then renamed over it. The file is 0600.
func write(path string, rec record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	data = append(data, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
