package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/importer"
)

func newImportFirefoxCmd(env *daemonctl.Env) *cobra.Command {
	var (
		filePath string
		flags    importFlags
	)
	cmd := &cobra.Command{
		Use:   "firefox",
		Short: "Import Firefox bookmarks (reads places.sqlite)",
		Long: `Import bookmarks from Firefox.

Default behavior: reads the default profile's places.sqlite, discovered via
profiles.ini (the per-install default the running browser uses). Use --file
to point at an arbitrary places.sqlite (e.g. a backup or another machine).

Firefox keeps places.sqlite open and in WAL mode while running; curio reads
a temporary copy (including the -wal sidecar), so you don't need to quit
Firefox first.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !flags.dryRun {
				if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
					return err
				}
			}

			path := filePath
			if path == "" {
				path = importer.FirefoxBookmarksPath()
				if path == "" {
					return errors.New("firefox bookmarks not found (is Firefox installed?); use --file to specify a places.sqlite path")
				}
			}

			bms, err := importer.ParseFirefox(path)
			if err != nil {
				return fmt.Errorf("parse: %w", err)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "parsed %d bookmarks from Firefox\n", len(bms))
			return importParsed(cmd.Context(), w, env.Client, "firefox", bms, &flags)
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "Path to an arbitrary places.sqlite file")
	attachImportFlags(cmd, &flags)
	return cmd
}
