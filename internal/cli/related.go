package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
)

func newRelatedCmd() *cobra.Command {
	var k int
	cmd := &cobra.Command{
		Use:   "related <document-id>",
		Short: "Find documents related to one by embedding similarity",
		Long: "Find documents related to the given one, ranked by vector similarity\n" +
			"over its indexed content (no query text involved). Returns nothing for\n" +
			"documents that haven't been fetched and indexed yet.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}

			res, err := ctx.Client.RelatedDocuments(cmd.Context(), args[0], k)
			if err != nil {
				return err
			}
			renderRelatedResults(res)
			return nil
		},
	}
	cmd.Flags().IntVarP(&k, "k", "k", 10, "Number of results to return")
	return cmd
}

func renderRelatedResults(res *client.RelatedResponse) {
	if len(res.Items) == 0 {
		fmt.Printf("no related documents for %s (is it fetched and indexed?)\n", res.DocID)
		return
	}
	fmt.Printf("%d documents related to %s\n\n", len(res.Items), res.DocID)
	for i, hit := range res.Items {
		title := hit.Document.URL
		if hit.Document.Title != nil && *hit.Document.Title != "" {
			title = *hit.Document.Title
		}
		fmt.Printf("%2d. %s\n", i+1, title)
		fmt.Printf("    %s   (similarity %.4f)\n", hit.Document.URL, hit.Score)
		fmt.Printf("    doc_id: %s\n", hit.Document.ID)
		if hit.MarkdownPath != "" {
			fmt.Printf("    path:   %s\n", hit.MarkdownPath)
		}
		fmt.Println()
	}
}
