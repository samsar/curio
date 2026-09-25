// Package api implements the daemon's HTTP+JSON surface. Each handler
// matches one operation in api/openapi.yaml. Errors follow RFC 7807
// (application/problem+json).
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/samsar/curio/internal/store"
)

// Problem is RFC 7807's "Problem Details" shape. RequestID is an extension
// member: the X-Request-Id of the response, which is also on the daemon's
// log lines for the request.
type Problem struct {
	Type      string `json:"type,omitempty"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// statusClientClosedRequest is nginx's status for a request whose client
// went away before the answer: nobody reads it, but the access log and the
// error log then agree on what happened.
const statusClientClosedRequest = 499

// writeProblem emits a problem+json response for r. It logs nothing:
// writeError logs the server errors it reports.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	p := Problem{
		Type:      "about:blank",
		Title:     title,
		Status:    status,
		Detail:    detail,
		Instance:  r.URL.Path,
		RequestID: middleware.GetReqID(r.Context()),
	}
	sendProblem(w, p, p)
}

// sendProblem answers p's status with body, which is p or p with extension
// members, as problem+json.
func sendProblem(w http.ResponseWriter, p Problem, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		// A problem is strings and numbers; should that change, the status
		// and detail still reach the client.
		http.Error(w, p.Detail, p.Status)
		return
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_, _ = w.Write(append(encoded, '\n')) // fails only when the client has gone
}

// requestError is a fault in what the client sent: a parameter, cursor or
// field the handler refuses. writeError answers it 400 with its message.
type requestError struct{ err error }

func (e *requestError) Error() string { return e.err.Error() }
func (e *requestError) Unwrap() error { return e.err }

// badRequest builds a requestError, formatting like fmt.Errorf.
func badRequest(format string, args ...any) error {
	return &requestError{err: fmt.Errorf(format, args...)}
}

// errorStatus maps an error from a handler to a status and title.
func errorStatus(err error) (int, string) {
	var reqErr *requestError
	switch {
	case errors.As(err, &reqErr):
		return http.StatusBadRequest, "bad request"
	case errors.Is(err, errBodyTooLarge):
		return http.StatusRequestEntityTooLarge, "request body too large"
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, "conflict"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// writeError answers a handler's error as a problem. A server error is
// logged once with its request ID, since its detail is all the client
// gets. The detail keeps the raw error text: the API's clients are the
// local operator's own tools (docs/decisions.md "Local API").
//
// When the client has already gone, the error is almost always the
// cancellation itself, so it is logged at info as a 499 rather than as a
// daemon failure.
func (d Deps) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, title := errorStatus(err)
	if status >= http.StatusInternalServerError {
		level := slog.LevelError
		if r.Context().Err() != nil {
			level, status, title = slog.LevelInfo, statusClientClosedRequest, "client closed request"
		}
		d.Log.Log(r.Context(), level, "request failed",
			"request_id", middleware.GetReqID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"err", err,
		)
	}
	writeProblem(w, r, status, title, err.Error())
}

// writeLookupError reports a failure to load the kind of resource the
// request names as id: a 404 naming it when it doesn't exist, writeError's
// answer otherwise.
func (d Deps) writeLookupError(w http.ResponseWriter, r *http.Request, kind, id string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, r, kind, id)
		return
	}
	d.writeError(w, r, err)
}

// notFound answers 404 for the kind of resource named id.
func notFound(w http.ResponseWriter, r *http.Request, kind, id string) {
	writeProblem(w, r, http.StatusNotFound, "not found", fmt.Sprintf("%s %q not found", kind, id))
}

// writeJSON answers v as JSON with status. v is encoded before anything is
// written, so a value that can't be encoded is a logged 500 rather than a
// 200 with a truncated body.
func (d Deps) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		d.writeError(w, r, fmt.Errorf("encode response: %w", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n')) // fails only when the client has gone
}
