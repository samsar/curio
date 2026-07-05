package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func newRefetchCmd() *cobra.Command {
	var (
		all   bool
		state string
		force bool
	)
	cmd := &cobra.Command{
		Use:   "refetch [document-id]",
		Short: "Re-fetch a document (or many) to pick up content changes / fetcher fixes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}

			if all {
				return refetchAll(cmd.Context(), ctx, state)
			}
			if len(args) != 1 {
				return errors.New("provide a document ID or pass --all")
			}
			return refetchOne(cmd.Context(), ctx, args[0], force)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false,
		"Refetch every document (use --state to filter; dead documents are skipped unless --state=dead)")
	cmd.Flags().StringVar(&state, "state", "",
		"With --all, only refetch documents in this state (pending|fetched|failed|dead)")
	cmd.Flags().BoolVar(&force, "force", false,
		"Refetch even if the document is dead (confirmed dead link)")
	return cmd
}

func refetchOne(httpCtx context.Context, c *Context, docID string, force bool) error {
	resp, err := c.Client.RefetchDocument(httpCtx, docID, force)
	if err != nil {
		return err
	}
	fmt.Printf("refetch enqueued for document %s (job %s)\n", docID, resp.JobID)
	return nil
}

func refetchAll(httpCtx context.Context, c *Context, state string) error {
	resp, err := c.Client.RefetchAll(httpCtx, state)
	if err != nil {
		return err
	}
	label := "all documents"
	if state != "" {
		label = "documents in state=" + state
	}
	fmt.Printf("refetch enqueued for %s: %d jobs\n", label, resp.JobsEnqueued)
	return nil
}
