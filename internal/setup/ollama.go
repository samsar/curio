package setup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/samsar/curio/internal/textutil"
)

const (
	// ollamaFormula and ollamaApp are Ollama's Homebrew formula and macOS
	// app. The cask (ollama-app) installs the app, which Detect finds as
	// the app.
	ollamaFormula = "ollama"
	ollamaApp     = "Ollama"
	// ollamaDownload is where the app is downloaded without Homebrew.
	ollamaDownload = "https://ollama.com/download"
)

// ollamaStep makes sure an Ollama new enough for the picked models answers
// at embedding.base_url. What counts is something answering there, never
// a binary on disk; when nothing does, it starts the Ollama that is
// installed, or installs one.
type ollamaStep struct {
	w *world
	// planned is the fix the last check found.
	planned *Fix
	// download: the fix opens the download page, and Apply waits for the
	// user to install Ollama from it.
	download bool
}

func (*ollamaStep) Name() string { return "ollama" }

func (s *ollamaStep) Check(ctx context.Context) Result {
	res, download := s.w.ollamaCheck(ctx)
	s.planned, s.download = res.Fix, download
	return res
}

// checkOllama is the Ollama check doctor shares.
func (w *world) checkOllama(ctx context.Context) Result {
	res, _ := w.ollamaCheck(ctx)
	return res
}

// ollamaCheck asks embedding.base_url for Ollama's version and judges it
// against the picked models' minimum, or, when nothing answers, finds what
// would start or install it. download says the fix is the download page.
func (w *world) ollamaCheck(ctx context.Context) (res Result, download bool) {
	hs := w.readHome()
	base := w.baseConfig(hs).Embedding.BaseURL
	c, err := w.ollamaClient(base, w.newEmbeddingModel())
	if err != nil {
		return Result{Status: Fail, Detail: err.Error(), Hint: "fix embedding.base_url in config.yaml"}, false
	}
	v, err := c.Version(ctx)
	if err == nil {
		return w.judgeVersion(ctx, hs, base, v), false
	}
	return w.notAnswering(ctx, base, err)
}

// judgeVersion checks an answering Ollama's version against the newest
// minimum the picked models have.
func (w *world) judgeVersion(ctx context.Context, hs homeState, base, v string) Result {
	answering := fmt.Sprintf("Ollama %s at %s", v, base)
	p, err := w.pick(ctx, hs)
	if err != nil { // the models check says why
		return Result{Status: OK, Detail: answering}
	}
	need, needFor := minOllama(p)
	if need == "" {
		return Result{Status: OK, Detail: answering}
	}
	older, err := olderThan(v, need)
	if err != nil {
		return Result{Status: Warn, Detail: answering,
			Hint: fmt.Sprintf("its version can't be read, so it isn't checked against the %s %s needs", need, needFor)}
	}
	if !older {
		return Result{Status: OK, Detail: answering}
	}
	res := Result{Status: Fail, Detail: fmt.Sprintf("%s is too old: %s needs %s or later", answering, needFor, need)}
	d, err := w.deps.Installer.Detect(ctx, ollamaFormula, ollamaApp)
	switch {
	case err != nil:
		res.Hint = "upgrade Ollama (finding how it was installed failed: " + err.Error() + ")"
	case d.Formula && w.opts.NoInstall:
		res.Hint = "run " + textutil.ShellList(Command(d, UpgradeFormula, ollamaFormula), Command(d, RestartService, ollamaFormula))
	case d.Formula:
		res.Fix = &Fix{Summary: "upgrade Ollama with Homebrew and restart it",
			Commands: [][]string{Command(d, UpgradeFormula, ollamaFormula), Command(d, RestartService, ollamaFormula)}}
	default:
		res.Hint = "quit Ollama and install the latest from " + ollamaDownload + " (the app also updates itself from its menu)"
	}
	return res
}

// notAnswering is the result when nothing answers at base: the start or
// install that would make something answer, or why curio up can't.
func (w *world) notAnswering(ctx context.Context, base string, cause error) (Result, bool) {
	res := Result{Status: Fail, Detail: fmt.Sprintf("nothing answers at %s (%v)", base, cause)}
	if !isLoopback(base) {
		res.Hint = "start Ollama at " + base
		return res, false
	}
	// A failed probe is the machine check's to report; an unknown machine
	// gets no Homebrew install.
	machine, err := w.probeMachine(ctx)
	if err == nil && machine.OS != "darwin" {
		res.Hint = "install and start Ollama: " + ollamaDownload
		return res, false
	}
	d, err := w.deps.Installer.Detect(ctx, ollamaFormula, ollamaApp)
	if err != nil {
		res.Hint = "start Ollama (finding how it is installed failed: " + err.Error() + ")"
		return res, false
	}
	fix, download, blocked := ollamaFix(d, machine, w.deps.UI.Interactive())
	switch {
	case fix == nil:
		res.Hint = blocked
	case w.opts.NoInstall && download:
		res.Hint = "install Ollama from " + ollamaDownload + " and open it (--no-install runs nothing)"
	case w.opts.NoInstall:
		res.Hint = "run " + textutil.ShellList(fix.Commands...) + " (--no-install runs nothing)"
	default:
		res.Fix = fix
		return res, download
	}
	return res, false
}

