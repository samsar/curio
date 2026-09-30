package ui

import "github.com/samsar/curio/internal/store"

// causeText is what a page says and draws of a failure cause: a label, a
// sentence on what it means for one document, and the icon and tone of its
// card on the Failures tab.
type causeText struct {
	label, why, icon, tone string
}

// causes are the words, icon and tone of every store.FailureCause
// (TestCauses checks that each has them). A tone is one of stateTone's:
// warn where the sentence says a refetch later usually works; neutral for
// a dead link, drawn as the dead state is, and where a refetch can't
// change the verdict or it isn't the site refusing; danger, the failed
// state's, for the rest.
var causes = map[store.FailureCause]causeText{
	store.FailureCauseDeadLink: {"Dead link", "The page is gone: a 404 or 410, a “not found” page, " +
		"or a redirect onto a site's front page. A refetch helps only if it came back.", "unlink", "neutral"},
	store.FailureCauseAntiBot: {"Blocked by bot protection", "The site blocked curio's request: " +
		"a 403 or 503, or a bot check or block page.", "shield-alert", "danger"},
	store.FailureCauseLoginWall: {"Behind a login", "The site answered with a sign-in page, " +
		"a redirect onto one, or too little text to be the article.", "lock", "danger"},
	store.FailureCauseJinaRefused: {"Refused by Jina Reader", "The site served a page curio can't use, " +
		"and Jina Reader refused to fetch it: the site opted out of Jina, or Jina rejected the request.", "ban", "danger"},
	store.FailureCauseTLS: {"Certificate problem", "The site's certificate failed verification: " +
		"expired, for another name, or from an unknown issuer.", "shield-x", "danger"},
	store.FailureCauseUnreachable: {"Host unreachable", "The host's name doesn't resolve, " +
		"or it refuses connections. Often a site that no longer exists.", "wifi-off", "danger"},
	store.FailureCauseTimeout: {"Timed out", "The site, or the tool fetching it, took too long to answer. " +
		"A refetch later often works.", "hourglass", "warn"},
	store.FailureCauseNetwork: {"Connection problem", "The connection failed: a reset, a TLS alert, " +
		"a redirect loop, or an answer cut short. A refetch later often works.", "zap-off", "warn"},
	store.FailureCauseRateLimited: {"Rate limited", "The site, GitHub, YouTube or Jina Reader asked curio to slow down. " +
		"A refetch later usually works.", "gauge", "warn"},
	store.FailureCauseHTTPError: {"HTTP error", "The site answered with an error status curio doesn't retry, " +
		"like 400 or 401.", "server", "danger"},
	store.FailureCauseUnsupported: {"Can't read this kind of page", "curio can't read this URL or its content: " +
		"a channel page, a GitHub profile, a file that isn't HTML, or a PDF it can't extract.", "file-x", "neutral"},
	store.FailureCauseTooLarge: {"Too large", "The answer was over the 32 MiB curio reads of a page.",
		"weight", "neutral"},
	store.FailureCauseIndex: {"Index failed", "The page was fetched, but indexing it gave up, " +
		"so search leaves it out.", "database", "danger"},
	store.FailureCauseOther: {"Other", "Something else went wrong; the full error says what.",
		"alert-circle", "neutral"},
}

// causeLabel names a failure cause: "" for none, and the code itself for a
// cause this build has no words for.
func causeLabel(cause string) string {
	if text, ok := causes[store.FailureCause(cause)]; ok {
		return text.label
	}
	return cause
}

// causeWhy is a sentence on what a failure cause means for one document,
// or "" for none or a cause this build has no words for.
func causeWhy(cause string) string { return causes[store.FailureCause(cause)].why }

// causeIcon is the icon of a failure cause's card: alert-circle for none,
// or for a cause this build doesn't know.
func causeIcon(cause string) string {
	if text, ok := causes[store.FailureCause(cause)]; ok {
		return text.icon
	}
	return "alert-circle"
}

// causeTone is the tone a failure cause's card is drawn in, a class of its
// icon: neutral for none, or for a cause this build doesn't know.
func causeTone(cause string) string {
	if text, ok := causes[store.FailureCause(cause)]; ok {
		return text.tone
	}
	return "neutral"
}
