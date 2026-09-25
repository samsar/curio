package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/version"
)

// Server timeouts. The peers are local processes, so anything slow is either
// a stuck client or a handler that has run away.
const (
	// readHeaderTimeout: a local client sends its headers immediately.
	readHeaderTimeout = 5 * time.Second
	// readTimeout covers the whole request; the largest body (a 32 MiB
	// import batch) crosses loopback in well under a second.
	readTimeout = 30 * time.Second
	// writeTimeout must outlast the slowest synchronous handler: a search
	// that waits on Ollama to embed the query, or a 500-bookmark import batch.
	writeTimeout = 2 * time.Minute
	// idleTimeout bounds keep-alive connections held by the CLI and MCP sidecar.
	idleTimeout = 2 * time.Minute
	// shutdownTimeout bounds the graceful drain of in-flight requests.
	shutdownTimeout = 5 * time.Second
)

// Deps bundles everything the API handlers need. The daemon constructs this
// once it has started and passes it to Server.Ready.
type Deps struct {
	Home           *curiohome.Home
	Documents      store.DocumentStore
	Extractions    store.ExtractionStore
	Bookmarks      store.BookmarkStore
	Chunks         store.ChunkStore
	Queue          store.JobStore
	Embedder       embedder.Embedder
	Search         *search.Engine
	Insights       store.InsightStore
	InsightEnabled bool   // gates POST /v1/interests/rebuild (config insight.enabled)
	TenantID       string // default store.LocalTenantID
	Log            *slog.Logger
}

// Server is the HTTP layer. It serves from the moment the daemon binds its
// port: until Ready, as a starting daemon (see newStartingRouter), then the
// full API. One listener and one http.Server serve both, so a client's
// keep-alive connection carries on across the swap. Construct via
// NewServer, run via Serve, stop by cancelling the context passed to Serve.
type Server struct {
	ln     net.Listener
	origin localOrigin
	log    *slog.Logger
	router atomic.Pointer[http.Handler] // what serves each request
	srv    *http.Server
}

// NewServer builds a server that answers as a starting daemon for home,
// reporting startup's progress, until Ready. ln is the already-bound
// listener; its port is what the Host and Origin checks accept, so the
// allowlists always match the socket actually serving. A nil log means
// slog.Default().
func NewServer(ln net.Listener, home string, startup *Startup, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	origin, err := newLocalOrigin(ln.Addr())
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, origin: origin, log: log}
	s.swap(newStartingRouter(origin, home, startup, log))
	s.srv = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			(*s.router.Load()).ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	return s, nil
}

// Ready swaps in the full API over deps, for every request from now on.
// The daemon calls it once, when it has started.
func (s *Server) Ready(deps Deps) error {
	if deps.Log == nil {
		deps.Log = s.log
	}
	if deps.TenantID == "" {
		deps.TenantID = store.LocalTenantID
	}
	router, err := newRouter(deps, s.origin)
	if err != nil {
		return err
	}
	s.swap(router)
	return nil
}

func (s *Server) swap(h http.Handler) { s.router.Store(&h) }

// useMiddleware installs the stack every response goes through, starting
// or ready. Router-level, so every response, 404s and 405s included,
// carries a request ID and is logged; the access checks after recovery
// then run before routing.
func useMiddleware(r chi.Router, origin localOrigin, log *slog.Logger) {
	r.Use(middleware.RequestID)
	r.Use(exposeRequestID)
	// middleware.RealIP is intentionally NOT used — it's deprecated due to
	// X-Forwarded-For spoofing risk and we listen on loopback only, so
	// remote addrs are always loopback anyway.
	r.Use(loggingMiddleware(log))
	r.Use(recoverProblem(log))
	r.Use(requireLocalHost(origin, log))
	r.Use(rejectForeignOrigin(origin, log))
	r.Use(requireJSONBody)
}

