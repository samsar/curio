package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
)

// failureCauses names the causes the daemon records for a failed or dead
// document, in its order, for --cause's help.
const failureCauses = "dead_link, anti_bot, login_wall, jina_refused, tls, unreachable, timeout, network, " +
	"rate_limited, http_error, unsupported, too_large, index, other"

func newRefetchCmd(env *daemonctl.Env) *cobra.Command {
	var (
		all   bool
		opts  client.RefetchAllOpts
		force bool
	)
	cmd := &cobra.Command{
		Use:   "refetch [document-id | url]",
		Short: "Re-fetch a document (or many) to pick up content changes / fetcher fixes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !all && (opts.State != "" || opts.Cause != "") {
				return errors.New("--state and --cause filter --all")
			}
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}

			if all {
				return refetchAll(cmd.Context(), cmd.OutOrStdout(), env.Client, opts)
			}
			if len(args) != 1 {
				return errors.New("provide a document ID or URL, or pass --all")
			}
			id, err := resolveDocumentID(cmd.Context(), env.Client, args[0])
			if err != nil {
				return err
			}
			return refetchOne(cmd.Context(), cmd.OutOrStdout(), env.Client, id, force)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false,
		"Refetch every document (use --state to filter; dead documents are skipped unless --state=dead)")
	cmd.Flags().StringVar(&opts.State, "state", "",
		"With --all, only refetch documents in this state (pending|fetched|failed|dead)")
	cmd.Flags().StringVar(&opts.Cause, "cause", "",
		"With --all, only refetch documents that failed for this cause: "+failureCauses+
			" (dead_link needs --state dead)")
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
	fmt.Fprintf(w, "  follow it: curio jobs show %s\n", resp.JobID)
	return nil
}

func refetchAll(ctx context.Context, w io.Writer, c *client.Client, opts client.RefetchAllOpts) error {
	resp, err := c.RefetchAll(ctx, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "refetch enqueued for %s: %d jobs\n", refetchedDocuments(opts), resp.JobsEnqueued)
	return nil
}

// refetchedDocuments names the documents a refetch-all with opts requeues.
func refetchedDocuments(opts client.RefetchAllOpts) string {
	var filters []string
	if opts.State != "" {
		filters = append(filters, "in state="+opts.State)
	}
	if opts.Cause != "" {
		filters = append(filters, "with cause="+opts.Cause)
	}
	if len(filters) == 0 {
		return "all documents"
	}
	return "documents " + strings.Join(filters, " ")
}
