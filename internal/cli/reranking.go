package cli

import (
	"fmt"
	"strings"

	"github.com/samsar/curio/internal/client"
)

// What the daemon's reranker says of itself, as healthz words it: the
// reason while search.rerank is false (rerank.OffReason), and how to turn
// on reranking that is off by default, which that reason ends with
// (rerank.OnFix).
const (
	rerankOffReason    = "search reranking is off (search.rerank: false)"
	rerankOnFix        = "search.rerank: true turns it on (then restart)"
	rerankSummariesFix = "summaries.enabled: true turns it on (then restart)"
)

// Fixes for search reranking that reorders no search: a model that failed
// its check (rerank.UnfitFix, which its reason ends with), one Ollama
// keeps unloading, reranks that keep timing out, and a check that keeps
// failing to reach the model.
const (
	rerankUnfitFix    = "set generation.roles.reranker.model to a model that passes, or search.rerank: false"
	rerankLogprobsFix = "this Ollama returns no logprobs: upgrade Ollama, then restart the daemon"
	rerankEvictFix    = "the reranker's model and the other models curio uses don't fit in memory together: give the " +
		"reranker a smaller model (generation.roles.reranker.model), or set search.rerank: false"
	rerankSlowFix = "give the reranker a faster model (generation.roles.reranker.model), more time " +
		"(search.rerank_timeout_seconds) or fewer candidates (search.rerank_depth), or set search.rerank: false"
	rerankCheckFix = "check that Ollama serves the reranker's model (`ollama ps`, and Ollama's log for its " +
		"errors): the daemon checks it again a minute after each failure"
)

// The causes the gate records for a fitness check that couldn't reach the
// model, and for one whose probe ran out of time, which holds reranking.
const (
	failureCheckUnreached = "unavailable:check"
	failureCheckSlow      = "timeout:check"
)

// slowCheck reports whether r's timeout hold is a check's that ran out of
// time, not reranks'.
func slowCheck(r *client.RerankingHealth) bool {
	return !r.TimeoutHoldUntil.IsZero() && r.LastFailure != nil && r.LastFailure.Cause == failureCheckSlow
}

// rerankUnplannedFix is the fix for reranking held back by understanding:
// the understanding check's.
const rerankUnplannedFix = "see the understanding check"

// rerankingCheck is doctor's check of search reranking, as the daemon's
// reranker knows its model, from its memory, and the planner (u, nil from
// a daemon that predates understanding's health) knows its own: fine while
// off, saying how to turn it on when it is off by default; a warning while
// it is on and no search can be reranked (withheld from the model or a
// provider its routes can't ask, no search understood, a model that failed
// its check, one Ollama keeps unloading, reranks that keep timing out, no
// question understood for now: the shipped routes rerank only understood
// searches) or its check keeps failing to reach the model, each with its
// fix; otherwise how the model stands.
func rerankingCheck(r client.RerankingHealth, u *client.UnderstandingHealth) (status checkStatus, detail,
	hint string) {
	switch {
	case !r.On && (r.Reason == rerankOffReason || r.Reason == ""):
		return statusOK, "off (search.rerank: false)", ""
	case !r.On && strings.HasSuffix(r.Reason, "; "+rerankSummariesFix):
		return statusOK, strings.TrimSuffix(strings.TrimPrefix(r.Reason, "search reranking is "),
			"; "+rerankSummariesFix), rerankSummariesFix
	case !r.On && strings.HasSuffix(r.Reason, "; "+rerankOnFix):
		return statusOK, strings.TrimSuffix(strings.TrimPrefix(r.Reason, "search reranking is "), "; "+rerankOnFix),
			rerankOnFix
	case !r.On:
		return statusOK, strings.TrimPrefix(r.Reason, "search reranking is "), ""
	case r.Reason != "":
		return statusWarn, "every search keeps its first ranking: " + r.Reason, ""
	case r.Check == checkUnfit && strings.HasSuffix(r.Unfit, "; "+rerankLogprobsFix):
		return statusWarn, fmt.Sprintf("%s (checked %s); every search keeps its first ranking",
			strings.TrimSuffix(r.Unfit, "; "+rerankLogprobsFix), localTime(r.CheckedAt)), rerankLogprobsFix
	case r.Check == checkUnfit:
		return statusWarn, fmt.Sprintf("%s (checked %s); every search keeps its first ranking",
			strings.TrimSuffix(r.Unfit, "; "+rerankUnfitFix), localTime(r.CheckedAt)), rerankUnfitFix
	case !r.EvictionHoldUntil.IsZero():
		return statusWarn, fmt.Sprintf("Ollama unloaded %s within its keep-alive twice, for other models; curio "+
			"doesn't load it again until %s, and searches keep their first ranking meanwhile", r.Model,
			localTime(r.EvictionHoldUntil)), rerankEvictFix
	case slowCheck(&r):
		return statusWarn, fmt.Sprintf("%s; curio checks it again at %s, and searches keep their first ranking "+
			"meanwhile", r.LastFailure.Error, localTime(r.TimeoutHoldUntil)), rerankSlowFix
	case !r.TimeoutHoldUntil.IsZero():
		return statusWarn, fmt.Sprintf("reranks with %s kept taking longer than the timeout; searches keep their "+
			"first ranking until %s", r.Model, localTime(r.TimeoutHoldUntil)), rerankSlowFix
	case r.Check == checkUnchecked && r.LastFailure != nil && r.LastFailure.Cause == failureCheckUnreached:
		return statusWarn, fmt.Sprintf("%s couldn't be checked: %s; searches keep their first ranking until it is",
			r.Model, r.LastFailure.Error), rerankCheckFix
	case unplannedBy(u) != "":
		return statusWarn, "searches keep their first ranking: curio reranks only understood searches, and none is " +
			"for now: " + unplannedBy(u), rerankUnplannedFix
	case r.Check == checkFit:
		return statusOK, byDefault(r) + fmt.Sprintf("%s passed its check (%s)", r.Model, localTime(r.CheckedAt)), ""
	}
	return statusOK, byDefault(r) + r.Model + " not checked yet: the first reranked search loads and checks it", ""
}

