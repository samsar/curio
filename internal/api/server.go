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
	"strconv"
	"strings"
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
// once at startup and passes it to NewServer.
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
	TenantID       string // hardcoded for single-tenant local mode; "local"
	Log            *slog.Logger
}

// Server is the HTTP layer. Construct via NewServer, run via Serve, stop by
// cancelling the context passed to Serve.
type Server struct {
	deps Deps
	ln   net.Listener
	srv  *http.Server
}

// NewServer wires the chi router with all middleware and handlers. ln is the
// already-bound listener; its port is what the Host and Origin checks accept,
// so the allowlists always match the socket actually serving.
func NewServer(ln net.Listener, deps Deps) (*Server, error) {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.TenantID == "" {
		deps.TenantID = "local"
	}
	origin, err := newLocalOrigin(ln.Addr())
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	// middleware.RealIP is intentionally NOT used — it's deprecated due to
	// X-Forwarded-For spoofing risk and we listen on loopback only, so
	// remote addrs are always loopback anyway.
	r.Use(loggingMiddleware(deps.Log))
	// Router-level, so they run before routing and cover 404/405 too.
	r.Use(requireLocalHost(origin, deps.Log))
	r.Use(rejectForeignOrigin(origin, deps.Log))
	r.Use(requireJSONBody)
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		writeProblem(w, http.StatusNotFound, "not found", "no route for "+req.URL.Path)
	})
	// The index is built after the routes below; this handler only runs
	// once the server is serving.
	var methods chi.Routes
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		allowed := strings.Join(allowedMethods(methods, req.URL.Path), ", ")
		w.Header().Set("Allow", allowed)
		writeProblem(w, http.StatusMethodNotAllowed, "method not allowed",
			fmt.Sprintf("%s %s is not supported; allowed: %s", req.Method, req.URL.Path, allowed))
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
	})
	if methods, err = methodIndex(r); err != nil {
		return nil, err
	}

	return &Server{
		deps: deps,
		ln:   ln,
		srv: &http.Server{
			Handler:           r,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
		},
	}, nil
}

// Serve serves on the listener passed to NewServer until ctx is cancelled,
// then shuts down gracefully, giving in-flight requests shutdownTimeout to
// finish. Returns nil after a ctx-initiated shutdown; any other error means
// the listener failed.
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.deps.Log.Info("api listening", "addr", s.ln.Addr().String(), "version", version.String())
		errCh <- s.srv.Serve(s.ln)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve api: %w", err)
	case <-ctx.Done():
	}

	s.deps.Log.Info("api stopping")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down api: %w", err)
	}
	return nil
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
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
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

// listLimit reads a list endpoint's ?limit: 1 through maxListLimit is
// honored, and anything else (absent, malformed, out of range) means
// defaultListLimit.
func listLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 || n > maxListLimit {
		return defaultListLimit
	}
	return n
}

// writeJSON is a small helper to set Content-Type and encode.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
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

// errBodyTooLarge marks a request body that exceeded its size limit.
var errBodyTooLarge = errors.New("request body too large")

// decodeJSON parses a request body of at most limit bytes holding exactly
// one JSON value. It returns an error wrapping errBodyTooLarge when the
// limit is exceeded; any other error is a malformed body. writeDecodeError
// maps both.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		err = expectEOF(dec)
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return fmt.Errorf("%w: limit is %d bytes", errBodyTooLarge, tooLarge.Limit)
	}
	return err
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

// writeDecodeError reports a decodeJSON failure: 413 for an oversized body,
// 400 for anything else.
func writeDecodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		writeProblem(w, http.StatusRequestEntityTooLarge, "request body too large", err.Error())
		return
	}
	writeProblem(w, http.StatusBadRequest, "bad request", err.Error())
}
