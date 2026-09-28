package ui

import "net/http"

// The Content-Security-Policy of the dashboard's responses. Everything a
// page loads comes from the daemon's own origin, with no inline script or
// style, so content that slips past the escaping and the sanitizer still
// can't run or load anything. img-src allows data: for the pages' own
// icons; remote images are CSPWithImages's.
const (
	CSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
		"connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
	// CSPWithImages is CSP for a document page that shows its remote
	// images: https ones only.
	CSPWithImages = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: https:; " +
		"connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
)

// SecurityHeaders sets the headers every dashboard response carries, pages,
// assets and refusals alike: the strict CSP, no MIME sniffing, and no
// Referer, so a link out of a page doesn't tell the site what was read in
// curio.
func SecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}
