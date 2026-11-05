package rerank

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
)

// TestOptionsFrom_Default: with config.yaml's defaults the reranker is off
// by default, its model unmeasured, the reason saying how to turn it on;
// its timeout, depth and keep-alive are the keys'.
func TestOptionsFrom_Default(t *testing.T) {
	o := OptionsFrom(config.Default())
	assert.False(t, o.On)
	assert.Equal(t, "search reranking is off by default for qwen3:4b-instruct, which isn't measured as the "+
		"reranker; search.rerank: true turns it on (then restart)", o.OffReason)
	assert.Equal(t, 7*time.Second, o.Timeout)
	assert.Empty(t, o.Shortened)
	assert.Equal(t, 30*time.Minute, o.KeepAlive, "search's keep-alive")
	assert.Equal(t, 30, o.Settings.Depth)
	assert.True(t, o.Shared, "on generation.model, with the labels and the planner")
	assert.True(t, o.ShortSharer, "the labels send no keep-alive")
	assert.Empty(t, o.Withheld)
	require.NoError(t, o.Settings.Validate())

	c := config.Default()
	c.Search.Rerank = new(false)
	assert.Equal(t, OffReason, OptionsFrom(c).OffReason)
	c.Search.Rerank = new(true)
	o = OptionsFrom(c)
	assert.True(t, o.On)
	assert.Empty(t, o.OffReason)
	c.Insight.Labeling = "terms"
	o = OptionsFrom(c)
	assert.True(t, o.Shared, "with the planner")
	assert.False(t, o.ShortSharer, "the planner sends the same keep-alive")
}

// TestOptionsFrom_Budget: the plan's timeout and loaded check, the embed,
// the rerank and the reranker's loaded check stay under 25 s, so curio
// search answers within 30 s: past it the rerank gets what is left, at
// least a second, or nothing.
func TestOptionsFrom_Budget(t *testing.T) {
	for _, tc := range []struct {
		name                string
		understand          bool
		plan, embed, rerank int
		timeout             time.Duration
		shortened, withheld bool
	}{
		{"the defaults: 5 + 1 + 10 + 7 + 1 = 24", true, 5, 10, 7, 7 * time.Second, false, false},
		{"at the budget", true, 5, 10, 8, 7 * time.Second, true, false},
		{"a long rerank", true, 5, 10, 23, 7 * time.Second, true, false},
		// Without understanding no shipped route reranks (Held), but a
		// route that reranked a search without a plan would get the plan's
		// time too.
		{"no understanding: no plan", false, 23, 10, 13, 13 * time.Second, false, false},
		{"no understanding, at the budget", false, 23, 10, 14, 13 * time.Second, true, false},
		{"a second left", true, 5, 16, 7, time.Second, true, false},
		{"none left", true, 5, 17, 7, 7 * time.Second, false, true},
		{"the planner's own shortening counts", true, 23, 10, 7, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Default()
			c.Search.Rerank = new(true)
			c.Search.Understand = tc.understand
			c.Search.UnderstandTimeoutSeconds, c.Search.EmbedTimeoutSeconds = tc.plan, tc.embed
			c.Search.RerankTimeoutSeconds = tc.rerank
			require.NoError(t, c.Validate())
			o := OptionsFrom(c)
			if !tc.withheld {
				assert.Equal(t, tc.timeout, o.Timeout)
			}
			assert.Equal(t, tc.shortened, o.Shortened != "", o.Shortened)
			assert.Equal(t, tc.withheld, o.Withheld != "", o.Withheld)
			assert.Equal(t, !tc.understand, o.Unplanned != "", o.Unplanned)
			assert.Equal(t, tc.withheld || !tc.understand, o.Held() != "", o.Held())
		})
	}
}

