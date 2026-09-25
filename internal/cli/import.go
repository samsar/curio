package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/importer"
)

// Batch size when POSTing to /v1/bookmarks/import. 500 keeps each HTTP
// request well under typical proxy limits even for thousand-bookmark
// folders and gives progress updates that feel responsive.
const importBatchSize = 500

// importFlags are the shared flags across all `curio import` subcommands.
// Attached via attachImportFlags so adding flags later only needs one edit.
type importFlags struct {
	limit  int
	dryRun bool
	follow bool
}

func attachImportFlags(cmd *cobra.Command, f *importFlags) {
	cmd.Flags().IntVar(&f.limit, "limit", 0, "Stop after N bookmarks (0 = no limit)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Parse + filter only; don't POST to the daemon")
	cmd.Flags().BoolVar(&f.follow, "follow", false, "After import, poll /v1/stats until the fetch/index queue drains")
}

// applyLimit trims a parsed slice to flags.limit if set, saying so.
func (f *importFlags) applyLimit(w io.Writer, bms []importer.ParsedBookmark) []importer.ParsedBookmark {
	if f.limit <= 0 {
		return bms
	}
	if len(bms) > f.limit {
		bms = bms[:f.limit]
	}
	fmt.Fprintf(w, "  limited to first %d\n", len(bms))
	return bms
}

func newImportCmd(env *daemonctl.Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Bulk-import bookmarks from a browser or exported file",
	}
	cmd.AddCommand(newImportHTMLCmd(env), newImportChromeCmd(env), newImportSafariCmd(env), newImportFirefoxCmd(env))
	return cmd
}

func newImportHTMLCmd(env *daemonctl.Env) *cobra.Command {
	var flags importFlags
	cmd := &cobra.Command{
		Use:   "html <file>",
		Short: "Import a Netscape HTML bookmark export (works for any browser)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !flags.dryRun {
				if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
					return err
				}
			}

			// An *os.PathError already names the file.
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()

			bms, err := importer.ParseHTML(f)
			if err != nil {
				return fmt.Errorf("parse %s: %w", args[0], err)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "parsed %d bookmarks from %s\n", len(bms), filepath.Base(args[0]))
			return importParsed(cmd.Context(), w, env.Client, "html", bms, &flags)
		},
	}
	attachImportFlags(cmd, &flags)
	return cmd
}

func newImportChromeCmd(env *daemonctl.Env) *cobra.Command {
	var (
		profile      string
		allProfiles  bool
		listProfiles bool
		filePath     string
		flags        importFlags
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
			w := cmd.OutOrStdout()
			if listProfiles {
				profiles, err := importer.DiscoverChromeProfiles()
				if err != nil {
					return err
				}
				if len(profiles) == 0 {
					fmt.Fprintln(w, "no Chrome profiles found")
					return nil
				}
				fmt.Fprintln(w, "Chrome profiles:")
				for _, p := range profiles {
					fmt.Fprintf(w, "  %-15s  %s\n", p.Dir, p.Name)
				}
				return nil
			}

			if !flags.dryRun {
				if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
					return err
				}
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
					return fmt.Errorf("chrome profile %q not found (use --list-profiles to see available)", want)
				}
				files = []string{match.BookmarkFile}
			}

			for _, fp := range files {
				if err := importChromeFile(cmd.Context(), w, env.Client, fp, &flags); err != nil {
					return err
				}
			}
			if flags.follow {
				return followProgress(cmd.Context(), w, env.Client)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "", "Chrome profile dir name (Default, Profile 1, ...) or display name")
	cmd.Flags().BoolVar(&allProfiles, "all-profiles", false, "Import every discovered Chrome profile")
	cmd.Flags().BoolVar(&listProfiles, "list-profiles", false, "List Chrome profiles and exit")
	cmd.Flags().StringVar(&filePath, "file", "", "Path to an arbitrary Chrome Bookmarks JSON file")
	attachImportFlags(cmd, &flags)
	return cmd
}

func importChromeFile(ctx context.Context, w io.Writer, c *client.Client, path string, flags *importFlags) error {
	// An *os.PathError already names the file.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bms, err := importer.ParseChrome(f)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	fmt.Fprintf(w, "parsed %d bookmarks from %s\n", len(bms), profileLabelFromPath(path))
	bms = flags.applyLimit(w, bms)
	if flags.dryRun {
		reportDryRun(w, bms)
		return nil
	}
	return sendBatches(ctx, w, c, "chrome", bms)
}

// importParsed applies the shared import flags to one source's parsed
// bookmarks: --limit, then either --dry-run's local report or the upload,
// followed by --follow.
func importParsed(ctx context.Context, w io.Writer, c *client.Client, source string, bms []importer.ParsedBookmark, flags *importFlags) error {
	bms = flags.applyLimit(w, bms)
	if flags.dryRun {
		reportDryRun(w, bms)
		return nil
	}
	if err := sendBatches(ctx, w, c, source, bms); err != nil {
		return err
	}
	if flags.follow {
		return followProgress(ctx, w, c)
	}
	return nil
}

