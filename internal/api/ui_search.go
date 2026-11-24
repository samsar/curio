package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/modelgate"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/retrieve"
	"github.com/samsar/curio/internal/starprompt"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
	"github.com/samsar/curio/internal/understand"
)

// search answers GET /ui/?q=&content_type=&page=, the dashboard's home:
// the form, and for a query, a page of the results of the same search POST
// /v1/search runs, ui.SearchPageSize of them from the page's offset,
// limited to the content type when one is given. A search that fails is
// shown where its results would be, with its status. A type that isn't
// one is a 400 page, as the Library answers it, and so is a page no search
// can have, before anything is searched; without a query the page is
// ignored. A page past this search's last is its answer, a 200 with the
// out-of-range card: the page's status is its search's, and the search
// answered, as POST /v1/search answers an offset past its total.
//
// Search as you type renders the whole page on every keystroke, so with a
// query the page reads nothing but the search; without one it reads what
// the home shows instead (home). A keystroke never asks for understanding:
// only ?understand=1, which Enter sends, asks the planner, and ?plan=, a
// token of the plan an understood page searched by, searches by it again
// without asking (planParams). Asked from htmx, an understood page's
// answer says which address to push: its plan's, or its words' when it
// went without one, so that reload, back and forward never plan again.
// A submitted search's first page counts toward the one ask for a star on
// GitHub, and shows it under its results when it is the one (ui.StarAsk).
//
// An understood page sends the reranker its head once it is shown, in a
// follow-up of its own (ui.Reorder), so its results never wait on a
// second model: only the page whose own request asked the planner, and
// got a plan the reranker would reorder (reorder). The follow-up is the
// page again with ?rerank=1, which asks the reranker only for the
// dashboard's or the person's own request (selfInitiated), and its answer
// swaps the reordered ranking in, replacing the page's address with the
// order's (answerFollowUp). ?order=, a token of that order's IDs, lays it
// over the plan's ranking again asking no model, on the page's other pages
// and on a reload (rerankParams).
//
// A browser's guess ahead of a navigation, a prefetch or a prerender, is
// declined when it would ask a model (declineSpeculation), before anything
// is read: the address bar's prerender of an understood address would
// plan, and the page it rendered would rerank, while the person was only
// typing.
//
// A whole page (never htmx's requests, whose answer is swapped into the
// results alone) reads what the asker knows of its model, from memory, for
// the switch to Ask mode. ?mode=ask opens the page in Ask mode, its
// question in the box and nothing read for it: only Enter asks
// (static/ask.js), so no address, reloaded or another site's, makes the
// writer's model load. A page that can't ask searches the words instead,
// and says why.
func (h pageHandlers) search(w http.ResponseWriter, r *http.Request) {
	began := time.Now()
	if speculative(r) && asksModel(r) {
		h.declineSpeculation(w, r)
		return
	}
	contentType, err := contentTypeParam(r)
	if err == nil {
		err = modeParam(r)
	}
	if err != nil {
		h.writePageError(w, r, err, ui.NavSearch)
		return
	}
	q := r.URL.Query().Get("q")
	vm := ui.Search{Layout: h.pages.layout("Search", ui.NavSearch), Query: q, Type: string(contentType),
		Asked: r.URL.Query().Get(ui.ModeParam) == ui.ModeAsk}
	htmx := r.Header.Get("Hx-Request") == "true"
	if !htmx {
		vm.Asking = h.asking()
	}
	if vm.Asked && (htmx || vm.CanAsk()) {
		vm.Mode = ui.ModeAsk
	}
	status := http.StatusOK
	if strings.TrimSpace(q) == "" {
		vm.Home = h.home(r)
		h.page(w, r, status, ui.PageSearch, vm)
		return
	}
	if vm.AskMode() {
		h.page(w, r, status, ui.PageSearch, vm)
		return
	}
	if vm.Page, err = searchPageParam(r); err != nil {
		h.writePageError(w, r, err, ui.NavSearch)
		return
	}
	req := SearchRequest{Query: q, K: ui.SearchPageSize, Offset: ui.PageOffset(vm.Page, ui.SearchPageSize)}
	if contentType != "" {
		req.Filters.ContentType = []string{string(contentType)}
	}
	var rerankAsked bool
	if req.Understand, req.Plan, err = planParams(r); err == nil {
		rerankAsked, req.RerankOrder, vm.Order, err = rerankParams(r)
	}
	if err != nil {
		h.writePageError(w, r, err, ui.NavSearch)
		return
	}
	req.Rerank = rerankAsked && selfInitiated(r)
	// A submitted search is the person's (Enter, the Search button, or the
	// same address typed or reloaded): its first page counts toward the one
	// ask for a star. A keystroke's, a later page's, a plan's, an order's
	// and the follow-up's don't.
	req.Interactive, req.surface = req.Understand && vm.Page == 1, starprompt.SurfaceDashboard
	resp, err := h.d.search(r.Context(), req)
	if err == nil {
		vm.Results = searchResults(resp)
		vm.Plan, vm.Understood, err = understoodView(resp)
		if resp.StarPrompt {
			vm.Star = ui.NewStarAsk()
		}
	}
	if err == nil {
		vm.Order, vm.Reordered, err = h.reorderedView(resp, vm.Order)
	}
	if rerankAsked && htmx {
		h.answerFollowUp(w, r, vm, err, began)
		return
	}
	if err == nil && req.Understand {
		vm.Reorder, vm.Reordered = h.reorder(q, req.Offset, resp)
	}
	if vm.Understood != nil && h.d.Planner != nil {
		vm.Understood.Timeout = h.d.Planner.Timeout()
	}
	if err != nil {
		status, vm.Err = h.reportPanel(r, err)
	}
	if (req.Understand || req.Plan != nil) && htmx {
		w.Header().Set("HX-Push-Url", vm.Address())
	}
	h.page(w, r, status, ui.PageSearch, vm)
}

