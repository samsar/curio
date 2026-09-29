package api

import (
	"net/http"

	"github.com/samsar/curio/internal/ui"
)

// failures answers GET /ui/failures, the Library's Failures tab: the
// library's failed and dead documents grouped by why they failed, as GET
// /v1/failures counts them, each group with its top hosts and a refetch of
// it through POST /v1/documents/refetch-all. It is a view of the Library,
// under its navigation item, at a route of its own: a Library URL's query
// stays a valid /v1/documents query. When the summary can't be read, the
// groups show its error while the head renders.
//
// Its poller asks for the groups and the subnav's count alone
// (?poll=causes), after a change the page makes and when the tab comes
// back into view, never on a timer: the summary reads the cause and URL of
// every failed and dead document, about 2 ms for 3,000. The page reads it
// once, with the library's counts for its lede; the poll reads it once,
// and nothing else.
func (h pageHandlers) failures(w http.ResponseWriter, r *http.Request) {
	poll, err := pollParam(r, ui.PollCauses)
	if err != nil {
		h.writePageError(w, r, err, ui.NavLibrary)
		return
	}
	vm := ui.Failures{Layout: h.pages.layout("Failures", ui.NavLibrary), Poll: poll}
	if summary, err := h.d.failures(r.Context()); err != nil {
		vm.Err = h.panelError(r, err)
	} else {
		vm.Total, vm.Groups = summary.Total, failureGroups(summary)
	}
	if poll == "" {
		vm.Counts = h.libraryCounts(r)
	}
	h.page(w, r, http.StatusOK, ui.PageFailures, vm)
}

// failureGroups are the summary's causes as the Failures tab groups them,
// in its order: by count, most first, then by cause.
func failureGroups(f FailuresResponse) []ui.FailureGroup {
	groups := make([]ui.FailureGroup, 0, len(f.Causes))
	for _, c := range f.Causes {
		g := ui.FailureGroup{Cause: c.Cause, Count: c.Count}
		for _, host := range c.Hosts {
			g.Hosts = append(g.Hosts, ui.Count{Name: host.Host, Count: host.Count})
		}
		groups = append(groups, g)
	}
	return groups
}
