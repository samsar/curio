package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func newReindexCmd() *cobra.Command {
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

Documents must already have content; --all targets state=fetched by default.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}

			if all {
				return reindexAll(cmd.Context(), ctx, state)
			}
			if len(args) != 1 {
				return errors.New("provide a document ID or pass --all")
			}
			return reindexOne(cmd.Context(), ctx, args[0])
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Reindex every document with content (default state=fetched; use --state to override)")
	cmd.Flags().StringVar(&state, "state", "",
		"With --all, only reindex documents in this state (defaults to fetched)")
	return cmd
}

func reindexOne(httpCtx context.Context, c *Context, docID string) error {
	resp, err := c.Client.ReindexDocument(httpCtx, docID)
	if err != nil {
		return err
	}
	fmt.Printf("reindex enqueued for document %s (job %s)\n", docID, resp.JobID)
	return nil
}

func reindexAll(httpCtx context.Context, c *Context, state string) error {
	resp, err := c.Client.ReindexAll(httpCtx, state)
	if err != nil {
		return err
	}
	label := "documents in state=fetched"
	if state != "" {
		label = "documents in state=" + state
	}
	fmt.Printf("reindex enqueued for %s: %d jobs\n", label, resp.JobsEnqueued)
	return nil
}