// byDefault opens the line of reranking that is on by default: what an
// owner gets without asking for it.
func byDefault(r client.RerankingHealth) string {
	if !r.OnByDefault {
		return ""
	}
	return "on by default (measured as the reranker): "
}

// unplannedBy is why no question gets a plan for now while understanding is
// on, as the planner knows its model: a reason that holds every question
// back, a model that failed its check, one Ollama keeps unloading, plans
// that keep timing out; "" while a question may get one, while
// understanding is off (the reranker's own reason then says what that
// means for it), and for a daemon that predates understanding's health.
func unplannedBy(u *client.UnderstandingHealth) string {
	switch {
	case u == nil || !u.On:
		return ""
	case u.Reason != "":
		return u.Reason
	case u.Check == checkUnfit:
		return u.Model + " failed the planner's check"
	case !u.EvictionHoldUntil.IsZero():
		return fmt.Sprintf("Ollama keeps unloading %s, which curio doesn't load again until %s", u.Model,
			localTime(u.EvictionHoldUntil))
	case !u.TimeoutHoldUntil.IsZero():
		return fmt.Sprintf("plans with %s keep timing out, and searches use their words until %s", u.Model,
			localTime(u.TimeoutHoldUntil))
	}
	return ""
}

// rerankingWarning is the line status prints while reranking is on but no
// search can be reranked, for the first that holds of: questions and pages
// withheld from the model or a provider its routes can't ask, a model
// that failed its check, a model Ollama keeps unloading, and reranks that
// keep timing out; empty otherwise, and from a daemon that predates
// reranking. The fix is doctor's.
func rerankingWarning(r *client.RerankingHealth) string {
	switch {
	case r == nil || !r.On:
		return ""
	case r.Reason != "":
		return fmt.Sprintf("warning: search reranking keeps every first ranking: %s; run `curio doctor`\n", r.Reason)
	case r.Check == checkUnfit:
		return fmt.Sprintf("warning: search reranking keeps every first ranking: %s failed its check; run "+
			"`curio doctor`\n", r.Model)
	case !r.EvictionHoldUntil.IsZero():
		return fmt.Sprintf("warning: search reranking keeps first rankings until %s: Ollama keeps unloading %s for "+
			"other models; run `curio doctor`\n", localTime(r.EvictionHoldUntil), r.Model)
	case slowCheck(r):
		return fmt.Sprintf("warning: search reranking keeps first rankings until %s: checking %s ran out of time; "+
			"run `curio doctor`\n", localTime(r.TimeoutHoldUntil), r.Model)
	case !r.TimeoutHoldUntil.IsZero():
		return fmt.Sprintf("warning: search reranking keeps first rankings until %s: reranks with %s keep timing "+
			"out; run `curio doctor`\n", localTime(r.TimeoutHoldUntil), r.Model)
	}
	return ""
}
