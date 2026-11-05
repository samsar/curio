package summarize

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/config"
)

// TestOptionsFrom_KeepAliveWithTheReranker: with the planner off and the
// reranker on the summaries' model, the summaries send search's keep-alive,
// so a page summarized between two reranked searches leaves the model
// warm for the next; on another model, they send none.
func TestOptionsFrom_KeepAliveWithTheReranker(t *testing.T) {
	cfg := config.Default()
	cfg.Search.Understand = false
	cfg.Search.Rerank = new(true)
	cfg.Search.UnderstandKeepAliveMinutes = 45
	cfg.Summaries.Enabled = new(true)
	assert.Equal(t, 45*time.Minute, OptionsFrom(cfg).KeepAlive, "the reranker's, on its model")

	cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleReranker: {Model: "gemma4:26b"}}
	assert.Zero(t, OptionsFrom(cfg).KeepAlive, "the reranker on a model of its own")

	cfg.Generation.Roles = nil
	cfg.Search.Rerank = new(false)
	assert.Zero(t, OptionsFrom(cfg).KeepAlive, "neither of search's roles in use")
}
