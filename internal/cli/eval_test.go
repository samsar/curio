package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/eval"
)

// searchFunc adapts a function to searcher.
type searchFunc func(ctx context.Context, req client.SearchRequest) (*client.SearchResponse, error)

func (f searchFunc) Search(ctx context.Context, req client.SearchRequest) (*client.SearchResponse, error) {
	return f(ctx, req)
}

func hits(urls ...string) []client.SearchHit {
	out := make([]client.SearchHit, len(urls))
	for i, u := range urls {
		out[i] = client.SearchHit{Document: client.Document{URL: u}}
	}
	return out
}

func TestRankQueries(t *testing.T) {
	qs := &eval.QuerySet{Queries: []eval.Query{{Query: "first"}, {Query: "second"}}}

	t.Run("collects each ranking at k", func(t *testing.T) {
		var gotK []int
		s := searchFunc(func(_ context.Context, req client.SearchRequest) (*client.SearchResponse, error) {
			gotK = append(gotK, req.K)
			return &client.SearchResponse{Items: hits("https://example.com/"+req.Query, "https://example.com/x")}, nil
		})
		ranked, err := rankQueries(context.Background(), s, qs, 5)
		require.NoError(t, err)
		assert.Equal(t, [][]string{
			{"https://example.com/first", "https://example.com/x"},
			{"https://example.com/second", "https://example.com/x"},
		}, ranked)
		assert.Equal(t, []int{5, 5}, gotK)
	})

	t.Run("refuses a degraded search", func(t *testing.T) {
		s := searchFunc(func(_ context.Context, req client.SearchRequest) (*client.SearchResponse, error) {
			res := &client.SearchResponse{Items: hits("https://example.com/a")}
			if req.Query == "second" {
				res.Degraded = true
				res.Warnings = []string{"semantic search unavailable (connection refused); keyword-only results"}
			}
			return res, nil
		})
		_, err := rankQueries(context.Background(), s, qs, 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"second"`)
		assert.Contains(t, err.Error(), "connection refused")
	})
}
