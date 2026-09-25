package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/eval"
)

func newEvalCmd() *cobra.Command {
	var (
		queriesPath string
		k           int
	)
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Score search quality against a labeled query set (NDCG@k, recall@k)",
		Long: "Run a set of queries with known-relevant documents through search and\n" +
			"report ranking metrics (recall@k, precision@k, NDCG@k, MRR).\n\n" +
			"The query set is a YAML file of {query, relevant: [urls]} entries; see\n" +
			"docs/eval.example.yaml. This is the measurement rig for tuning search\n" +
			"and, later, comparing RAG approaches (M6) on the same ground truth.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if queriesPath == "" {
				return errors.New("--queries is required (path to a qrels YAML file)")
			}
			if k < 1 {
				return fmt.Errorf("--k must be at least 1, got %d", k)
			}
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}

			qs, err := eval.LoadQuerySet(queriesPath)
			if err != nil {
				return err
			}

			ranked, err := rankQueries(cmd.Context(), ctx.Client, qs, k)
			if err != nil {
				return err
			}
			return renderEvalReport(cmd.OutOrStdout(), eval.Evaluate(qs, ranked, k))
		},
	}
	cmd.Flags().StringVar(&queriesPath, "queries", "", "Path to a qrels YAML file (query + relevant URLs)")
	cmd.Flags().IntVarP(&k, "k", "k", 10, "Rank cutoff for @k metrics, 1-100")
	return cmd
}

// searcher is the slice of the daemon client that rankQueries needs.
type searcher interface {
	Search(ctx context.Context, req client.SearchRequest) (*client.SearchResponse, error)
}

// rankQueries runs every query through search and returns the URLs each one
// ranked. A degraded (keyword-only) response is an error: scoring it would
// report BM25 alone as if it were the hybrid pipeline being measured.
func rankQueries(ctx context.Context, s searcher, qs *eval.QuerySet, k int) ([][]string, error) {
	ranked := make([][]string, len(qs.Queries))
	for i, q := range qs.Queries {
		res, err := s.Search(ctx, client.SearchRequest{Query: q.Query, K: k})
		if err != nil {
			return nil, fmt.Errorf("search %q: %w", q.Query, err)
		}
		if res.Degraded {
			return nil, fmt.Errorf("search %q returned keyword-only results (%s); not scoring a degraded search",
				q.Query, strings.Join(res.Warnings, "; "))
		}
		urls := make([]string, 0, len(res.Items))
		for _, hit := range res.Items {
			urls = append(urls, hit.Document.URL)
		}
		ranked[i] = urls
	}
	return ranked, nil
}

func renderEvalReport(w io.Writer, r eval.Report) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "query\trel\tR@%d\tP@%d\tNDCG@%d\tRR\n", r.K, r.K, r.K)
	for _, q := range r.Results {
		fmt.Fprintf(tw, "%s\t%d\t%.3f\t%.3f\t%.3f\t%.3f\n",
			truncate(q.Query, 40), q.NumRelevant, q.RecallAtK, q.PrecisionAtK, q.NDCGAtK, q.RR)
	}
	fmt.Fprintf(tw, "MEAN\t\t%.3f\t%.3f\t%.3f\t%.3f\n",
		r.MeanRecall, r.MeanPrecision, r.MeanNDCG, r.MRR)
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	fmt.Fprintf(w, "\n%d queries · NDCG@%d %.3f · recall@%d %.3f · MRR %.3f\n",
		len(r.Results), r.K, r.MeanNDCG, r.K, r.MeanRecall, r.MRR)
	return nil
}
