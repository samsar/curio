package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/samsar/curio/internal/curiohome"
)

// BaseLabel is the launchd label of the default home's agent. Every other
// home's agent adds a suffix (see agentLabel).
const BaseLabel = "com.github.samsar.curio.daemon"

const (
	defaultLaunchctl = "/bin/launchctl"
	// defaultCallTimeout bounds a launchctl run that doesn't wait on the
	// daemon; each answers in milliseconds.
	defaultCallTimeout = 10 * time.Second
	// defaultLongCallTimeout bounds what waits on a daemon's exit, which
	// launchd allows ExitTimeout: kickstart -k, and a bootout's unloading.
	defaultLongCallTimeout = ExitTimeout + 15*time.Second
	defaultPollInterval    = 100 * time.Millisecond

	// launchctl's stdout is a few KiB at most; its stderr, which errors
	// quote, is a line or two.
	maxLaunchctlStdout = 1 << 20
	maxLaunchctlStderr = 4 << 10
	// launchctlWaitDelay bounds the wait for launchctl's pipes to close
	// once it has exited or been killed.
	launchctlWaitDelay = time.Second
)

// Exit statuses of `launchctl print`.
const (
	// exitNoService: "Could not find service ... in domain for user gui":
	// the agent isn't loaded.
	exitNoService = 113
	// exitNoDomain: "Could not find domain for user gui": the user has no
	// GUI login session (logged in only over ssh, say), which an agent
	// runs in.
	exitNoDomain = 112
)

// LaunchdOptions adjust a Launchd. The zero value is the real launchctl
// and the user's own agents directory; tests point both elsewhere.
type LaunchdOptions struct {
	Launchctl string // default /bin/launchctl
	AgentsDir string // default ~/Library/LaunchAgents
	UID       int    // whose GUI domain; 0 means os.Getuid()
	// Geteuid reports the effective user, which Install refuses when it
	// is root; default os.Geteuid.
	Geteuid func() int
	// CallTimeout bounds each launchctl run; default 10s.
	CallTimeout time.Duration
	// LongCallTimeout bounds kickstart -k and the wait for a bootout to
	// unload the agent, both of which wait out the daemon's shutdown;
	// default ExitTimeout+15s.
	LongCallTimeout time.Duration
	// PollInterval paces the checks of a bootout's unloading; default
	// 100ms.
	PollInterval time.Duration
}

// Launchd is the Manager of a home's per-user launchd agent. It drives
// launchctl's domain verbs (bootstrap, bootout, kickstart, kill, print) in
// the user's GUI domain, never as root.
type Launchd struct {
	home           *curiohome.Home
	label          string
	nonDefaultHome bool
	opts           LaunchdOptions
}

var _ Manager = (*Launchd)(nil)

// NewLaunchd returns the Manager of home's launchd agent.
func NewLaunchd(home *curiohome.Home, opts LaunchdOptions) (*Launchd, error) {
	userHome, err := userHomeDir()
	if err != nil {
		return nil, err
	}
	defaultHome := filepath.Join(userHome, curiohome.DefaultDirName)
	if opts.AgentsDir == "" {
		opts.AgentsDir = filepath.Join(userHome, "Library", "LaunchAgents")
	}
	if opts.Launchctl == "" {
		opts.Launchctl = defaultLaunchctl
	}
	if opts.UID == 0 {
		opts.UID = os.Getuid()
	}
	if opts.Geteuid == nil {
		opts.Geteuid = os.Geteuid
	}
	if opts.CallTimeout == 0 {
		opts.CallTimeout = defaultCallTimeout
	}
	if opts.LongCallTimeout == 0 {
		opts.LongCallTimeout = defaultLongCallTimeout
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = defaultPollInterval
	}
	label, isDefault := agentLabel(home.Path, defaultHome)
	return &Launchd{home: home, label: label, nonDefaultHome: !isDefault, opts: opts}, nil
}

// userHomeDir is $HOME, as curiohome.DefaultPath reads it, or the user
// database's home directory where the environment leaves $HOME unset. A
// command told its home by --curio-home or $CURIO_HOME needs no $HOME,
// and the agent's directory and label belong to the user all the same.
func userHomeDir() (string, error) {
	if dir, err := os.UserHomeDir(); err == nil {
		return dir, nil
	}
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("locate the user's home directory, for ~/Library/LaunchAgents: %w", err)
	}
	return u.HomeDir, nil
}

