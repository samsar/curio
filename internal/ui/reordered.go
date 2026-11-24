package ui

import "time"

// Reorder is the follow-up an understood search's first ranking sends as
// soon as it is shown: a request of its own page with rerank=1, whose
// answer swaps in the head reordered (#ranking), so the results never wait
// for the reranker. Model is the reranker's, which its pending line names.
type Reorder struct {
	Model string
}

// ReorderHref is the follow-up's request: the page's own window of its
// plan's ranking, reordered by the reranker; never understand=1, so the
// planner isn't asked again.
func (s Search) ReorderHref() string { return rerankHref(s.Query, s.Type, s.page(), s.Plan) }

// The ways a page says its head was reordered, or why it wasn't, by
// Reordered.Status.
const (
	ReorderedByModel = "reranked" // the reranker's order
	ReorderedAsFirst = "supplied" // the order a first page returned, a later page's or a reload's
	ReorderedUnfit   = "unfit"    // the reranker's model failed its check
	ReorderedHeld    = "held"     // reranks keep timing out, or Ollama keeps unloading the model
)

// Reordered is how a search's page says its head was reordered, or why it
// kept curio's order where that is worth saying: the reranker's model, how
// many of a supplied order's documents had left the head (Stale), and, for
// a hold, when reranking resumes.
type Reordered struct {
	Status    string
	Model     string
	Stale     int
	HeldUntil time.Time
}

// Note is what the page says, from the status alone, never the daemon's
// reason; "" for a status it doesn't word.
func (r Reordered) Note() string {
	model := r.Model
	if model == "" {
		model = "the reranker's model"
	}
	switch r.Status {
	case ReorderedByModel:
		return "Reordered by " + model
	case ReorderedAsFirst:
		if r.Stale > 0 {
			return "Reordered as the first page was · " + count(r.Stale, "document has", "documents have") +
				" left the top since"
		}
		return "Reordered as the first page was"
	case ReorderedUnfit:
		return "Kept curio's order: " + model + " didn't pass curio's check for reordering results; set " +
			"generation.roles.reranker.model to a model that passes, or search.rerank: false."
	case ReorderedHeld:
		return "Kept curio's order: reordering resumes at " + r.HeldUntil.Local().Format(time.Kitchen) +
			"; curio doctor says why."
	}
	return ""
}

// ReorderSaid is what #rerank-said, the search page's status outside its
// results, announces: on the follow-up's answer, which swaps the head in
// silently, how it was reordered or why it wasn't (Reordered.Note); ""
// on every other page, whose own load or swap shows its order.
func (s Search) ReorderSaid() string {
	if !s.FollowUp || s.Reordered == nil {
		return ""
	}
	return s.Reordered.Note()
}

// The ids of the elements a reordered head can hold focus on are the same
// in the first ranking and the reordered one, so that htmx gives focus
// back to the element that had it once #ranking is swapped: a result's by
// its document, the rest by what they are (the pager's and the
// out-of-range card's, pager.go). Focus on a result the reorder moved off
// the page, which no element on it has the id of, search.js gives to the
// title of the result now in its place.

// TitleID is the id of the result's title link.
func (h SearchHit) TitleID() string { return "result-" + h.DocumentID }

// OriginalID is the id of the link to a gone page's original address.
func (h SearchHit) OriginalID() string { return h.TitleID() + "-original" }

// MoreID is the id of the summary of the result's other passages.
func (h SearchHit) MoreID() string { return h.TitleID() + "-more" }
