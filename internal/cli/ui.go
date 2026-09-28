package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/textutil"
)

func newUICmd(env *daemonctl.Env, openURL func(context.Context, string) error) *cobra.Command {
	var printURL bool
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the dashboard in your browser",
		Long: `Open curio's dashboard in your browser: the library's counts, the queue and
its progress, search, the documents with their text, and your interests.
It starts the daemon first if it isn't running, and opens the dashboard as
soon as the daemon answers: while it is still starting (a migration, say),
the page shows its progress and turns into the dashboard once it is ready.
The dashboard is served by the daemon on its own address, for this Mac
only; config.yaml's daemon.ui turns it off. --print prints its address
instead: open "$(curio ui --print)".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !env.Config.Daemon.UI {
				return fmt.Errorf("the dashboard is off: daemon.ui is false in %s", env.Home.ConfigPath())
			}
			// Not EnsureRunning: a starting daemon serves the page that
			// shows its progress, where a migration can take minutes.
			if _, err := env.Controller.EnsureStarted(cmd.Context()); err != nil {
				return err
			}
			url := dashboardURL(env.Controller.BaseURL)
			if printURL {
				fmt.Fprintln(cmd.OutOrStdout(), url)
				return nil
			}
			if err := openURL(cmd.Context(), url); err != nil {
				return fmt.Errorf("open the dashboard at %s: %w", url, err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&printURL, "print", false, "print the dashboard's address instead of opening it")
	return cmd
}

// dashboardURL is the dashboard of the daemon serving at base, which
// daemonctl.BaseURL leaves without a trailing slash.
func dashboardURL(base string) string { return base + "/ui/" }

// openInBrowser opens url in the default browser with macOS's open(1),
// without a shell.
func openInBrowser(ctx context.Context, url string) error {
	argv := setup.Command(setup.Detection{}, setup.OpenURL, url)
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput() //nolint:gosec // G204: /usr/bin/open with the dashboard's URL, from daemon.listen or --daemon-url, as its one argument
	if err != nil {
		return fmt.Errorf("%s: %w: %s", textutil.ShellJoin(argv), err, strings.TrimSpace(string(out)))
	}
	return nil
}
