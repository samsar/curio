package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samansartipi/curio/internal/client"
)

func newSearchCmd() *cobra.Command {
	var k int
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

			res, err := ctx.Client.Search(cmd.Context(), client.SearchRequest{
				Query: query, K: k,
			})
			if err != nil {
				return err
			}
			renderSearchResults(res)
			return nil
		},
	}
	cmd.Flags().IntVarP(&k, "k", "k", 10, "Number of results to return")
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
		if len(hit.Matches) > 0 {
			m := hit.Matches[0]
			snippet := m.Snippet
			if snippet == "" {
				snippet = truncate(m.Text, 180)
			}
			fmt.Printf("    %s\n", snippet)
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
