// Package cli wires the curio CLI commands. The root command discovers the
// home and its daemon (daemonctl.Discover) before any command runs, and
// every command closes over the result.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/version"
)

// Run runs the CLI with args until it finishes or ctx is cancelled, and
// returns the process exit code. A failure is printed to stderr once, as
// "Error: <message>".
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func newRootCmd() *cobra.Command {
	var (
		daemonURL string
		homeFlag  string
		// env is filled in before any command runs; the commands hold a
		// pointer to it.
		env daemonctl.Env
	)

	root := &cobra.Command{
		Use:   "curio",
		Short: "Personal context layer built from your bookmarks",
		Long: `curio imports your bookmarks, indexes the content, and exposes a hybrid
BM25 + vector search over the corpus. Talks to a long-running curio-daemon
over HTTP; auto-starts the daemon if it's not running.`,
		SilenceUsage: true,
		// Run prints the error, so it appears once.
		SilenceErrors: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			var err error
			env, err = daemonctl.Discover(homeFlag, daemonURL)
			return err
		},
	}

	root.PersistentFlags().StringVar(&daemonURL, "daemon-url", "", "Override daemon base URL (default: http://127.0.0.1:8765)")
	root.PersistentFlags().StringVar(&homeFlag, "curio-home", "", "Override $CURIO_HOME (default: ~/.curio or $CURIO_HOME env)")

	root.AddCommand(
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
		newDoctorCmd(&env),
		newDaemonCmd(&env),
	)
	return root
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
