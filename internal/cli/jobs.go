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
	// One header line per job + indented detail. No truncation — full
	// error messages are the whole point of looking at this list. If
	// terminal width is the concern, pipe to less or use --limit.
	for i, j := range resp.Items {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%-7s  %-9s  attempts=%-2d  %s\n", j.Status, j.Kind, j.Attempts, j.ID)
		if j.DocURL != "" {
			fmt.Printf("  url: %s\n", j.DocURL)
			if j.DocTitle != "" && j.DocTitle != j.DocURL {
				fmt.Printf("  title: %s\n", truncate(j.DocTitle, 100))
			}
		}
		if j.LastError != nil && *j.LastError != "" {
			for _, line := range wrapLines(*j.LastError, 100) {
				fmt.Printf("  err: %s\n", line)
			}
		}
		// Payload is debugging signal when no DocURL was joined (import,
		// cluster, summarize jobs). For fetch/index it's redundant with
		// the URL we just printed.
		if j.DocURL == "" && len(j.Payload) > 0 {
			fmt.Printf("  payload: %s\n", condense(string(j.Payload)))
		}
		// next attempt only makes sense while the job can still run. For
		// terminal status (done, failed) the run_after field carries
		// stale data from the last retry cycle — display would be
		// confusing.
		if (j.Status == "pending" || j.Status == "running") && !j.RunAfter.IsZero() {
			fmt.Printf("  next attempt: %s\n", j.RunAfter.Local().Format("2006-01-02 15:04:05 MST"))
		}
	}
	fmt.Printf("\n%d job(s)\n", len(resp.Items))
}

// wrapLines breaks s on word boundaries so a long error message renders
// across multiple indented lines instead of one runaway. The first slice
// element has no leading whitespace; the caller indents each line itself.
func wrapLines(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if len(s) <= width {
		return []string{s}
	}
	var out []string
	for len(s) > width {
		// Try to break at the last space before width; fall back to a hard cut.
		cut := strings.LastIndex(s[:width], " ")
		if cut < width/2 {
			cut = width
		}
		out = append(out, s[:cut])
		s = strings.TrimLeft(s[cut:], " ")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
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
