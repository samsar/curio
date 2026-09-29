package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/samsar/curio/internal/store"
)

// Every change a page can ask for is an Action built here, whole: the
// method, /v1 path and JSON body static/actions.js sends as they are, and
// what the page says once the daemon has taken the change. A template
// renders an Action as data attributes (the action partial) and writes
// none of it itself (TestTemplatesHaveNoInlineCode), and
// TestDashboard_ActionsMatchTheAPI, in internal/api, holds every Action
// the pages render to the router and to api/openapi.yaml, and a refetch by
// cause to refetch-all's own checks.

// ActionKind names what an Action changes: one per constructor below.
type ActionKind string

// The kinds of Action, in ActionKinds' order.
const (
	ActionRefetch          ActionKind = "refetch"
	ActionForcedRefetch    ActionKind = "refetch-forced"
	ActionReindex          ActionKind = "reindex"
	ActionRebuild          ActionKind = "rebuild"
	ActionPause            ActionKind = "pause"
	ActionResume           ActionKind = "resume"
	ActionThrottle         ActionKind = "throttle"
	ActionKeepAwake        ActionKind = "keep-awake"
	ActionSchedule         ActionKind = "schedule"
	ActionScheduleOff      ActionKind = "schedule-off"
	ActionRefetchCause     ActionKind = "refetch-cause"
	ActionRefetchDeadLinks ActionKind = "refetch-dead-links"
)

// ActionKinds are every kind of Action: a page must render each one
// somewhere (TestDashboard_ActionsMatchTheAPI).
var ActionKinds = []ActionKind{ActionRefetch, ActionForcedRefetch, ActionReindex, ActionRebuild, ActionPause,
	ActionResume, ActionThrottle, ActionKeepAwake, ActionSchedule, ActionScheduleOff, ActionRefetchCause,
	ActionRefetchDeadLinks}

// The ids of the pages' action statuses, the .action-status elements their
// Actions report to.
const (
	documentStatus = "doc-status"
	queueStatus    = "queue-status"
	rebuildStatus  = "rebuild-status"
	failuresStatus = "failures-status"
)

// Action is a change a control asks the daemon for, and how it reports
// what came of it. The control is a button (clicked), a checkbox (changed)
// or a form (submitted); actions.js sends Method to Path with the button's
// Body, the checkbox's {Field: checked}, or the form's fields, the values
// of those sharing a name joined by Join.
type Action struct {
	Kind   ActionKind
	Method string // POST or PUT
	Path   string // under /v1/, with its query
	body   map[string]any
	Field  string // a checkbox's: the key its checked state is sent under
	Join   string // a form's: what joins the values of its fields that share a name
	// Status is the id of the .action-status the control reports to, and
	// Done what it says there once the daemon has taken the change; DoneOff
	// is a checkbox's, for a change that left it unchecked.
	Status, Done, DoneOff string
}

// Body is the JSON object a button sends, or "" for none.
func (a Action) Body() (string, error) {
	if a.body == nil {
		return "", nil
	}
	b, err := json.Marshal(a.body)
	if err != nil {
		return "", fmt.Errorf("ui: encode the body of the %s action: %w", a.Kind, err)
	}
	return string(b), nil
}

// documentAPIPath is the path of verb on document id: one escaped segment
// for the ID, however odd.
func documentAPIPath(id, verb string) string {
	return "/v1/documents/" + url.PathEscape(id) + "/" + verb
}

// refetchAction queues a new fetch of document id.
func refetchAction(id string) Action {
	return Action{Kind: ActionRefetch, Method: http.MethodPost, Path: documentAPIPath(id, "refetch"),
		Status: documentStatus, Done: "Refetch requested"}
}

// forcedRefetchAction refetches document id though its link is dead, which
// the API refuses without force.
func forcedRefetchAction(id string) Action {
	return Action{Kind: ActionForcedRefetch, Method: http.MethodPost,
		Path:   documentAPIPath(id, "refetch") + "?" + url.Values{"force": {"1"}}.Encode(),
		Status: documentStatus, Done: "Refetch requested"}
}

// refetchCauseAction refetches the documents that failed for cause, as
// curio refetch --all --cause does; n is how many the page counts, for
// what it says once the daemon has taken the change. The cause is one
// escaped query value, however odd.
func refetchCauseAction(cause string, n int) Action {
	return refetchAllAction(ActionRefetchCause, cause, url.Values{"cause": {cause}}, n)
}

// refetchDeadLinksAction refetches the n dead links. refetch-all refuses
// their cause without state=dead, since its default states leave dead
// documents out.
func refetchDeadLinksAction(n int) Action {
	cause := string(store.FailureCauseDeadLink)
	return refetchAllAction(ActionRefetchDeadLinks, cause,
		url.Values{"cause": {cause}, "state": {string(store.DocStateDead)}}, n)
}

// refetchAllAction is a refetch of the n documents POST
// /v1/documents/refetch-all selects with query, all of them failed for
// cause.
func refetchAllAction(kind ActionKind, cause string, query url.Values, n int) Action {
	return Action{Kind: kind, Method: http.MethodPost, Path: "/v1/documents/refetch-all?" + query.Encode(),
		Status: failuresStatus, Done: causeLabel(cause) + ": " + count(n, "refetch", "refetches") + " queued"}
}

// reindexAction queues a new index of document id's current text.
func reindexAction(id string) Action {
	return Action{Kind: ActionReindex, Method: http.MethodPost, Path: documentAPIPath(id, "reindex"),
		Status: documentStatus, Done: "Reindex requested"}
}

// rebuildAction queues a clustering run, which rebuilds the interests.
func rebuildAction() Action {
	return Action{Kind: ActionRebuild, Method: http.MethodPost, Path: "/v1/interests/rebuild",
		Status: rebuildStatus, Done: "Rebuild requested"}
}

// queueAction is a change of the queue's settings: body holds the fields
// PUT /v1/queue changes.
func queueAction(kind ActionKind, body map[string]any, done string) Action {
	return Action{Kind: kind, Method: http.MethodPut, Path: "/v1/queue", body: body, Status: queueStatus,
		Done: done}
}

// pauseAction pauses the queue; resumeAction resumes it.
func pauseAction() Action {
	return queueAction(ActionPause, map[string]any{"paused": true}, "Queue paused")
}

func resumeAction() Action {
	return queueAction(ActionResume, map[string]any{"paused": false}, "Queue resumed")
}

// throttleAction sets the throttle to t.
func throttleAction(t store.Throttle) Action {
	return queueAction(ActionThrottle, map[string]any{"throttle": string(t)}, "Throttle set to "+string(t))
}

// keepAwakeAction is the keep-awake switch: it sends its checked state.
func keepAwakeAction() Action {
	a := queueAction(ActionKeepAwake, nil, "Keep awake turned on")
	a.Field, a.DoneOff = "keep_awake", "Keep awake turned off"
	return a
}

// scheduleAction is the schedule form: its two time fields, named
// schedule, are sent as one HH:MM-HH:MM.
func scheduleAction() Action {
	a := queueAction(ActionSchedule, nil, "Schedule saved")
	a.Join = "-"
	return a
}

// scheduleOffAction clears the schedule.
func scheduleOffAction() Action {
	return queueAction(ActionScheduleOff, map[string]any{"schedule": "off"}, "Schedule turned off")
}