// rerankParams reads how the head of the search page's plan's ranking is
// to be ordered: ?rerank=1, asking the reranker, or ?order=, a token of
// the order a reranked page returned (encodeOrder), which asks no model;
// one or neither, either with the plan its first page searched by and
// never with understanding, which is that first page's. It returns
// whether rerank=1 was sent, and the order sent and its token. Anything
// else is a requestError, refused before anything is planned or searched.
// Whether rerank=1 asks the reranker is selfInitiated's to say.
func rerankParams(r *http.Request) (bool, []string, string, error) {
	query := r.URL.Query()
	asked, ordered := query.Has(ui.RerankParam), query.Has(ui.OrderParam)
	switch {
	case !asked && !ordered:
		return false, nil, "", nil
	case asked && query.Get(ui.RerankParam) != "1":
		return false, nil, "", badRequest("%s %q must be 1", ui.RerankParam, query.Get(ui.RerankParam))
	case asked && ordered:
		return false, nil, "", badRequest("send %s or %s, not both", ui.RerankParam, ui.OrderParam)
	case query.Has("understand"):
		return false, nil, "", badRequest("send %s or %s with the plan the first page searched by, not "+
			"understand", ui.RerankParam, ui.OrderParam)
	case query.Get(ui.PlanParam) == "":
		return false, nil, "", badRequest("%s and %s reorder a plan's ranking: send the plan its first page "+
			"searched by (%s)", ui.RerankParam, ui.OrderParam, ui.PlanParam)
	case asked:
		return true, nil, "", nil
	}
	token := query.Get(ui.OrderParam)
	order, err := decodeOrder(token)
	return false, order, token, err
}

