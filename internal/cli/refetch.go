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

func newRefetchCmd(env *daemonctl.Env) *cobra.Command {
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
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}

			if all {
				return refetchAll(cmd.Context(), cmd.OutOrStdout(), env.Client, state)
			}
			if len(args) != 1 {
				return errors.New("provide a document ID or pass --all")
			}
			return refetchOne(cmd.Context(), cmd.OutOrStdout(), env.Client, args[0], force)
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

func refetchOne(ctx context.Context, w io.Writer, c *client.Client, docID string, force bool) error {
	resp, err := c.RefetchDocument(ctx, docID, force)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "refetch enqueued for document %s (job %s)\n", docID, resp.JobID)
	return nil
}

func refetchAll(ctx context.Context, w io.Writer, c *client.Client, state string) error {
	resp, err := c.RefetchAll(ctx, state)
	if err != nil {
		return err
	}
	label := "all documents"
	if state != "" {
		label = "documents in state=" + state
	}
	fmt.Fprintf(w, "refetch enqueued for %s: %d jobs\n", label, resp.JobsEnqueued)
	return nil
}
