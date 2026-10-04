package ui

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Every link a page makes is built here, whole, from the values it names:
// html/template leaves a path segment unescaped, and escapes a query
// string built in the template a second time.

// documentHref is a document's page.
func documentHref(id string) string { return "/ui/documents/" + url.PathEscape(id) }

// documentImagesHref is a document's page with its remote images.
func documentImagesHref(id string) string { return documentHref(id) + "?images=1" }

// interestHref is an interest's page.
func interestHref(id string) string { return "/ui/interests/" + url.PathEscape(id) }

// PageParam is the query parameter of a numbered list's page, from 1
// (pager.go).
const PageParam = "page"

// interestsPageHref is page (from 1) of the interests of the rebuild run:
// a page read from another run says so. The first page carries no number,
// and no run when run is "".
func interestsPageHref(page int, run string) string {
	return runPageHref(navHref(NavInterests), page, run)
}

// unsortedHref is page (from 1) of the rebuild run's unsorted documents,
// named as interestsPageHref names a page of its groups.
func unsortedHref(page int, run string) string {
	return runPageHref("/ui/interests/unsorted", page, run)
}

// runPageHref is page (from 1) at path of a list of the rebuild run's.
func runPageHref(path string, page int, run string) string {
	q := url.Values{}
	if page > 1 {
		q.Set(PageParam, strconv.Itoa(page))
	}
	if run != "" {
		q.Set(runParam, run)
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// changesHref is the page of what the latest rebuild changed.
func changesHref() string { return "/ui/interests/changes" }

// The interest map's views, as its view parameter names them: All
// documents, the default, which links leave out, and Zoom in.
const (
	MapViewAll  = "all"
	MapViewZoom = "zoom"
)

// What the interest map's select parameter names: kind:id for an area, an
// interest or a document, or Unsorted alone.
const (
	MapSelectArea     = "area"
	MapSelectInterest = "interest"
	MapSelectDocument = "document"
	MapSelectUnsorted = "unsorted"
)

// The interest map's query parameters.
const (
	mapViewParam   = "view"
	mapSelectParam = "select"
)

// maxMapSelectID is the longest ID a selection may name, in bytes: well
// over a UUID's 36, and short enough that no query is a page's worth.
const maxMapSelectID = 128

// MapQuery is what an interest map URL asks for: a view, MapViewAll or
// MapViewZoom, and what is selected in it, the zero MapSelection for the
// library.
type MapQuery struct {
	View   string
	Select MapSelection
}

// MapSelection is what the map has selected: an area, an interest or a
// document by ID, or Unsorted; the zero value is the library.
type MapSelection struct {
	Kind, ID string
}

// String is the selection as the select parameter writes it: kind:id,
// unsorted, or "" for the library.
func (s MapSelection) String() string {
	switch s.Kind {
	case "":
		return ""
	case MapSelectUnsorted:
		return s.Kind
	}
	return s.Kind + ":" + s.ID
}

// mapHref is the interest map showing q: the default view and the library
// are left out, so the plain map is /ui/interests/map. static/map.js keeps
// its address in this form as the view and selection change.
func mapHref(q MapQuery) string {
	const path = "/ui/interests/map"
	v := url.Values{}
	if sel := q.Select.String(); sel != "" {
		v.Set(mapSelectParam, sel)
	}
	if q.View != "" && q.View != MapViewAll {
		v.Set(mapViewParam, q.View)
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

// ParseMapQuery reads the interest map's query, as mapHref writes it: no
// view is All documents, and no selection the library. A view or a
// selection of another form is an error naming the forms there are,
// answered before the page reads anything. A selection's ID is everything
// after its first colon, so an ID may hold colons of its own.
func ParseMapQuery(q url.Values) (MapQuery, error) {
	out := MapQuery{View: MapViewAll}
	switch view := q.Get(mapViewParam); view {
	case "", MapViewAll:
	case MapViewZoom:
		out.View = view
	default:
		return MapQuery{}, fmt.Errorf("%s %q: want %s or %s", mapViewParam, view, MapViewAll, MapViewZoom)
	}
	sel := q.Get(mapSelectParam)
	if sel == "" {
		return out, nil
	}
	if sel == MapSelectUnsorted {
		out.Select = MapSelection{Kind: MapSelectUnsorted}
		return out, nil
	}
	kind, id, _ := strings.Cut(sel, ":")
	switch {
	case kind != MapSelectArea && kind != MapSelectInterest && kind != MapSelectDocument, id == "":
		return MapQuery{}, fmt.Errorf("%s %q: %s", mapSelectParam, sel, mapSelectForms)
	case len(id) > maxMapSelectID:
		return MapQuery{}, fmt.Errorf("%s: an ID of %d bytes, over the %d one may have: %s", mapSelectParam, len(id),
			maxMapSelectID, mapSelectForms)
	}
	out.Select = MapSelection{Kind: kind, ID: id}
	return out, nil
}

// mapSelectForms names the selections the map takes, for a refusal.
const mapSelectForms = "want " + MapSelectUnsorted + ", " + MapSelectArea + ":<id>, " + MapSelectInterest +
	":<id> or " + MapSelectDocument + ":<id>"

// interestPageHref is page (from 1) of interest id's members, or of an
// area's interests.
func interestPageHref(id string, page int) string {
	if page <= 1 {
		return interestHref(id)
	}
	return interestHref(id) + "?" + url.Values{PageParam: {strconv.Itoa(page)}}.Encode()
}

// PollParam is the query parameter that asks a page for its live regions
// alone, the ones a poller refreshes, rather than the whole page: its
// value names which, and the page reads only what those show. The access
// log knows a poll by it, too.
const PollParam = "poll"

// The live regions a poll asks for: Status's that change by the second
// (PollLive) and its health (PollHealth), a document's jobs (PollJobs),
// the Interests' rebuild (PollRebuild), and the Failures tab's groups with
// the Library's subnav (PollCauses).
const (
	PollLive    = "live"
	PollHealth  = "health"
	PollJobs    = "jobs"
	PollRebuild = "rebuild"
	PollCauses  = "causes"
)

// The baseline a poll carries: what its page showed when it was rendered,
// which the answer compares the library against.
const (
	updatedParam    = "updated"
	extractionParam = "extraction"
	runParam        = "run"
)

// pollTimeLayout writes a baseline's time: RFC 3339 in UTC to the
// millisecond, as the store keeps times, so it compares equal to the time
// it came from.
const pollTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// statusPollHref is Status's live regions of kind, PollLive or PollHealth.
func statusPollHref(kind string) string {
	return navHref(NavStatus) + "?" + url.Values{PollParam: {kind}}.Encode()
}

// DocumentBaseline is what a document's page shows of it, which its poll
// compares the library against: when the document was last updated, and
// its current extraction ("" for none).
type DocumentBaseline struct {
	Updated    time.Time
	Extraction string
}

// documentPollHref is the live regions of document id's page, whose
// baseline is b.
func documentPollHref(id string, b DocumentBaseline) string {
	return documentHref(id) + "?" + url.Values{PollParam: {PollJobs},
		updatedParam: {b.Updated.UTC().Format(pollTimeLayout)}, extractionParam: {b.Extraction}}.Encode()
}

// ParseDocumentBaseline reads the baseline a document poll carries, as
// documentPollHref wrote it. A poll without a time, or with one that isn't
// RFC 3339, has none.
func ParseDocumentBaseline(q url.Values) (DocumentBaseline, error) {
	s := q.Get(updatedParam)
	if s == "" {
		return DocumentBaseline{}, errors.New(PollJobs + " poll without its " + updatedParam + " time")
	}
	updated, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return DocumentBaseline{}, fmt.Errorf("%s %q: %w", updatedParam, s, err)
	}
	return DocumentBaseline{Updated: updated, Extraction: q.Get(extractionParam)}, nil
}

// interestsPollHref is the Interests' rebuild regions, for a page showing
// the rebuild shown ("" for none).
func interestsPollHref(shown string) string {
	return navHref(NavInterests) + "?" + url.Values{PollParam: {PollRebuild}, runParam: {shown}}.Encode()
}

// ShownRun reads the rebuild an Interests URL names, "" for none:
// for a poll, the run its page shows; for a page, the run the page it was
// reached from showed.
func ShownRun(q url.Values) string { return q.Get(runParam) }

// failuresHref is the Library's Failures tab.
func failuresHref() string { return "/ui/failures" }

// failuresPollHref is the Failures tab's live regions.
func failuresPollHref() string {
	return failuresHref() + "?" + url.Values{PollParam: {PollCauses}}.Encode()
}

// causeCardID is the id of cause's card on the Failures tab, which
// failureCauseHref leads to.
func causeCardID(cause string) string { return "cause-" + cause }

// failureCauseHref is cause's card on the Failures tab, its fragment
// escaped however odd the cause.
func failureCauseHref(cause string) string {
	return (&url.URL{Path: failuresHref(), Fragment: causeCardID(cause)}).String()
}

// searchHref is page, from 1, of the search page's results for q, limited
// to contentType when it is set; the first page names none. A blank q is
// the search home, which has no pages.
func searchHref(q, contentType string, page int) string {
	v := url.Values{}
	if strings.TrimSpace(q) != "" {
		v.Set("q", q)
		if page > 1 {
			v.Set(PageParam, strconv.Itoa(page))
		}
	}
	if contentType != "" {
		v.Set("content_type", contentType)
	}
	if len(v) == 0 {
		return "/ui/"
	}
	return "/ui/?" + v.Encode()
}

// libraryHref is the Library under f, from the page after cursor; an empty
// cursor is the first page.
func libraryHref(f LibraryFilters, cursor string) string {
	q := libraryQuery(f, cursor)
	if len(q) == 0 {
		return "/ui/library"
	}
	return "/ui/library?" + q.Encode()
}

// libraryMoreHref is libraryHref(f, cursor) as htmx's "load more" asks for
// it: htmx appends the next page's rows to the shown rows the page holds,
// and shown lets that page's Showing line count them too. A plain link to
// the next page shows its own rows alone, and never carries shown.
func libraryMoreHref(f LibraryFilters, cursor string, shown int) string {
	q := libraryQuery(f, cursor)
	q.Set("shown", strconv.Itoa(shown))
	return "/ui/library?" + q.Encode()
}

// libraryQuery is the query of the Library under f from the page after
// cursor: the order unless it is the default, the filters that filter
// something, and the page size when it isn't the default.
func libraryQuery(f LibraryFilters, cursor string) url.Values {
	q := url.Values{}
	for name, v := range map[string]string{"order": f.Order, "state": f.State, "content_type": f.ContentType,
		"host": f.Host, "folder": f.Folder, "cause": f.Cause, "cursor": cursor} {
		if v != "" {
			q.Set(name, v)
		}
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	return q
}

// stateHref is the Library of the documents in state.
func stateHref(state string) string { return libraryHref(LibraryFilters{State: state}, "") }

// stateTabHref is the first page of the Library under f with its state
// filter set to state, or dropped for "".
func stateTabHref(f LibraryFilters, state string) string {
	f.State = state
	return libraryHref(f, "")
}

// clearFiltersHref is the first page of the Library in f's order, with
// no filter.
func clearFiltersHref(f LibraryFilters) string {
	return libraryHref(LibraryFilters{Order: f.Order}, "")
}

// clearCauseHref is the first page of the Library under f without its
// cause filter: the other filters, the order and the page size kept.
func clearCauseHref(f LibraryFilters) string {
	f.Cause = ""
	return libraryHref(f, "")
}

// causeHref is the Library of the documents that failed for cause, dead
// links among them: the list's cause filter takes every state.
func causeHref(cause string) string { return libraryHref(LibraryFilters{Cause: cause}, "") }

// causeHostHref is the Library of the documents on host that failed for
// cause: a host tag of cause's card, which the tag counts exactly.
func causeHostHref(cause, host string) string {
	return libraryHref(LibraryFilters{Cause: cause, Host: host}, "")
}

// navHrefs are the navigation's pages.
var navHrefs = map[Nav]string{
	NavSearch:    "/ui/",
	NavLibrary:   "/ui/library",
	NavInterests: "/ui/interests",
	NavStatus:    "/ui/status",
}

// navHref is nav's page, or "" for none.
func navHref(nav Nav) string { return navHrefs[nav] }

// NavItem is one item of the pages' navigation: its label and icon, and
// the page it links to.
type NavItem struct {
	Nav   Nav
	Label string
	Icon  string
	Href  string
}

// navItems is the pages' navigation, in its order, each item linking to
// its navHrefs page, as an error page's Retry does.
func navItems() []NavItem {
	items := []NavItem{
		{Nav: NavSearch, Label: "Search", Icon: "search"},
		{Nav: NavLibrary, Label: "Library", Icon: "library"},
		{Nav: NavInterests, Label: "Interests", Icon: "sparkles"},
		{Nav: NavStatus, Label: "Status", Icon: "activity"},
	}
	for i := range items {
		items[i].Href = navHref(items[i].Nav)
	}
	return items
}

// outbound is u when it is an http or https URL, which a page may link
// to, and "" otherwise: a stored URL is never trusted to be one.
func outbound(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return u
}
