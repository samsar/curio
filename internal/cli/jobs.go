package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
)

func newJobsCmd() *cobra.Command {
	var (
		failedOnly bool
		status     string
		kind       string
		limit      int
	)
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List recent background jobs (filter with --failed, --status, --kind)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}
			s := status
			if failedOnly {
				s = "failed"
			}

			resp, err := ctx.Client.ListJobs(cmd.Context(), client.JobListOpts{
				Status: s, Kind: kind, Limit: limit,
			})
			if err != nil {
				return err
			}
			renderJobList(resp)
			return nil
		},
	}
	cmd.Flags().BoolVar(&failedOnly, "failed", false, "Shortcut for --status=failed")
	cmd.Flags().StringVar(&status, "status", "", "pending|running|done|failed")
	cmd.Flags().StringVar(&kind, "kind", "", "fetch|index|import|cluster|summarize")
	cmd.Flags().IntVar(&limit, "limit", 50, "Max rows to return (server caps at 500)")
	return cmd
}

func renderJobList(resp *client.JobList) {
	if len(resp.Items) == 0 {
		fmt.Println("no jobs match")
		return
	}
	fmt.Printf("%-36s  %-9s  %-7s  %-3s  %s\n", "ID", "KIND", "STATUS", "ATT", "DETAIL")
	for _, j := range resp.Items {
		detail := ""
		if j.LastError != nil {
			detail = truncate(*j.LastError, 80)
		} else if len(j.Payload) > 0 {
			detail = truncate(condense(string(j.Payload)), 80)
		}
		fmt.Printf("%-36s  %-9s  %-7s  %-3d  %s\n", j.ID, j.Kind, j.Status, j.Attempts, detail)
	}
}

// condense flattens JSON whitespace so a job's payload renders on one line.
func condense(s string) string {
	// Cheap: drop newlines/tabs. Don't bother re-parsing.
	r := strings.NewReplacer("\n", " ", "\t", " ", "  ", " ")
	out := r.Replace(s)
	for strings.Contains(out, "  ") {
		out = strings.ReplaceAll(out, "  ", " ")
	}
	return out
}

// silenced linter complaining about unused context.
var _ = context.Background
