package search

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
)

func TestConfigFrom_Default(t *testing.T) {
	assert.Equal(t, Config{
		BM25Weight:   1,
		VectorWeight: 1,
		RRFK:         60,
		Collapse:     CollapseMax,
		QueryPrefix:  config.QwenQueryPrefix,
		DefaultK:     10,
		EmbedTimeout: 10 * time.Second,
	}, ConfigFrom(config.Default()), "PreFanout and Log left to New's defaults")
}

// otherSearchStrings are valid values, other than the defaults, for the
// string keys of config.Search.
var otherSearchStrings = map[string]string{"Collapse": "sum"}

// TestConfigFrom_MapsEveryKey: a change to any key of config.yaml's search
// section, or to embedding.query_prefix, changes the engine's Config, so a
// search key added later fails here until ConfigFrom maps it.
func TestConfigFrom_MapsEveryKey(t *testing.T) {
	base := ConfigFrom(config.Default())
	typ := reflect.TypeFor[config.Search]()
	for i := range typ.NumField() {
		field := typ.Field(i)
		t.Run("search."+field.Tag.Get("yaml"), func(t *testing.T) {
			cfg := config.Default()
			v := reflect.ValueOf(&cfg.Search).Elem().Field(i)
			switch was := v.Interface().(type) {
			case int:
				v.SetInt(int64(was + 1))
			case float64:
				v.SetFloat(was / 2)
			case string:
				other, ok := otherSearchStrings[field.Name]
				require.True(t, ok, "add a valid non-default value for %s to otherSearchStrings", field.Name)
				v.SetString(other)
			default:
				t.Fatalf("no non-default value for a %T field; add one", was)
			}
			require.NoError(t, cfg.Validate(), "the changed value is valid")
			assert.NotEqual(t, base, ConfigFrom(cfg), "ConfigFrom doesn't map search.%s", field.Tag.Get("yaml"))
		})
	}
	t.Run("embedding.query_prefix", func(t *testing.T) {
		cfg := config.Default()
		cfg.Embedding.QueryPrefix = "search_query: "
		assert.Equal(t, "search_query: ", ConfigFrom(cfg).QueryPrefix)
	})
}
