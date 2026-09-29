package ui

import "github.com/samsar/curio/internal/store"

// causeText is what a page says of a failure cause: a label, and a
// sentence on what it means for one document.
type causeText struct {
	label, why string
}

// causes are the words for every store.FailureCause (TestCauses checks
// that each has them).
var causes = map[store.FailureCause]causeText{
	store.FailureCauseDeadLink: {"Dead link", "The page is gone: a 404 or 410, a “not found” page, " +
		"or a redirect onto a site's front page. A refetch helps only if it came back."},
	store.FailureCauseAntiBot: {"Blocked by bot protection", "The site blocked curio's request: " +
		"a 403 or 503, or a bot check or block page."},
	store.FailureCauseLoginWall: {"Behind a login", "The site answered with a sign-in page, " +
		"a redirect onto one, or too little text to be the article."},
	store.FailureCauseJinaRefused: {"Refused by Jina Reader", "The site served a page curio can't use, " +
		"and Jina Reader refused to fetch it: the site opted out, or Jina is limiting it."},
	store.FailureCauseTLS: {"Certificate problem", "The site's certificate failed verification: " +
		"expired, for another name, or from an unknown issuer."},
	store.FailureCauseUnreachable: {"Host unreachable", "The host's name doesn't resolve, " +
		"or it refuses connections. Often a site that no longer exists."},
	store.FailureCauseTimeout: {"Timed out", "The site, or the tool fetching it, took too long to answer. " +
		"A refetch later often works."},
	store.FailureCauseNetwork: {"Connection problem", "The connection failed: a reset, a TLS alert, " +
		"a redirect loop, or an answer cut short. A refetch later often works."},
	store.FailureCauseRateLimited: {"Rate limited", "The site, GitHub or YouTube asked curio to slow down. " +
		"A refetch later usually works."},
	store.FailureCauseHTTPError: {"HTTP error", "The site answered with an error status curio doesn't retry, " +
		"like 400 or 401."},
	store.FailureCauseUnsupported: {"Can't read this kind of page", "curio can't read this URL or its content: " +
		"a channel page, a GitHub profile, a file that isn't HTML, or a PDF it can't extract."},
	store.FailureCauseTooLarge: {"Too large", "The answer was over the 32 MiB curio reads of a page."},
	store.FailureCauseIndex: {"Index failed", "The page was fetched, but indexing it gave up, " +
		"so search leaves it out."},
	store.FailureCauseOther: {"Other", "Something else went wrong; the full error says what."},
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
