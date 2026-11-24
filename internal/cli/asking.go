package cli

import (
	"fmt"
	"strings"

	"github.com/samsar/curio/internal/client"
)

// What the daemon's asker says of itself, as healthz words it: the reason
// while ask.enabled is false (ask.OffReason), and how to turn on an Ask
// that is off by default, which that reason ends with (ask.OnFix).
const (
	askOffReason = "curio ask is off (ask.enabled: false)"
	askOnFix     = "ask.enabled: true turns it on, its fitness check still guarding it (then restart)"
)

// Fixes for an Ask that answers no question: a model that failed its check
// (ask.UnfitFix, which its reason ends with), one Ollama keeps unloading,
// answers that keep timing out, and a check that keeps failing to reach
// the model.
const (
	askUnfitFix = "set generation.roles.answers.model to a model that passes, or ask.enabled: false"
	askEvictFix = "the answer writer's model and the other models curio uses don't fit in memory together: give " +
		"the answers role a smaller model (generation.roles.answers.model), or set ask.enabled: false"
	askSlowFix = "give the answers role a faster model (generation.roles.answers.model), more time " +
		"(ask.timeout_seconds) or fewer sources (ask.sources), or set ask.enabled: false"
	askCheckFix = "check that Ollama serves the answer writer's model (`ollama ps`, and Ollama's log for its " +
		"errors): the daemon checks it again a minute after each failure"
)

// askSlowCheck reports whether a's timeout hold is a check's that ran out
// of time, not answers'.
func askSlowCheck(a *client.AskingHealth) bool {
	return !a.TimeoutHoldUntil.IsZero() && a.LastFailure != nil && a.LastFailure.Cause == failureCheckSlow
}

// askingCheck is doctor's check of curio ask, as the daemon's asker knows
// its model, from its memory: fine while off, saying how to turn it on
// when it is off by default for an unmeasured model; a warning while it
// is on and no question can be answered (withheld from the model or a
// provider that can't stream, a model that failed its check, one Ollama
// keeps unloading, answers that keep timing out) or its check keeps
// failing to reach the model, each with its fix; otherwise how the model
// stands.
func askingCheck(a client.AskingHealth) (status checkStatus, detail, hint string) {
	switch {
	case !a.On && (a.Reason == askOffReason || a.Reason == ""):
		return statusOK, "off (ask.enabled: false)", ""
	case !a.On && strings.HasSuffix(a.Reason, "; "+askOnFix):
		return statusOK, strings.TrimSuffix(strings.TrimPrefix(a.Reason, "curio ask is "), "; "+askOnFix), askOnFix
	case !a.On:
		return statusOK, strings.TrimPrefix(a.Reason, "curio ask is "), ""
	case a.Reason != "":
		return statusWarn, "no question is answered: " + a.Reason, ""
	case a.Check == checkUnfit:
		return statusWarn, fmt.Sprintf("%s (checked %s); no question is answered",
			strings.TrimSuffix(a.Unfit, "; "+askUnfitFix), localTime(a.CheckedAt)), askUnfitFix
	case !a.EvictionHoldUntil.IsZero():
		return statusWarn, fmt.Sprintf("Ollama unloaded %s within its keep-alive twice, for other models; curio "+
			"doesn't load it again until %s, and no question is answered meanwhile", a.Model,
			localTime(a.EvictionHoldUntil)), askEvictFix
	case askSlowCheck(&a):
		return statusWarn, fmt.Sprintf("%s; curio checks it again at %s, and no question is answered meanwhile",
			a.LastFailure.Error, localTime(a.TimeoutHoldUntil)), askSlowFix
	case !a.TimeoutHoldUntil.IsZero():
		return statusWarn, fmt.Sprintf("answers with %s kept running past ask.timeout_seconds; no question is "+
			"answered until %s", a.Model, localTime(a.TimeoutHoldUntil)), askSlowFix
	case a.Check == checkUnchecked && a.LastFailure != nil && a.LastFailure.Cause == failureCheckUnreached:
		return statusWarn, fmt.Sprintf("%s couldn't be checked: %s; no question is answered until it is", a.Model,
			a.LastFailure.Error), askCheckFix
	case a.Check == checkFit:
		return statusOK, askByDefault(a) + fmt.Sprintf("%s passed its check (%s)", a.Model, localTime(a.CheckedAt)),
			""
	}
	return statusOK, askByDefault(a) + a.Model + " not checked yet: the first question loads and checks it", ""
}

// askByDefault opens the line of an Ask that is on by default: what an
// owner gets without asking for it.
func askByDefault(a client.AskingHealth) string {
	if !a.OnByDefault {
		return ""
	}
	return "on by default (measured as the answer writer): "
}

// askingWarning is the line status prints while Ask is on but no question
// can be answered, for the first that holds of: questions and pages
// withheld from the model or a provider that can't stream, a model that
// failed its check, a model Ollama keeps unloading, answers that keep
// timing out, and a check that can't reach the model; empty otherwise,
// and from a daemon that predates Ask. The fix is doctor's.
func askingWarning(a *client.AskingHealth) string {
	switch {
	case a == nil || !a.On:
		return ""
	case a.Reason != "":
		return fmt.Sprintf("warning: curio ask answers nothing: %s; run `curio doctor`\n", a.Reason)
	case a.Check == checkUnfit:
		return fmt.Sprintf("warning: curio ask answers nothing: %s failed its check; run `curio doctor`\n", a.Model)
	case !a.EvictionHoldUntil.IsZero():
		return fmt.Sprintf("warning: curio ask answers nothing until %s: Ollama keeps unloading %s for other "+
			"models; run `curio doctor`\n", localTime(a.EvictionHoldUntil), a.Model)
	case askSlowCheck(a):
		return fmt.Sprintf("warning: curio ask answers nothing until %s: checking %s ran out of time; run "+
			"`curio doctor`\n", localTime(a.TimeoutHoldUntil), a.Model)
	case !a.TimeoutHoldUntil.IsZero():
		return fmt.Sprintf("warning: curio ask answers nothing until %s: answers with %s keep timing out; run "+
			"`curio doctor`\n", localTime(a.TimeoutHoldUntil), a.Model)
	case a.Check == checkUnchecked && a.LastFailure != nil && a.LastFailure.Cause == failureCheckUnreached:
		return fmt.Sprintf("warning: curio ask answers nothing until %s can be checked; run `curio doctor`\n",
			a.Model)
	}
	return ""
}
