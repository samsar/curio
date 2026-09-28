package keepawake

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Power is where the Mac draws its power from.
type Power string

// Power sources, as pmset reports them.
const (
	PowerAC      Power = "ac"
	PowerBattery Power = "battery" // a UPS counts: it is a battery too
	PowerUnknown Power = "unknown" // no answer, or one this package can't read
)

const (
	// DefaultPmset is macOS's pmset.
	DefaultPmset = "/usr/bin/pmset"
	// defaultPmsetTimeout bounds a pmset run, which answers in
	// milliseconds.
	defaultPmsetTimeout = 5 * time.Second
	// pmset's answer is a few lines; more is not an answer.
	maxPmsetOutput = 64 << 10
	toolWaitDelay  = 2 * time.Second
)

// Probe reads the power source.
type Probe interface {
	Power(ctx context.Context) (Power, error)
}

// Pmset is the Probe that asks `pmset -g ps`. Only the absolute path of
// macOS's own pmset is run; tests point Bin at a fake.
type Pmset struct {
	Bin     string        // default DefaultPmset
	Timeout time.Duration // bounds a run; default 5s
}

// Power implements Probe. A failed or unreadable run is PowerUnknown, with
// the error for a failed one.
func (p Pmset) Power(ctx context.Context) (Power, error) {
	bin := p.Bin
	if bin == "" {
		bin = DefaultPmset
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultPmsetTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "-g", "ps") //nolint:gosec // G204: pmset's fixed path, fixed arguments
	cmd.Stdout = &limitedWriter{w: &out, left: maxPmsetOutput}
	cmd.WaitDelay = toolWaitDelay
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PowerUnknown, fmt.Errorf("pmset -g ps: no answer within %s: %w", timeout, ctxErr)
		}
		return PowerUnknown, fmt.Errorf("pmset -g ps: %w", err)
	}
	return parsePower(out.String()), nil
}

// parsePower reads `pmset -g ps`'s first line: "Now drawing from 'AC
// Power'", "'Battery Power'" or "'UPS Power'". A desktop Mac reports AC
// Power and no battery line after it.
func parsePower(out string) Power {
	_, rest, found := strings.Cut(out, "Now drawing from '")
	if !found {
		return PowerUnknown
	}
	source, _, found := strings.Cut(rest, "'")
	if !found {
		return PowerUnknown
	}
	switch source {
	case "AC Power":
		return PowerAC
	case "Battery Power", "UPS Power":
		return PowerBattery
	}
	return PowerUnknown
}

// limitedWriter passes on at most left bytes and drops the rest, never
// failing a write, so a runaway tool isn't blocked on its pipe.
type limitedWriter struct {
	w    *bytes.Buffer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	n := min(len(p), max(l.left, 0))
	l.w.Write(p[:n])
	l.left -= n
	return len(p), nil
}
