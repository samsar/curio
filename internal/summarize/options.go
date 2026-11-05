package summarize

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/samsar/curio/internal/config"
)

// Options configure a Writer.
type Options struct {
	// On is whether summaries are written (config.Config.SummariesOn):
	// off, the writer writes nothing.
	On bool
	// OffByDefault says they are off because config.yaml leaves
	// summaries.enabled out, rather than setting it false.
	OffByDefault bool
	// Model is the summaries role's writing model, as the role resolves
	// it.
	Model config.ModelSpec
	// Shared says another role in use writes with the same model on the
	// same server (config.SharesModel): the writer then never unloads it.
	Shared bool
	// KeepAlive is how long the provider keeps the model loaded after each
	// call and load; 0 sends none, leaving the provider's default.
	KeepAlive time.Duration
	// Withheld, when set, is why no page may be sent to the model: it runs
	// off this machine, or with another provider, which the summaries role
	// doesn't name itself.
	Withheld string
	// Measure is for the developer's tools, which measure a model whatever
	// its check says: Write never runs the check, and a verdict Check
	// finds never stops a write. The daemon never sets it.
	Measure bool
	// Now is the writer's clock; nil is time.Now.
	Now func() time.Time
	// Log receives the writer's warnings; nil is slog.Default().
	Log *slog.Logger
}

// OptionsFrom is the writer's Options as config.yaml sets them: the one
// mapping the daemon and the developer's tools share. The clock and log
// stay unset, for New's defaults.
func OptionsFrom(c config.Config) Options {
	return Options{
		On:           c.SummariesOn(),
		OffByDefault: c.Summaries.Enabled == nil,
		Model:        c.Generation.Role(config.RoleSummaries),
		Shared:       c.SharesModel(config.RoleSummaries),
		KeepAlive:    c.KeepAliveOf(config.RoleSummaries),
		Withheld:     withheld(c.Generation),
	}
}

// withheld is why pages may not go to the summaries' model, "" when they
// may. A page is the user's own library, so it goes off this machine, or
// to a provider other than a local Ollama, only when the summaries role
// names that server or provider itself (config.Generation.Unnamed).
func withheld(g config.Generation) string {
	switch key, value := g.Unnamed(config.RoleSummaries); key {
	case config.KeyProvider:
		return fmt.Sprintf("pages would go to the provider %s, generation.provider, which the summaries role doesn't "+
			"name; set generation.roles.summaries.provider to send them there, or summaries.enabled: false", value)
	case config.KeyBaseURL:
		return fmt.Sprintf("pages would leave this machine for %s, generation.base_url, which the summaries role "+
			"doesn't name; set generation.roles.summaries.base_url to send them there, or summaries.enabled: false",
			value)
	}
	return ""
}