// agentLabel is the label of home's agent: BaseLabel for the default home,
// ~/.curio, and for any other home BaseLabel and the first 8 hex digits of
// the SHA-256 of its canonical path. A label per home keeps `curio
// --curio-home B daemon start` from kickstarting home A's daemon. The
// paths are compared canonically, so a symlink to a home, or CURIO_HOME
// spelling it another way, names the same agent.
func agentLabel(home, defaultHome string) (label string, isDefault bool) {
	canonical := curiohome.CanonicalPath(home)
	if canonical == curiohome.CanonicalPath(defaultHome) {
		return BaseLabel, true
	}
	sum := sha256.Sum256([]byte(canonical))
	return BaseLabel + "." + hex.EncodeToString(sum[:4]), false
}

// Label is the agent's launchd label.
func (l *Launchd) Label() string { return l.label }

// PlistPath is where the agent's plist is installed.
func (l *Launchd) PlistPath() string { return filepath.Join(l.opts.AgentsDir, l.label+".plist") }

// domain is the user's GUI domain, and target the agent in it.
func (l *Launchd) domain() string { return "gui/" + strconv.Itoa(l.opts.UID) }
func (l *Launchd) target() string { return l.domain() + "/" + l.label }

// Status reads the plist and asks launchd about the agent. With no plist,
// launchd isn't asked: that keeps every home without an agent, and every
// command run in it, from forking launchctl.
func (l *Launchd) Status(ctx context.Context) (Status, error) {
	st := Status{Supported: true, Label: l.label, Path: l.PlistPath()}
	data, err := os.ReadFile(st.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("read the launchd agent's plist: %w", err)
	}
	st.Installed = true
	// A plist edited out of shape reports no program, which is what
	// `curio doctor` flags; its load state is still launchd's to say.
	if program, err := plistProgram(data); err == nil {
		st.Program = program
	}
	p, err := l.print(ctx)
	if err != nil {
		return st, err
	}
	st.Loaded, st.State, st.PID, st.LastExit = p.loaded, p.state, p.pid, p.lastExit
	return st, nil
}

// Install writes the agent's plist for spec and loads it, which starts the
// daemon (RunAtLoad). It is idempotent: an agent loaded from the same
// plist is left alone (changed false), and one loaded from another is
// booted out, waited out, and loaded again. `enable` comes first, so an
// explicit install undoes an earlier `launchctl disable`.
func (l *Launchd) Install(ctx context.Context, spec Spec) (bool, error) {
	if l.opts.Geteuid() == 0 {
		return false, errors.New("install the launchd agent as your own user, not root: " +
			"the agent is per-user, and curio never uses sudo")
	}
	if err := checkProgram(spec.Program); err != nil {
		return false, err
	}
	plist, err := renderPlist(agentPlist{
		Label: l.label, Program: spec.Program, Home: l.home.Path, NonDefaultHome: l.nonDefaultHome,
		StdoutPath: l.home.DaemonLogPath(), StderrPath: l.home.LaunchdErrPath(),
	})
	if err != nil {
		return false, err
	}
	// launchd refuses to bootstrap a label it has loaded ("Bootstrap
	// failed: 5"), so what it has decides the rest.
	p, err := l.print(ctx)
	if err != nil {
		return false, err
	}
	if p.noDomain != nil {
		return false, l.noDomainError(p.noDomain)
	}
	if p.loaded {
		current, err := os.ReadFile(l.PlistPath())
		switch {
		case err == nil && bytes.Equal(current, plist):
			return false, nil
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return false, fmt.Errorf("read the launchd agent's plist: %w", err)
		}
		if err := l.unload(ctx); err != nil {
			return false, err
		}
	}
	if err := l.writePlist(plist); err != nil {
		return false, err
	}
	if _, err := l.run(ctx, l.opts.CallTimeout, "enable", l.target()); err != nil {
		return false, l.loadError(err)
	}
	if _, err := l.run(ctx, l.opts.CallTimeout, "bootstrap", l.domain(), l.PlistPath()); err != nil {
		return false, l.loadError(err)
	}
	return true, nil
}

