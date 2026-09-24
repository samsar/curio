package api

import (
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"slices"
	"strings"
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
// Non-browser clients (the CLI, the MCP sidecar, curl) send a loopback Host
// and no Origin, and pass untouched.

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
				writeProblem(w, http.StatusForbidden, "forbidden",
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
				writeProblem(w, http.StatusForbidden, "forbidden",
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
				writeProblem(w, http.StatusUnsupportedMediaType, "unsupported media type",
					"request bodies must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func logRejected(log *slog.Logger, r *http.Request, reason string) {
	log.Warn("request rejected: "+reason,
		"method", r.Method,
		"path", r.URL.Path,
		"host", r.Host,
		"origin", r.Header.Get("Origin"),
	)
}
