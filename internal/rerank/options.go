package rerank

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/understand"
)

// OffReason is why a search wasn't reranked while search.rerank is false.
const OffReason = "search reranking is off (search.rerank: false)"

// OnFix is how reranking that is off by default is turned on.
const OnFix = "search.rerank: true turns it on (then restart)"

// SummariesFix is how reranking that is off by default for a model
// measured only with the summaries is turned on: by writing them.
const SummariesFix = "summaries.enabled: true turns it on (then restart)"

// Options configure a Reranker.
type Options struct {
	// On is whether searches may be reranked (config.Config.RerankOn);
	// off, every rerank is StatusOff with OffReason as the reason.
	On        bool
	OffReason string
	// ByDefault says On is config.yaml leaving search.rerank out, for a
	// model measured as the reranker (config.RerankMeasured).
	ByDefault bool
	// Model is the reranker role's writing model, as the role resolves it.
	Model config.ModelSpec
	// Settings are the routes, depth, blend and inputs reranks use.
	Settings Settings
	// Timeout bounds one rerank, the wait for the model included
	// (search.rerank_timeout_seconds, shortened to fit the CLI's budget:
	// OptionsFrom).
	Timeout time.Duration
	// Shortened, when set, says why Timeout is shorter than
	// search.rerank_timeout_seconds; New logs it once, at WARN.
	Shortened string
	// KeepAlive is how long the model stays loaded after a rerank or a
	// warm-up (search.understand_keep_alive_minutes, search's models'
	// keep-alive); 0 is 30 minutes.
	KeepAlive time.Duration
	// Shared says another role in use writes with the same model on the
	// same server (config.SharesModel): the reranker then never unloads it.
	Shared bool
	// ShortSharer says such a role sends no keep-alive, or a shorter one
	// (config.SharesModelShorter): the reranker then allows its requests
	// to shorten its keep-alive, to Ollama's default.
	ShortSharer bool
	// Withheld, when set, is why no question or candidate may be sent to
	// the model; every rerank is then StatusUnavailable with it.
	Withheld string
	// Unplanned, when set, is why no search has a plan: search
	// understanding is off, or withheld from its model. While every route
	// needs a plan (Held), every rerank is then StatusUnavailable with it.
	Unplanned string
	// Using, when set, is told of each call of the model (Gate's Using).
	Using func() (end func())
	// Now is the gate's clock; nil is time.Now.
	Now func() time.Time
	// Log receives the reranker's records; nil is slog.Default().
	Log *slog.Logger
}

// OptionsFrom is the reranker's Options as config.yaml sets them: the one
// mapping the daemon and the developer's tools share. The clock and log
// stay unset, for New's defaults.
func OptionsFrom(c config.Config) Options {
	settings := Shipped()
	settings.Depth = c.Search.RerankDepth
	model := c.Generation.Role(config.RoleReranker)
	o := Options{
		On:          c.RerankOn(),
		Model:       model,
		Settings:    settings,
		Timeout:     time.Duration(c.Search.RerankTimeoutSeconds) * time.Second,
		KeepAlive:   time.Duration(c.Search.UnderstandKeepAliveMinutes) * time.Minute,
		Shared:      c.SharesModel(config.RoleReranker),
		ShortSharer: c.SharesModelShorter(config.RoleReranker),
		Withheld:    withheld(c.Generation),
		Unplanned:   unplanned(c),
	}
	if !o.On {
		o.OffReason = offReason(c, model.Model)
	}
	o.ByDefault = o.On && c.Search.Rerank == nil
	if o.ByDefault && o.Unplanned != "" {
		// Its measured point reranks understood searches, and none is:
		// off by default, not a hold of reranking nobody asked for.
		o.On, o.ByDefault = false, false
		o.OffReason = fmt.Sprintf("search reranking is off by default for %s: %s", model.Model, o.Unplanned)
	}
	o.fitBudget(planSeconds(c), c.Search.EmbedTimeoutSeconds, c.Search.RerankTimeoutSeconds)
	return o
}

// Held is why no search is reranked while reranking is on, "" when one
// may be: Withheld, or Unplanned while every route the model's family
// takes needs a plan. A route that reranked a search without a plan would
// lift Unplanned's hold; the shipped routes all need one.
func (o Options) Held() string {
	switch {
	case o.Withheld != "":
		return o.Withheld
	case o.Unplanned != "" && o.Settings.needsPlan(FamilyOf(o.Model.Model)):
		return o.Unplanned
	}
	return ""
}

// unplanned is why no search has a plan under c, "" when one may: search
// understanding is off, or withheld from its model (understand.OptionsFrom).
func unplanned(c config.Config) string {
	switch p := understand.OptionsFrom(c); {
	case !p.On:
		return "curio reranks only understood searches, and search.understand is off; set search.understand: " +
			"true, or search.rerank: false"
	case p.Withheld != "":
		return "curio reranks only understood searches, and no question is understood: " + p.Withheld
	}
	return ""
}

