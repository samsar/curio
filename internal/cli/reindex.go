package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
)

func newReindexCmd(env *daemonctl.Env) *cobra.Command {
	var (
		all   bool
		state string
	)
	cmd := &cobra.Command{
		Use:   "reindex [document-id]",
		Short: "Re-chunk and re-embed already-fetched documents (no re-fetch)",
		Long: `Reindex re-runs chunking + embedding over a document's existing
extraction — without re-fetching it. Use it after changing the embedding
model (same dimension) or chunker settings, or to pick up new bookmark tags.

Documents must already have content: --all targets state=fetched by default
and, in any state, skips documents that were never fetched.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}

			if all {
				return reindexAll(cmd.Context(), cmd.OutOrStdout(), env.Client, state)
			}
			if len(args) != 1 {
				return errors.New("provide a document ID or pass --all")
			}
			return reindexOne(cmd.Context(), cmd.OutOrStdout(), env.Client, args[0])
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Reindex every document with content (default state=fetched; use --state to override)")
	cmd.Flags().StringVar(&state, "state", "",
		"With --all, reindex the documents with content in this state (pending|fetched|failed|dead; default fetched)")
	return cmd
}

func reindexOne(ctx context.Context, w io.Writer, c *client.Client, docID string) error {
	resp, err := c.ReindexDocument(ctx, docID)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "reindex enqueued for document %s (job %s)\n", docID, resp.JobID)
	fmt.Fprintf(w, "  follow it: curio jobs show %s\n", resp.JobID)
	return nil
}

func reindexAll(ctx context.Context, w io.Writer, c *client.Client, state string) error {
	resp, err := c.ReindexAll(ctx, state)
	if err != nil {
		return err
	}
	label := "documents in state=fetched"
	if state != "" {
		label = "documents in state=" + state
	}
	fmt.Fprintf(w, "reindex enqueued for %s: %d jobs\n", label, resp.JobsEnqueued)
	return nil
}
