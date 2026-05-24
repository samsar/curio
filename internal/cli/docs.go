package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
)

func newDocsCmd() *cobra.Command {
	var (
		failedOnly bool
		state      string
		limit      int
	)
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "List indexed documents (filter with --failed, --state, --limit)",
		Long: `Show recent documents in the corpus. Each row carries the most recent
error from a failed job that targeted it, so 'curio docs --failed' is
the one-stop debug view for stuck content.

Cross-reference: 'curio jobs --failed' shows the underlying job rows
with full error messages and attempt counts.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}
			s := state
			if failedOnly {
				s = "failed"
			}

			resp, err := ctx.Client.ListDocuments(cmd.Context(), client.ListDocumentsOpts{
				State: s, Limit: limit,
			})
			if err != nil {
				return err
			}
			renderDocList(resp)
			return nil
		},
	}
	cmd.Flags().BoolVar(&failedOnly, "failed", false, "Shortcut for --state=failed")
	cmd.Flags().StringVar(&state, "state", "", "pending|fetched|failed|dead")
	cmd.Flags().IntVar(&limit, "limit", 50, "Max rows (server caps at 500)")
	return cmd
}

func renderDocList(resp *client.DocumentList) {
	if len(resp.Items) == 0 {
		fmt.Println("no documents match")
		return
	}
	// Two columns: STATE + URL on one line; LAST_ERR indented below.
	for _, d := range resp.Items {
		title := d.URL
		if d.Title != nil && *d.Title != "" {
			title = *d.Title
		}
		fmt.Printf("%-8s %s\n", d.State, d.URL)
		if title != d.URL {
			fmt.Printf("         (%s)\n", truncate(title, 100))
		}
		if d.LastError != "" {
			fmt.Printf("         err: %s\n", truncate(strings.TrimSpace(d.LastError), 200))
		}
		fmt.Printf("         id: %s\n\n", d.ID)
	}
	fmt.Printf("%d document(s)\n", len(resp.Items))
}
