package search

import (
	"time"

	"github.com/samsar/curio/internal/config"
)

// ConfigFrom is the engine's Config as config.yaml's search section and
// embedding.query_prefix set it: the one mapping the daemon and the
// developer's tools share, so a tool measures the search the daemon
// serves. PreFanout and Log stay zero, which New fills with its defaults.
// The section's understanding keys are the planner's
// (understand.OptionsFrom), not the engine's.
//
// search reads config, never the reverse: config is read by every binary,
// and must not link the engines into them.
func ConfigFrom(c config.Config) Config {
	return Config{
		BM25Weight:    c.Search.BM25Weight,
		VectorWeight:  c.Search.VectorWeight,
		FieldsWeight:  c.Search.FieldsWeight,
		CardsWeight:   c.Search.CardsWeight,
		TitleWeight:   c.Search.TitleWeight,
		RewriteWeight: c.Search.RewriteWeight,
		NameWeight:    c.Search.NameWeight,
		RRFK:          c.Search.RRFK,
		Collapse:      CollapseStrategy(c.Search.Collapse),
		QueryPrefix:   c.Embedding.QueryPrefix,
		DefaultK:      c.Search.DefaultK,
		EmbedTimeout:  time.Duration(c.Search.EmbedTimeoutSeconds) * time.Second,
	}
}
