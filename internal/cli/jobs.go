package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
)

func newJobsCmd() *cobra.Command {
	cmd := newJobsListCmd()
	cmd.AddCommand(newJobsPruneCmd(), newJobsDeleteCmd())
	return cmd
}

func newJobsListCmd() *cobra.Command {
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
			if docID := extractDocID(j.Payload); docID != "" {
				fmt.Printf("  doc_id: %s\n", docID)
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

func newJobsPruneCmd() *cobra.Command {
	var olderThan string
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete jobs older than a duration (use to keep the jobs table from growing without bound)",
		Long: `Delete any job — regardless of status — whose updated_at is older
than the given duration. Useful for periodic cleanup so the jobs table
doesn't accumulate forever as you re-import and refetch.

Duration accepts standard Go syntax plus "Nd" (days):

  curio jobs prune --older-than 30d
  curio jobs prune --older-than 24h
  curio jobs prune --older-than 2h30m

Deleting a job doesn't change any document state. A failed doc stays
failed (still visible in 'curio docs --failed') and can still be
refetched. This command only trims the audit/history table.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if olderThan == "" {
				return errors.New("--older-than is required (e.g. 30d, 24h, 2h30m)")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}
			resp, err := ctx.Client.PruneJobsOlderThan(cmd.Context(), olderThan)
			if err != nil {
				return err
			}
			fmt.Printf("pruned %d job(s) older than %s\n", resp.Deleted, olderThan)
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "", "Duration like 30d, 24h, 2h30m")
	return cmd
}

func newJobsDeleteCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete jobs in a given status (no all-status nuke path on purpose)",
		Long: `Remove every job in a specific status. Useful after you've triaged
failures and decided "these are real, I'm not going to recover them"
to keep 'curio jobs --failed' output focused.

  curio jobs delete --status failed
  curio jobs delete --status done

Deleting a job doesn't change any document state. A failed doc stays
failed (still visible in 'curio docs --failed') and can still be
refetched.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if status == "" {
				return errors.New("--status is required (pending|running|done|failed)")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}
			resp, err := ctx.Client.DeleteJobsByStatus(cmd.Context(), status)
			if err != nil {
				return err
			}
			fmt.Printf("deleted %d job(s) in status=%s\n", resp.Deleted, status)
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "pending|running|done|failed")
	return cmd
}

// extractDocID pulls a document_id field from a job's payload JSON.
// Returns "" if absent or the payload isn't parseable. We don't care
// about other fields — this is purely for printing the next-step hint.
func extractDocID(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var p struct {
		DocumentID string `json:"document_id"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	return p.DocumentID
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
