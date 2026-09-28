package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/samsar/curio/internal/textutil"
)

const (
	// detectTimeout bounds a detection subprocess (`brew list`), which
	// answers in about a second.
	detectTimeout = 10 * time.Second
	// maxDetectOutput caps what is kept of a detection's output.
	maxDetectOutput = 64 << 10
	// commandGrace is how long a command has to exit after the SIGINT a
	// cancelled run sends it, as ctrl-c would, before it is killed: long
	// enough for brew to finish writing what it has started.
	commandGrace = 10 * time.Second
)

// Homebrew is the Installer on macOS. It looks for brew where Homebrew
// installs it (/opt/homebrew on Apple silicon, /usr/local on Intel) and on
// PATH, and for an app in /Applications and ~/Applications.
type Homebrew struct {
	brews   []string // where brew is looked for before PATH
	appDirs []string
	// timeout bounds each detection subprocess.
	timeout time.Duration
}

// NewHomebrew returns the Installer for this Mac.
func NewHomebrew() *Homebrew {
	appDirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		appDirs = append(appDirs, filepath.Join(home, "Applications"))
	}
	return &Homebrew{brews: []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"}, appDirs: appDirs,
		timeout: detectTimeout}
}

// Detect implements Installer. Only asking brew whether the formula is
// installed runs anything, bounded by its timeout.
func (h *Homebrew) Detect(ctx context.Context, formula, app string) (Detection, error) {
	d := Detection{Brew: h.brew()}
	if d.Brew != "" && formula != "" {
		installed, err := formulaInstalled(ctx, d.Brew, formula, h.timeout)
		if err != nil {
			return d, err
		}
		d.Formula = installed
	}
	if app != "" {
		d.App = h.app(app)
	}
	if formula != "" {
		if path, err := exec.LookPath(formula); err == nil {
			d.Binary = path
		}
	}
	return d, nil
}

// brew is brew's path, or empty without Homebrew.
func (h *Homebrew) brew() string {
	for _, p := range h.brews {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	if p, err := exec.LookPath("brew"); err == nil {
		return p
	}
	return ""
}

// app is the path of the app bundle name.app, or empty when there is none.
func (h *Homebrew) app(name string) string {
	for _, dir := range h.appDirs {
		p := filepath.Join(dir, name+".app")
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
	}
	return ""
}

// formulaInstalled asks `brew list --versions <formula>`, which prints the
// installed versions and exits 0 for an installed formula, and exits 1 for
// one that isn't, and gives up after timeout.
func formulaInstalled(ctx context.Context, brew, formula string, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := []string{brew, "list", "--versions", formula}
	stdout := &cappedBuffer{max: maxDetectOutput}
	stderr := &cappedBuffer{max: maxDetectOutput}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: brew's own path and a formula this package names; no shell
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return strings.TrimSpace(stdout.String()) != "", nil
	case ctx.Err() != nil:
		return false, fmt.Errorf("`%s`: no answer within %s: %w", textutil.ShellJoin(argv), timeout, ctx.Err())
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	default:
		return false, fmt.Errorf("`%s`: %w: %s", textutil.ShellJoin(argv), err, strings.TrimSpace(stderr.String()))
	}
}

// Run implements Installer. The command's stdin is the terminal only when
// ui can ask, and /dev/null otherwise, so a command that prompts fails a
// script instead of hanging it. A cancelled ctx sends it SIGINT and kills
// it only if it hasn't exited commandGrace later: never at once, in the
// middle of an install. A failure names the command line and how it
// ended.
func (*Homebrew) Run(ctx context.Context, ui UI, argv []string) error {
	if len(argv) == 0 {
		return errors.New("run: no command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: an argv this package builds (Command), run without a shell
	cmd.Stdout, cmd.Stderr = ui.Output(), ui.Output()
	if ui.Interactive() {
		cmd.Stdin = os.Stdin
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = commandGrace
	err := cmd.Run()
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("`%s` was interrupted: %w", textutil.ShellJoin(argv), ctx.Err())
	default:
		return fmt.Errorf("`%s` failed: %w", textutil.ShellJoin(argv), err)
	}
}
