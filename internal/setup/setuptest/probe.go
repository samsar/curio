package setuptest

import (
	"context"
	"sync"

	"github.com/samsar/curio/internal/setup"
)

// Probe is a setup.Probe that reports the machine a test set.
type Probe struct {
	Facts setup.Machine
	Err   error

	mu    sync.Mutex
	calls int
}

var _ setup.Probe = (*Probe)(nil)

// AppleSilicon is an M4 Max with memory GiB of memory, 12 performance and
// 4 efficiency cores, 40 GPU cores, and 200 GB free on one volume.
func AppleSilicon(memoryGiB uint64) setup.Machine {
	free := setup.Volume{Path: "/", Free: 200e9, ID: "disk1"}
	return setup.Machine{OS: "darwin", Chip: "Apple M4 Max", AppleSilicon: true, Memory: memoryGiB * setup.GiB,
		PerfCores: 12, EffCores: 4, GPUCores: 40, Home: free, Models: free}
}

// NewProbe returns a Probe reporting AppleSilicon(64).
func NewProbe() *Probe { return &Probe{Facts: AppleSilicon(64)} }

func (p *Probe) Machine(context.Context, string, string) (setup.Machine, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.Facts, p.Err
}

// Calls is how many times the machine was probed.
func (p *Probe) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}
