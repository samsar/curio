package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// StateFile is setup.json, in the home: what `curio up` remembers between
// runs that isn't the world's to say, the optional installs the user
// declined, so a later run doesn't offer them again. It stays out of
// config.yaml, which is strictly validated and the user's.
const StateFile = "setup.json"

// stateVersion is the setup.json layout this curio writes.
const stateVersion = 1

// State is setup.json.
type State struct {
	Version int `json:"version"`
	// Declined maps an optional install ("yt-dlp") to when the user
	// declined it.
	Declined map[string]time.Time `json:"declined"`
}

// LoadState reads setup.json from home. A missing file is an empty state;
// so is one that can't be read or parsed, with a warning through ui: what
// it remembers only spares the user a question, which isn't worth
// failing a run for.
func LoadState(home string, ui UI) State {
	empty := State{Version: stateVersion, Declined: map[string]time.Time{}}
	path := filepath.Join(home, StateFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return empty
	}
	if err != nil {
		ui.Warn(fmt.Sprintf("ignoring %s: %v", path, err))
		return empty
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		ui.Warn(fmt.Sprintf("ignoring %s, which isn't valid: %v", path, err))
		return empty
	}
	if s.Declined == nil {
		s.Declined = map[string]time.Time{}
	}
	return s
}

// SaveState writes setup.json into home, which must exist: nothing is
// written before the home is made. The file is 0600, written atomically
// (a temp file in the home, synced, then renamed over the old one).
func SaveState(home string, s State) (err error) {
	s.Version = stateVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", StateFile, err)
	}
	path := filepath.Join(home, StateFile)
	tmp, err := os.CreateTemp(home, "."+StateFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name()) // the write already failed; a leftover temp file is harmless
		}
	}()
	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if err = errors.Join(err, tmp.Close()); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("put %s in place: %w", path, err)
	}
	return nil
}
