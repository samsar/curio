package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/samsar/curio/internal/version"
)

// StartingProblemType is the problem type of every answer a starting daemon
// gives. Clients recognize a starting daemon by it together with status
// 503; RFC 7807 reserves the problem's status member for the HTTP status.
const StartingProblemType = "urn:curio:problem:daemon-starting"

// startingRetryAfter is the Retry-After, in seconds, of a starting daemon's
// answers. Clients waiting on a migration poll no faster: each request is
// an access-log line in daemon.log, which is never rotated.
const startingRetryAfter = "1"

// Phase is what a starting daemon is doing.
type Phase string

const (
	// PhaseInitializing: anything but migrating, such as opening the
	// database and recovering the last run's jobs. Usually milliseconds.
	PhaseInitializing Phase = "initializing"
	// PhaseMigrating: applying schema migrations to an existing database,
	// which can take minutes on a large library.
	PhaseMigrating Phase = "migrating"
)

// Startup tracks a starting daemon's progress for its /v1/healthz answer.
// It is safe for concurrent use: the daemon updates it while the server
// reads it.
type Startup struct {
	mu      sync.Mutex
	phase   Phase
	applied int
	total   int
}

// NewStartup returns progress in the initializing phase.
func NewStartup() *Startup {
	return &Startup{phase: PhaseInitializing}
}

// SetMigrating enters the migrating phase with total migrations to apply.
func (s *Startup) SetMigrating(total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase, s.applied, s.total = PhaseMigrating, 0, total
}

// MigrationApplied counts one more migration applied.
func (s *Startup) MigrationApplied() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applied++
}

// SetInitializing returns to the initializing phase, as when migrations are
// done and the daemon is building the rest of itself.
func (s *Startup) SetInitializing() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = PhaseInitializing
}

// Progress returns the phase and, while migrating, how far along it is.
func (s *Startup) Progress() (Phase, *MigrationProgress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != PhaseMigrating {
		return s.phase, nil
	}
	return s.phase, &MigrationProgress{Applied: s.applied, Total: s.total}
}

// Starting is a starting daemon's /v1/healthz answer: a problem naming the
// daemon, like Health does, and saying how far along it is.
type Starting struct {
	Problem
	PID        int                `json:"pid"`
	Home       string             `json:"home"`
	Version    string             `json:"version"`
	Phase      Phase              `json:"phase"`
	Migrations *MigrationProgress `json:"migrations,omitempty"` // present exactly while migrating
}

// MigrationProgress counts the migrations a starting daemon has applied.
type MigrationProgress struct {
	Applied int `json:"applied"`
	Total   int `json:"total"`
}

// newStartingRouter answers every request 503 with the starting problem
// until the daemon swaps in the full API. It has no Deps: nothing a
// starting daemon is asked reaches a handler or the database, so clients
// can resend any request once it is ready. /v1/healthz also names the
// daemon, so clients know it is theirs and worth waiting for. The access
// policy applies as it does to the full API.
func newStartingRouter(origin localOrigin, home string, startup *Startup, log *slog.Logger) chi.Router {
	s := startingAPI{home: home, startup: startup}
	r := chi.NewRouter()
	useMiddleware(r, origin, log)
	r.Get("/v1/healthz", s.identify)
	r.NotFound(s.refuse)
	r.MethodNotAllowed(s.refuse)
	return r
}

// startingAPI is what a starting daemon answers with.
type startingAPI struct {
	home    string
	startup *Startup
}

// identify answers /v1/healthz: the starting problem, with the daemon's
// identity and progress.
func (s startingAPI) identify(w http.ResponseWriter, r *http.Request) {
	phase, migrations := s.startup.Progress()
	p := startingProblem(r, phase, migrations)
	w.Header().Set("Retry-After", startingRetryAfter)
	sendProblem(w, p, Starting{
		Problem:    p,
		PID:        os.Getpid(),
		Home:       s.home,
		Version:    version.String(),
		Phase:      phase,
		Migrations: migrations,
	})
}

// refuse answers any other request: the starting problem alone.
func (s startingAPI) refuse(w http.ResponseWriter, r *http.Request) {
	phase, migrations := s.startup.Progress()
	p := startingProblem(r, phase, migrations)
	w.Header().Set("Retry-After", startingRetryAfter)
	sendProblem(w, p, p)
}

// startingProblem is the problem a starting daemon answers r with. Its
// detail says what the daemon is doing, readably for a client that only
// prints a problem's detail.
func startingProblem(r *http.Request, phase Phase, migrations *MigrationProgress) Problem {
	detail := fmt.Sprintf("curio-daemon is starting: %s", phase)
	if migrations != nil {
		detail = fmt.Sprintf("curio-daemon is starting: migrating the database, %d of %d migrations applied",
			migrations.Applied, migrations.Total)
	}
	return Problem{
		Type:      StartingProblemType,
		Title:     "daemon starting",
		Status:    http.StatusServiceUnavailable,
		Detail:    detail,
		Instance:  r.URL.Path,
		RequestID: middleware.GetReqID(r.Context()),
	}
}
