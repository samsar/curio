package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/samansartipi/curio/internal/client"
	"github.com/samansartipi/curio/internal/importer"
)

// Batch size when POSTing to /v1/bookmarks/import. 500 keeps each HTTP
// request well under typical proxy limits even for thousand-bookmark
// folders and gives progress updates that feel responsive.
const importBatchSize = 500

func newImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Bulk-import bookmarks from a browser or exported file",
	}
	cmd.AddCommand(newImportHTMLCmd(), newImportChromeCmd())
	return cmd
}

func newImportHTMLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "html <file>",
		Short: "Import a Netscape HTML bookmark export (works for any browser)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}

			f, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open %s: %w", args[0], err)
			}
			defer f.Close()

			bms, err := importer.ParseHTML(f)
			if err != nil {
				return fmt.Errorf("parse: %w", err)
			}
			fmt.Printf("parsed %d bookmarks from %s\n", len(bms), filepath.Base(args[0]))
			return sendBatches(cmd.Context(), ctx, "html", bms)
		},
	}
}

func newImportChromeCmd() *cobra.Command {
	var (
		profile      string
		allProfiles  bool
		listProfiles bool
		filePath     string
	)
	cmd := &cobra.Command{
		Use:   "chrome",
		Short: "Import Chrome bookmarks (reads the live profile file)",
		Long: `Import bookmarks from Chrome.

Default behavior: reads the "Default" profile's Bookmarks file. Use
--profile to pick another profile, --all-profiles to import every
profile, --list-profiles to see what's available, or --file to point
at an arbitrary Bookmarks JSON file (e.g. a backup).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}

			if listProfiles {
				profiles, err := importer.DiscoverChromeProfiles()
				if err != nil {
					return err
				}
				if len(profiles) == 0 {
					fmt.Println("no Chrome profiles found")
					return nil
				}
				fmt.Println("Chrome profiles:")
				for _, p := range profiles {
					fmt.Printf("  %-15s  %s\n", p.Dir, p.Name)
				}
				return nil
			}

			if err := ensureDaemon(ctx); err != nil {
				return err
			}

			var files []string
			switch {
			case filePath != "":
				files = []string{filePath}
			case allProfiles:
				profiles, err := importer.DiscoverChromeProfiles()
				if err != nil {
					return err
				}
				if len(profiles) == 0 {
					return errors.New("no Chrome profiles found")
				}
				for _, p := range profiles {
					files = append(files, p.BookmarkFile)
				}
			default:
				want := profile
				if want == "" {
					want = "Default"
				}
				profiles, err := importer.DiscoverChromeProfiles()
				if err != nil {
					return err
				}
				match := pickChromeProfile(profiles, want)
				if match == nil {
					return fmt.Errorf("Chrome profile %q not found (use --list-profiles to see available)", want)
				}
				files = []string{match.BookmarkFile}
			}

			for _, fp := range files {
				if err := importChromeFile(cmd.Context(), ctx, fp); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "", "Chrome profile dir name (Default, Profile 1, ...) or display name")
	cmd.Flags().BoolVar(&allProfiles, "all-profiles", false, "Import every discovered Chrome profile")
	cmd.Flags().BoolVar(&listProfiles, "list-profiles", false, "List Chrome profiles and exit")
	cmd.Flags().StringVar(&filePath, "file", "", "Path to an arbitrary Chrome Bookmarks JSON file")
	return cmd
}

func importChromeFile(httpCtx context.Context, c *Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	bms, err := importer.ParseChrome(f)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	fmt.Printf("parsed %d bookmarks from %s\n", len(bms), profileLabelFromPath(path))
	return sendBatches(httpCtx, c, "chrome", bms)
}

// profileLabelFromPath turns ".../Chrome/Default/Bookmarks" into "Default".
func profileLabelFromPath(p string) string {
	dir := filepath.Dir(p)
	return filepath.Base(dir)
}

// pickChromeProfile matches by Dir or display Name (case-insensitive on name).
func pickChromeProfile(profiles []importer.ChromeProfile, want string) *importer.ChromeProfile {
	for i, p := range profiles {
		if p.Dir == want {
			return &profiles[i]
		}
	}
	for i, p := range profiles {
		if p.Name == want {
			return &profiles[i]
		}
	}
	return nil
}

// sendBatches POSTs the parsed list to /v1/bookmarks/import in chunks and
// prints progress. Returns nil iff every batch succeeded.
func sendBatches(httpCtx context.Context, c *Context, source string, bms []importer.ParsedBookmark) error {
	if len(bms) == 0 {
		fmt.Println("nothing to import")
		return nil
	}
	var (
		totalCreated, totalSkipped, totalFiltered, totalJobs int
		totalErrors                                          []string
		filteredBy                                           = map[importer.FilterReason]int{}
		start                                                = time.Now()
	)

	for i := 0; i < len(bms); i += importBatchSize {
		end := i + importBatchSize
		if end > len(bms) {
			end = len(bms)
		}
		batch := bms[i:end]
		converted := make([]client.ImportBookmark, len(batch))
		for j, b := range batch {
			converted[j] = client.ImportBookmark{
				URL:        b.URL,
				Title:      b.Title,
				FolderPath: b.FolderPath,
				Tags:       b.Tags,
				SavedAt:    b.SavedAt,
			}
		}

		resp, err := c.Client.ImportBookmarks(httpCtx, client.ImportRequest{
			Source:    source,
			Bookmarks: converted,
		})
		if err != nil {
			return fmt.Errorf("batch %d-%d: %w", i, end, err)
		}
		totalCreated += resp.Created
		totalSkipped += resp.Skipped
		totalFiltered += resp.Filtered
		totalJobs += resp.JobsEnqueued
		for k, v := range resp.FilteredBy {
			filteredBy[importer.FilterReason(k)] += v
		}
		totalErrors = append(totalErrors, resp.Errors...)
		fmt.Printf("  ...sent %d/%d (created %d, skipped %d, filtered %d so far)\n",
			end, len(bms), totalCreated, totalSkipped, totalFiltered)
	}

	dur := time.Since(start)
	fmt.Printf("\ndone in %s\n", dur.Round(time.Millisecond))
	fmt.Printf("  created:       %d\n", totalCreated)
	fmt.Printf("  skipped (dup): %d\n", totalSkipped)
	fmt.Printf("  filtered:      %d\n", totalFiltered)
	if len(filteredBy) > 0 {
		keys := make([]importer.FilterReason, 0, len(filteredBy))
		for k := range filteredBy {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, k := range keys {
			fmt.Printf("    %s: %d\n", k, filteredBy[k])
		}
	}
	fmt.Printf("  fetch jobs:    %d enqueued\n", totalJobs)
	if len(totalErrors) > 0 {
		fmt.Printf("  errors:        %d (first 10 shown)\n", len(totalErrors))
		for i, e := range totalErrors {
			if i >= 10 {
				break
			}
			fmt.Printf("    %s\n", e)
		}
	}
	return nil
}
