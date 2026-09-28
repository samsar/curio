package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/importer"
)

func newImportSafariCmd(env *daemonctl.Env) *cobra.Command {
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
			if !flags.dryRun {
				if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
					return err
				}
			}

			path := filePath
			if path == "" {
				var err error
				path, err = importer.SafariPlist()
				switch {
				case errors.Is(err, fs.ErrPermission):
					return needsFullDiskAccess(err)
				case errors.Is(err, fs.ErrNotExist):
					return errors.New("safari bookmarks not found (is this macOS?); use --file to specify a path")
				case err != nil:
					return err
				}
			}

			// An *os.PathError already names the file.
			f, err := os.Open(path)
			if errors.Is(err, fs.ErrPermission) {
				return needsFullDiskAccess(err)
			}
			if err != nil {
				return err
			}
			defer f.Close()

			bms, err := importer.ParseSafari(f)
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "parsed %d bookmarks from Safari\n", len(bms))
			return importParsed(cmd.Context(), w, env.Client, "safari", bms, &flags)
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "Path to an arbitrary Bookmarks.plist file")
	attachImportFlags(cmd, &flags)
	return cmd
}

// needsFullDiskAccess is a permission error reading Safari's bookmarks,
// which macOS withholds, stat included, from a terminal app without Full
// Disk Access.
func needsFullDiskAccess(err error) error {
	return fmt.Errorf("%w\n\nGrant Full Disk Access to your terminal in System Settings → Privacy & Security", err)
}
