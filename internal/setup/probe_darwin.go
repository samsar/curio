//go:build darwin

package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// ioregPath is ioreg's absolute path: run without a shell or a PATH
	// lookup.
	ioregPath = "/usr/sbin/ioreg"
	// ioregTimeout bounds the GPU probe; ioreg answers in well under a
	// second.
	ioregTimeout = 5 * time.Second
	// maxIoregOutput caps what is kept of ioreg's output: the accelerator's
	// properties are a few KiB.
	maxIoregOutput = 1 << 20
)

// SystemProbe probes this Mac: sysctl for the chip, its memory and cores,
// statfs for free space, and ioreg, best effort, for the GPU's cores.
type SystemProbe struct{}

func (SystemProbe) Machine(ctx context.Context, home, models string) (Machine, error) {
	memory, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return Machine{}, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	chip, err := unix.Sysctl("machdep.cpu.brand_string")
	if err != nil {
		return Machine{}, fmt.Errorf("sysctl machdep.cpu.brand_string: %w", err)
	}
	m := Machine{OS: runtime.GOOS, Chip: chip, Memory: memory}
	var arm64, translated int
	for _, c := range []struct {
		name string
		into *int
	}{
		{"hw.optional.arm64", &arm64},
		{"sysctl.proc_translated", &translated},
		{"hw.perflevel0.physicalcpu", &m.PerfCores},
		{"hw.perflevel1.physicalcpu", &m.EffCores},
	} {
		if *c.into, err = sysctlCount(c.name); err != nil {
			return Machine{}, err
		}
	}
	if m.PerfCores == 0 { // a chip without performance levels: Intel
		if m.PerfCores, err = sysctlCount("hw.physicalcpu"); err != nil {
			return Machine{}, err
		}
	}
	m.AppleSilicon, m.Translated = arm64 == 1, translated == 1
	m.GPUCores = gpuCores(ctx)
	if m.Home, err = volumeOf(home); err != nil {
		return Machine{}, err
	}
	if m.Models, err = volumeOf(models); err != nil {
		return Machine{}, err
	}
	return m, nil
}

// sysctlCount reads a numeric sysctl. One this Mac lacks (ENOENT: an Intel
// Mac has no hw.optional.arm64 and no performance levels) reads as 0.
func sysctlCount(name string) (int, error) {
	v, err := unix.SysctlUint32(name)
	switch {
	case errors.Is(err, unix.ENOENT):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("sysctl %s: %w", name, err)
	}
	return int(v), nil
}

// volumeOf measures the volume path is on, at the nearest directory that
// exists: the home and the models directory may not yet. An empty path is
// a volume unknown.
func volumeOf(path string) (Volume, error) {
	if path == "" {
		return Volume{}, nil
	}
	dir := nearestExisting(path)
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return Volume{}, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return Volume{Path: dir, Free: st.Bavail * uint64(st.Bsize), ID: fmt.Sprint(st.Fsid.Val)}, nil
}

// nearestExisting is path, or its nearest ancestor that exists.
func nearestExisting(path string) string {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil || filepath.Dir(p) == p {
			return p
		}
	}
}

// gpuCores asks ioreg for the Apple GPU's core count, best effort: a
// failure is 0, unknown, which nothing depends on.
func gpuCores(ctx context.Context) int {
	ctx, cancel := context.WithTimeout(ctx, ioregTimeout)
	defer cancel()
	out := &cappedBuffer{max: maxIoregOutput}
	cmd := exec.CommandContext(ctx, ioregPath, "-rc", "AGXAccelerator", "-d1")
	cmd.Stdout = out
	cmd.WaitDelay = time.Second
	if cmd.Run() != nil {
		return 0
	}
	return parseGPUCores(out.String())
}
