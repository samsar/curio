package api

import (
	"cmp"
	"context"
	"net/http"
	"time"

	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/keepawake"
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
// settings, what they mean now, each pool's limit and load, and whether
// the Mac is held awake for them.
type QueueResponse struct {
	Paused    bool   `json:"paused"`
	Throttle  string `json:"throttle"`
	Schedule  string `json:"schedule,omitempty"`
	KeepAwake bool   `json:"keep_awake"`
	// KeepAwakeActive: the daemon holds the Mac awake now.
	KeepAwakeActive bool `json:"keep_awake_active"`
	// PowerSource is ac, battery or unknown, as the keeper last read it;
	// present exactly while keep_awake is on.
	PowerSource string              `json:"power_source,omitempty"`
	State       string              `json:"state"`
	Reason      string              `json:"reason,omitempty"`
	OpensAt     time.Time           `json:"opens_at,omitzero"`
	Kinds       []QueueKindResponse `json:"kinds"`
}

// KeepAwake reports whether the daemon holds the Mac awake: the daemon's
// is a *keepawake.Keeper. State reads memory only.
type KeepAwake interface {
	State() keepawake.State
}

// QueueKindResponse is one pool's kind: how many of its jobs may run at
// once while the queue is open, and how many are running and waiting,
// daemon-wide. DueLater is how many of the waiting ones can't run yet (a
// retry's backoff, a deferral's hold); NextDue, present exactly when
// there are some, is when the first of them can.
type QueueKindResponse struct {
	Kind     string    `json:"kind"`
	Limit    int       `json:"limit"`
	Running  int       `json:"running"`
	Pending  int       `json:"pending"`
	DueLater int       `json:"due_later"`
	NextDue  time.Time `json:"next_due,omitzero"`
}

// QueueUpdateRequest is the body of PUT /v1/queue. Only the fields given
// change.
type QueueUpdateRequest struct {
	Paused    *bool   `json:"paused,omitempty"`
	Throttle  *string `json:"throttle,omitempty"`
	Schedule  *string `json:"schedule,omitempty"`
	KeepAwake *bool   `json:"keep_awake,omitempty"`
}

// update validates r into the gate's update.
func (r QueueUpdateRequest) update() (jobs.QueueUpdate, error) {
	if r.Paused == nil && r.Throttle == nil && r.Schedule == nil && r.KeepAwake == nil {
		return jobs.QueueUpdate{}, badRequest("nothing to change: give paused, throttle, schedule or keep_awake")
	}
	u := jobs.QueueUpdate{Paused: r.Paused, KeepAwake: r.KeepAwake}
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
	resp, err := d.queueState(r.Context())
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// queueState is the queue's state now: the gate's settings and what they
// mean, each pool's limit and load, and whether the Mac is held awake.
func (d Deps) queueState(ctx context.Context) (QueueResponse, error) {
	counts, err := d.Queue.QueueCounts(ctx)
	if err != nil {
		return QueueResponse{}, err
	}
	resp := queueResponse(d.Gate.State(time.Now()), counts)
	resp.KeepAwakeActive, resp.PowerSource = d.keepAwake(resp.KeepAwake)
	return resp, nil
}

// keepAwake is whether the Mac is held awake, and the power source while
// keep-awake is on (enabled, the stored setting): unknown until the
// keeper has read it. A keeper that hasn't caught up with keep-awake
// being turned off holds nothing it will keep.
func (d Deps) keepAwake(enabled bool) (active bool, power string) {
	st := keepawake.State{Power: keepawake.PowerUnknown}
	if d.KeepAwake != nil {
		st = d.KeepAwake.State()
	}
	if !enabled {
		return false, ""
	}
	return st.Active, cmp.Or(string(st.Power), string(keepawake.PowerUnknown))
}

func queueResponse(st jobs.QueueState, counts map[store.JobKind]store.QueueCount) QueueResponse {
	resp := QueueResponse{
		Paused:    st.Settings.Paused,
		Throttle:  string(st.Settings.Throttle),
		KeepAwake: st.Settings.KeepAwake,
		State:     queueOpen,
		Kinds:     make([]QueueKindResponse, 0, len(st.Limits)),
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
			Running: c.Running, Pending: c.Pending, DueLater: c.DueLater, NextDue: c.NextDue})
	}
	return resp
}