// followProgress polls /v1/stats every 2 seconds and prints a one-line
// progress update until the queue is drained (zero pending + zero running).
// Cancelling ctx (ctrl-c) is a quiet stop: it prints an interrupt notice
// and returns nil, since the import itself has finished.
func followProgress(ctx context.Context, w io.Writer, c *client.Client) error {
	fmt.Fprintln(w, "\nwatching queue drain — ctrl-c to exit")
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	startedAt := time.Now()
	var (
		lastFinished int // done + failed (terminal counters)
		lastTick     = startedAt
	)
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(w, "\ninterrupted")
			return nil
		case <-tick.C:
		}
		stats, err := c.Stats(ctx)
		if err != nil {
			if ctx.Err() == nil { // an interrupt is reported by the select
				fmt.Fprintf(w, "  (stats unavailable: %v)\n", err)
			}
			continue
		}
		pending := stats.JobsByStatus["pending"]
		running := stats.JobsByStatus["running"]
		done := stats.JobsByStatus["done"]
		failed := stats.JobsByStatus["failed"]
		fetched := stats.DocumentsByState["fetched"]

		// Rate = jobs that finished (succeeded OR failed) per second since
		// the previous tick. We compute against the previous tick's
		// timestamp, not startedAt, so the rate doesn't smear over the
		// whole run.
		finished := done + failed
		elapsed := time.Since(lastTick).Seconds()
		var rate float64
		if elapsed > 0 {
			rate = float64(finished-lastFinished) / elapsed
		}
		var eta time.Duration
		if rate > 0 && pending+running > 0 {
			eta = time.Duration(float64(pending+running) / rate * float64(time.Second)).Round(time.Second)
		}
		fmt.Fprintf(w, "  done=%d  pending=%d  running=%d  failed=%d  fetched=%d   rate≈%.1f/s   eta≈%s\n",
			done, pending, running, failed, fetched, rate, eta)
		lastFinished = finished
		lastTick = time.Now()

		if pending == 0 && running == 0 {
			fmt.Fprintf(w, "\nqueue drained after %s   (%d done, %d failed)\n",
				time.Since(startedAt).Round(time.Second), done, failed)
			if failed > 0 {
				fmt.Fprintln(w, "  see failures: curio jobs --failed")
			}
			return nil
		}
	}
}

// reportDryRun prints the same summary sendBatches would, computed
// locally from the parsed list without contacting the daemon.
func reportDryRun(w io.Writer, bms []importer.ParsedBookmark) {
	filtered := 0
	by := map[importer.FilterReason]int{}
	for _, b := range bms {
		if ok, why := importer.Indexable(b.URL); !ok {
			filtered++
			by[why]++
		}
	}
	fmt.Fprintln(w, "\ndry-run — nothing sent to the daemon")
	fmt.Fprintf(w, "  would import:  %d\n", len(bms)-filtered)
	fmt.Fprintf(w, "  would filter:  %d\n", filtered)
	printFilterReasons(w, by)
}

// printFilterReasons prints how many bookmarks each reason filtered, one
// indented line per reason in a stable order.
func printFilterReasons(w io.Writer, by map[importer.FilterReason]int) {
	for _, reason := range slices.Sorted(maps.Keys(by)) {
		fmt.Fprintf(w, "    %s: %d\n", reason, by[reason])
	}
}

// profileLabelFromPath turns ".../Chrome/Default/Bookmarks" into "Default".
func profileLabelFromPath(p string) string {
	dir := filepath.Dir(p)
	return filepath.Base(dir)
}

// pickChromeProfile matches want against a profile's directory exactly,
// then against its display name, ignoring case.
func pickChromeProfile(profiles []importer.ChromeProfile, want string) *importer.ChromeProfile {
	for i, p := range profiles {
		if p.Dir == want {
			return &profiles[i]
		}
	}
	for i, p := range profiles {
		if strings.EqualFold(p.Name, want) {
			return &profiles[i]
		}
	}
	return nil
}

// sendBatches POSTs the parsed list to /v1/bookmarks/import in chunks and
// prints progress. Returns nil iff every batch succeeded.
func sendBatches(ctx context.Context, w io.Writer, c *client.Client, source string, bms []importer.ParsedBookmark) error {
	if len(bms) == 0 {
		fmt.Fprintln(w, "nothing to import")
		return nil
	}
	var (
		totalCreated, totalSkipped, totalFiltered, totalJobs int
		totalErrors                                          []string
		filteredBy                                           = map[importer.FilterReason]int{}
		start                                                = time.Now()
	)

	for i := 0; i < len(bms); i += importBatchSize {
		end := min(i+importBatchSize, len(bms))
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

		resp, err := c.ImportBookmarks(ctx, client.ImportRequest{
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
		fmt.Fprintf(w, "  ...sent %d/%d (created %d, skipped %d, filtered %d so far)\n",
			end, len(bms), totalCreated, totalSkipped, totalFiltered)
	}

	dur := time.Since(start)
	fmt.Fprintf(w, "\ndone in %s\n", dur.Round(time.Millisecond))
	fmt.Fprintf(w, "  created:       %d\n", totalCreated)
	fmt.Fprintf(w, "  skipped (dup): %d\n", totalSkipped)
	fmt.Fprintf(w, "  filtered:      %d\n", totalFiltered)
	printFilterReasons(w, filteredBy)
	fmt.Fprintf(w, "  fetch jobs:    %d enqueued\n", totalJobs)
	if len(totalErrors) > 0 {
		fmt.Fprintf(w, "  errors:        %d (first 10 shown)\n", len(totalErrors))
		for i, e := range totalErrors {
			if i >= 10 {
				break
			}
			fmt.Fprintf(w, "    %s\n", e)
		}
	}
	return nil
}
