package ask

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/modelgate"
)

// OffReason is why no question is answered while ask.enabled is false.
const OffReason = "curio ask is off (ask.enabled: false)"

// OnFix is how Ask that is off by default is turned on.
const OnFix = "ask.enabled: true turns it on, its fitness check still guarding it (then restart)"

// Options configure an Asker.
type Options struct {
	// On is whether questions are answered (config.Config.AskOn); off,
	// every ask is StatusOff with OffReason as the reason, the package's
	// OffReason when it is empty.
	On        bool
	OffReason string
	// ByDefault says On is config.yaml leaving ask.enabled out, for a
	// model measured as the answer writer (config.AskMeasured), and
	// OffByDefault that off is, for a model that isn't
	// (config.AskOffByDefault).
	ByDefault    bool
	OffByDefault bool
	// Sources is how many pages an answer is written from (ask.sources).
	Sources int
	// MaxTokens caps an answer (ask.max_tokens).
	MaxTokens int
	// Timeout bounds a whole ask, the waits included (ask.timeout_seconds).
	Timeout time.Duration
	// Rerank is whether an ask that leaves it to the daemon reorders its
	// search's head with the reranker (ask.rerank).
	Rerank bool
	// Model is the answers role's writing model, as the role resolves it.
	Model config.ModelSpec
	// KeepAlive is how long the model stays loaded after an answer, a
	// probe or a warm-up: search's keep-alive
	// (search.understand_keep_alive_minutes), so an answer between two
	// searches never cuts their warm window; 0 is 30 minutes.
	KeepAlive time.Duration
	// Shared says another role in use writes with the same model on the
	// same server (config.SharesModel): the writer then never unloads it.
	Shared bool
	// ShortSharer says such a role sends no keep-alive, or a shorter one
	// (config.SharesModelShorter): the writer then allows its requests to
	// shorten its keep-alive, to Ollama's default.
	ShortSharer bool
	// Withheld, when set, is why no question or page may be sent to the
	// model: it runs off this machine, or with another provider, which the
	// answers role doesn't name itself. Every ask is then StatusWithheld
	// with it as the reason.
	Withheld string
	// Using, when set, is told of each call the writer makes of its model
	// (an answer, a probe, a load, an unload), and is held around a whole
	// ask: the daemon's summary pass yields to them (jobs.Foreground).
	Using func() (end func())
	// Traffic, when set, is the process's record of every feature's calls
	// of each model (modelgate.Traffic): the writer marks its slot and its
	// fitness check there as long calls, so a plan or a rerank that waited
	// behind one doesn't count toward holding them, and its own answer
	// that ran past the ask's deadline while another feature called the
	// model doesn't count toward holding Ask.
	Traffic *modelgate.Traffic
	// Now is the clock that says what day today is; nil is time.Now.
	Now func() time.Time
	// Location is where today is; nil is time.Local, the user's.
	Location *time.Location
	// Log receives the asker's records; nil is slog.Default().
	Log *slog.Logger
}

// OptionsFrom is the asker's Options as config.yaml sets them: the one
// mapping the daemon and the developer's tools share. The clock, location
// and log stay unset, for New's defaults.
func OptionsFrom(c config.Config) Options {
	model := c.Generation.Role(config.RoleAnswers)
	o := Options{
		On:           c.AskOn(),
		ByDefault:    c.AskOn() && c.Ask.Enabled == nil,
		OffByDefault: c.AskOffByDefault(),
		Sources:      c.Ask.Sources,
		MaxTokens:    c.Ask.MaxTokens,
		Timeout:      time.Duration(c.Ask.TimeoutSeconds) * time.Second,
		Rerank:       c.Ask.Rerank,
		Model:        model,
		KeepAlive:    c.KeepAliveOf(config.RoleAnswers),
		Shared:       c.SharesModel(config.RoleAnswers),
		ShortSharer:  c.SharesModelShorter(config.RoleAnswers),
		Withheld:     withheld(c.Generation),
	}
	if !o.On {
		o.OffReason = offReason(c, model.Model)
	}
	return o
}

// offReason is why Ask is off under c: config.yaml turned it off, or,
// left out, its model isn't measured as the answer writer
// (config.AskOffByDefault).
func offReason(c config.Config, model string) string {
	if !c.AskOffByDefault() {
		return OffReason
	}
	return fmt.Sprintf("curio ask is off by default for %s, which isn't measured as the answer writer; %s", model,
		OnFix)
}

// withheld is why questions and pages may not go to the answers' model, ""
// when they may. Both are the user's own (a question; their pages, their
// summaries, interest labels and saved dates), so they go off this
// machine, or to a provider other than a local Ollama, only when the
// answers role names that server or provider itself
// (config.Generation.Unnamed).
func withheld(g config.Generation) string {
	switch key, value := g.Unnamed(config.RoleAnswers); key {
	case config.KeyProvider:
		return fmt.Sprintf("questions and pages would go to the provider %s, generation.provider, which the "+
			"answers role doesn't name; set generation.roles.answers.provider to send them there, or "+
			"ask.enabled: false", value)
	case config.KeyBaseURL:
		return fmt.Sprintf("questions and pages would leave this machine for %s, generation.base_url, which the "+
			"answers role doesn't name; set generation.roles.answers.base_url to send them there, or "+
			"ask.enabled: false", value)
	}
	return ""
}
