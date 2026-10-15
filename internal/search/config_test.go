package search

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/config"
)

func TestConfigFrom_Default(t *testing.T) {
	assert.Equal(t, Config{
		BM25Weight:    1,
		VectorWeight:  1,
		FieldsWeight:  0.25,
		CardsWeight:   0.5,
		TitleWeight:   2,
		RewriteWeight: 0.5,
		NameWeight:    1,
		RRFK:          60,
		Collapse:      CollapseMax,
		QueryPrefix:   config.QwenQueryPrefix,
		DefaultK:      10,
		EmbedTimeout:  10 * time.Second,
	}, ConfigFrom(config.Default()), "PreFanout and Log left to New's defaults")
}
