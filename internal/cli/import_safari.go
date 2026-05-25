package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/importer"
)

func newImportSafariCmd() *cobra.Command {
	var (
		filePath string
		flags    importFlags
	)
	cmd := &cobra.Command{
		Use:   "safari",
		Short: "Import Safari bookmarks (reads Bookmarks.plist)",
		Long: `Import bookmarks from Safari.

Default behavior: reads ~/Library/Safari/Bookmarks.plist. Use --file to
point at an arbitrary plist (e.g. a backup or a copy from another Mac).

Note: macOS requires Full Disk Access for the terminal app reading
Safari data. Grant it in System Settings → Privacy & Security → Full
Disk Access if you get a permission error.`,
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
				path = importer.SafariBookmarksPath()
				if path == "" {
					return errors.New("safari bookmarks not found (is this macOS?); use --file to specify a path")
				}
			}

			f, err := os.Open(path)
			if err != nil {
				if os.IsPermission(err) {
					return fmt.Errorf("open %s: %w\n\nGrant Full Disk Access to your terminal in System Settings → Privacy & Security", path, err)
				}
				return fmt.Errorf("open %s: %w", path, err)
			}
			defer f.Close()

			bms, err := importer.ParseSafari(f)
			if err != nil {
				return fmt.Errorf("parse: %w", err)
			}
			fmt.Printf("parsed %d bookmarks from Safari\n", len(bms))
			bms = flags.applyLimit(bms)
			if flags.limit > 0 {
				fmt.Printf("  limited to first %d\n", len(bms))
			}
			if flags.dryRun {
				return reportDryRun(bms)
			}
			if err := sendBatches(cmd.Context(), ctx, "safari", bms); err != nil {
				return err
			}
			if flags.follow {
				return followProgress(cmd.Context(), ctx)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "Path to an arbitrary Bookmarks.plist file")
	attachImportFlags(cmd, &flags)
	return cmd
}
