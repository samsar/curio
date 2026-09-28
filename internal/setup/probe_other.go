//go:build !darwin

package setup

import (
	"context"
	"runtime"
)

// SystemProbe reports the platform and nothing else: automatic setup is
// macOS-only, and Assess calls every other platform unsupported.
type SystemProbe struct{}

func (SystemProbe) Machine(context.Context, string, string) (Machine, error) {
	return Machine{OS: runtime.GOOS}, nil
}