// newRouter builds the API's routes and middleware for a daemon that is its
// own origin under origin. deps must have Log and TenantID set.
func newRouter(deps Deps, origin localOrigin) (chi.Router, error) {
	r := chi.NewRouter()
	useMiddleware(r, origin, deps.Log)
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		writeProblem(w, req, http.StatusNotFound, "not found", "no route for "+routingPath(req))
	})
	// The index is built after the routes below; this handler only runs
	// once the server is serving.
	var methods chi.Routes
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		path := routingPath(req)
		allowed := strings.Join(allowedMethods(methods, path), ", ")
		w.Header().Set("Allow", allowed)
		writeProblem(w, req, http.StatusMethodNotAllowed, "method not allowed",
			fmt.Sprintf("%s %s is not supported; allowed: %s", req.Method, path, allowed))
	})

	r.Route("/v1", func(r chi.Router) {
		r.Get("/healthz", deps.handleHealth)
		r.Get("/stats", deps.handleStats)
		r.Get("/metrics", deps.handleMetrics)

		r.Route("/bookmarks", func(r chi.Router) {
			r.Post("/", deps.handleCreateBookmark)
			r.Post("/import", deps.handleImportBookmarks)
			r.Get("/", deps.handleListBookmarks)
			r.Get("/{id}", deps.handleGetBookmark)
			r.Delete("/{id}", deps.handleDeleteBookmark)
		})

		r.Route("/documents", func(r chi.Router) {
			r.Get("/", deps.handleListDocuments)
			r.Get("/{id}", deps.handleGetDocument)
			r.Get("/{id}/content", deps.handleGetDocumentContent)
			r.Get("/{id}/related", deps.handleRelatedDocuments)
			r.Post("/{id}/refetch", deps.handleRefetchDocument)
			r.Post("/refetch-all", deps.handleRefetchAll)
			r.Post("/{id}/reindex", deps.handleReindexDocument)
			r.Post("/reindex-all", deps.handleReindexAll)
		})

		r.Post("/search", deps.handleSearch)

		r.Route("/interests", func(r chi.Router) {
			r.Get("/", deps.handleListInterests)
			r.Post("/rebuild", deps.handleRebuildInterests)
			r.Get("/{id}", deps.handleGetInterest)
		})

		r.Get("/jobs", deps.handleListJobs)
		r.Delete("/jobs", deps.handleDeleteJobs)
		r.Get("/jobs/{id}", deps.handleGetJob)
	})
	var err error
	if methods, err = methodIndex(r); err != nil {
		return nil, err
	}
	return r, nil
}

// Serve serves on the listener passed to NewServer until ctx is cancelled,
// then shuts down gracefully, giving in-flight requests shutdownTimeout to
// finish. Returns nil after a ctx-initiated shutdown; any other error means
// the listener failed. Either way the listener is closed when it returns.
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("api listening", "addr", s.ln.Addr().String(), "version", version.String())
		errCh <- s.srv.Serve(s.ln)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve api: %w", err)
	case <-ctx.Done():
	}

	s.log.Info("api stopping")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	err := s.srv.Shutdown(shutdownCtx)
	// http.Server.Serve returns, closing the listener, as soon as Shutdown
	// begins, even if Shutdown got there before Serve had started.
	<-errCh
	if err != nil {
		return fmt.Errorf("shut down api: %w", err)
	}
	return nil
}

// exposeRequestID returns the request's ID (see middleware.RequestID) in
// the X-Request-Id response header, so a client can quote it against the
// daemon's log.
func exposeRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(middleware.RequestIDHeader, middleware.GetReqID(r.Context()))
		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware records each request at info level with status and
