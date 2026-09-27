package api

import (
	"net/http"
	"time"

	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
)

// Queue states on the wire.
const (
	queueOpen   = "open"
	queueClosed = "closed"
	// scheduleOff clears the schedule in a PUT.
	scheduleOff = "off"
)

// QueueResponse is the body of GET and PUT /v1/queue: the queue gate's
// settings, what they mean now, and each pool's limit and load.
type QueueResponse struct {
	Paused   bool                `json:"paused"`
	Throttle string              `json:"throttle"`
	Schedule string              `json:"schedule,omitempty"`
	State    string              `json:"state"`
	Reason   string              `json:"reason,omitempty"`
	OpensAt  time.Time           `json:"opens_at,omitzero"`
	Kinds    []QueueKindResponse `json:"kinds"`
}

// QueueKindResponse is one pool's kind: how many of its jobs may run at
// once while the queue is open, and how many are running and waiting,
// daemon-wide.
type QueueKindResponse struct {
	Kind    string `json:"kind"`
	Limit   int    `json:"limit"`
	Running int    `json:"running"`
	Pending int    `json:"pending"`
}

// QueueUpdateRequest is the body of PUT /v1/queue. Only the fields given
// change.
type QueueUpdateRequest struct {
	Paused   *bool   `json:"paused,omitempty"`
	Throttle *string `json:"throttle,omitempty"`
	Schedule *string `json:"schedule,omitempty"`
}

// update validates r into the gate's update.
func (r QueueUpdateRequest) update() (jobs.QueueUpdate, error) {
	if r.Paused == nil && r.Throttle == nil && r.Schedule == nil {
		return jobs.QueueUpdate{}, badRequest("nothing to change: give paused, throttle or schedule")
	}
	u := jobs.QueueUpdate{Paused: r.Paused}
	if r.Throttle != nil {
		throttle := store.Throttle(*r.Throttle)
		if !throttle.Valid() {
			return jobs.QueueUpdate{}, badRequest("throttle %q must be one of: normal, gentle", *r.Throttle)
		}
		u.Throttle = &throttle
	}
	if r.Schedule != nil {
		var w store.DailyWindow // off: no schedule
		if *r.Schedule != scheduleOff {
			var err error
			if w, err = store.ParseDailyWindow(*r.Schedule); err != nil {
				return jobs.QueueUpdate{}, badRequest("schedule %q: %v; give HH:MM-HH:MM on the daemon's clock, "+
					"e.g. 22:00-07:00, or off", *r.Schedule, err)
			}
		}
		u.Schedule = &w
	}
	return u, nil
}

func (d Deps) handleGetQueue(w http.ResponseWriter, r *http.Request) {
	d.writeQueue(w, r)
}

// handleUpdateQueue applies the fields given. The change takes effect at
// the next claim; jobs already running finish.
func (d Deps) handleUpdateQueue(w http.ResponseWriter, r *http.Request) {
	var req QueueUpdateRequest
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		d.writeError(w, r, err)
		return
	}
	u, err := req.update()
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	if _, err := d.Gate.Update(r.Context(), u); err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeQueue(w, r)
}

// writeQueue answers the queue's state now.
func (d Deps) writeQueue(w http.ResponseWriter, r *http.Request) {
	counts, err := d.Queue.QueueCounts(r.Context())
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, queueResponse(d.Gate.State(time.Now()), counts))
}

func queueResponse(st jobs.QueueState, counts map[store.JobKind]store.QueueCount) QueueResponse {
	resp := QueueResponse{
		Paused:   st.Settings.Paused,
		Throttle: string(st.Settings.Throttle),
		State:    queueOpen,
		Kinds:    make([]QueueKindResponse, 0, len(st.Limits)),
	}
	if !st.Settings.Schedule.IsZero() {
		resp.Schedule = st.Settings.Schedule.String()
	}
	if st.Closed != "" {
		resp.State, resp.Reason, resp.OpensAt = queueClosed, string(st.Closed), st.OpensAt.UTC()
	}
	for _, l := range st.Limits {
		c := counts[l.Kind]
		resp.Kinds = append(resp.Kinds, QueueKindResponse{Kind: string(l.Kind), Limit: l.Limit,
			Running: c.Running, Pending: c.Pending})
	}
	return resp
}