// ollamaFix picks how to make Ollama answer from what is installed: the
// formula's service before the app, the app, then an install, Homebrew's
// where it has a build (Apple silicon) and the download page otherwise,
// which opens only in a terminal: a script is never sent to a browser.
// blocked says what to do by hand when there is no fix.
func ollamaFix(d Detection, m Machine, interactive bool) (fix *Fix, download bool, blocked string) {
	switch {
	case d.Formula:
		return &Fix{Summary: "start Ollama with Homebrew (brew services)",
			Commands: [][]string{Command(d, StartService, ollamaFormula)}}, false, ""
	case d.App != "":
		return &Fix{Summary: "open the Ollama app (" + d.App + ")",
			Commands: [][]string{Command(d, OpenApp, ollamaApp)}}, false, ""
	case d.Binary != "":
		return nil, false, "an ollama is installed at " + d.Binary + ", but not as the Homebrew formula or the app: " +
			"start it (ollama serve)"
	case d.Brew != "" && m.AppleSilicon:
		return &Fix{Summary: "install Ollama with Homebrew and start it",
			Commands: [][]string{Command(d, InstallFormula, ollamaFormula), Command(d, StartService, ollamaFormula)}}, false, ""
	case interactive:
		return &Fix{Summary: "open " + ollamaDownload + " to download Ollama, and wait for it to answer",
			Commands: [][]string{Command(d, OpenURL, ollamaDownload)}}, true, ""
	default:
		return nil, false, "install Ollama from " + ollamaDownload + " and open it " +
			"(curio up opens the page itself when it runs in a terminal)"
	}
}

// Apply runs the fix's commands, then waits for Ollama to answer: a minute
// after a start or an install, and, after opening the download page, long
// enough for the user to install and open it.
func (s *ollamaStep) Apply(ctx context.Context, ui UI) error {
	if s.planned == nil {
		return nil
	}
	for _, argv := range s.planned.Commands {
		if err := s.w.deps.Installer.Run(ctx, ui, argv); err != nil {
			return err
		}
	}
	base := s.w.baseConfig(s.w.readHome()).Embedding.BaseURL
	wait := s.w.times.Start
	if s.download {
		wait = s.w.times.Download
		ui.Info(fmt.Sprintf("waiting up to %s for Ollama to answer at %s: install it from the page and open it. "+
			"Ctrl-C is safe; curio up picks up from here", humanDuration(wait), base))
	}
	return s.w.waitForOllama(ctx, base, wait)
}

// waitForOllama polls base's /api/version until it answers or wait runs
// out.
func (w *world) waitForOllama(ctx context.Context, base string, wait time.Duration) error {
	c, err := w.ollamaClient(base, w.newEmbeddingModel())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	tick := time.NewTicker(w.times.Poll)
	defer tick.Stop()
	for {
		_, err := c.Version(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("no answer from Ollama at %s within %s: %w", base, wait, err)
			}
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// humanDuration writes a wait in whole minutes when it is some: "10
// minutes" rather than "10m0s".
func humanDuration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		if d == time.Minute {
			return "a minute"
		}
		return fmt.Sprintf("%d minutes", d/time.Minute)
	}
	return d.String()
}

// minOllama is the newest Ollama the picked models need, and the model
// that needs it; empty when none needs one.
func minOllama(p picks) (version, model string) {
	for _, m := range p.all() {
		if m.MinOllama == "" {
			continue
		}
		if version == "" {
			version, model = m.MinOllama, m.Name
			continue
		}
		if older, err := olderThan(version, m.MinOllama); err == nil && older {
			version, model = m.MinOllama, m.Name
		}
	}
	return version, model
}

// olderThan reports whether Ollama version v comes before want. Versions
// compare numerically, part by part (0.30.10 is past 0.30.5), and a
// prerelease comes before its release (0.35.0-rc1 before 0.35.0). A leading
// "v" is allowed; anything else that isn't a version is an error.
func olderThan(v, want string) (bool, error) {
	a, err := parseVersion(v)
	if err != nil {
		return false, err
	}
	b, err := parseVersion(want)
	if err != nil {
		return false, err
	}
	for i := range max(len(a.parts), len(b.parts)) {
		if c := cmp.Compare(part(a.parts, i), part(b.parts, i)); c != 0 {
			return c < 0, nil
		}
	}
	switch {
	case a.pre == b.pre:
		return false, nil
	case a.pre == "":
		return false, nil
	case b.pre == "":
		return true, nil
	default:
		return a.pre < b.pre, nil
	}
}

// ollamaVersion is a version's numbers and its prerelease ("rc1").
type ollamaVersion struct {
	parts []int
	pre   string
}

func parseVersion(s string) (ollamaVersion, error) {
	core, pre, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(s), "v"), "-")
	var v ollamaVersion
	for p := range strings.SplitSeq(core, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return ollamaVersion{}, fmt.Errorf("%q is not a version", s)
		}
		v.parts = append(v.parts, n)
	}
	v.pre = pre
	return v, nil
}

// part is parts[i], or 0 past its end: 0.30 is 0.30.0.
func part(parts []int, i int) int {
	if i < len(parts) {
		return parts[i]
	}
	return 0
}

// isLoopback reports whether base names this machine: Ollama elsewhere is
// for its owner to start, and curio up offers no install for it.
func isLoopback(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
