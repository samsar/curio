package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/version"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status, embedding info, and basic counts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}

			fmt.Printf("cli:     %s\n", version.String())

			pctx, cancel := context.WithTimeout(cmd.Context(), 500*time.Millisecond)
			defer cancel()
			health, err := ctx.Client.Healthz(pctx)
			if err != nil {
				fmt.Println("daemon:  not running")
				if ctx.Home != nil {
					fmt.Printf("home:    %s\n", ctx.Home.Path)
					printDiskUsage(ctx.Home.Path)
				}
				return nil
			}

			fmt.Printf("daemon:  running  (version %s)\n", health.Version)
			fmt.Printf("home:    %s\n", ctx.Home.Path)
			fmt.Printf("schema:  v%d\n", health.SchemaVersion)
			fmt.Printf("embed:   %s (dim %d)\n", health.EmbeddingModel, health.EmbeddingDim)

			sctx, scancel := context.WithTimeout(cmd.Context(), 1*time.Second)
			defer scancel()
			stats, err := ctx.Client.Stats(sctx)
			if err == nil && stats != nil {
				fmt.Printf("\nbookmarks: %d\n", stats.BookmarksTotal)
				fmt.Printf("documents: %d\n", stats.DocumentsTotal)
				if len(stats.DocumentsByState) > 0 {
					fmt.Printf("           %s\n", formatMap(stats.DocumentsByState))
				}
				if len(stats.JobsByStatus) > 0 {
					fmt.Printf("jobs:      %s\n", formatMap(stats.JobsByStatus))
				}
			}

			printDiskUsage(ctx.Home.Path)

			mctx, mcancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer mcancel()
			if m, err := ctx.Client.Metrics(mctx, 0); err == nil && m != nil && len(m.ByKind) > 0 {
				fmt.Printf("\nperformance (last %ds):\n", m.WindowSeconds)
				for _, k := range m.ByKind {
					fmt.Printf("  %-9s  done=%-5d  fail=%-4d  mean=%5.0fms  p50=%5.0fms  p95=%5.0fms  p99=%5.0fms",
						k.Kind, k.Count, k.Failed, k.MeanMS, k.P50MS, k.P95MS, k.P99MS)
					if k.Running > 0 {
						fmt.Printf("  running=%d (oldest %ds)", k.Running, k.OldestRunningSeconds)
					}
					fmt.Println()
				}
			}
			return nil
		},
	}
}

// formatMap renders a map[string]int as "key=val  key=val" sorted by key.
func formatMap(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, "  ")
}

// printDiskUsage shows the size of the database, content dir, and logs dir.
// Labels are left-aligned to a common width and sizes are right-aligned so
// the numbers line up in a column regardless of unit (GB/MB/KB).
func printDiskUsage(homePath string) {
	fmt.Println()
	fmt.Println("disk:")

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
		fmt.Printf("  %-9s%*s%s\n", r.label+":", sizeW, r.size, r.suffix)
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