// TestOptionsFrom_Unplanned: every shipped route needs a plan, so with
// search understanding off, or withheld from its model, no search is
// reranked and the reranker says why; a route for searches without a plan
// lifts it, and a privacy withhold is said first.
func TestOptionsFrom_Unplanned(t *testing.T) {
	c := config.Default()
	c.Search.Rerank = new(true)
	assert.Empty(t, OptionsFrom(c).Held(), "understanding on")

	c.Search.Understand = false
	o := OptionsFrom(c)
	assert.True(t, o.On)
	assert.Empty(t, o.Withheld)
	assert.Equal(t, "curio reranks only understood searches, and search.understand is off; set "+
		"search.understand: true, or search.rerank: false", o.Held())
	o.Settings.Routes = map[Group]Method{GroupTopical: MethodListwise, GroupNone: MethodListwise}
	assert.Empty(t, o.Held(), "a route for searches without a plan")
	o.Settings = Shipped()
	o.Settings.Force = MethodPointwise
	assert.Empty(t, o.Held(), "every search reranked by one method")

	c = config.Default()
	c.Search.Rerank = new(true)
	c.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleReranker: {BaseURL: "http://gpu.example:11434"}}
	c.Generation.BaseURL = "http://gpu.example:11434"
	o = OptionsFrom(c)
	assert.Empty(t, o.Withheld, "the reranker names its server")
	assert.Contains(t, o.Held(), "curio reranks only understood searches, and no question is understood: "+
		"questions would leave this machine for http://gpu.example:11434")

	c = config.Default()
	c.Search.Rerank = new(true)
	c.Search.Understand = false
	c.Generation.BaseURL = "http://gpu.example:11434"
	assert.Contains(t, OptionsFrom(c).Held(), "generation.roles.reranker.base_url", "the privacy withhold first")
}

// TestOptionsFrom_Withheld: questions and pages go off this machine, or to
// another provider, only when the reranker role names where.
func TestOptionsFrom_Withheld(t *testing.T) {
	c := config.Default()
	c.Search.Rerank = new(true)
	c.Generation.BaseURL = "http://gpu.example:11434"
	assert.Contains(t, OptionsFrom(c).Withheld, "generation.roles.reranker.base_url")
	c.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleReranker: {BaseURL: "http://gpu.example:11434"}}
	assert.Empty(t, OptionsFrom(c).Withheld, "named by the role")
	c.Generation.Roles = map[config.Role]config.ModelSpec{config.RolePlanner: {BaseURL: "http://gpu.example:11434"}}
	assert.NotEmpty(t, OptionsFrom(c).Withheld, "the planner's naming isn't the reranker's")

	c = config.Default()
	c.Search.Rerank = new(true)
	c.Generation.Provider = "elsewhere"
	assert.Contains(t, OptionsFrom(c).Withheld, "generation.roles.reranker.provider")
}

// TestOptionsFrom_MapsItsKeys: a change to any key of config.yaml's search
// section the reranker owns (SearchFields) changes its Options, and so
// does the reranker role's model; search.ConfigFrom's and
// understand.OptionsFrom's tests hold the other keys.
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
			case *bool:
				v.Set(reflect.ValueOf(new(was == nil || !*was)))
			case int:
				// One up, or one down for a default at its key's maximum.
				if v.SetInt(int64(was + 1)); cfg.Validate() != nil {
					v.SetInt(int64(was - 1))
				}
			default:
				t.Fatalf("no non-default value for a %T field; add one", was)
			}
			require.NoError(t, cfg.Validate(), "the changed value is valid")
			assert.NotEqual(t, base, OptionsFrom(cfg), "OptionsFrom doesn't map search.%s", field.Tag.Get("yaml"))
		})
	}
	assert.Len(t, slices.Compact(slices.Sorted(slices.Values(SearchFields()))), len(SearchFields()), "listed once")

	cfg := config.Default()
	cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleReranker: {Model: "gemma4:26b"}}
	require.NoError(t, cfg.Validate())
	assert.Equal(t, "gemma4:26b", OptionsFrom(cfg).Model.Model)
	assert.False(t, OptionsFrom(cfg).Shared, "a model no other role uses")
}

