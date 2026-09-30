package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/service"
)

// daemonStep keeps the daemon running under its launchd agent: it installs
// the agent, starts the daemon through it, and restarts a daemon that runs
// another build of curio or another writing model than config.yaml's.
// Without a GUI session (over ssh) or a service manager, the daemon is
// started on demand instead.
type daemonStep struct{ w *world }

func (*daemonStep) Name() string { return "daemon" }

func (s *daemonStep) Check(ctx context.Context) Result { return s.w.checkDaemon(ctx) }

// checkDaemon is the daemon's check: this curio's curio-daemon serves the
// home, with config.yaml's writing model, under a loaded launchd agent
// that runs it.
func (w *world) checkDaemon(ctx context.Context) Result {
	hs := w.readHome()
	bin, err := w.deps.DaemonBin()
	if err != nil {
		return Result{Status: Fail, Detail: err.Error()}
	}
	if problem := programProblem(bin); problem != "" {
		return Result{Status: Fail, Detail: problem,
			Hint: "reinstall curio, or set CURIO_DAEMON_BIN to the curio-daemon to run"}
	}
	if r := w.refusal(hs); r.why != "" {
		return Result{Status: Fail, Detail: "not started: curio-daemon would refuse this home (see the curio home and config checks)",
			Hint: r.hint}
	}
	if why := hs.unusable(); why != "" {
		return Result{Status: Warn, Detail: "not checked: " + why + " (see the curio home check)"}
	}
	cfg := w.baseConfig(hs)
	if !w.homeReady(hs) {
		if res, taken := w.portTaken(ctx, hs, cfg); taken {
			return res
		}
		detail := "not running: there is no home for it yet"
		if hs.kind == homeOurs {
			detail = "to start once the new home is made"
		}
		return Result{Status: Fail, Detail: detail, Fix: &Fix{Summary: w.startSummary(ctx, bin)}}
	}
	env, err := w.connect(hs)
	if err != nil {
		return Result{Status: Fail, Detail: err.Error()}
	}
	st, err := bounded(ctx, w.times.Probe, env.Controller.Status)
	switch {
	case err != nil:
		return Result{Status: Fail, Detail: "can't read the daemon's status: " + err.Error()}
	case st.ServiceErr != nil:
		return Result{Status: Fail, Detail: "can't read the launchd agent's status: " + st.ServiceErr.Error()}
	case st.State == daemonctl.Legacy:
		return Result{Status: Fail, Detail: daemonctl.LegacyStopError(env.Controller.BaseURL, st.PID).Error()}
	}
	if _, home, answered := st.AnsweredBy(); answered && home != "" && !daemonctl.SameHome(home, hs.path) {
		return anotherHome(env.Controller.BaseURL, home)
	}
	pctx, cancel := context.WithTimeout(ctx, w.times.Probe)
	preflight := env.Controller.Service.Preflight(pctx, service.Spec{Program: bin})
	cancel()
	onDemand := errors.Is(preflight, service.ErrNoGUISession) || errors.Is(preflight, service.ErrUnsupported)
	if preflight != nil && !onDemand {
		return Result{Status: Fail, Detail: "the launchd agent can't be installed: " + preflight.Error()}
	}

	problems := w.daemonProblems(ctx, env, st, cfg)
	agent := ""
	if !onDemand {
		agent = agentProblem(st, bin)
	}
	if len(problems) == 0 && agent == "" {
		res := Result{Status: OK, Detail: describeRunning(st) + ", managed by launchd agent " + st.Service.Label}
		if onDemand {
			res.Status, res.Detail = Warn, describeRunning(st)+", started on demand: "+onDemandReason(preflight)
		}
		if st.Startup != nil {
			res.Status, res.Hint = Warn, "wait for it; `curio daemon logs -f` follows it"
		}
		return res
	}
	status := Warn
	if st.Health == nil && st.Startup == nil {
		status = Fail
	}
	summary := "restart curio-daemon"
	switch {
	case agent != "":
		summary = "install the launchd agent (it runs " + bin + ") and start curio-daemon through it"
		problems = append(problems, agent)
	case st.Health == nil && st.Startup == nil:
		summary = "start curio-daemon"
	}
	return Result{Status: status, Detail: strings.Join(problems, "; "), Fix: &Fix{Summary: summary}}
}

