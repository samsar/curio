package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/version"
)

func newDaemonCmd(env *daemonctl.Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the curio-daemon process",
	}
	cmd.AddCommand(newDaemonStartCmd(env), newDaemonStopCmd(env), newDaemonStatusCmd(env), newDaemonLogsCmd(env),
		newDaemonInstallCmd(env), newDaemonUninstallCmd(env))
	return cmd
}

func newDaemonStartCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the background (through its launchd agent, when installed)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := env.Controller.EnsureRunning(ctx); err != nil {
				return err
			}
			st, err := env.Controller.Status(ctx)
			if err != nil {
				return err
			}
			if st.Managed() {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon running (launchd)")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon running")
			}
			if h := st.Health; h != nil && h.Version != version.String() {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: the daemon runs curio %s, and this is curio %s; %s\n",
					h.Version, version.String(), switchAdvice(env, st))
			}
			return nil
		},
	}
}

// switchAdvice says how to run this curio's daemon in place of the one
// running. A loaded agent starts its own program at the next command, so
// when that is another curio-daemon (an older install elsewhere), a stop
// would only bring the same build back: the agent needs repointing.
func switchAdvice(env *daemonctl.Env, st daemonctl.Status) string {
	bin := env.Controller.DaemonBin
	if svc := st.Service; svc != nil && svc.Loaded && !daemonctl.SameFile(svc.Program, bin) {
		from := ""
		if svc.Program != "" { // empty when the agent's definition can't be read
			from = " from " + svc.Program
		}
		return fmt.Sprintf("to switch, run `curio daemon install`, which repoints the launchd agent%s to %s "+
			"and restarts the daemon", from, bin)
	}
	return "to switch, run `curio daemon stop`, and the next command starts " + daemonProgram(env, st)
}

// daemonProgram is the curio-daemon the next start runs: the launchd
// agent's program when the agent runs the daemon, and the one next to
// this curio otherwise.
func daemonProgram(env *daemonctl.Env, st daemonctl.Status) string {
	if st.Service != nil && st.Service.Loaded && st.Service.Program != "" {
		return st.Service.Program
	}
	return env.Controller.DaemonBin
}

func newDaemonStopCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			stopped, err := env.Controller.Stop(ctx)
			if err != nil {
				return err
			}
			if !stopped {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon not running")
				return nil
			}
			if agentLoaded(ctx, env) {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped; its launchd agent starts it again at login, "+
					"or when a curio command needs it")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
			}
			return nil
		},
	}
}

// agentLoaded reports whether the daemon's launchd agent is loaded; a
// manager that can't say is taken for none.
func agentLoaded(ctx context.Context, env *daemonctl.Env) bool {
	if env.Controller.Service == nil {
		return false
	}
	st, err := env.Controller.Service.Status(ctx)
	return err == nil && st.Loaded
}

// agentName names the daemon's launchd agent in a message.
func agentName(st daemonctl.Status) string {
	if st.Service == nil {
		return "launchd agent"
	}
	return "launchd agent " + st.Service.Label
}

func newDaemonStatusCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the daemon for this $CURIO_HOME is running, its PID, and its launchd agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := env.Controller.Status(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), describeDaemonStatus(st, env.Home.Path))
			if line := describeAgent(st); line != "" {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
}

func describeDaemonStatus(st daemonctl.Status, home string) string {
	switch st.State {
	case daemonctl.Running:
		switch {
		case st.PID == 0:
			return "starting (lock held, pid not recorded yet)"
		case st.Startup != nil:
			return fmt.Sprintf("starting (pid %d, home %s, version %s): %s",
				st.PID, st.Startup.Home, st.Startup.Version, st.Startup.Progress())
		case st.Health == nil:
			return fmt.Sprintf("running (pid %d), not answering HTTP yet", st.PID)
		default:
			return fmt.Sprintf("running (pid %d, home %s, version %s)", st.PID, st.Health.Home, st.Health.Version)
		}
	case daemonctl.Stale:
		return fmt.Sprintf("not running (stale PID file: pid %d is left over from an earlier run and is ignored)", st.PID)
	case daemonctl.Legacy:
		return fmt.Sprintf("legacy daemon from an older curio is answering (version %s); "+
			"run `curio daemon stop` for how to retire it", st.Health.Version)
	case daemonctl.NotRunning:
	}
	if _, other, answered := st.AnsweredBy(); answered && other != "" && !daemonctl.SameHome(other, home) {
		return fmt.Sprintf("not running (the port is served by the daemon for %s)", other)
	}
	return "not running"
}

// describeAgent is status's line about the daemon's launchd agent, or
// empty where there is no launchd.
func describeAgent(st daemonctl.Status) string {
	if st.ServiceErr != nil {
		return fmt.Sprintf("launchd: can't read the agent's status: %v", st.ServiceErr)
	}
	svc := st.Service
	switch {
	case svc == nil || !svc.Supported:
		return ""
	case !svc.Installed:
		return "launchd: no agent (`curio daemon install` keeps the daemon running across logins and crashes)"
	case svc.NoGUISession:
		return fmt.Sprintf("launchd: agent %s is installed, but there is no GUI login session for it to run in (ssh); "+
			"curio commands start the daemon themselves meanwhile", svc.Label)
	case !svc.Loaded:
		return fmt.Sprintf("launchd: agent %s is installed but not loaded (`curio daemon install` loads it)", svc.Label)
	case st.Managed():
		return fmt.Sprintf("launchd: manages this daemon (agent %s, runs %s)", svc.Label, svc.Program)
	case svc.Running() && st.State != daemonctl.Running:
		return fmt.Sprintf("launchd: agent %s runs pid %d, which doesn't hold the home's lock (yet): "+
			"it is starting, or exiting", svc.Label, svc.PID)
	case st.State == daemonctl.Running:
		return fmt.Sprintf("launchd: agent %s is loaded, but the running daemon (pid %d) was started outside launchd; "+
			"`curio daemon stop` hands it over at the next command", svc.Label, st.PID)
	case svc.LastExit != "":
		return fmt.Sprintf("launchd: agent %s is loaded, daemon not running: it last exited with %s, "+
			"and launchd restarts it (`curio daemon logs` says why it exited)", svc.Label, svc.LastExit)
	default:
		return fmt.Sprintf("launchd: agent %s is loaded, daemon not running; launchd starts it at login "+
			"or when a curio command needs it", svc.Label)
	}
}

func newDaemonInstallCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Keep the daemon running with a launchd agent: started at login, restarted after a crash",
		Long: `Install a per-user launchd agent for this home's daemon (macOS only).
launchd then starts the daemon at login and restarts it if it crashes;
'curio daemon stop' stops it until a command needs it again. The agent
runs the daemon next to this curio, with a PATH of Homebrew's and the
system's directories, and none of this shell's environment: settings such
as tokens belong in config.yaml. Running it again repoints the agent at
this curio's daemon. Never uses sudo.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			// The agent would run a daemon that refuses the home, and
			// launchd would retry it every 10s.
			if _, err := env.Home.CheckEmbedding(env.Config.Embedding.Model, env.Config.Embedding.Dim); err != nil {
				return err
			}
			changed, err := env.Controller.Install(ctx)
			if err != nil {
				return err
			}
			st, err := env.Controller.Status(ctx)
			if err != nil {
				return err
			}
			printInstalled(cmd.OutOrStdout(), st, changed, daemonProgram(env, st))
			warnEnvOnlySettings(cmd.ErrOrStderr(), env.Config, env.Home.ConfigPath())
			return nil
		},
	}
}

// envOnlySettings are the settings the daemon also reads from its
// environment, by variable and config.yaml key.
var envOnlySettings = []struct {
	variable, key string
	configured    func(config.Config) bool
}{
	{"CURIO_GITHUB_TOKEN", "fetcher.github.token", func(c config.Config) bool { return c.Fetcher.GitHub.Token != "" }},
	{"CURIO_JINA_API_KEY", "fetcher.native.jina_api_key", func(c config.Config) bool { return c.Fetcher.Native.JinaAPIKey != "" }},
}

// warnEnvOnlySettings warns about each setting this shell's environment
// gives and config.yaml doesn't: a daemon the CLI spawns inherits it, and
// the agent's daemon doesn't. Nothing of the environment is copied into
// the agent, tokens least of all.
func warnEnvOnlySettings(w io.Writer, cfg config.Config, configPath string) {
	for _, s := range envOnlySettings {
		if os.Getenv(s.variable) != "" && !s.configured(cfg) {
			fmt.Fprintf(w, "warning: %s is set here, but the launchd agent's daemon won't see it; "+
				"set %s in %s instead\n", s.variable, s.key, configPath)
		}
	}
}

// printInstalled reports an install: what the agent is and runs, and the
// daemon it started.
func printInstalled(w io.Writer, st daemonctl.Status, changed bool, program string) {
	if changed {
		fmt.Fprintf(w, "%s installed; it runs %s\n", agentName(st), program)
	} else {
		fmt.Fprintf(w, "%s already installed; it runs %s\n", agentName(st), program)
	}
	fmt.Fprintln(w, "launchd starts the daemon at login and restarts it after a crash")
	if pid, _, answered := st.AnsweredBy(); answered {
		fmt.Fprintf(w, "daemon running (pid %d)\n", pid)
	}
	if changed {
		fmt.Fprintln(w, "macOS may announce a background item from curio-daemon: keep it allowed in "+
			"System Settings > General > Login Items & Extensions")
	}
}

func newDaemonUninstallCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the daemon's launchd agent; the CLI starts the daemon on demand again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			before, err := env.Controller.Status(ctx)
			if err != nil {
				return err
			}
			removed, err := env.Controller.Uninstall(ctx)
			if err != nil {
				return err
			}
			switch {
			case !removed:
				fmt.Fprintln(cmd.OutOrStdout(), "no launchd agent installed")
			case before.Managed():
				fmt.Fprintf(cmd.OutOrStdout(), "%s removed; daemon stopped "+
					"(the next curio command starts it on demand)\n", agentName(before))
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "%s removed\n", agentName(before))
			}
			return nil
		},
	}
}

// migratingNotice tells the user why a command is waiting on the daemon.
func migratingNotice(s client.Startup) string {
	what := "the database"
	if s.Migrations != nil {
		what = fmt.Sprintf("the database (%d migrations)", s.Migrations.Total)
	}
	return "curio-daemon is migrating " + what + "; this can take a minute on a large library; " +
		"`curio daemon logs -f` shows progress"
}

func newDaemonLogsCmd(env *daemonctl.Env) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Tail the daemon log",
		RunE: func(cmd *cobra.Command, _ []string) error {
			args := []string{"-n", "100"}
			if follow {
				args = append(args, "-f")
			}
			args = append(args, env.Home.DaemonLogPath())
			c := exec.CommandContext(cmd.Context(), "tail", args...)
			c.Stdout = cmd.OutOrStdout()
			c.Stderr = cmd.ErrOrStderr()
			return c.Run()
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow the log (like tail -f)")
	return cmd
}
