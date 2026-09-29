package api

import (
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/samsar/curio/internal/ui"
)

// The API is unauthenticated: it trusts every process that can reach the
// loopback socket. What it must not trust is a web browser, which will
// happily send requests there on behalf of any page. The middleware below
// closes the two browser routes in:
//
//   - Cross-site requests. A page can POST to the daemon without a CORS
//     preflight as long as the body is a "simple" content type (text/plain,
//     form data) or empty. Browsers always attach an Origin header to those,
//     so any request whose Origin isn't the daemon itself is refused, and
//     request bodies must be application/json, which a page can only send
//     after a preflight the daemon never approves.
//   - DNS rebinding. A hostile name that re-resolves to 127.0.0.1 makes the
//     browser treat the daemon as same-origin with the attacker's page,
//     giving it full read access. The browser still sends the attacker's
//     name in Host, so only loopback names are accepted there.
//
// Two Fetch Metadata rules add to those, from what a browser says about
// where a request came from (Sec-Fetch-Site):
//
//   - A change (any method but GET, HEAD and OPTIONS) must come from the
//     daemon's own pages when the header is there: same-origin.
//   - The dashboard's pages refuse what another site's page loads as a
//     subresource (an <img> pointing at /ui/?q= runs a search), which
//     carries no Origin. Only a top-level navigation the user made passes.
//
// Non-browser clients (the CLI, the MCP sidecar, curl) send a loopback Host,
// no Origin and no Sec-Fetch-* headers, and pass untouched.

// localOrigin is the set of names under which the daemon is its own origin:
// localhost, the loopback addresses and the address it is bound to, all on
// the bound port.
type localOrigin struct {
	port    string
	ips     []net.IP        // accepted Host addresses, compared by value
	origins map[string]bool // accepted Origin header values, exact
}

func newLocalOrigin(addr net.Addr) (localOrigin, error) {
	boundHost, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return localOrigin{}, fmt.Errorf("api: listener address %q: %w", addr, err)
	}
	bound := net.ParseIP(boundHost)
	if bound == nil {
		return localOrigin{}, fmt.Errorf("api: listener address %q is not an IP address", addr)
	}
	return localOrigin{
		port: port,
		ips:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback, bound},
		origins: map[string]bool{
			"http://127.0.0.1:" + port: true,
			"http://localhost:" + port: true,
			"http://[::1]:" + port:     true,
			// A dashboard served from another loopback address
			// (daemon.listen 127.0.0.2, say) sends its changes with that
			// origin, which is the daemon's own.
			"http://" + net.JoinHostPort(bound.String(), port): true,
		},
	}, nil
}

// allowsHost reports whether a request's Host header names this daemon.
// Addresses compare by value, so every spelling of an allowed one matches
// (::ffff:127.0.0.1, 0:0:0:0:0:0:0:1): clients put daemon.listen in Host
// as the user wrote it.
func (o localOrigin) allowsHost(hostHeader string) bool {
	host, port, err := net.SplitHostPort(hostHeader)
	if err != nil || port != o.port {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && slices.ContainsFunc(o.ips, ip.Equal)
}

// requireLocalHost refuses requests whose Host header isn't a loopback name
// on the daemon's port, which is what a DNS-rebinding page sends.
func requireLocalHost(o localOrigin, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !o.allowsHost(r.Host) {
				logRejected(log, r, "host not allowed")
				writeProblem(w, r, http.StatusForbidden, "forbidden",
					"the daemon only answers requests addressed to a loopback name on its own port")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rejectForeignOrigin refuses any request carrying an Origin other than the
// daemon itself. That includes "null" (sandboxed frames, file:// pages) and
// localhost on another port (a different local web app). The daemon never
// emits CORS headers, so cross-origin pages also can't read responses.
func rejectForeignOrigin(o localOrigin, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin, sent := r.Header["Origin"]; sent && (len(origin) != 1 || !o.origins[origin[0]]) {
				logRejected(log, r, "origin not allowed")
				writeProblem(w, r, http.StatusForbidden, "forbidden",
					"cross-origin requests to the daemon are not allowed")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireJSONBody refuses request bodies that aren't application/json.
// Body-less requests (ContentLength 0) pass regardless of Content-Type, so
// the bare POSTs that trigger refetch, reindex and rebuild keep working.
func requireJSONBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeProblem(w, r, http.StatusUnsupportedMediaType, "unsupported media type",
					"request bodies must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireSameOriginChanges refuses a change a browser says came from
// anywhere but the daemon's own pages. A browser sends Sec-Fetch-Site on
// every request, so a change carrying it must carry it once, as
// same-origin; one without it is not a browser's and passes. The Origin
// check refuses other sites' changes already; this one holds where a
// browser leaves Origin off, and keeps the daemon's names apart: a page
// from localhost:P changing 127.0.0.1:P is cross-site.
func requireSameOriginChanges(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isChange(r.Method) {
				if site, sent := r.Header["Sec-Fetch-Site"]; sent && (len(site) != 1 || site[0] != "same-origin") {
					logRejected(log, r, "cross-site change")
					writeProblem(w, r, http.StatusForbidden, "forbidden",
						"changes must come from the daemon's own pages (Sec-Fetch-Site: same-origin)")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isChange reports whether method may change something: every method but
// the safe ones.
func isChange(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// isDashboard reports whether r is for the dashboard: /, /ui or anything
// under /ui/.
func isDashboard(r *http.Request) bool {
	p := routingPath(r)
	return p == "/" || p == "/ui" || strings.HasPrefix(p, "/ui/")
}

// dashboardHeaders gives every dashboard response its security headers
// (ui.SecurityHeaders): pages, assets, redirects and refusals alike.
func dashboardHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isDashboard(r) {
			ui.SecurityHeaders(w.Header())
		}
		next.ServeHTTP(w, r)
	})
}

// isolateDashboard refuses a dashboard request another site's page made
// without the user navigating: an <img>, <link> or fetch pointing at
// /ui/?q= makes the daemon run a search, and Ollama embed its query,
// and a no-cors GET carries no Origin. The browser says the request is
// cross-site or same-site in Sec-Fetch-Site; only a top-level navigation
// (Sec-Fetch-Mode navigate to a document), such as following a link to the
// dashboard, passes. The dashboard's own requests are same-origin.
func isolateDashboard(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isDashboard(r) && fromAnotherSite(r) && !userNavigation(r) {
				logRejected(log, r, "another site's subresource request")
				writeProblem(w, r, http.StatusForbidden, "forbidden",
					"the dashboard's pages answer other sites only when the user navigates to them")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// fromAnotherSite reports whether a browser says r came from a page of
// another site, or of another origin on this one.
func fromAnotherSite(r *http.Request) bool {
	return slices.ContainsFunc(r.Header["Sec-Fetch-Site"], func(site string) bool {
		return site == "cross-site" || site == "same-site"
	})
}

// userNavigation reports whether a browser says r loads a page into a
// window or tab: a navigation, not a subresource.
func userNavigation(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document"
}

func logRejected(log *slog.Logger, r *http.Request, reason string) {
	log.Warn("request rejected: "+reason,
		"request_id", middleware.GetReqID(r.Context()),
		"method", r.Method,
		"path", r.URL.Path,
		"host", r.Host,
		"origin", r.Header.Get("Origin"),
		"sec_fetch_site", strings.Join(r.Header["Sec-Fetch-Site"], ", "),
		"sec_fetch_mode", r.Header.Get("Sec-Fetch-Mode"),
		"sec_fetch_dest", r.Header.Get("Sec-Fetch-Dest"),
	)
}
