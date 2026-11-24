package ask

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
)

// TestOptionsFrom_Default: config.yaml's defaults answer from 6 sources,
// 768 tokens at most, within 90 s, with search's keep-alive, on
// generation.model, which the labels and the planner share; and that
// model, unmeasured as the answer writer, answers nothing by default,
// saying how to turn it on.
func TestOptionsFrom_Default(t *testing.T) {
	o := OptionsFrom(config.Default())
	assert.False(t, o.On)
	assert.False(t, o.ByDefault)
	assert.True(t, o.OffByDefault)
	assert.Equal(t, "curio ask is off by default for qwen3:4b-instruct, which isn't measured as the answer writer; "+
		"ask.enabled: true turns it on, its fitness check still guarding it (then restart)", o.OffReason)
	assert.Equal(t, 6, o.Sources)
	assert.False(t, o.Rerank)
	assert.Equal(t, 768, o.MaxTokens)
	assert.Equal(t, 90*time.Second, o.Timeout)
	assert.Equal(t, 30*time.Minute, o.KeepAlive)
	assert.Equal(t, "qwen3:4b-instruct", o.Model.Model)
	assert.True(t, o.Shared)
	assert.True(t, o.ShortSharer, "the labels send no keep-alive")
	assert.Empty(t, o.Withheld)
}

// TestOptionsFrom_OnByDefault: left out, Ask is on for a model measured
// as the answer writer, saying it is on by default; set, it is as set for
// any model, never by default.
func TestOptionsFrom_OnByDefault(t *testing.T) {
	c := config.Default()
	c.Generation.Model = "gemma4:26b"
	o := OptionsFrom(c)
	assert.True(t, o.On)
	assert.True(t, o.ByDefault)
	assert.False(t, o.OffByDefault)
	assert.Empty(t, o.OffReason)

	c.Ask.Enabled = new(true)
	o = OptionsFrom(c)
	assert.True(t, o.On)
	assert.False(t, o.ByDefault, "on as set")

	c.Ask.Enabled = new(false)
	o = OptionsFrom(c)
	assert.False(t, o.On)
	assert.False(t, o.OffByDefault, "off as set")
	assert.Equal(t, OffReason, o.OffReason)
}

// TestOptionsFrom_MapsEveryAskKey: changing any key of config.Ask changes
// the options, so a key added later that OptionsFrom doesn't map fails.
func TestOptionsFrom_MapsEveryAskKey(t *testing.T) {
	base := OptionsFrom(config.Default())
	typ := reflect.TypeFor[config.Ask]()
	for i := range typ.NumField() {
		field := typ.Field(i)
		t.Run("ask."+field.Tag.Get("yaml"), func(t *testing.T) {
			cfg := config.Default()
			v := reflect.ValueOf(&cfg.Ask).Elem().Field(i)
			switch was := v.Interface().(type) {
			case bool:
				v.SetBool(!was)
			case *bool:
				v.Set(reflect.ValueOf(new(was == nil)))
			case int:
				v.SetInt(int64(was + 1))
			default:
				t.Fatalf("no non-default value for a %T field; add one", was)
			}
			require.NoError(t, cfg.Validate(), "the changed value is valid")
			assert.NotEqual(t, base, OptionsFrom(cfg), "OptionsFrom doesn't map ask.%s", field.Tag.Get("yaml"))
		})
	}
	t.Run("generation.roles.answers", func(t *testing.T) {
		cfg := config.Default()
		cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleAnswers: {Model: "gemma4:26b"}}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, "gemma4:26b", OptionsFrom(cfg).Model.Model)
		cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RolePlanner: {Model: "gemma4:26b"}}
		assert.Equal(t, "qwen3:4b-instruct", OptionsFrom(cfg).Model.Model, "the planner's model isn't the answers'")
	})
}

// TestOptionsFrom_Withheld: questions and pages go off this machine, or
// to another provider, only when the answers role names where.
func TestOptionsFrom_Withheld(t *testing.T) {
	c := config.Default()
	c.Generation.BaseURL = "http://gpu.example:11434"
	assert.Equal(t, "questions and pages would leave this machine for http://gpu.example:11434, "+
		"generation.base_url, which the answers role doesn't name; set generation.roles.answers.base_url to send "+
		"them there, or ask.enabled: false", OptionsFrom(c).Withheld)

	c.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleAnswers: {BaseURL: "http://gpu.example:11434"}}
	assert.Empty(t, OptionsFrom(c).Withheld, "the role names the server")

	c = config.Default()
	c.Generation.Provider = "openai"
	assert.Contains(t, OptionsFrom(c).Withheld, "generation.roles.answers.provider")

	c = config.Default()
	c.Generation.BaseURL = "http://127.0.0.1:11500"
	assert.Empty(t, OptionsFrom(c).Withheld, "on this machine")
}
