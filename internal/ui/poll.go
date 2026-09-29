package ui

import (
	"strconv"
	"strings"
	"time"
)

// A page keeps its live regions current with pollers: hidden, empty
// elements (the poller partial) that GET the page's poll URL and swap in,
// out of band, the regions they name, each only when it changed
// (actions.js). A poller swaps nothing itself (hx-swap="none"), so an
// answer that isn't the page, such as the starting page while the daemon
// restarts, can't remove a region; the newest request is the one answered
// (hx-sync this:replace). docs/decisions.md, "Dashboard: actions through
// /v1, sent by a first-party module", has the why.

// Poller is a page's poller: it GETs Href every Every (never for 0) and,
// with OnChange, after every change the page makes, and swaps in the
// Regions, by id. A poller whose own render decides whether it keeps
// polling lists itself among its Regions.
type Poller struct {
	ID       string
	Href     string
	Every    time.Duration
	OnChange bool
	Regions  []string
}

// changedEvent is what actions.js dispatches on the body once a change
// settles.
const changedEvent = "curio:changed"

// pollEvery is how often a page polls while something it shows is in
// flight.
const pollEvery = 2 * time.Second

// Trigger is the poller's hx-trigger.
func (p Poller) Trigger() string {
	var on []string
	if p.Every > 0 {
		on = append(on, "every "+strconv.Itoa(int(p.Every/time.Second))+"s")
	}
	if p.OnChange {
		on = append(on, changedEvent+" from:body")
	}
	return strings.Join(on, ", ")
}

// Select is the poller's hx-select-oob: its regions' ids.
func (p Poller) Select() string { return "#" + strings.Join(p.Regions, ",#") }

// The ids of the pages' live regions and pollers. Status's fast poller
// refreshes what changes by the second; its health poller, what pings
// Ollama, every 15 seconds; the failures card is read with the page alone.
var (
	statusLiveRegions = []string{"library-live", "queue-state", "queue-toggle", "queue-throttle", "queue-keep-awake",
		"keep-awake-hint", "schedule-state", "queue-pools", "progress-live", "jobs-live"}
	statusHealthRegions = []string{"attention", "health-live"}
)

const (
	statusPoller     = "status-poll"
	healthPoller     = "health-poll"
	healthPollEvery  = 15 * time.Second
	documentJobsID   = "doc-jobs"
	documentPoller   = "doc-poll"
	rebuildStateID   = "rebuild-state"
	rebuildControlID = "rebuild-control"
	rebuildPoller    = "rebuild-poll"
)
