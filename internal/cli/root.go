// Package cli wires the curio CLI commands. The root command discovers the
// home and its daemon (daemonctl.Discover) before any command runs, and
// every command closes over the result, except the commands that build
// their own environment without creating a home: `curio up`, `curio
// doctor`, and bare `curio`.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/version"
)

// exitInterrupted is the exit code of a run that ctx cancelled: 128+SIGINT,
// what a shell reports for a command stopped with ctrl-c. SIGTERM gets it
// too, since the context says only that it was cancelled, not by which
// signal. So does a prompt left without an answer (setup.ErrAborted).
const exitInterrupted = 130

// ownsEnvironment is the annotation of a command that builds its own
// environment: the root's hook doesn't discover the home for it, which
// would create a missing one.
const ownsEnvironment = "curio.owns-environment"

// Run runs the CLI with args until it finishes or ctx is cancelled, and
// returns the process exit code. A failure is printed to stderr once, as
// "Error: <message>", followed for a usage error by where to find the
// command's usage. A run that ctx cancelled, or whose prompt the user left
// unanswered, prints nothing: its error is the interruption itself, which
// the user just caused.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return run(ctx, newRootCmd(), args, stdout, stderr)
}

// run is Run over root, which tests build with fakes.
func run(ctx context.Context, root *cobra.Command, args []string, stdout, stderr io.Writer) int {
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	// Cobra reports an unknown command, a flag it can't parse and a wrong
	// number of arguments before any hook runs, and curio declares no
	// required flags or flag groups, whose checks come later. So an error
	// from a run that never reached the root's hook is a usage error.
	started := false
	discover := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		started = true
		return discover(cmd, args)
	}

	cmd, err := root.ExecuteContextC(ctx)
	switch {
	case err == nil:
		return 0
	case ctx.Err() != nil, errors.Is(err, setup.ErrAborted):
		return exitInterrupted
	}
	fmt.Fprintf(stderr, "Error: %v\n", err)
	if !started {
		fmt.Fprintf(stderr, "Run '%s --help' for usage.\n", cmd.CommandPath())
	}
	return 1
}

// deps are what the commands work through. newRootCmd builds the real
// ones; tests build fakes, so no test runs brew or launchctl, or probes
// the machine it runs on.
type deps struct {
	// connect builds the daemon's environment for a home that exists;
	// every command's controller comes from it.
	connect   daemonctl.ConnectFunc
	probe     setup.Probe
	installer setup.Installer
	// defaults is what a new home's config.yaml starts from, and what a
	// home without one runs with; nil means config.Default().
	defaults *config.Config
	// newUI is setup.NewUI, or a test's scripted UI.
	newUI func(stdin io.Reader, stderr io.Writer, yes bool) setup.UI
	// geteuid is os.Geteuid: curio up refuses root.
	geteuid func() int
}

func newRootCmd() *cobra.Command {
	return newRootCmdWith(deps{
		connect:   daemonctl.Connect,
		probe:     setup.SystemProbe{},
		installer: setup.NewHomebrew(),
		newUI:     setup.NewUI,
		geteuid:   os.Geteuid,
	})
}

// newRootCmdWith is the root command over d.
func newRootCmdWith(d deps) *cobra.Command {
	var (
		daemonURL string
		homeFlag  string
		// env is filled in before any command that doesn't own its
		// environment runs; the commands hold a pointer to it.
		env daemonctl.Env
	)
	flags := &rootFlags{home: &homeFlag, daemonURL: &daemonURL}

	root := &cobra.Command{
		Use:   "curio",
		Short: "Personal context layer built from your bookmarks",
		Long: `curio imports your bookmarks, indexes the content, and exposes a hybrid
BM25 + vector search over the corpus. Talks to a long-running curio-daemon
over HTTP; auto-starts the daemon if it's not running. New here? Run
'curio up', which sets everything up.`,
		SilenceUsage: true,
		// Run prints the error, so it appears once.
		SilenceErrors: true,
		Annotations:   map[string]string{ownsEnvironment: "true"},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Annotations[ownsEnvironment] != "" {
				return nil
			}
			var err error
			env, err = daemonctl.DiscoverWith(homeFlag, daemonURL, d.connect)
			if err != nil {
				return err
			}
			// A command waiting on a migration says once why it waits.
			stderr := cmd.ErrOrStderr()
			env.Controller.OnMigrating = func(s client.Startup) {
				fmt.Fprintln(stderr, migratingNotice(s))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmd.Help(); err != nil {
				return err
			}
			if hint := upHint(cmd.Context(), homeFlag, daemonURL); hint != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "\n"+hint)
			}
			return nil
		},
	}

	root.PersistentFlags().StringVar(&daemonURL, "daemon-url", "", "Override daemon base URL (default: http://127.0.0.1:8765)")
	root.PersistentFlags().StringVar(&homeFlag, "curio-home", "", "Override $CURIO_HOME (default: ~/.curio or $CURIO_HOME env)")

	root.AddCommand(
		newUpCmd(flags, d),
		newVersionCmd(&env),
		newAddCmd(&env),
		newImportCmd(&env),
		newRefetchCmd(&env),
		newReindexCmd(&env),
		newJobsCmd(&env),
		newDocsCmd(&env),
		newSearchCmd(&env),
		newRelatedCmd(&env),
		newInterestsCmd(&env),
		newEvalCmd(&env),
		newStatusCmd(&env),
		newPauseCmd(&env),
		newResumeCmd(&env),
		newThrottleCmd(&env),
		newScheduleCmd(&env),
		newKeepAwakeCmd(&env),
		newDoctorCmd(flags, d),
		newDaemonCmd(&env),
	)
	return root
}

// rootFlags are the root's flags, which the commands that build their own
// environment read when they run.
type rootFlags struct {
	home, daemonURL *string
}

// setupFor is the setup Runner for opts over d, with the root's home and
// daemon URL.
func (f *rootFlags) setupFor(d deps, ui setup.UI, opts setup.Options) (*setup.Runner, error) {
	opts.Home, opts.DaemonURL = *f.home, *f.daemonURL
	return setup.New(opts, setup.Deps{
		UI:        ui,
		Probe:     d.probe,
		Installer: d.installer,
		Connect:   d.connect,
		Defaults:  d.defaults,
	})
}

func newVersionCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "curio %s\n", version.String())
			meta, err := env.Home.Meta()
			if err != nil {
				return fmt.Errorf("read the schema and embedder of %s: %w", env.Home.Path, err)
			}
			fmt.Fprintf(w, "schema: v%d  embedder: %s/%d\n", meta.SchemaVersion, meta.EmbeddingModel, meta.EmbeddingDim)
			return nil
		},
	}
}
