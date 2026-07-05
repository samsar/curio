package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
)

func newSearchCmd() *cobra.Command {
	var (
		k           int
		contentType []string
		source      []string
		host        []string
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Hybrid BM25 + vector search across indexed bookmarks",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}
			query := strings.Join(args, " ")

			var filters *client.SearchFilters
			if len(contentType) > 0 || len(source) > 0 || len(host) > 0 {
				filters = &client.SearchFilters{
					ContentType: contentType,
					Host:        host,
					Source:      source,
				}
			}

			res, err := ctx.Client.Search(cmd.Context(), client.SearchRequest{
				Query: query, K: k, Filters: filters,
			})
			if err != nil {
				return err
			}
			renderSearchResults(res)
			return nil
		},
	}
	cmd.Flags().IntVarP(&k, "k", "k", 10, "Number of results to return")
	cmd.Flags().StringSliceVar(&contentType, "type", nil, "Filter by content type (article|repo|video|pdf|thread|unknown); repeatable")
	cmd.Flags().StringSliceVar(&source, "source", nil, "Filter by bookmark source (chrome|safari|firefox|html|manual); repeatable")
	cmd.Flags().StringSliceVar(&host, "host", nil, "Filter by URL host, e.g. github.com; repeatable")
	return cmd
}

func renderSearchResults(res *client.SearchResponse) {
	if len(res.Items) == 0 {
		fmt.Printf("no results for %q\n", res.Query)
		return
	}
	fmt.Printf("%d results for %q  (BM25: %d, vector: %d)\n\n",
		len(res.Items), res.Query, res.BM25Hits, res.VectorHits)
	for i, hit := range res.Items {
		title := hit.Document.URL
		if hit.Document.Title != nil && *hit.Document.Title != "" {
			title = *hit.Document.Title
		}
		fmt.Printf("%2d. %s\n", i+1, title)
		fmt.Printf("    %s   (score %.4f)\n", hit.Document.URL, hit.Score)
		// Doc ID and on-disk path for direct follow-up: open the file,
		// run `curio docs show <id>`, or `curio refetch <id>` without
		// going hunting.
		fmt.Printf("    doc_id: %s\n", hit.Document.ID)
		if hit.MarkdownPath != "" {
			fmt.Printf("    path:   %s\n", hit.MarkdownPath)
		}
		if len(hit.Matches) > 0 {
			m := hit.Matches[0]
			snippet := m.Snippet
			if snippet == "" {
				snippet = m.Text
			}
			// Wrap on word boundaries so long matches aren't cut off
			// at 180 chars. 100 cols keeps it readable in narrow
			// terminals; the `--content` flag on `curio docs show`
			// is the right tool for the full body.
			for _, line := range wrapLines(snippet, 100) {
				fmt.Printf("    %s\n", line)
			}
		}
		fmt.Println()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
