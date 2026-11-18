package understand

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/modelgate"
)

// Options configure a Planner.
type Options struct {
	// On is search.understand: off, every plan is StatusOff.
	On bool
	// Timeout bounds one plan (search.understand_timeout_seconds, shortened
	// to fit the CLI's budget: OptionsFrom).
	Timeout time.Duration
	// Shortened, when set, says why Timeout is shorter than
	// search.understand_timeout_seconds; New logs it once, at WARN.
	Shortened string
	// KeepAlive is how long the model stays loaded after a plan or a
	// warm-up (search.understand_keep_alive_minutes); 0 is 30 minutes.
	KeepAlive time.Duration
	// Model is the planner role's writing model, as the role resolves it.
	Model config.ModelSpec
	// Shared says another role in use writes with the same model on the
	// same server (config.SharesModel): the planner then never unloads it.
	Shared bool
	// ShortSharer says such a role sends no keep-alive, or a shorter one
	// (config.SharesModelShorter): the planner then allows its requests to
	// shorten its keep-alive, to Ollama's default.
	ShortSharer bool
	// Withheld, when set, is why no question may be sent to the model:
	// it runs off this machine, or with another provider, which the
	// planner role doesn't name itself, or search.embed_timeout_seconds
	// leaves a plan no time. Every plan is then StatusUnavailable with it
	// as the reason.
	Withheld string
	// Now is the clock that says what day today is, and when a failure's
	// retry is due; nil is time.Now.
	Now func() time.Time
	// Location is where today is, and where a saved day starts; nil is
	// time.Local, the user's.
	Location *time.Location
	// Log receives the planner's warnings; nil is slog.Default().
	Log *slog.Logger
	// Using, when set, is told of each call the planner makes of its model
	// (a plan, a probe of the fitness check, a load, an unload): it returns
	// the func that marks the call ended. The daemon's summary pass yields
	// to these calls; nil tells nobody.
	Using func() (end func())
	// Traffic, when set, is the process's record of every feature's calls
	// of each model (modelgate.Traffic): a plan that runs past its timeout
	// while an answer holds the model waited for it, and doesn't count
	// toward holding planning.
	Traffic *modelgate.Traffic
}

// OptionsFrom is the planner's Options as config.yaml sets them: the one
// mapping the daemon and the developer's tools share. The clock, location
// and log stay unset, for New's defaults.
func OptionsFrom(c config.Config) Options {
	o := Options{
		On:          c.Search.Understand,
		Timeout:     time.Duration(c.Search.UnderstandTimeoutSeconds) * time.Second,
		KeepAlive:   time.Duration(c.Search.UnderstandKeepAliveMinutes) * time.Minute,
		Model:       c.Generation.Role(config.RolePlanner),
		Shared:      c.SharesModel(config.RolePlanner),
		ShortSharer: c.SharesModelShorter(config.RolePlanner),
		Withheld:    withheld(c.Generation),
	}
	o.fitBudget(c)
	return o
}

// fitBudget holds a plan to search's budget (config.Config.PlanTimeout):
// past it, the plan's timeout is shortened to what the embed timeout
// leaves, or, when that is under a second, understanding is withheld.
func (o *Options) fitBudget(c config.Config) {
	asked, embed := c.Search.UnderstandTimeoutSeconds, c.Search.EmbedTimeoutSeconds
	switch left := c.PlanTimeout(); {
	case left == o.Timeout:
	case left > 0:
		o.Timeout = left
		o.Shortened = fmt.Sprintf("search.understand_timeout_seconds (%d) plus search.embed_timeout_seconds (%d) "+
			"plus 1 s reaches %d s, past which curio search could give up before its results: plans get %d s",
			asked, embed, config.SearchBudgetSeconds, int(left/time.Second))
	case o.Withheld == "":
		o.Withheld = fmt.Sprintf("search.embed_timeout_seconds (%d) leaves no time for a plan: with "+
			"search.understand_timeout_seconds and 1 s it must stay under %d s, so curio search answers within "+
			"30 s; lower search.embed_timeout_seconds, or set search.understand: false", embed,
			config.SearchBudgetSeconds)
	}
}

// withheld is why questions may not go to the planner's model, "" when
// they may. A question is the user's own words, so it goes off this
// machine, or to a provider other than a local Ollama, only when the
// planner role names that server or provider itself
// (config.Generation.Unnamed).
func withheld(g config.Generation) string {
	switch key, value := g.Unnamed(config.RolePlanner); key {
	case config.KeyProvider:
		return fmt.Sprintf("questions would go to the provider %s, generation.provider, which the planner role "+
			"doesn't name; set generation.roles.planner.provider to send them there, or search.understand: false",
			value)
	case config.KeyBaseURL:
		return fmt.Sprintf("questions would leave this machine for %s, generation.base_url, which the planner role "+
			"doesn't name; set generation.roles.planner.base_url to send them there, or search.understand: false",
			value)
	}
	return ""
}

// SearchFields are the fields of config.Search that OptionsFrom maps: the
// planner's. search.ConfigFrom maps the others, and each package's test
// holds its function to its share.
func SearchFields() []string {
	return []string{"Understand", "UnderstandTimeoutSeconds", "UnderstandKeepAliveMinutes"}
}