// checkProgram refuses a daemon launchd couldn't run: a relative path
// (launchd has no working directory to resolve it against), a missing
// file, or a directory.
func checkProgram(program string) error {
	if !filepath.IsAbs(program) {
		return fmt.Errorf("the launchd agent needs the daemon's absolute path, not %q", program)
	}
	info, err := os.Stat(program)
	if err != nil {
		return fmt.Errorf("the daemon the launchd agent would run: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("the daemon the launchd agent would run, %s, is a directory", program)
	}
	return nil
}

// writePlist puts data at PlistPath atomically: a temp file in the same
// directory, synced, then renamed over the old plist, so launchd never
// loads half a plist. It creates the agents directory and the home's logs
// directory, where launchd opens the daemon's output, when missing.
func (l *Launchd) writePlist(data []byte) (err error) {
	//nolint:gosec // G301: ~/Library/LaunchAgents is conventionally 0755, and it holds no secrets
	if err := os.MkdirAll(l.opts.AgentsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", l.opts.AgentsDir, err)
	}
	if err := os.MkdirAll(l.home.LogsDir(), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", l.home.LogsDir(), err)
	}
	tmp, err := os.CreateTemp(l.opts.AgentsDir, "."+l.label+".*.tmp")
	if err != nil {
		return fmt.Errorf("write the launchd agent's plist: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		// CreateTemp's 0600 widened to a plist's usual 0644; launchd
		// refuses one that is group- or world-writable.
		err = tmp.Chmod(0o644)
	}
	if err = errors.Join(err, tmp.Close()); err != nil {
		return fmt.Errorf("write the launchd agent's plist: %w", err)
	}
	if err = os.Rename(tmp.Name(), l.PlistPath()); err != nil {
		return fmt.Errorf("install the launchd agent's plist: %w", err)
	}
	return nil
}

// Uninstall boots the agent out, which stops its daemon, waits until
// launchd has unloaded it, and removes the plist. An agent loaded without
// its plist is still booted out.
func (l *Launchd) Uninstall(ctx context.Context) (bool, error) {
	p, err := l.print(ctx)
	if err != nil {
		return false, err
	}
	removed := false
	if p.loaded {
		if err := l.unload(ctx); err != nil {
			return false, err
		}
		removed = true
	}
	switch err := os.Remove(l.PlistPath()); {
	case err == nil:
		removed = true
	case !errors.Is(err, fs.ErrNotExist):
		return removed, fmt.Errorf("remove the launchd agent's plist: %w", err)
	}
	return removed, nil
}

// unload boots the agent out and waits until launchd no longer has it.
// bootout returns before the job has exited, and launchd would refuse to
// load the label again until it has; polling print serves every macOS
// release, where bootout's --wait may not. A bootout that fails because
// the agent went away meanwhile has done its job.
func (l *Launchd) unload(ctx context.Context) error {
	if _, err := l.run(ctx, l.opts.CallTimeout, "bootout", l.target()); err != nil {
		p, perr := l.print(ctx)
		if perr != nil || p.loaded {
			return fmt.Errorf("unload the launchd agent: %w", err)
		}
		return nil
	}
	return l.waitUnloaded(ctx)
}

// waitUnloaded polls until launchd no longer has the agent, for as long
// as its daemon may take to exit.
func (l *Launchd) waitUnloaded(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, l.opts.LongCallTimeout)
	defer cancel()
	for {
		p, err := l.print(ctx)
		if err != nil {
			return err
		}
		if !p.loaded {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("launchd still has %s %s after bootout: %w", l.target(), l.opts.LongCallTimeout, ctx.Err())
		case <-time.After(l.opts.PollInterval):
		}
	}
}

// Start kickstarts the agent, which starts its daemon unless one runs.
func (l *Launchd) Start(ctx context.Context) (int, error) {
	out, err := l.run(ctx, l.opts.CallTimeout, "kickstart", "-p", l.target())
	if err != nil {
		return 0, fmt.Errorf("start the daemon through launchd: %w", err)
	}
	return l.startedPID(ctx, out)
}

// Restart kickstarts the agent with -k, which kills a running daemon
// first. launchd picks the signal, and gives the daemon ExitTimeout to
// exit on it.
func (l *Launchd) Restart(ctx context.Context) (int, error) {
	out, err := l.run(ctx, l.opts.LongCallTimeout, "kickstart", "-k", "-p", l.target())
	if err != nil {
		return 0, fmt.Errorf("restart the daemon through launchd: %w", err)
	}
	return l.startedPID(ctx, out)
}

// Stop sends the agent's daemon SIGTERM. It exits 0 once it has shut
// down, which KeepAlive {SuccessfulExit: false} leaves stopped.
func (l *Launchd) Stop(ctx context.Context) error {
	if _, err := l.run(ctx, l.opts.CallTimeout, "kill", "SIGTERM", l.target()); err != nil {
		return fmt.Errorf("stop the daemon through launchd: %w", err)
	}
	return nil
}

// number is a decimal number in launchctl's output.
var number = regexp.MustCompile(`\d+`)

// startedPID is the PID kickstart -p printed, taken as the last number in
// its output: launchctl documents only that it prints the PID, not in what
// words ("123", "service spawned with pid: 123"). When it printed none,
// launchd's status names the process that runs.
func (l *Launchd) startedPID(ctx context.Context, out string) (int, error) {
	if found := number.FindAllString(out, -1); len(found) > 0 {
		pid, err := strconv.Atoi(found[len(found)-1])
		if err == nil && pid > 0 {
			return pid, nil
		}
	}
	p, err := l.print(ctx)
	if err != nil {
		return 0, err
	}
	if p.pid == 0 {
		return 0, fmt.Errorf("launchctl kickstart printed no pid (%q), and launchd runs no daemon for %s (%s)",
			strings.TrimSpace(out), l.label, p.state)
	}
	return p.pid, nil
}

// printed is what `launchctl print` says about the agent.
type printed struct {
	loaded bool
	// noDomain is print's error when the user has no GUI domain; nil
	// otherwise.
	noDomain *LaunchctlError
	state    string
	pid      int
	lastExit string
}

// print asks launchd about the agent. Not loaded, and no GUI domain to be
// loaded in, are answers, not errors.
func (l *Launchd) print(ctx context.Context) (printed, error) {
	out, err := l.run(ctx, l.opts.CallTimeout, "print", l.target())
	var lerr *LaunchctlError
	switch {
	case err == nil:
		return parsePrint(out)
	case errors.As(err, &lerr) && lerr.ExitCode == exitNoService:
		return printed{}, nil
	case errors.As(err, &lerr) && lerr.ExitCode == exitNoDomain:
		return printed{noDomain: lerr}, nil
	}
	return printed{}, err
}

// parsePrint reads the service's own properties from print's output: the
// lines one tab deep. Deeper blocks (endpoints, sockets, the environment)
// have lines of their own, `state = active` among them, that say nothing
// about the process.
func parsePrint(out string) (printed, error) {
	p := printed{loaded: true}
	var exitCode, signal string
	for line := range strings.Lines(out) {
		if !strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "\t\t") {
			continue
		}
		key, value, _ := strings.Cut(strings.TrimSpace(line), " = ")
		switch key {
		case "state":
			p.state = value
		case "pid":
			pid, err := strconv.Atoi(value)
			if err != nil {
				return printed{}, fmt.Errorf("launchctl print: pid %q: %w", value, err)
			}
			p.pid = pid
		case "last exit code":
			exitCode = value
		case "last terminating signal":
			signal = value
		}
	}
	p.lastExit = lastExit(exitCode, signal)
	return p, nil
}

