package setuptest

import (
	"context"
	"slices"
	"sync"

	"github.com/samsar/curio/internal/setup"
)

// Installer is a setup.Installer that runs nothing: Detect reports what a
// test set, and Run records each command and runs OnRun instead, which can
// make the fake Ollama answer, say.
type Installer struct {
	// Detected is what Detect reports; DetectErr fails it.
	Detected  setup.Detection
	DetectErr error
	// OnRun, when set, stands in for each command, and its error fails
	// the command.
	OnRun func(argv []string) error

	mu      sync.Mutex
	detects int
	runs    [][]string
}

var _ setup.Installer = (*Installer)(nil)

// NewInstaller returns an Installer that finds Homebrew and nothing else
// installed.
func NewInstaller() *Installer {
	return &Installer{Detected: setup.Detection{Brew: "/opt/homebrew/bin/brew"}}
}

func (f *Installer) Detect(context.Context, string, string) (setup.Detection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detects++
	return f.Detected, f.DetectErr
}

func (f *Installer) Run(_ context.Context, _ setup.UI, argv []string) error {
	f.mu.Lock()
	f.runs = append(f.runs, slices.Clone(argv))
	run := f.OnRun
	f.mu.Unlock()
	if run == nil {
		return nil
	}
	return run(argv)
}

// Runs are the commands run, in order.
func (f *Installer) Runs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

// Detects is how many times Detect was called.
func (f *Installer) Detects() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.detects
}
