package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
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

			// Check daemon liveness without auto-starting; the user wants
			// to know if it's down.
			pctx, cancel := context.WithTimeout(cmd.Context(), 500*time.Millisecond)
			defer cancel()
			health, err := ctx.Client.Healthz(pctx)
			if err != nil {
				fmt.Println("daemon: not running")
				if ctx.Home != nil {
					fmt.Printf("home:   %s\n", ctx.Home.Path)
				}
				return nil
			}

			fmt.Printf("daemon:  running  (version %s)\n", health.Version)
			fmt.Printf("home:    %s\n", ctx.Home.Path)
			fmt.Printf("schema:  v%d\n", health.SchemaVersion)
			fmt.Printf("embed:   %s (dim %d)\n", health.EmbeddingModel, health.EmbeddingDim)

			// Also pull /v1/stats for queue depth and bookmark counts.
			sctx, scancel := context.WithTimeout(cmd.Context(), 1*time.Second)
			defer scancel()
			stats, err := ctx.Client.Stats(sctx)
			if err == nil && stats != nil {
				fmt.Printf("bookmarks: %d\n", stats.BookmarksTotal)
				fmt.Printf("documents: %d\n", stats.DocumentsTotal)
				if len(stats.DocumentsByState) > 0 {
					fmt.Printf("           by state: %v\n", stats.DocumentsByState)
				}
				if len(stats.JobsByStatus) > 0 {
					fmt.Printf("jobs:      %v\n", stats.JobsByStatus)
				}
			}

			// /v1/metrics gives us per-kind latency + throughput over a
			// rolling window. Useful for tuning workers, watching
			// imports, sanity-checking that things are progressing.
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
