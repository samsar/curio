package service

import (
	"context"
	"fmt"
	"runtime"
)

// Unsupported is the Manager of a platform with no service manager curio
// supports: it manages nothing, and each change it is asked for fails with
// ErrUnsupported.
type Unsupported struct{}

var _ Manager = Unsupported{}

// Status reports that no service manager is supported, without running
// anything.
func (Unsupported) Status(context.Context) (Status, error) { return Status{}, nil }

func (Unsupported) Install(context.Context, Spec) (bool, error) { return false, unsupported() }
func (Unsupported) Uninstall(context.Context) (bool, error)     { return false, unsupported() }
func (Unsupported) Start(context.Context) (int, error)          { return 0, unsupported() }
func (Unsupported) Stop(context.Context) error                  { return unsupported() }
func (Unsupported) Restart(context.Context) (int, error)        { return 0, unsupported() }

func unsupported() error {
	return fmt.Errorf("%w (%s): launchd agents are macOS-only; the CLI starts the daemon on demand",
		ErrUnsupported, runtime.GOOS)
}
