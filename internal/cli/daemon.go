package cli

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/daemonctl"
)

func newDaemonCmd(env *daemonctl.Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the curio-daemon process",
	}
	cmd.AddCommand(newDaemonStartCmd(env), newDaemonStopCmd(env), newDaemonStatusCmd(env), newDaemonLogsCmd(env))
	return cmd
}

func newDaemonStartCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the background",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "daemon running")
			return nil
		},
	}
}

func newDaemonStopCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := env.Controller.Stop(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
			return nil
		},
	}
}

func newDaemonStatusCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the daemon for this $CURIO_HOME is running, and its PID",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := env.Controller.Status(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), describeDaemonStatus(st, env.Home.Path))
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
	default:
		if st.Health != nil && st.Health.Home != "" && !daemonctl.SameHome(st.Health.Home, home) {
			return fmt.Sprintf("not running (the port is served by the daemon for %s)", st.Health.Home)
		}
		return "not running"
	}
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
