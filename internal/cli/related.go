package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
)

func newRelatedCmd(env *daemonctl.Env) *cobra.Command {
	var k int
	cmd := &cobra.Command{
		Use:   "related <document-id>",
		Short: "Find documents related to one by embedding similarity",
		Long: "Find documents related to the given one, ranked by vector similarity\n" +
			"over its indexed content (no query text involved). Returns nothing for\n" +
			"documents that haven't been fetched and indexed yet.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}

			res, err := env.Client.RelatedDocuments(cmd.Context(), args[0], k)
			if err != nil {
				return err
			}
			renderRelatedResults(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().IntVarP(&k, "k", "k", 10, "Number of results to return")
	return cmd
}

func renderRelatedResults(w io.Writer, res *client.RelatedResponse) {
	if len(res.Items) == 0 {
		fmt.Fprintf(w, "no related documents for %s (is it fetched and indexed?)\n", res.DocID)
		return
	}
	fmt.Fprintf(w, "%d documents related to %s\n\n", len(res.Items), res.DocID)
	for i, hit := range res.Items {
		title := hit.Document.URL
		if hit.Document.Title != nil && *hit.Document.Title != "" {
			title = *hit.Document.Title
		}
		fmt.Fprintf(w, "%2d. %s\n", i+1, title)
		fmt.Fprintf(w, "    %s   (similarity %.4f)\n", hit.Document.URL, hit.Score)
		fmt.Fprintf(w, "    doc_id: %s\n", hit.Document.ID)
		if hit.MarkdownPath != "" {
			fmt.Fprintf(w, "    path:   %s\n", hit.MarkdownPath)
		}
		fmt.Fprintln(w)
	}
}
