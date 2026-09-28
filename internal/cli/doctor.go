package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/textutil"
)

// newDoctorCmd diagnoses the parts of curio that fail silently.
//
// Inspired by `brew doctor`: walk through every dependency, print a
// status line per check, end with a one-line summary and a suggested
// next action if anything's wrong. Its checks of the machine, the home,
// config.yaml, Ollama, the models, the daemon, its launchd agent and the
// embeddings are `curio up`'s own (setup.Runner.Checks), so the two agree
// on what healthy means; doctor never creates the home to look at it.
func newDoctorCmd(flags *rootFlags, d deps) *cobra.Command {
	return &cobra.Command{
		Use:         "doctor",
		Short:       "Diagnose curio's environment: the Mac, Ollama and its models, the home, the daemon, the fetcher",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{ownsEnvironment: "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := flags.setupFor(d, d.newUI(cmd.InOrStdin(), cmd.ErrOrStderr(), false), setup.Options{})
			if err != nil {
				return err
			}
			rep := newDoctorReport()
			ctx := cmd.Context()
			for _, c := range r.Checks() {
				res := c.Run(ctx)
				rep.add(c.Name, statusOf(res.Status), res.Detail, doctorHint(res))
			}
			runOwnChecks(ctx, flags, d, rep)
			rep.print(cmd.OutOrStdout())
			if rep.failures > 0 {
				return fmt.Errorf("%d check(s) failed", rep.failures)
			}
			return nil
		},
	}
}

// statusOf is a setup check's status as a doctor line's.
func statusOf(s setup.Status) checkStatus {
	switch s {
	case setup.OK:
		return statusOK
	case setup.Warn:
		return statusWarn
	case setup.Fail:
	}
	return statusFail // a failure, or a status this curio doesn't know: no pass
}

// doctorHint is what a setup check's line suggests: for what curio up
// fixes, the commands it would run or that it does it; otherwise the
// check's own hint.
func doctorHint(res setup.Result) string {
	switch {
	case res.Fix == nil:
		return res.Hint
	case len(res.Fix.Commands) > 0:
		return "run " + textutil.ShellList(res.Fix.Commands...) + ", or `curio up`, which does it"
	default:
		return "`curio up` does it: " + res.Fix.Summary
	}
}

type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

// marker is the status's one-character column in the report.
func (s checkStatus) marker() string {
	switch s {
	case statusOK:
		return "✓"
	case statusWarn:
		return "!"
	case statusFail:
		return "✗"
	default:
		return "?"
	}
}

type checkResult struct {
	name   string
	status checkStatus
	detail string
	hint   string
}

type doctorReport struct {
	checks   []checkResult
	failures int
	warns    int
}

func newDoctorReport() *doctorReport { return &doctorReport{} }

func (r *doctorReport) add(name string, status checkStatus, detail, hint string) {
	r.checks = append(r.checks, checkResult{name, status, detail, hint})
	switch status {
	case statusOK:
	case statusWarn:
		r.warns++
	case statusFail:
		r.failures++
	}
}

func (r *doctorReport) print(w io.Writer) {
	for _, c := range r.checks {
		fmt.Fprintf(w, "%s %-22s %s\n", c.status.marker(), c.name, c.detail)
		if c.hint != "" {
			fmt.Fprintf(w, "  → %s\n", c.hint)
		}
	}
	fmt.Fprintln(w)
	if r.failures == 0 && r.warns == 0 {
		fmt.Fprintln(w, "all checks passed")
	} else {
		fmt.Fprintf(w, "%d failure(s), %d warning(s)\n", r.failures, r.warns)
	}
}

// runOwnChecks adds the checks curio up has no part in: the upstreams
// fetches depend on, as the daemon reports them, the fetcher, and whether
// the content directory is writable. They read config.yaml, or the
// defaults when it doesn't load (its own check says why); with no home
// there is no daemon or content directory to check.
func runOwnChecks(ctx context.Context, flags *rootFlags, d deps, r *doctorReport) {
	cfg := config.Default()
	if d.defaults != nil {
		cfg = *d.defaults
	}
	path, err := daemonctl.HomePath(*flags.home)
	if err != nil {
		r.add("home", statusFail, err.Error(), "")
		return
	}
	home, homeErr := curiohome.Open(path)
	if homeErr == nil {
		if loaded, err := config.Load(home.ConfigPath()); err == nil {
			cfg = loaded
		}
		health, err := client.New(daemonctl.BaseURL(cfg, *flags.daemonURL)).Healthz(ctx)
		if err == nil && daemonctl.SameHome(health.Home, path) {
			// The services fetches depend on (the Jina fallback), as the
			// daemon has seen them answer.
			for _, u := range health.Upstreams {
				status, detail, hint := upstreamCheck(u)
				r.add(u.Name, status, detail, hint)
			}
		}
	}

	// The fetcher backend: native is always fine; web2md needs the bin.
	switch cfg.Fetcher.Default {
	case "native":
		r.add("fetcher", statusOK, "native (Go, no external deps)", "")
	case "web2md":
		bin := cfg.Fetcher.Web2MD.Bin
		if _, err := exec.LookPath(bin); err != nil {
			if _, statErr := os.Stat(bin); statErr != nil {
				r.add("fetcher", statusFail, "web2md not found at "+bin,
					"install Node + web2md, or switch to fetcher.default: native")
				break
			}
		}
		r.add("fetcher", statusOK, "web2md at "+bin, "")
	}

	if homeErr == nil {
		checkContentDir(home.ContentDir(), r)
	}
}