// daemonProblems lists what is wrong with the daemon itself: not running,
// another build, another writing model. A daemon that doesn't report its
// writing model (one from before it did) is never restarted for it. What
// answered with an error, when nothing served, is asked again for it.
func (w *world) daemonProblems(ctx context.Context, env daemonctl.Env, st daemonctl.Status, cfg config.Config) []string {
	var problems []string
	running := ""
	switch {
	case st.Health != nil:
		running = st.Health.Version
		if m := st.Health.GenerationModel; m != "" && m != cfg.Generation.Model {
			problems = append(problems, fmt.Sprintf("it writes with %s, not config.yaml's %s", m, cfg.Generation.Model))
		}
	case st.Startup != nil:
		running = st.Startup.Version
	case st.State == daemonctl.Running:
		return []string{fmt.Sprintf("running (pid %d), not answering", st.PID)}
	default:
		if _, err := bounded(ctx, w.times.Probe, env.Client.Healthz); err != nil && !errors.Is(err, client.ErrDaemonUnreachable) {
			return []string{fmt.Sprintf("not running: %s answers, but healthz failed: %v", env.Controller.BaseURL, err)}
		}
		return []string{"not running"}
	}
	if running != w.version {
		problems = append([]string{fmt.Sprintf("it runs curio %s, not this curio's %s", running, w.version)}, problems...)
	}
	return problems
}

// agentProblem says what is wrong with the launchd agent for the daemon
// to be kept running, or "".
func agentProblem(st daemonctl.Status, bin string) string {
	svc := st.Service
	switch {
	case svc == nil || !svc.Supported:
		return ""
	case !svc.Installed:
		return "no launchd agent keeps it running across logins and crashes"
	case svc.Program == "" || programProblem(svc.Program) != "":
		return fmt.Sprintf("its launchd agent runs %q, which is missing", svc.Program)
	case !svc.Loaded:
		return "its launchd agent is installed but not loaded"
	case !daemonctl.SameFile(svc.Program, bin):
		return fmt.Sprintf("its launchd agent runs %s, not this curio's %s", svc.Program, bin)
	case st.State == daemonctl.Running && !st.Managed():
		return fmt.Sprintf("the daemon (pid %d) was started outside its launchd agent", st.PID)
	}
	return ""
}

// describeRunning is the daemon as it runs: "running (pid 42, curio v1)",
// or starting and how far along.
func describeRunning(st daemonctl.Status) string {
	if s := st.Startup; s != nil {
		return fmt.Sprintf("starting (pid %d): %s", s.PID, s.Progress())
	}
	return fmt.Sprintf("running (pid %d, curio %s)", st.PID, st.Health.Version)
}

// onDemandReason says why the daemon is started on demand rather than by
// its launchd agent.
func onDemandReason(preflight error) string {
	if errors.Is(preflight, service.ErrNoGUISession) {
		return "there is no GUI login session for its launchd agent to run in (ssh); curio commands start it themselves"
	}
	return "there is no service manager on this platform; curio commands start it themselves"
}

// startSummary is the fix for a daemon that runs nowhere yet.
func (w *world) startSummary(ctx context.Context, bin string) string {
	if machine, err := w.probeMachine(ctx); err == nil && machine.OS == "darwin" {
		return "install the launchd agent (it runs " + bin + ") and start curio-daemon through it"
	}
	return "start curio-daemon (" + bin + ")"
}

// portTaken asks the daemon's address who is there, for a home not ready
// to connect to: another home's daemon, or one from before the lock
// protocol, holds the port the new daemon needs. taken is false otherwise.
func (w *world) portTaken(ctx context.Context, hs homeState, cfg config.Config) (Result, bool) {
	base := daemonctl.BaseURL(cfg, w.opts.DaemonURL)
	h, err := bounded(ctx, w.times.Probe, client.New(base).Healthz)
	home := ""
	switch {
	case err == nil && (h.PID == 0 || h.Home == ""):
		return Result{Status: Fail, Detail: daemonctl.LegacyStopError(base, 0).Error()}, true
	case err == nil:
		home = h.Home
	case client.StartupOf(err) != nil:
		home = client.StartupOf(err).Home
	}
	if home != "" && !daemonctl.SameHome(home, hs.path) {
		return anotherHome(base, home), true
	}
	return Result{}, false
}

