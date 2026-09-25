package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/textutil"
)

func newJobsCmd() *cobra.Command {
	cmd := newJobsListCmd()
	cmd.AddCommand(newJobsPruneCmd(), newJobsDeleteCmd())
	return cmd
}

func newJobsListCmd() *cobra.Command {
	var (
		failedOnly bool
		showAll    bool
		status     string
		kind       string
		limit      int
	)
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List recent background jobs (defaults to status=done; --failed / --all to widen)",
		Long: `Show recent background jobs. By default lists only status=done —
the audit of work that succeeded. Add --failed to debug failures,
--all to see every status, or --status for exact filtering.

Each row carries the target doc's URL, title, doc_id, and on-disk
markdown path (when applicable), so jumping to the underlying file
or running curio refetch is one copy/paste away.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}
			s := resolveJobsStatus(status, failedOnly, showAll)

			resp, err := ctx.Client.ListJobs(cmd.Context(), client.JobListOpts{
				Status: s, Kind: kind, Limit: limit,
			})
			if err != nil {
				return err
			}
			renderJobList(cmd.OutOrStdout(), resp)
			return nil
		},
	}
	cmd.Flags().BoolVar(&failedOnly, "failed", false, "Shortcut for --status=failed")
	cmd.Flags().BoolVar(&showAll, "all", false, "Show every status instead of just done")
	cmd.Flags().StringVar(&status, "status", "", "pending|running|done|failed (overrides defaults)")
	cmd.Flags().StringVar(&kind, "kind", "", "fetch|index|import|cluster|summarize")
	cmd.Flags().IntVar(&limit, "limit", 50, "Max rows to return (server caps at 500)")
	return cmd
}

func resolveJobsStatus(status string, failedOnly, all bool) string {
	switch {
	case status != "":
		return status
	case failedOnly:
		return "failed"
	case all:
		return ""
	default:
		return "done"
	}
}

func renderJobList(w io.Writer, resp *client.JobList) {
	if len(resp.Items) == 0 {
		fmt.Fprintln(w, "no jobs match")
		return
	}
	// One header line per job + indented detail. No truncation — full
	// error messages are the whole point of looking at this list. If
	// terminal width is the concern, pipe to less or use --limit.
	for i, j := range resp.Items {
		if i > 0 {
			fmt.Fprintln(w)
		}
		ts := j.UpdatedAt.Local().Format("2006-01-02 15:04:05 MST")
		fmt.Fprintf(w, "%-7s  %-9s  attempts=%-2d  %s  %s\n", j.Status, j.Kind, j.Attempts, ts, j.ID)
		if j.DocURL != "" {
			fmt.Fprintf(w, "  url: %s\n", j.DocURL)
			if j.DocTitle != "" && j.DocTitle != j.DocURL {
				fmt.Fprintf(w, "  title: %s\n", truncate(j.DocTitle, 100))
			}
			if docID := extractDocID(j.Payload); docID != "" {
				fmt.Fprintf(w, "  doc_id: %s\n", docID)
			}
			if j.MarkdownPath != "" {
				fmt.Fprintf(w, "  path: %s\n", j.MarkdownPath)
			}
		}
		if j.LastError != nil && *j.LastError != "" {
			for _, line := range wrapLines(*j.LastError, 100) {
				fmt.Fprintf(w, "  err: %s\n", line)
			}
		}
		// Payload is debugging signal when no DocURL was joined (import,
		// cluster, summarize jobs). For fetch/index it's redundant with
		// the URL we just printed.
		if j.DocURL == "" && len(j.Payload) > 0 {
			fmt.Fprintf(w, "  payload: %s\n", condense(string(j.Payload)))
		}
		// next attempt only makes sense while the job can still run. For
		// terminal status (done, failed) the run_after field carries
		// stale data from the last retry cycle — display would be
		// confusing.
		if (j.Status == "pending" || j.Status == "running") && !j.RunAfter.IsZero() {
			fmt.Fprintf(w, "  next attempt: %s\n", j.RunAfter.Local().Format("2006-01-02 15:04:05 MST"))
		}
	}
	fmt.Fprintf(w, "\n%d job(s)\n", len(resp.Items))
}

// wrapLines breaks s on word boundaries so a long error message renders
// across multiple indented lines instead of one runaway. The first slice
// element has no leading whitespace; the caller indents each line itself.
// width counts runes (see truncate).
func wrapLines(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for utf8.RuneCountInString(s) > width {
		line := textutil.TruncateRunes(s, width)
		// Break at the last space if that keeps at least half the line;
		// otherwise (one long token, or CJK text) hard-cut at width.
		if sp := strings.LastIndexByte(line, ' '); sp >= 0 && utf8.RuneCountInString(line[:sp]) >= width/2 {
			line = line[:sp]
		}
		out = append(out, line)
		s = strings.TrimLeft(s[len(line):], " ")
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
		Short: "Delete finished jobs older than a duration (keeps the jobs table from growing without bound)",
		Long: `Delete finished jobs (done or failed) whose updated_at is older than
the given duration. Useful for periodic cleanup so the jobs table
doesn't accumulate forever as you re-import and refetch.

Pending and running jobs are never pruned, however old: they are work
still in flight, and deleting one would leave its document stuck in
pending with nothing left to fetch it.

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
			fmt.Fprintf(cmd.OutOrStdout(), "pruned %d job(s) older than %s\n", resp.Deleted, olderThan)
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
		Short: "Delete finished jobs in a given status: done or failed",
		Long: `Remove every job in a finished status (done or failed). Useful after
you've triaged failures and decided "these are real, I'm not going to
recover them" to keep 'curio jobs --failed' output focused.

  curio jobs delete --status failed
  curio jobs delete --status done

Pending and running jobs can't be deleted: they are work still in
flight, and deleting one would leave its document stuck in pending.

Deleting a job doesn't change any document state. A failed doc stays
failed (still visible in 'curio docs --failed') and can still be
refetched.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if status == "" {
				return errors.New("--status is required (done|failed)")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}
			resp, err := ctx.Client.DeleteJobsByStatus(cmd.Context(), status)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted %d job(s) in status=%s\n", resp.Deleted, status)
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "done|failed")
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