// reorderedView is how the head of resp's ranking was ordered, as its
// page says it, and the token of the order the page's other pages keep:
// the reranker's order; one supplied, whose token, the one the page was
// searched by, is kept as it came; or, without an order, why the reranker
// kept curio's, for a model that failed its check or a reranker resting
// after timeouts or evictions, which the page says until it is fixed or
// passes. Any other outcome says nothing: a rerank skipped, or one that
// didn't come, leaves the first ranking as it was.
func (h pageHandlers) reorderedView(resp SearchResponse, token string) (string, *ui.Reordered, error) {
	rr := resp.Reranking
	if rr == nil {
		return "", nil, nil
	}
	switch rerank.Status(rr.Status) {
	case rerank.StatusReranked:
		order, err := encodeOrder(rr.Order)
		return order, &ui.Reordered{Status: ui.ReorderedByModel, Model: rr.Model}, err
	case rerank.StatusSupplied:
		return token, &ui.Reordered{Status: ui.ReorderedAsFirst, Stale: rr.Stale}, nil
	case rerank.StatusUnfit:
		return "", &ui.Reordered{Status: ui.ReorderedUnfit, Model: h.rerankModel(rr.Model)}, nil
	case rerank.StatusTimeout, rerank.StatusUnavailable:
		if h.d.Reranker == nil {
			break
		}
		if until := heldUntil(h.d.Reranker.State()); !until.IsZero() {
			return "", &ui.Reordered{Status: ui.ReorderedHeld, HeldUntil: until}, nil
		}
	case rerank.StatusSkipped, rerank.StatusOff, rerank.StatusCold, rerank.StatusChecking, rerank.StatusInvalid,
		rerank.StatusTooLong:
	}
	return "", nil, nil
}

// rerankModel is model, the one a rerank's outcome named, or else the
// reranker's.
func (h pageHandlers) rerankModel(model string) string {
	if model == "" && h.d.Reranker != nil {
		return h.d.Reranker.State().Model
	}
	return model
}

// heldUntil is when the later of the reranker's holds, s says, ends: the
// zero time when none holds.
func heldUntil(s rerank.State) time.Time {
	until := s.TimeoutHoldUntil
	if s.EvictionHoldUntil.After(until) {
		until = s.EvictionHoldUntil
	}
	if !until.After(time.Now()) {
		return time.Time{}
	}
	return until
}

// reorder is the follow-up of the page a request that asked the planner
// itself (understand=1, the dashboard's or the person's) answers, nil when
// there is none to send: its plan's ranking has a head of two documents or
// more, the reranker would reorder it, as POST /v1/search decides, from
// memory, before it searches, and this page, at offset in the ranking,
// holds some of it. A later page past the head would ask the reranker for
// an order nothing on it shows, and says nothing of reordering. Every other
// page, a keystroke's, another site's link's, a plan's and an order's,
// has none, so nothing elsewhere can load the reranker's model.
//
// A reranker whose model failed its check, or that rests after timeouts
// or evictions, would answer the follow-up with a note alone, after the
// search ran again for it: the page says so at once instead, from the
// reranker's memory (the follow-up's own note stays, for a state that
// changes between the two), and sends none.
func (h pageHandlers) reorder(q string, offset int, resp SearchResponse) (*ui.Reorder, *ui.Reordered) {
	u := resp.Understanding
	if h.d.Reranker == nil || u == nil || u.Status != string(understand.StatusPlanned) || u.Plan == nil ||
		len(resp.Items) == 0 || resp.Total < 2 {
		return nil, nil
	}
	// A skip is a head of none, which no page holds.
	if depth, _ := h.d.Reranker.Decide(q, understand.Kind(u.Plan.Kind), true); offset >= depth {
		return nil, nil
	}
	s := h.d.Reranker.State()
	if s.Check == rerank.CheckUnfit {
		return nil, &ui.Reordered{Status: ui.ReorderedUnfit, Model: s.Model}
	}
	if until := heldUntil(s); !until.IsZero() {
		return nil, &ui.Reordered{Status: ui.ReorderedHeld, HeldUntil: until}
	}
	return &ui.Reorder{Model: s.Model}, nil
}

// How a follow-up ended, as its log line says it.
const (
	followUpReranked = "reranked" // the page reordered, swapped in
	followUpKept     = "kept"     // curio's order kept with nothing to say: a 204
	followUpNote     = "note"     // curio's order kept, and the note why
	followUpFailed   = "failed"   // the search failed (a 5xx, logged at ERROR) or was refused (a 4xx): a 204
	followUpAborted  = "aborted"  // the page aborted it (a keystroke, Enter, a page, Ask) or was left: a 499
)