// lastExit words how the agent's last process ended, from print's "last
// exit code" ("(never exited)", "0", "1", or "78: EX_CONFIG" on newer
// releases) and "last terminating signal" ("Killed: 9"); empty for a clean
// exit or none.
func lastExit(exitCode, signal string) string {
	if signal != "" {
		return "signal " + signal
	}
	code, _, _ := strings.Cut(exitCode, ":")
	if code == "" || code == "0" || strings.HasPrefix(code, "(") {
		return ""
	}
	return "exit code " + exitCode
}

// LaunchctlError is a launchctl run that exited non-zero.
type LaunchctlError struct {
	Args     []string
	ExitCode int
	Stderr   string // at most 4 KiB of it
}

func (e *LaunchctlError) Error() string {
	msg := fmt.Sprintf("launchctl %s: exit %d", strings.Join(e.Args, " "), e.ExitCode)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// loadError adds, to a failure to enable or bootstrap the agent, where to
// look next.
func (l *Launchd) loadError(err error) error {
	return fmt.Errorf("load the launchd agent: %w (`launchctl print %s` says more; "+
		"curio-daemon must stay allowed in System Settings > General > Login Items & Extensions)", err, l.target())
}

// noDomainError explains print's answer for a user with no GUI session.
func (l *Launchd) noDomainError(err *LaunchctlError) error {
	return fmt.Errorf("no GUI login session for uid %d to run the launchd agent in: "+
		"log in to the Mac's desktop session and run this again (until then, curio commands start the daemon themselves): %w",
		l.opts.UID, err)
}

// run runs launchctl with args, bounded by timeout, and returns its
// stdout. A non-zero exit is a *LaunchctlError quoting its stderr; a run
// cut short wraps the context's error.
func (l *Launchd) run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout := &headBuffer{max: maxLaunchctlStdout}
	stderr := &headBuffer{max: maxLaunchctlStderr}
	cmd := exec.CommandContext(ctx, l.opts.Launchctl, args...) //nolint:gosec // G204: launchctl's own path, with verbs and labels this package builds; no shell
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = launchctlWaitDelay
	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	what := "launchctl " + strings.Join(args, " ")
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", fmt.Errorf("%s: no answer within %s: %w", what, timeout, ctxErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.String(), &LaunchctlError{Args: args, ExitCode: exit.ExitCode(), Stderr: strings.TrimSpace(stderr.String())}
	}
	return "", fmt.Errorf("%s: %w", what, err)
}

// headBuffer keeps the first max bytes written to it and drops the rest,
// never failing a write, so a chatty launchctl isn't blocked on its pipe.
type headBuffer struct {
	buf []byte
	max int
}

func (b *headBuffer) Write(p []byte) (int, error) {
	room := b.max - len(b.buf)
	b.buf = append(b.buf, p[:max(0, min(room, len(p)))]...)
	return len(p), nil
}

func (b *headBuffer) String() string { return string(b.buf) }
