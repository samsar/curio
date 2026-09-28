package setup

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GiB is a binary gigabyte: memory tiers and the disk margin are counted
// in them, as macOS counts memory.
const GiB = 1 << 30

// Machine is what the probe found out about the computer.
type Machine struct {
	OS           string // runtime.GOOS
	Chip         string // "Apple M4 Max"
	AppleSilicon bool
	// Translated: this curio is an Intel build running under Rosetta.
	Translated bool
	Memory     uint64 // bytes of unified memory
	// PerfCores and EffCores are the performance and efficiency cores; a
	// chip without the split reports all its cores as performance ones.
	PerfCores, EffCores int
	GPUCores            int // 0 when unknown
	// Home is the volume $CURIO_HOME is on, Models the one Ollama keeps
	// its models on.
	Home, Models Volume
}

// Volume is a filesystem's free space as the probe measured it. Its zero
// value is a volume the probe couldn't measure.
type Volume struct {
	Path string // the directory measured: the nearest one that exists
	Free uint64 // bytes available to this user
	ID   string // tells volumes apart; empty when unknown
}

// Known reports whether the probe measured the volume.
func (v Volume) Known() bool { return v.ID != "" }

// Probe finds out about the machine. It changes nothing.
type Probe interface {
	// Machine probes the computer, measuring free space on the volumes
	// of home and of models (directories that may not exist yet).
	Machine(ctx context.Context, home, models string) (Machine, error)
}

// ModelsDir is where Ollama keeps its models: $OLLAMA_MODELS, or
// ~/.ollama/models. It is this process's view; an Ollama started with
// another environment may keep them elsewhere.
func ModelsDir() string {
	if dir := os.Getenv("OLLAMA_MODELS"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ollama", "models")
}

// String describes the machine in a line: "Apple M4 Max, 64 GB of
// memory, 12 performance and 4 efficiency cores, 40 GPU cores".
func (m Machine) String() string {
	parts := []string{cmp.Or(m.Chip, m.OS), fmt.Sprintf("%d GB of memory", m.Memory/GiB)}
	switch {
	case m.EffCores > 0:
		parts = append(parts, fmt.Sprintf("%d performance and %d efficiency cores", m.PerfCores, m.EffCores))
	case m.PerfCores > 0:
		parts = append(parts, fmt.Sprintf("%d cores", m.PerfCores))
	}
	if m.GPUCores > 0 {
		parts = append(parts, fmt.Sprintf("%d GPU cores", m.GPUCores))
	}
	return strings.Join(parts, ", ")
}

// Level is how well curio can run on a machine.
type Level int

const (
	// Supported: Apple silicon with 16 GB or more.
	Supported Level = iota
	// Limited: it runs, with a warning (8 GB of memory, or under Rosetta).
	Limited
	// Degraded: it runs slowly, and the user is asked whether to go on
	// (an Intel Mac).
	Degraded
	// Unsupported: `curio up` can't set it up (not macOS); it offers no
	// installs.
	Unsupported
)

// Verdict is the machine check's finding.
type Verdict struct {
	Level   Level
	Reasons []string
	// Question is asked once, before anything is applied, on a degraded
	// machine: whether to go on (the default) or stop.
	Question string
}

// smallTier is memory under which a Mac gets the smallest models, which
// swap in and out of memory.
const smallTier = 16 * GiB

// Assess judges the machine: supported, limited, degraded or unsupported,
// and why.
func Assess(m Machine) Verdict {
	if m.OS != "darwin" {
		return Verdict{Level: Unsupported, Reasons: []string{
			"automatic setup is macOS-only: install and start Ollama yourself (https://ollama.com/download); " +
				"curio up still pulls the models, creates the home and starts the daemon"}}
	}
	if !m.AppleSilicon {
		return Verdict{Level: Degraded, Reasons: []string{
			"an Intel Mac: Ollama runs on the CPU only",
			"Homebrew has no Intel build of Ollama, so curio up offers the app from ollama.com",
			"the first import takes 1 to 4 days",
		}, Question: "This Mac has an Intel chip: curio runs, but slowly. Continue?"}
	}
	v := Verdict{Level: Supported}
	if m.Translated {
		v.Level = Limited
		v.Reasons = append(v.Reasons, "this curio is the Intel build, running under Rosetta: install the arm64 build")
	}
	if m.Memory < smallTier {
		v.Level = Limited
		v.Reasons = append(v.Reasons, fmt.Sprintf("%d GB of memory: curio uses the smallest models, "+
			"which swap in and out of memory; import overnight", m.Memory/GiB))
	}
	return v
}

// diskMargin is the free space kept on top of the models to download, and
// what a new home needs on its own volume: the database, extracted pages,
// logs.
const diskMargin = 2 * GiB

// diskShortage returns why the models volume can't take the missing
// models, their sizes plus diskMargin, or "" when it can. A model of
// unknown size counts only the margin and is said to be of unknown size;
// a volume the probe couldn't measure isn't judged. The home's own need is
// the home check's (newHomeShortage).
func diskShortage(m Machine, missing []Model) string {
	if !m.Models.Known() || len(missing) == 0 {
		return ""
	}
	need := uint64(diskMargin)
	names := make([]string, 0, len(missing))
	for _, mod := range missing {
		need += mod.Size
		size := "size unknown"
		if mod.Size > 0 {
			size = FormatSize(mod.Size)
		}
		names = append(names, fmt.Sprintf("%s (%s)", mod.Name, size))
	}
	if m.Models.Free >= need {
		return ""
	}
	return fmt.Sprintf("the volume of %s has %s free, and %s need %s with a 2 GiB margin",
		m.Models.Path, FormatSize(m.Models.Free), strings.Join(names, " and "), FormatSize(need))
}

// newHomeShortage returns why the home's volume can't take a new home,
// diskMargin, or "" when it can or the probe couldn't measure it. On the
// models' volume the margin the missing models keep is that same 2 GiB,
// so the two checks never count it twice.
func newHomeShortage(m Machine) string {
	if !m.Home.Known() || m.Home.Free >= diskMargin {
		return ""
	}
	return fmt.Sprintf("the volume of %s has %s free, and a new curio home needs 2 GiB",
		m.Home.Path, FormatSize(m.Home.Free))
}