// TestShipped: the daemon reranks the topical kinds listwise, a page
// remembered, keywords and searches without a plan keeping their first
// ranking, by the reranker's order alone, every input but folders and
// claims, textless pages read; a Qwen3-Reranker build the same routes
// pointwise.
func TestShipped(t *testing.T) {
	s := Shipped()
	s.Depth = config.Default().Search.RerankDepth
	require.NoError(t, s.Validate())
	assert.Equal(t, 30, s.Depth)
	for g, want := range map[Group]Method{GroupOne: MethodSkip, GroupTopical: MethodListwise,
		GroupNone: MethodSkip, GroupKeywords: MethodSkip} {
		assert.Equal(t, want, s.route(g, FamilyInstruct), g)
	}
	assert.Equal(t, MethodPointwise, s.route(GroupTopical, FamilyQwen3Reranker))
	assert.Equal(t, MethodSkip, s.route(GroupOne, FamilyQwen3Reranker))
	assert.Equal(t, BlendPure, s.Blend)
	assert.Equal(t, TextlessRead, s.Textless)
	assert.Equal(t, Inputs{Input: InputBoth}, s.Inputs)
}

// TestShipped_NeedsAPlan: every shipped route, for either family, needs a
// plan. The planner's and the reranker's gates keep their eviction holds
// apart, though they may hold one model on one server: a reranker that
// never asks about a search without a plan never warms the model while
// the planner's hold, which leaves searches without one, covers it
// (api's TestSearch_PlannerHeldLeavesTheRerankerIdle). A route for
// searches without a plan breaks that: key the holds by server and model
// first.
func TestShipped_NeedsAPlan(t *testing.T) {
	for _, f := range []Family{FamilyInstruct, FamilyQwen3Reranker} {
		assert.True(t, Shipped().needsPlan(f), "%s: a shipped route reranks searches without a plan", f)
	}
}

// TestOptionsFrom_MeasuredDefault: with search.rerank left out, gemma4:26b,
// the one model measured to pass, reranks by default while summaries are
// written and questions understood; without the summaries, or without
// understanding, it is off by default saying what turns it on, and so is
// any other model; a planner withheld from its model leaves it off by
// default too, never a hold of reranking nobody asked for.
func TestOptionsFrom_MeasuredDefault(t *testing.T) {
	c := config.Default()
	c.Generation.Model = "gemma4:26b"
	c.Summaries.Enabled = new(true)
	o := OptionsFrom(c)
	assert.True(t, o.On)
	assert.True(t, o.ByDefault)
	assert.Empty(t, o.OffReason)
	assert.Empty(t, o.Held())

	c.Summaries.Enabled = new(false)
	o = OptionsFrom(c)
	assert.False(t, o.On)
	assert.False(t, o.ByDefault)
	assert.Equal(t, "search reranking is off by default for gemma4:26b without the summaries, which it was "+
		"measured with; "+SummariesFix, o.OffReason)

	c.Summaries.Enabled = new(true)
	c.Search.Understand = false
	assert.Equal(t, "search reranking is off by default for gemma4:26b without search understanding, which it "+
		"reranks; search.understand: true turns it on (then restart)", OptionsFrom(c).OffReason)

	c.Search.Understand = true
	c.Generation.Model = "gemma4:12b"
	o = OptionsFrom(c)
	assert.False(t, o.On)
	assert.Contains(t, o.OffReason, "gemma4:12b, which isn't measured as the reranker")

	c.Generation.Model = "gemma4:26b"
	c.Generation.BaseURL = "http://gpu.example:11434"
	c.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleReranker: {BaseURL: "http://gpu.example:11434"}}
	o = OptionsFrom(c)
	assert.False(t, o.On, "no question is understood")
	assert.False(t, o.ByDefault)
	assert.Equal(t, "search reranking is off by default for gemma4:26b: curio reranks only understood searches, "+
		"and no question is understood: questions would leave this machine for http://gpu.example:11434, "+
		"generation.base_url, which the planner role doesn't name; set generation.roles.planner.base_url to send "+
		"them there, or search.understand: false", o.OffReason)

	c.Search.Rerank = new(true)
	o = OptionsFrom(c)
	assert.True(t, o.On, "asked for: held, and said")
	assert.False(t, o.ByDefault)
	assert.NotEmpty(t, o.Held())
}
