// Package api implements the daemon's HTTP+JSON surface. Each handler
// matches one operation in api/openapi.yaml. Errors follow RFC 7807
// (application/problem+json).
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/samansartipi/curio/internal/store"
)

// Problem is RFC 7807's "Problem Details" shape.
type Problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// writeProblem emits a problem+json response. Logs nothing — callers log
// at their level if needed.
func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
	})
}

// errorStatus maps store sentinels and validation errors to an HTTP status.
// Returns (status, title) — title is a short identifier; detail comes from
// the err itself.
func errorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, "conflict"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// writeError is the boilerplate-free way to surface an error from a handler.
func writeError(w http.ResponseWriter, err error) {
	status, title := errorStatus(err)
	writeProblem(w, status, title, err.Error())
}