// offReason is why reranking is off under c: config.yaml turned it off,
// or, left out, its model isn't measured, search understanding, which its
// measured point reranks, is off, or that point needs the summaries,
// which aren't written.
func offReason(c config.Config, model string) string {
	switch p, measured := config.RerankMeasured(model); {
	case c.Search.Rerank != nil:
		return OffReason
	case measured && !c.Search.Understand:
		return fmt.Sprintf("search reranking is off by default for %s without search understanding, which it "+
			"reranks; search.understand: true turns it on (then restart)", model)
	case measured && p.NeedsSummaries:
		return fmt.Sprintf("search reranking is off by default for %s without the summaries, which it was "+
			"measured with; %s", model, SummariesFix)
	}
	return fmt.Sprintf("search reranking is off by default for %s, which isn't measured as the reranker; %s",
		model, OnFix)
}

// budgetSeconds is what an understood, reranked search may spend before
// and between its reads: asking whether the planner's model is loaded
// (1 s), the plan, the query's embed, asking whether the reranker's model
// is loaded (1 s) and the rerank stay under it, so `curio search`, which
// gives up after 30 s, keeps 5 s for the reads and gets its first ranking
// rather than a client timeout when Ollama is slow. The planner holds its
// own share to the same budget (understand.OptionsFrom).
const budgetSeconds = 25

// planSeconds is the most an understood search's plan costs under c: its
// timeout as understand.OptionsFrom sets it, and a second for asking
// whether the planner's model is loaded; 0 when no plan is asked for.
func planSeconds(c config.Config) int {
	p := understand.OptionsFrom(c)
	if !p.On || p.Withheld != "" {
		return 0
	}
	return int(p.Timeout/time.Second) + 1
}

// fitBudget holds a rerank to the budget: past it, the rerank's timeout is
// shortened to what the plan and the embed leave, or, when that is under a
// second, reranking is withheld. config.Validate leaves the sum to the
// reranker, since the keys it adds to, valid before reranking existed,
// can reach it: a refused config would stop the daemon on an upgrade.
func (o *Options) fitBudget(planSeconds, embedSeconds, rerankSeconds int) {
	if planSeconds+embedSeconds+rerankSeconds+1 < budgetSeconds {
		return
	}
	left := budgetSeconds - 1 - planSeconds - embedSeconds - 1
	if left >= 1 {
		o.Timeout = time.Duration(left) * time.Second
		o.Shortened = fmt.Sprintf("search.rerank_timeout_seconds (%d) with the plan's %d s, "+
			"search.embed_timeout_seconds (%d) and 1 s reaches %d s, past which curio search could give up before "+
			"its results: reranks get %d s", rerankSeconds, planSeconds, embedSeconds, budgetSeconds, left)
		return
	}
	if o.Withheld == "" {
		o.Withheld = fmt.Sprintf("the plan's %d s and search.embed_timeout_seconds (%d) leave no time for a "+
			"rerank: with search.rerank_timeout_seconds and 1 s for asking whether its model is loaded they must "+
			"stay under %d s, so curio search answers within 30 s; lower search.understand_timeout_seconds or "+
			"search.embed_timeout_seconds, or set search.rerank: false", planSeconds, embedSeconds, budgetSeconds)
	}
}

// withheld is why questions and candidates may not go to the reranker's
// model, "" when they may. Both are the user's own (a question, their
// pages), so they go off this machine, or to a provider other than a local
// Ollama, only when the reranker role names that server or provider
// itself (config.Generation.Unnamed).
func withheld(g config.Generation) string {
	switch key, value := g.Unnamed(config.RoleReranker); key {
	case config.KeyProvider:
		return fmt.Sprintf("questions and pages would go to the provider %s, generation.provider, which the "+
			"reranker role doesn't name; set generation.roles.reranker.provider to send them there, or "+
			"search.rerank: false", value)
	case config.KeyBaseURL:
		return fmt.Sprintf("questions and pages would leave this machine for %s, generation.base_url, which the "+
			"reranker role doesn't name; set generation.roles.reranker.base_url to send them there, or "+
			"search.rerank: false", value)
	}
	return ""
}

// SearchFields are the fields of config.Search that OptionsFrom maps for
// the reranker alone: search.ConfigFrom maps the engine's, and
// understand.SearchFields the planner's, whose keep-alive the reranker
// shares. Each package's test holds its function to its share.
func SearchFields() []string {
	return []string{"Rerank", "RerankDepth", "RerankTimeoutSeconds"}
}