// duration. Avoids middleware.Logger because we want structured slog output
// instead of stdlib log.
func loggingMiddleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			log.Info("http",
				"request_id", middleware.GetReqID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// recoverProblem turns a panicking handler into a logged 500 problem. It
// replaces middleware.Recoverer, which answers with a bare 500 and prints
// a colored stack to stderr, the daemon's JSON log. http.ErrAbortHandler
// is re-panicked: it is how a handler asks net/http to abort the response.
//
// A panic after the status line went out can't become a problem any more,
// and returning would let net/http finish the response as if it were
// complete: a client would read a 200 with half a body and no error. So it
// is logged here and then turned into http.ErrAbortHandler, which makes
// net/http cut the connection. loggingMiddleware's access line is skipped
// for that request; the panic record carries its request ID, method and
// path.
func recoverProblem(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				log.Error("handler panicked",
					"request_id", middleware.GetReqID(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(v),
					"stack", string(debug.Stack()),
				)
				if ww, ok := w.(middleware.WrapResponseWriter); ok && ww.Status() != 0 {
					panic(http.ErrAbortHandler)
				}
				writeProblem(w, r, http.StatusInternalServerError, "internal error",
					fmt.Sprintf("the handler panicked: %v", v))
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// routingPath is the path chi routes r on: the escaped form when the URL
// has one, so an ID with an encoded "/" (a%2Fb) stays one segment.
func routingPath(r *http.Request) string {
	if r.URL.RawPath != "" {
		return r.URL.RawPath
	}
	return r.URL.Path
}

// routeMethods are the methods allowedMethods probes for.
var routeMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

// methodIndex copies router's routes onto a mux with no mounts, for
// allowedMethods. chi fills a 405's Allow header only in its own handler,
// and its Match can't stand in on the router itself: a mount point such as
// /v1/bookmarks is registered for every method, so Match reports them all.
// A subrouter's "/" route also answers without the trailing slash, as it
// does on the router.
func methodIndex(router chi.Routes) (chi.Routes, error) {
	index := chi.NewMux()
	stub := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		index.Method(method, route, stub)
		if bare := strings.TrimSuffix(route, "/"); bare != route && bare != "" {
			index.Method(method, bare, stub)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("index routes: %w", err)
	}
	return index, nil
}

// allowedMethods lists the methods index serves for path.
func allowedMethods(index chi.Routes, path string) []string {
	var out []string
	for _, m := range routeMethods {
		if index.Match(chi.NewRouteContext(), m, path) {
			out = append(out, m)
		}
	}
	return out
}

// Page sizes for the list endpoints.
const (
	defaultListLimit = 50
	maxListLimit     = 500
)

// listLimit reads a list endpoint's ?limit.
func listLimit(r *http.Request) int {
	return intQuery(r, "limit", defaultListLimit, 1, maxListLimit)
}

// intQuery reads a sizing parameter such as ?limit: a value in lo..hi is
// honored, and anything else (absent, malformed, out of range) means def.
// Filters are validated instead (docs/decisions.md "API: filters are
// validated, sizing knobs default"): a wrong filter returns wrong rows,
// while a wrong size still returns the right ones.
func intQuery(r *http.Request, name string, def, lo, hi int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}

// Request body limits. Oversized bodies are rejected with 413 rather than
// buffered: the daemon has no reason to hold more than this in memory.
const (
	// maxJSONBody covers every request except imports; the largest is a
	// search or a single bookmark, a few KiB at most.
	maxJSONBody = 1 << 20
	// maxImportBody covers POST /v1/bookmarks/import. The CLI sends
	// 500-bookmark batches, typically a few hundred KiB.
	maxImportBody = 32 << 20
)

// errBodyTooLarge marks a request body that exceeded its size limit;
// writeError answers it 413.
var errBodyTooLarge = errors.New("request body too large")

// decodeJSON parses a request body of at most limit bytes holding exactly
// one JSON value into v. An oversized body is an error wrapping
// errBodyTooLarge, and anything else wrong with it a requestError, so
// writeError maps both.
//
// Unknown fields are refused: a field the daemon ignored would be a filter
// or knob silently not applied, and a newer client sending one to an older
// daemon is told to restart it (docs/decisions.md "API: tolerant responses,
// strict requests").
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		err = expectEOF(dec)
	}
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &tooLarge):
		return fmt.Errorf("%w: limit is %d bytes", errBodyTooLarge, tooLarge.Limit)
	}
	// encoding/json has no error type for an unknown field, only this text.
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return badRequest("unknown field %s: this curio-daemon (version %s) doesn't support it. "+
			"If the client is newer, restart the daemon: run `curio daemon stop`, and the next command starts the current one",
			field, version.String())
	}
	return badRequest("malformed JSON body: %w", err)
}

// expectEOF checks that only whitespace follows the decoded value. Reading
// to the end also holds bytes after the value to the size limit.
func expectEOF(dec *json.Decoder) error {
	switch _, err := dec.Token(); {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return err
	default:
		return errors.New("unexpected data after the JSON value")
	}
}
