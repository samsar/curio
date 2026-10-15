package understand

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
)

// TestOptionsFrom_Default: by default the planner writes with
// generation.model, which the labels share, on this machine.
func TestOptionsFrom_Default(t *testing.T) {
	assert.Equal(t, Options{On: true, Timeout: 5 * time.Second, KeepAlive: 30 * time.Minute,
		Model: config.ModelSpec{Provider: "ollama", Model: "qwen3:4b-instruct", BaseURL: "http://localhost:11434",
			TimeoutSeconds: 120}, Shared: true}, OptionsFrom(config.Default()))
}

// TestOptionsFrom_Budget: a plan, a second asking whether the model is
// loaded and the query's embed stay under 25 s: past it the plan gets
// what the embed timeout leaves, and with less than a second left no
// question is planned, the reason naming both keys and the way out.
func TestOptionsFrom_Budget(t *testing.T) {
	for _, tc := range []struct {
		name              string
		understand, embed int
		timeout           time.Duration
		shortened         bool
		withheld          string
	}{
		{"the defaults", 5, 10, 5 * time.Second, false, ""},
		{"just under", 13, 10, 13 * time.Second, false, ""},
		{"at the budget", 14, 10, 13 * time.Second, true, ""},
		{"a 2.6 embed timeout", 5, 20, 3 * time.Second, true, ""},
		{"a second left", 5, 22, time.Second, true, ""},
		{"no second left", 5, 23, 5 * time.Second, false, "search.embed_timeout_seconds (23) leaves no time for a " +
			"plan: with search.understand_timeout_seconds and 1 s it must stay under 25 s, so curio search answers " +
			"within 30 s; lower search.embed_timeout_seconds, or set search.understand: false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Search.UnderstandTimeoutSeconds, cfg.Search.EmbedTimeoutSeconds = tc.understand, tc.embed
			require.NoError(t, cfg.Validate(), "config.yaml takes it")
			o := OptionsFrom(cfg)
			assert.Equal(t, tc.timeout, o.Timeout)
			assert.Equal(t, tc.shortened, o.Shortened != "", o.Shortened)
			assert.Equal(t, tc.withheld, o.Withheld)
		})
	}
	cfg := config.Default()
	cfg.Search.EmbedTimeoutSeconds = 20
	assert.Equal(t, "search.understand_timeout_seconds (5) plus search.embed_timeout_seconds (20) plus 1 s reaches "+
		"25 s, past which curio search could give up before its results: plans get 3 s", OptionsFrom(cfg).Shortened)

	var logs bytes.Buffer
	New(context.Background(), &fakeModel{}, Options{On: true, Timeout: 3 * time.Second,
		Shortened: OptionsFrom(cfg).Shortened, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"))
	assert.Contains(t, logs.String(), "plans get 3 s")
}

// TestOptionsFrom_Withheld: questions go off this machine only to a
// server the planner role names itself; generation's, which the role
// inherits, isn't enough.
func TestOptionsFrom_Withheld(t *testing.T) {
	cfg := config.Default()
	cfg.Generation.BaseURL = "http://gpu-box.lan:11434"
	require.NoError(t, cfg.Validate())
	assert.Equal(t, "questions would leave this machine for http://gpu-box.lan:11434, generation.base_url, which "+
		"the planner role doesn't name; set generation.roles.planner.base_url to send them there, or "+
		"search.understand: false", OptionsFrom(cfg).Withheld)

	cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RolePlanner: {BaseURL: "http://gpu-box.lan:11434"}}
	require.NoError(t, cfg.Validate())
	assert.Empty(t, OptionsFrom(cfg).Withheld, "the role names it")

	cfg.Generation.BaseURL = "http://127.0.0.1:11434"
	cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleLabels: {BaseURL: "http://gpu-box.lan:11434"}}
	require.NoError(t, cfg.Validate())
	assert.Empty(t, OptionsFrom(cfg).Withheld, "only the labels leave")

	g := config.Default().Generation
	g.Provider = "acme"
	assert.Contains(t, withheld(g), "questions would go to the provider acme, generation.provider")
	g.Roles = map[config.Role]config.ModelSpec{config.RolePlanner: {Provider: "acme"}}
	assert.Empty(t, withheld(g))
}

// TestOptionsFrom_MapsItsKeys: a change to any key of config.yaml's search
// section the planner owns (SearchFields) changes its Options, and so does
// the planner role's model; search.ConfigFrom's test holds the engine to
// the other keys, so a key added later that neither maps fails one of the
// two.
func TestOptionsFrom_MapsItsKeys(t *testing.T) {
	base := OptionsFrom(config.Default())
	typ := reflect.TypeFor[config.Search]()
	for _, name := range SearchFields() {
		field, ok := typ.FieldByName(name)
		require.True(t, ok, "config.Search has no field %s", name)
		t.Run("search."+field.Tag.Get("yaml"), func(t *testing.T) {
			cfg := config.Default()
			v := reflect.ValueOf(&cfg.Search).Elem().FieldByIndex(field.Index)
			switch was := v.Interface().(type) {
			case bool:
				v.SetBool(!was)
			case int:
				v.SetInt(int64(was + 1))
			default:
				t.Fatalf("no non-default value for a %T field; add one", was)
			}
			require.NoError(t, cfg.Validate(), "the changed value is valid")
			assert.NotEqual(t, base, OptionsFrom(cfg), "OptionsFrom doesn't map search.%s", field.Tag.Get("yaml"))
		})
	}
	assert.Len(t, slices.Compact(slices.Sorted(slices.Values(SearchFields()))), len(SearchFields()), "listed once")

	t.Run("generation.roles.planner", func(t *testing.T) {
		cfg := config.Default()
		cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RolePlanner: {Model: "gemma4:26b"}}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, "gemma4:26b", OptionsFrom(cfg).Model.Model)
		cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleLabels: {Model: "gemma4:26b"}}
		assert.Equal(t, "qwen3:4b-instruct", OptionsFrom(cfg).Model.Model, "the labels' model isn't the planner's")
	})
}