// checkContentDir checks that the daemon can write extracted content to dir
// by writing, then removing, a probe file.
func checkContentDir(dir string, r *doctorReport) {
	probe := filepath.Join(dir, ".doctor-probe")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.add("content dir", statusFail, "cannot create "+dir+": "+err.Error(), "")
		return
	}
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		r.add("content dir", statusFail, "not writable: "+err.Error(), "fix perms on "+dir)
		return
	}
	if err := os.Remove(probe); err != nil {
		r.add("content dir", statusWarn, dir+" writable, but the probe file stayed: "+err.Error(), "delete "+probe)
		return
	}
	r.add("content dir", statusOK, dir+" writable", "")
}

// upstreamCheck is doctor's check of an upstream the daemon reports, the
// Jina fallback today. It renders the state the daemon derived; only the
// failure class the hint is about is chosen here (dominantFailure).
func upstreamCheck(u client.UpstreamHealth) (status checkStatus, detail, hint string) {
	window := windowText(u.WindowSeconds)
	switch u.State {
	case client.UpstreamDisabled:
		return statusOK, "off (fetcher.native.jina_fallback: false)", ""
	case client.UpstreamIdle:
		return statusOK, "no calls in the last " + window + lastAnswer(u), ""
	case client.UpstreamOK:
		calls, _ := callCounts(u.Recent)
		return statusOK, fmt.Sprintf("%d calls in the last %s (%s)%s", calls, window, formatMap(u.Recent), lastAnswer(u)), ""
	case client.UpstreamDegraded:
		calls, failed := callCounts(u.Recent)
		return statusWarn, fmt.Sprintf("degraded: %d of %d calls in the last %s failed (%s)",
			failed, calls, window, formatMap(u.Recent)), upstreamHint(u)
	case client.UpstreamPaused:
		return statusWarn, "paused until " + localTime(u.CooldownUntil), upstreamHint(u)
	case client.UpstreamFailing:
		return statusFail, fmt.Sprintf("failing: no answer since %s; last failure %s at %s",
			noAnswerSince(u), u.LastFailureClass, localTime(u.LastFailureAt)), upstreamHint(u)
	}
	return statusWarn, fmt.Sprintf("state %q, which this curio doesn't know", u.State), "update curio"
}

// failureClasses are the call classes that count as an upstream's failures.
var failureClasses = []string{
	client.CallChallenged, client.CallForbidden, client.CallRateLimited,
	client.CallAuth, client.CallServerError, client.CallNetwork,
}

// jinaAdvice says, per failure class, what it means for the Jina fallback,
// the one upstream the daemon reports, and what to do about it.
var jinaAdvice = map[string]string{
	client.CallChallenged:  "r.jina.ai is challenging curio; see Troubleshooting in docs/setup.md",
	client.CallForbidden:   "r.jina.ai answered HTTP 403 without naming a target; see `curio daemon logs`",
	client.CallRateLimited: "Jina is rate-limiting curio; a fetcher.native.jina_api_key raises the limit",
	client.CallAuth: "check fetcher.native.jina_api_key or CURIO_JINA_API_KEY " +
		"(401: the key is invalid, 402: it has no balance left)",
	client.CallServerError: "r.jina.ai is failing on its side; fetches keep retrying",
	client.CallNetwork:     "curio can't reach r.jina.ai, or it doesn't answer in time; check connectivity",
}

// upstreamHint names the failure class that dominates u and what to do
// about it, or is empty when there is none to name.
func upstreamHint(u client.UpstreamHealth) string {
	class := dominantFailure(u)
	advice, ok := jinaAdvice[class]
	if !ok {
		return ""
	}
	return class + ": " + advice
}

// dominantFailure is the failure class with the most calls in u's window,
// or its last failure's class when two classes tie for the most or the
// window holds no failure.
func dominantFailure(u client.UpstreamHealth) string {
	var best string
	var most int
	tie := false
	for _, class := range failureClasses {
		switch n := u.Recent[class]; {
		case n > most:
			best, most, tie = class, n, false
		case n > 0 && n == most:
			tie = true
		}
	}
	if best == "" || tie {
		return u.LastFailureClass
	}
	return best
}

// callCounts sums an upstream's recent calls, and those of them that
// failed.
func callCounts(recent map[string]int) (calls, failed int) {
	for class, n := range recent {
		calls += n
		if slices.Contains(failureClasses, class) {
			failed += n
		}
	}
	return calls, failed
}

// lastAnswer says when an upstream last answered, after a "; ", or nothing
// when it hasn't since the daemon started.
func lastAnswer(u client.UpstreamHealth) string {
	if u.LastSuccessAt.IsZero() {
		return ""
	}
	return "; last answer " + localTime(u.LastSuccessAt)
}

// noAnswerSince says since when an upstream has not answered.
func noAnswerSince(u client.UpstreamHealth) string {
	if u.LastSuccessAt.IsZero() {
		return "the daemon started"
	}
	return localTime(u.LastSuccessAt)
}

// windowText writes a window of seconds without its zero units: "15m",
// "1h", "1m30s".
func windowText(seconds int) string {
	s := (time.Duration(seconds) * time.Second).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// localTime writes t in local time, to the minute.
func localTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}