// answerFollowUp answers an understood page's follow-up, which htmx sent
// to have the page's head reordered: the page reordered, whose #ranking
// htmx swaps in and whose address replaces the page's in history, so a
// search is one entry and Back never lands on its first ranking; for a
// model that failed its check or a reranker resting, the note alone, in
// the slot the follow-up holds (HX-Retarget, HX-Reselect); and a 204,
// nothing to swap, for anything else, the first ranking staying as it is.
// A search that fails is answered with a 204 too: the follow-up is the
// page's own, and its first ranking stands. Its error is logged as every
// page's is, at ERROR for the daemon's own failure (a 5xx); a refusal (a
// 4xx), which a follow-up the page sent never earns, since it carries
// what the page was searched by, is said by the follow-up's line alone,
// without the error, which can quote the plan. One the page aborted, or
// left, is answered with the 499 the access line then records: the person
// moved on, and nothing of the daemon's failed.
//
// Each follow-up logs one line at DEBUG, beside its access line, which
// alone says nothing of it but its path: how it ended and how long it
// took since it was asked, never its query, plan or order.
func (h pageHandlers) answerFollowUp(w http.ResponseWriter, r *http.Request, vm ui.Search, err error,
	began time.Time) {
	outcome := followUpKept
	vm.FollowUp = true
	defer func() {
		h.d.Log.Debug("search page: follow-up", "request_id", middleware.GetReqID(r.Context()), "status", outcome,
			"took_ms", time.Since(began).Milliseconds())
	}()
	switch {
	case r.Context().Err() != nil:
		outcome = followUpAborted
		w.WriteHeader(statusClientClosedRequest)
		return
	case err != nil:
		outcome = followUpFailed
		h.quietError(r, err)
	case vm.Reordered == nil:
	case vm.Reordered.Status == ui.ReorderedByModel:
		outcome = followUpReranked
		w.Header().Set("HX-Replace-Url", vm.Address())
		h.page(w, r, http.StatusOK, ui.PageSearch, vm)
		return
	case vm.Reordered.Status == ui.ReorderedUnfit, vm.Reordered.Status == ui.ReorderedHeld:
		outcome = followUpNote
		w.Header().Set("HX-Retarget", "#rerank-status")
		w.Header().Set("HX-Reselect", "#rerank-status")
		w.Header().Set("HX-Reswap", "outerHTML")
		h.page(w, r, http.StatusOK, ui.PageSearch, vm)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// modeParam checks the search page's ?mode: ui.ModeSearch, the default,
// or ui.ModeAsk, which takes the question (q) alone, since an address in
// Ask mode searches nothing and asks nothing as it loads: a parameter of a
// search's would be one it drops. Anything else is a requestError, before
// anything is read.
func modeParam(r *http.Request) error {
	query := r.URL.Query()
	switch mode := query.Get(ui.ModeParam); mode {
	case "", ui.ModeSearch:
		return nil
	case ui.ModeAsk:
		for _, p := range []string{"understand", ui.PlanParam, ui.RerankParam, ui.OrderParam, ui.PageParam,
			"content_type"} {
			if query.Has(p) {
				return badRequest("%s %s takes q alone, not %s", ui.ModeParam, ui.ModeAsk, p)
			}
		}
		return nil
	default:
		return badRequest("%s %q must be %s or %s", ui.ModeParam, mode, ui.ModeSearch, ui.ModeAsk)
	}
}

// asking is what the search page knows of Ask, read from the asker's
// memory alone (ask.State): whether a question can be asked from it, or
// why not, in the kinds the page words itself, each from a fact of the
// state's, never from the asker's reason. A hold or a model not checked
// yet still asks, and its answer says why it didn't come; so does a model
// that isn't pulled, which the asker learns of only by asking, and so
// would a refusal no fact of the state's names, whose ask ends off, in
// words that name no cause.
func (h pageHandlers) asking() *ui.Asking {
	a := &ui.Asking{Off: ui.AskOffSet, MaxQuestion: ask.MaxQuestionBytes}
	for _, s := range ask.Statuses() {
		a.Statuses = append(a.Statuses, string(s))
	}
	if h.d.Asker == nil {
		return a
	}
	s := h.d.Asker.State()
	a.Model, a.Timeout = s.Model, h.d.Asker.Timeout()
	switch {
	case !s.On && s.OffByDefault:
		a.Off = ui.AskOffByDefault
	case !s.On:
	case s.Withheld:
		a.Off = ui.AskWithheld
	case s.NoStream:
		a.Off = ui.AskNoStream
	case s.Check == modelgate.CheckUnfit:
		a.Off = ui.AskUnfit
	default:
		a.Off = ui.AskCan
	}
	return a
}

// maxPlanParam is the longest ?plan the search page takes. A planner's plan
// at its caps, three rewrites and four names each of characters JSON
// writes twice as long (a quote, a backslash; understand refuses control
// characters, which it writes six times as long), with every filter the
// planner sets and include_undated, is 2,079 bytes of JSON (encodePlan
// leaves &, < and > as they are), 2,772 as base64.
const maxPlanParam = 4 << 10

// planParams reads how the search page's query is to be understood:
// ?understand=1, or ?plan=, a token encodePlan made; one or neither. A
// token that doesn't decode to a plan understand.Validate takes, a value
// of understand but 1, or both, is a requestError, refused before
// anything is searched. ?understand=1 asks the planner only from the
// dashboard itself or the user's own navigation (selfInitiated): another
// site's link to the page searches its words, so no page elsewhere can
// load a writing model of gigabytes for its keep-alive. A plan asks no
// model, wherever it comes from.
func planParams(r *http.Request) (bool, *PlanJSON, error) {
	query := r.URL.Query()
	asked, token := query.Get("understand"), query.Get(ui.PlanParam)
	switch {
	case asked != "" && asked != "1":
		return false, nil, badRequest("understand %q must be 1", asked)
	case asked != "" && token != "":
		return false, nil, badRequest("send understand or %s, not both", ui.PlanParam)
	case token != "":
		p, err := decodePlan(token)
		return false, p, err
	}
	return asked == "1" && selfInitiated(r), nil, nil
}

// speculative reports whether r is a browser's guess ahead of a
// navigation the person may make: Sec-Purpose, which Chrome sends with
// its prefetches ("prefetch") and prerenders ("prefetch;prerender", the
// address bar's among them), and the older headers of a link's prefetch
// (Purpose, Firefox's X-Moz) and Safari's previews (X-Purpose).
func speculative(r *http.Request) bool {
	h := r.Header
	return h.Get("Sec-Purpose") != "" || strings.Contains(strings.ToLower(h.Get("Purpose")), "prefetch") ||
		strings.Contains(strings.ToLower(h.Get("X-Moz")), "prefetch") ||
		strings.Contains(strings.ToLower(h.Get("X-Purpose")), "preview")
}

// asksModel reports whether the search page r asks for would ask a model:
// the planner (understand) or the reranker (rerank), whatever the value,
// which is refused later if it isn't 1.
func asksModel(r *http.Request) bool {
	query := r.URL.Query()
	return query.Has("understand") || query.Has(ui.RerankParam)
}

// declineSpeculation answers a browser's guess at a page that would ask a
// model with a 503 never stored, having read nothing: Chrome discards a
// prefetch, and cancels a prerender, whose answer isn't a 2xx, and sends
// the request again when the person commits to the navigation, which is
// then theirs. A 204, or a 205, would be a prefetch's valid answer, kept
// for the navigation, which would then show nothing.
func (h pageHandlers) declineSpeculation(w http.ResponseWriter, r *http.Request) {
	h.d.Log.Debug("search page: a browser's guess declined", "request_id", middleware.GetReqID(r.Context()))
	w.Header().Set("Cache-Control", "no-store")
	h.errorPage(w, r, http.StatusServiceUnavailable, "not prefetched",
		"curio asks its models only for a search you make, never for a browser's guess at one.", ui.NavSearch)
}

// selfInitiated reports whether r came from the dashboard's own pages or
// from the user, by what the browser says in Sec-Fetch-Site: same-origin;
// none, a navigation no page made (an address typed, a bookmark, a link
// opened from another app such as mail, chat or a terminal); or nothing,
// a client that isn't a browser, a local process the API trusts, or a
// Safari before 16.4, which sends no Sec-Fetch headers. No website can
// produce none or leave the header out of a modern browser's request. A
// cross-site or same-site request, which isolateDashboard lets through
// when it is a top-level navigation, didn't come from either.
func selfInitiated(r *http.Request) bool {
	sites := r.Header.Values("Sec-Fetch-Site")
	return len(sites) == 0 || (len(sites) == 1 && (sites[0] == "same-origin" || sites[0] == "none"))
}

// encodePlan is p as a search page's ?plan: its JSON, base64url without
// padding, opaque to the page (encodeToken).
func encodePlan(p PlanJSON) (string, error) {
	token, err := encodeToken(p)
	if err != nil {
		return "", fmt.Errorf("encode the plan: %w", err)
	}
	return token, nil
}

// decodePlan reads a ?plan token, strictly: at most maxPlanParam bytes of
// base64url, one JSON plan of the API's shape and no unknown key, and a
// plan understand.Validate takes. Anything else is a requestError.
func decodePlan(token string) (*PlanJSON, error) {
	var p PlanJSON
	if err := decodeToken(token, ui.PlanParam, "a plan", maxPlanParam, &p); err != nil {
		return nil, err
	}
	if err := understand.Validate(p.plan()); err != nil {
		return nil, &requestError{err: err}
	}
	return &p, nil
}

// maxOrderParam is the longest ?order the search page takes. An order is
// the reranked head's document IDs, at most rerank.MaxDepth (30): thirty
// UUIDs are 1,171 bytes of JSON, 1,562 as base64, and the cap leaves room
// for longer IDs, up to retrieve's 64 bytes each.
const maxOrderParam = 4 << 10

// encodeOrder is order, the reranked head's document IDs, as a search
// page's ?order: never positions or a key the daemon keeps, so a page
// searched by it lays the head the reranker gave over the same ranking
// again, and asks no model (docs/decisions.md "Paging by the order, not a
// cache").
func encodeOrder(order []string) (string, error) {
	token, err := encodeToken(order)
	if err != nil {
		return "", fmt.Errorf("encode the order: %w", err)
	}
	return token, nil
}

// decodeOrder reads an ?order token, strictly: at most maxOrderParam bytes
// of base64url, one JSON list of strings, and an order
// retrieve.CheckOrder takes (1 to 30 distinct IDs of at most 64 bytes, no
// control character). Anything else is a requestError.
func decodeOrder(token string) ([]string, error) {
	var order []string
	if err := decodeToken(token, ui.OrderParam, "an order", maxOrderParam, &order); err != nil {
		return nil, err
	}
	if order == nil {
		return nil, badRequest("%s isn't an order this page made: null", ui.OrderParam)
	}
	if err := retrieve.CheckOrder(false, false, order); err != nil {
		return nil, &requestError{err: fmt.Errorf("%s: %w", ui.OrderParam, err)}
	}
	return order, nil
}

// encodeToken is v's JSON as base64url without padding: an opaque token a
// search page's link carries. The JSON leaves &, < and > as they are:
// escaped for HTML, each is six bytes, and a plan of them outgrows
// maxPlanParam.
func encodeToken(v any) (string, error) {
	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(raw.Bytes(), []byte("\n"))), nil
}

// decodeToken reads token, the value of the search page's parameter param,
// into v, strictly: at most limit bytes of base64url, then exactly one
// JSON value of v's shape, with no key v lacks. Anything else is a
// requestError saying it isn't what (a plan, an order) this page made.
func decodeToken(token, param, what string, limit int, v any) error {
	if len(token) > limit {
		return badRequest("%s is %d bytes, at most %d", param, len(token), limit)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return badRequest("%s isn't %s this page made: %w", param, what, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("%s isn't %s this page made: %w", param, what, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return badRequest("%s isn't %s this page made: more than one value", param, what)
	}
	return nil
}

// understoodView is how resp's query was understood, as the search page
// shows it, and the token of the plan it was searched by (empty without
// one): each of the plan's filters with the token of the plan without it,
// and the token of the plan with its undated documents switched. Nil for
// a search that didn't ask.
func understoodView(resp SearchResponse) (string, *ui.Understood, error) {
	u := resp.Understanding
	if u == nil {
		return "", nil, nil
	}
	view := &ui.Understood{Status: u.Status, Model: u.Model}
	if u.Plan == nil {
		return "", view, nil
	}
	p := *u.Plan
	token, err := encodePlan(p)
	if err != nil {
		return "", nil, err
	}
	view.Rewrites, view.Names = p.Rewrites, p.Names
	view.Undated, view.UndatedKept = resp.Undated, p.Filters.IncludeUndated
	for _, f := range planFilters(p.Filters) {
		without := p
		without.Filters = filtersOf(withoutDimension(p.Filters.request(), f.Field))
		if f.Token, err = encodePlan(without); err != nil {
			return "", nil, err
		}
		view.Filters = append(view.Filters, f)
	}
	if view.Undated != nil {
		switched := p
		switched.Filters.IncludeUndated = !p.Filters.IncludeUndated
		if view.UndatedToken, err = encodePlan(switched); err != nil {
			return "", nil, err
		}
	}
	return token, view, nil
}

// planFilters are the dimensions f sets, in a fixed order, as the search
// page's chips show them.
func planFilters(f Filters) []ui.PlanFilter {
	var out []ui.PlanFilter
	for _, l := range []struct {
		field  string
		values []string
	}{
		{"content_type", f.ContentType}, {"source", f.Source}, {"site", f.Site}, {"host", f.Host},
		{"folder", f.Folder},
	} {
		if len(l.values) > 0 {
			out = append(out, ui.PlanFilter{Field: l.field, Values: l.values})
		}
	}
	if f.SavedAfter != "" || f.SavedBefore != "" {
		out = append(out, ui.PlanFilter{Field: "saved", After: f.SavedAfter, Before: f.SavedBefore})
	}
	if f.PublishedAfter != "" || f.PublishedBefore != "" {
		out = append(out, ui.PlanFilter{Field: "published", After: f.PublishedAfter, Before: f.PublishedBefore})
	}
	return out
}

// searchPageParam reads the search page's ?page (ui.PageParam) as pageParam
// reads a numbered list's, and refuses a page past ui.MaxSearchPages as
// well: a search ranks store.MaxSearchK documents at most, so no search has
// one, and the page's offset would be one POST /v1/search refuses.
func searchPageParam(r *http.Request) (int, error) {
	s := r.URL.Query().Get(ui.PageParam)
	if s == "" {
		return 1, nil
	}
	page, err := strconv.Atoi(s)
	if err != nil || page < 1 || page > ui.MaxSearchPages {
		return 0, badRequest("%s %q must be a whole number from 1 to %d", ui.PageParam, s, ui.MaxSearchPages)
	}
	return page, nil
}

// home is what the search page shows without a query: how many documents
// there are to search, for the box's placeholder, and the latest rebuild's
// largest top-level groups (areas, or interests in the flat shape),
// without their interests' members. Each read the home does without when
// it fails: it is logged, and what it was for is left out.
func (h pageHandlers) home(r *http.Request) *ui.SearchHome {
	ctx := r.Context()
	home := &ui.SearchHome{}
	if st, err := h.d.stats(ctx); err != nil {
		h.quietError(r, err)
	} else {
		home.Searchable = st.DocumentsByState[string(store.DocStateFetched)]
	}
	interests, err := h.d.interests(ctx, interestsOpts{Limit: homeInterests, Bare: true})
	if err != nil {
		h.quietError(r, err)
		return home
	}
	home.AllInterests = interests.Total
	for _, in := range interests.Items {
		home.Interests = append(home.Interests, interestView(in))
	}
	return home
}

func searchResults(resp SearchResponse) *ui.SearchResults {
	res := &ui.SearchResults{Degraded: resp.Degraded, Warnings: resp.Warnings, TookMS: resp.TookMS,
		Total: resp.Total, Capped: resp.Capped,
		WordHits: resp.BM25Hits + resp.FieldsHits + resp.TitleHits + resp.SummaryHits}
	for _, item := range resp.Items {
		hit := ui.SearchHit{DocumentID: item.Document.ID, Title: deref(item.Document.Title),
			BookmarkTitle: strings.TrimSpace(item.BookmarkTitle), URL: item.Document.URL,
			ContentType: item.Document.ContentType, State: item.Document.State, Score: item.Score}
		for _, m := range item.Matches {
			hit.Matches = append(hit.Matches, ui.Match{Segments: ui.Passage(m.Snippet, m.Text), BM25: m.BM25Score,
				Vector: m.VectorScore})
		}
		for _, b := range item.Bookmarks {
			hit.Bookmarks = append(hit.Bookmarks, ui.SavedBookmark{Title: b.Title, FolderPath: b.FolderPath,
				Source: b.Source})
		}
		res.Hits = append(res.Hits, hit)
	}
	return res
}
