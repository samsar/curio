package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/version"
)

// Deps bundles everything the API handlers need. The daemon constructs this
// once at startup and passes it to NewServer.
type Deps struct {
	Home        *curiohome.Home
	Documents   store.DocumentStore
	Extractions store.ExtractionStore
	Bookmarks   store.BookmarkStore
	Chunks      store.ChunkStore
	Queue       store.JobQueue
	Embedder    embedder.Embedder
	Search      *search.Engine
	TenantID    string // hardcoded for single-tenant local mode; "local"
	Log         *slog.Logger
}

// Server is the HTTP layer. Construct via NewServer, run via Run, stop by
// cancelling the context passed to Run.
type Server struct {
	deps Deps
	srv  *http.Server
}

// NewServer wires the chi router with all middleware and handlers.
func NewServer(addr string, deps Deps) *Server {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.TenantID == "" {
		deps.TenantID = "local"
	}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	// middleware.RealIP is intentionally NOT used — it's deprecated due to
	// X-Forwarded-For spoofing risk and we listen on 127.0.0.1 only, so
	// remote addrs are always loopback anyway.
	r.Use(loggingMiddleware(deps.Log))

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
			r.Post("/{id}/refetch", deps.handleRefetchDocument)
			r.Post("/refetch-all", deps.handleRefetchAll)
			r.Post("/{id}/reindex", deps.handleReindexDocument)
			r.Post("/reindex-all", deps.handleReindexAll)
		})

		r.Post("/search", deps.handleSearch)

		r.Get("/jobs", deps.handleListJobs)
		r.Delete("/jobs", deps.handleDeleteJobs)
	})

	return &Server{
		deps: deps,
		srv: &http.Server{
			Addr:              addr,
			Handler:           r,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

// Run starts the listener and blocks until ctx is cancelled. Performs
// graceful shutdown with a 5s timeout.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.deps.Log.Info("api listening", "addr", s.srv.Addr, "version", version.String())
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	s.deps.Log.Info("api stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return ctx.Err()
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

// writeJSON is a small helper to set Content-Type and encode.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON parses a request body, returning a problem-friendly error.
func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}