// anotherHome is the result for a port another home's daemon serves.
func anotherHome(base, home string) Result {
	return Result{Status: Fail, Detail: fmt.Sprintf("%s is served by the curio-daemon for %s", base, home),
		Hint: "stop that daemon, or give this home another daemon.listen port in its config.yaml"}
}

// programProblem says why curio-daemon at path can't run, or "".
func programProblem(path string) string {
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return "curio-daemon is missing: " + err.Error()
	case info.IsDir():
		return "curio-daemon is missing: " + path + " is a directory"
	}
	return ""
}

// Apply validates the home again, as the daemon will, then installs the
// agent, which starts the daemon and waits until it serves (a migration is
// reported through ui), restarts a daemon of another build
// (EnsureVersion), and one running another writing model than
// config.yaml's. Over ssh, or with no service manager, the daemon is
// started on demand instead.
func (s *daemonStep) Apply(ctx context.Context, ui UI) error {
	w := s.w
	hs := w.readHome()
	if hs.kind != homeOurs {
		return errors.New("there is no curio home at " + hs.path)
	}
	cfg, err := config.Load(hs.home.ConfigPath())
	if err != nil {
		return err
	}
	if _, err := hs.home.CheckEmbedding(cfg.Embedding.Model, cfg.Embedding.Dim); err != nil {
		return err
	}
	env, err := w.deps.Connect(hs.home, cfg, w.opts.DaemonURL)
	if err != nil {
		return err
	}
	ctl := env.Controller
	ctl.OnMigrating = func(st client.Startup) { ui.Info(migratingNotice(st)) }
	switch err := ctl.Service.Preflight(ctx, service.Spec{Program: ctl.DaemonBin}); {
	case err == nil:
		changed, err := ctl.Install(ctx)
		if err != nil {
			return err
		}
		if changed {
			ui.Info("installed the launchd agent; it runs " + ctl.DaemonBin + ". macOS may announce a background item " +
				"from curio-daemon: keep it allowed in System Settings > General > Login Items & Extensions")
		}
	case errors.Is(err, service.ErrNoGUISession):
		ui.Warn("no GUI login session (ssh?): the launchd agent can't run now, so curio starts the daemon itself; " +
			"run curio up again from the Mac's desktop to install it")
	case errors.Is(err, service.ErrUnsupported):
	default:
		return err
	}
	restarted, err := ctl.EnsureVersion(ctx, w.version)
	if err != nil {
		return err
	}
	if restarted {
		ui.Info("restarted curio-daemon to run this curio (" + w.version + ")")
	}
	h, err := env.Client.Healthz(ctx)
	if err != nil {
		return err
	}
	if h.GenerationModel != "" && h.GenerationModel != cfg.Generation.Model {
		if err := ctl.Restart(ctx); err != nil {
			return err
		}
		ui.Info("restarted curio-daemon to write with " + cfg.Generation.Model)
	}
	return nil
}

// migratingNotice says why the step waits on the daemon.
func migratingNotice(s client.Startup) string {
	what := "its database"
	if s.Migrations != nil {
		what = fmt.Sprintf("its database (%d migrations)", s.Migrations.Total)
	}
	return "curio-daemon is migrating " + what + "; this can take a minute on a large library"
}

