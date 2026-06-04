package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/importer"
)

func newImportFirefoxCmd() *cobra.Command {
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
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if !flags.dryRun {
				if err := ensureDaemon(ctx); err != nil {
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
			fmt.Printf("parsed %d bookmarks from Firefox\n", len(bms))
			bms = flags.applyLimit(bms)
			if flags.limit > 0 {
				fmt.Printf("  limited to first %d\n", len(bms))
			}
			if flags.dryRun {
				return reportDryRun(bms)
			}
			if err := sendBatches(cmd.Context(), ctx, "firefox", bms); err != nil {
				return err
			}
			if flags.follow {
				return followProgress(cmd.Context(), ctx)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "Path to an arbitrary places.sqlite file")
	attachImportFlags(cmd, &flags)
	return cmd
}
