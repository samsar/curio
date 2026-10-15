package search_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/understand"
)

// otherSearchStrings are valid values, other than the defaults, for the
// string keys of config.Search.
var otherSearchStrings = map[string]string{"Collapse": "sum"}

// TestConfigFrom_MapsEveryKey: a change to any key of config.yaml's search
// section, or to embedding.query_prefix, changes the engine's Config, so a
// search key added later fails here until ConfigFrom maps it. The keys the
// planner owns (understand.SearchFields) are left to its own test, which
// holds understand.OptionsFrom to them; an external test package, since
// understand imports search.
func TestConfigFrom_MapsEveryKey(t *testing.T) {
	base := search.ConfigFrom(config.Default())
	typ := reflect.TypeFor[config.Search]()
	for i := range typ.NumField() {
		field := typ.Field(i)
		if slices.Contains(understand.SearchFields(), field.Name) {
			continue
		}
		t.Run("search."+field.Tag.Get("yaml"), func(t *testing.T) {
			cfg := config.Default()
			v := reflect.ValueOf(&cfg.Search).Elem().Field(i)
			switch was := v.Interface().(type) {
			case bool:
				v.SetBool(!was)
			case int:
				v.SetInt(int64(was + 1))
			case float64:
				// A list on by default is halved; one off is turned on.
				if was == 0 {
					v.SetFloat(0.5)
				} else {
					v.SetFloat(was / 2)
				}
			case string:
				other, ok := otherSearchStrings[field.Name]
				require.True(t, ok, "add a valid non-default value for %s to otherSearchStrings", field.Name)
				v.SetString(other)
			default:
				t.Fatalf("no non-default value for a %T field; add one", was)
			}
			require.NoError(t, cfg.Validate(), "the changed value is valid")
			assert.NotEqual(t, base, search.ConfigFrom(cfg), "ConfigFrom doesn't map search.%s", field.Tag.Get("yaml"))
		})
	}
	t.Run("embedding.query_prefix", func(t *testing.T) {
		cfg := config.Default()
		cfg.Embedding.QueryPrefix = "search_query: "
		assert.Equal(t, "search_query: ", search.ConfigFrom(cfg).QueryPrefix)
	})
}
