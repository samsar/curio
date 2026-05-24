package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/daemonctl"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the curio-daemon process",
	}
	cmd.AddCommand(newDaemonStartCmd(), newDaemonStopCmd(), newDaemonStatusCmd(), newDaemonLogsCmd())
	return cmd
}

func newDaemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the background",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if ctx.Controller == nil {
				return errors.New("$CURIO_HOME not initialized; the daemon will create it on first run, but daemonctl needs it now")
			}
			if err := ctx.Controller.Start(); err != nil {
				return err
			}
			fmt.Println("daemon started")
			return nil
		},
	}
}

func newDaemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if ctx.Controller == nil {
				return errors.New("no daemon controller available")
			}
			if err := ctx.Controller.Stop(); err != nil {
				return err
			}
			fmt.Println("daemon stopped")
			return nil
		},
	}
}

func newDaemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon process status (PID, alive/stale)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if ctx.Controller == nil {
				fmt.Println("not running (no $CURIO_HOME)")
				return nil
			}
			s, pid, err := ctx.Controller.Status()
			if err != nil {
				return err
			}
			switch s {
			case daemonctl.StatusRunning:
				fmt.Printf("running (pid %d)\n", pid)
			case daemonctl.StatusStale:
				fmt.Printf("stale PID file (pid %d, process gone)\n", pid)
			default:
				fmt.Println("not running")
			}
			return nil
		},
	}
}

func newDaemonLogsCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Tail the daemon log",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok || ctx.Home == nil {
				return errors.New("no $CURIO_HOME")
			}
			logPath := ctx.Home.LogsDir() + "/daemon.log"
			args := []string{"-n", "100"}
			if follow {
				args = append(args, "-f")
			}
			args = append(args, logPath)
			c := exec.Command("tail", args...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow the log (like tail -f)")
	return cmd
}
