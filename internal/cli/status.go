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
	"github.com/samsar/curio/internal/version"
)

func newStatusCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status, embedding info, and basic counts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "cli:     %s\n", version.String())

			health, err := env.Client.Healthz(cmd.Context())
			if err != nil {
				if errors.Is(err, client.ErrDaemonUnreachable) {
					fmt.Fprintln(w, "daemon:  not running")
				} else {
					fmt.Fprintf(w, "daemon:  not answering healthz: %v\n", err)
				}
				fmt.Fprintf(w, "home:    %s\n", env.Home.Path)
				printDiskUsage(w, env.Home.Path)
				return nil
			}

			fmt.Fprintf(w, "daemon:  running  (version %s)\n", health.Version)
			fmt.Fprintf(w, "home:    %s\n", env.Home.Path)
			if warning := homeMismatchWarning(health.Home, env.Home.Path); warning != "" {
				fmt.Fprint(w, warning)
			}
			fmt.Fprintf(w, "schema:  v%d\n", health.SchemaVersion)
			fmt.Fprintf(w, "embed:   %s (dim %d)\n", health.EmbeddingModel, health.EmbeddingDim)

			sctx, scancel := context.WithTimeout(cmd.Context(), 1*time.Second)
			defer scancel()
			stats, err := env.Client.Stats(sctx)
			if err != nil {
				fmt.Fprintf(w, "\ncounts:    unavailable: %v\n", err)
			} else {
				fmt.Fprintf(w, "\nbookmarks: %d\n", stats.BookmarksTotal)
				fmt.Fprintf(w, "documents: %d\n", stats.DocumentsTotal)
				if len(stats.DocumentsByState) > 0 {
					fmt.Fprintf(w, "           %s\n", formatMap(stats.DocumentsByState))
				}
				if len(stats.JobsByStatus) > 0 {
					fmt.Fprintf(w, "jobs:      %s\n", formatMap(stats.JobsByStatus))
				}
			}

			printDiskUsage(w, env.Home.Path)

			mctx, mcancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer mcancel()
			m, err := env.Client.Metrics(mctx, 0)
			if err != nil {
				fmt.Fprintf(w, "\nperformance: unavailable: %v\n", err)
			} else if len(m.ByKind) > 0 {
				fmt.Fprintf(w, "\nperformance (last %ds):\n", m.WindowSeconds)
				for _, k := range m.ByKind {
					fmt.Fprintf(w, "  %-9s  done=%-5d  fail=%-4d  mean=%5.0fms  p50=%5.0fms  p95=%5.0fms  p99=%5.0fms",
						k.Kind, k.Count, k.Failed, k.MeanMS, k.P50MS, k.P95MS, k.P99MS)
					if k.Running > 0 {
						fmt.Fprintf(w, "  running=%d (oldest %ds)", k.Running, k.OldestRunningSeconds)
					}
					fmt.Fprintln(w)
				}
			}
			return nil
		},
	}
}

// formatMap renders a map[string]int as "key=val  key=val" sorted by key.
func formatMap(m map[string]int) string {
	parts := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, "  ")
}

// homeMismatchWarning warns when the daemon answering at this address serves
// another home, so the numbers that follow aren't this home's. It is empty
// when the homes match or the daemon predates reporting its home.
func homeMismatchWarning(daemonHome, localHome string) string {
	if daemonHome == "" || daemonctl.SameHome(daemonHome, localHome) {
		return ""
	}
	return fmt.Sprintf("warning: the daemon answering at this address serves %s, not %s;\n"+
		"         results below are for that home. Give each home its own daemon.listen port.\n",
		daemonHome, localHome)
}

// printDiskUsage shows the size of the database, content dir, and logs dir.
// Labels are left-aligned to a common width and sizes are right-aligned so
// the numbers line up in a column regardless of unit (GB/MB/KB).
func printDiskUsage(w io.Writer, homePath string) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "disk:")

	type diskRow struct{ label, size, suffix string }
	var rows []diskRow

	dbPath := filepath.Join(homePath, "curio.db")
	if info, err := os.Stat(dbPath); err == nil {
		rows = append(rows, diskRow{"db", humanSize(info.Size()), ""})
	}
	if info, err := os.Stat(dbPath + "-wal"); err == nil {
		rows = append(rows, diskRow{"db wal", humanSize(info.Size()), ""})
	}
	contentDir := filepath.Join(homePath, "content")
	if size, count, err := dirSize(contentDir); err == nil {
		rows = append(rows, diskRow{"content", humanSize(size), fmt.Sprintf(" (%d files)", count)})
	}
	logsDir := filepath.Join(homePath, "logs")
	if size, count, err := dirSize(logsDir); err == nil && count > 0 {
		rows = append(rows, diskRow{"logs", humanSize(size), fmt.Sprintf(" (%d files)", count)})
	}

	// Width the size column to the widest value so right edges align.
	sizeW := 0
	for _, r := range rows {
		if len(r.size) > sizeW {
			sizeW = len(r.size)
		}
	}
	for _, r := range rows {
		fmt.Fprintf(w, "  %-9s%*s%s\n", r.label+":", sizeW, r.size, r.suffix)
	}
}

func dirSize(path string) (int64, int, error) {
	var total int64
	var count int
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			if info, infoErr := d.Info(); infoErr == nil {
				total += info.Size()
				count++
			}
		}
		return nil
	})
	return total, count, err
}

func humanSize(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