// checkAgent is doctor's check of the daemon's launchd agent: installed,
// loaded and running this curio's daemon, or why the daemon is started on
// demand instead.
func (w *world) checkAgent(ctx context.Context) Result {
	hs := w.readHome()
	if hs.kind != homeOurs {
		return Result{Status: Warn, Detail: "not checked: there is no curio home yet"}
	}
	bin, err := w.deps.DaemonBin()
	if err != nil {
		return Result{Status: Warn, Detail: "not checked: " + err.Error()}
	}
	env, err := w.connect(hs)
	if err != nil {
		return Result{Status: Warn, Detail: "not checked: " + err.Error()}
	}
	st, err := bounded(ctx, w.times.Probe, env.Controller.Service.Status)
	install := &Fix{Summary: "install the launchd agent (it runs " + bin + ")"}
	switch {
	case err != nil:
		return Result{Status: Warn, Detail: "can't read the agent's status: " + err.Error()}
	case !st.Supported:
		return Result{Status: OK, Detail: "none on this platform: curio commands start the daemon on demand"}
	case st.NoGUISession:
		return Result{Status: Warn, Detail: fmt.Sprintf("agent %s is installed, but there is no GUI login session for it "+
			"to run in (ssh); curio commands start the daemon themselves meanwhile", st.Label)}
	case !st.Installed:
		return Result{Status: Warn, Detail: "no agent: nothing keeps the daemon running across logins and crashes", Fix: install}
	case st.Program == "" || programProblem(st.Program) != "":
		return Result{Status: Warn, Detail: fmt.Sprintf("agent %s runs %q, which is missing", st.Label, st.Program), Fix: install}
	case !st.Loaded:
		return Result{Status: Warn, Detail: fmt.Sprintf("agent %s is installed but not loaded", st.Label), Fix: install}
	case !daemonctl.SameFile(st.Program, bin):
		return Result{Status: Warn, Detail: fmt.Sprintf("agent %s runs %s, not this curio's %s", st.Label, st.Program, bin),
			Fix: install}
	}
	return Result{Status: OK, Detail: fmt.Sprintf("agent %s loaded, runs %s", st.Label, st.Program)}
}

// checkDrift is the embeddings check: whether the daemon reports a drift
// since the library was indexed, a change of build whose re-embedded
// sample doesn't match the stored vectors or couldn't be checked, with the
// daemon's evidence. It is a warning, fixed by `curio reindex --all`,
// which curio up never runs by itself.
func (w *world) checkDrift(ctx context.Context) Result {
	hs := w.readHome()
	if !w.homeReady(hs) {
		return Result{Status: Warn, Detail: "not checked: no daemon serves this home"}
	}
	env, err := w.connect(hs)
	if err != nil {
		return Result{Status: Warn, Detail: "not checked: " + err.Error()}
	}
	h, err := bounded(ctx, w.times.Probe, env.Client.Healthz)
	switch {
	case client.StartupOf(err) != nil:
		return Result{Status: Warn, Detail: "not checked while the daemon starts"}
	case err != nil:
		return Result{Status: Warn, Detail: "not checked: " + err.Error()}
	case h.Home != "" && !daemonctl.SameHome(h.Home, hs.path):
		return Result{Status: Warn, Detail: "not checked: the daemon answering serves " + h.Home}
	case h.EmbeddingDrift == nil:
		return Result{Status: OK, Detail: "no drift reported since the library was indexed"}
	}
	d := h.EmbeddingDrift
	detail := "drifted: " + DriftChanges(d)
	if v := d.Verification; v != nil {
		detail = driftVerb(v) + ": " + DriftChanges(d) + "; " + v.Detail
	}
	return Result{Status: Warn, Detail: detail,
		Hint: "searches compare vectors from two builds; run `" + d.Fix + "` to re-embed the library"}
}

// DriftWarning is the warning line curio status and curio up print for a
// drift: what changed and the daemon's evidence, then the fix. A daemon
// that predates verifying a change sends no evidence.
func DriftWarning(d *client.EmbeddingDrift) string {
	v := d.Verification
	if v == nil {
		return fmt.Sprintf("warning: embeddings drifted since the library was indexed (%s); run `%s`",
			DriftChanges(d), d.Fix)
	}
	return fmt.Sprintf("warning: embeddings %s since the library was indexed (%s; %s); run `%s`",
		driftVerb(v), DriftChanges(d), v.Detail, d.Fix)
}

// driftVerb says how sure a drift is: a sample showed it, or none could.
func driftVerb(v *client.DriftVerification) string {
	if v.Verified {
		return "drifted"
	}
	return "may have drifted"
}

// DriftChanges lists what changed in a drift, recorded value first: "model
// digest sha256:0a → sha256:ac, Ollama 0.30.0 → 0.34.4".
func DriftChanges(d *client.EmbeddingDrift) string {
	parts := make([]string, 0, len(d.Changes))
	for _, c := range d.Changes {
		what := c.What
		switch c.What {
		case client.DriftModelDigest:
			what = "model digest"
		case client.DriftOllamaVersion:
			what = "Ollama"
		}
		parts = append(parts, fmt.Sprintf("%s %s → %s", what, c.Recorded, c.Current))
	}
	return strings.Join(parts, ", ")
}
