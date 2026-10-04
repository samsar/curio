package uitest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestProblems: the check finds each way HTML can do something, and passes
// what the pages are made of.
func TestProblems(t *testing.T) {
	bad := map[string]string{
		"inline script":             `<script>alert(1)</script>`,
		"foreign script":            `<script src="https://cdn.example/x.js"></script>`,
		"asset script with code":    `<script src="/ui/static/app.js">alert(1)</script>`,
		"style element":             `<style>body{}</style>`,
		"style attribute":           `<p style="color:red">x</p>`,
		"event handler":             `<img src="/ui/static/x.png" onerror="alert(1)">`,
		"event handler in caps":     `<body ONLOAD="alert(1)">`,
		"hx-on":                     `<button hx-on:click="alert(1)">x</button>`,
		"data-hx-on":                `<button data-hx-on-click="alert(1)">x</button>`,
		"javascript link":           `<a href="javascript:alert(1)">x</a>`,
		"data link":                 `<a href="data:text/html,<script>alert(1)</script>">x</a>`,
		"relative link":             `<a href="../admin">x</a>`,
		"fragment link":             `<a href="#top">x</a>`,
		"fragment of nothing":       `<main id="main"></main><a href="#top">x</a>`,
		"empty fragment":            `<a id="" href="#">x</a>`,
		"fragment image":            `<img id="x" src="#x">`,
		"link without rel":          `<a href="https://example.com/">x</a>`,
		"link with half a rel":      `<a href="https://example.com/" rel="noopener">x</a>`,
		"form to another site":      `<form action="https://evil.example/"></form>`,
		"iframe":                    `<iframe src="https://example.com/"></iframe>`,
		"object":                    `<object data="x"></object>`,
		"base":                      `<base href="https://evil.example/">`,
		"data image":                `<img src="data:image/png;base64,AAAA">`,
		"html/template's refusal":   `<a href="#ZgotmplZ">x</a>`,
		"refusal naming an id":      `<p id="ZgotmplZ"></p><a href="#ZgotmplZ">x</a>`,
		"a change by GET":           `<button data-method="GET" data-path="/v1/queue">x</button>`,
		"a change by DELETE":        `<button data-method="DELETE" data-path="/v1/jobs">x</button>`,
		"a lower-case method":       `<button data-method="post" data-path="/v1/queue">x</button>`,
		"a change to another site":  `<button data-method="POST" data-path="https://evil.example/v1/queue">x</button>`,
		"a change to a host":        `<button data-method="POST" data-path="//evil.example/v1/queue">x</button>`,
		"a change to a page":        `<button data-method="POST" data-path="/ui/status">x</button>`,
		"a relative change":         `<button data-method="POST" data-path="v1/queue">x</button>`,
		"a dot segment":             `<button data-method="POST" data-path="/v1/documents/../queue">x</button>`,
		"an escaped dot segment":    `<button data-method="POST" data-path="/v1/documents/%2E%2e/queue">x</button>`,
		"an empty segment":          `<button data-method="POST" data-path="/v1//queue">x</button>`,
		"a backslash":               `<button data-method="POST" data-path="/v1/\\evil">x</button>`,
		"hx-get off the pages":      `<div hx-get="/v1/stats"></div>`,
		"hx-get to another site":    `<div data-hx-get="https://evil.example/ui/"></div>`,
		"hx-post":                   `<button hx-post="/v1/queue">x</button>`,
		"hx-put":                    `<button hx-put="/v1/queue">x</button>`,
		"hx-patch":                  `<button hx-patch="/v1/queue">x</button>`,
		"hx-delete":                 `<button hx-delete="/v1/jobs">x</button>`,
		"data-hx-post":              `<button data-hx-post="/v1/queue">x</button>`,
		"a read from a script URL":  `<div data-src="javascript:alert(1)"></div>`,
		"a read from another site":  `<div data-src="https://evil.example/v1/interests/map"></div>`,
		"a read from a host":        `<div data-src="//evil.example/v1/interests/map"></div>`,
		"a read off the API":        `<div data-src="/ui/interests"></div>`,
		"a read up a dot segment":   `<div data-src="/v1/../ui/status"></div>`,
		"a page by a script URL":    `<div data-page="javascript:alert(1)"></div>`,
		"a page on a host":          `<div data-page="//evil.example/ui/interests/map"></div>`,
		"a page off the pages":      `<div data-page="/v1/interests/map"></div>`,
		"a prefix by a script URL":  `<div data-document-page="javascript:"></div>`,
		"a prefix up a dot segment": `<div data-interest-page="/ui/interests/%2e%2e/"></div>`,
		"a prefix with a query":     `<div data-unsorted-page="/ui/interests/unsorted?run="></div>`,
		"a prefix's empty segment":  `<div data-document-page="/ui//documents/"></div>`,
	}
	for name, page := range bad {
		t.Run(name, func(t *testing.T) {
			assert.NotEmpty(t, Problems(page), page)
		})
	}

	good := `<!doctype html><html><head>
<link rel="stylesheet" href="/ui/static/app.0123456789abcdef.css">
<script src="/ui/static/htmx-2.0.11.min.0123456789abcdef.js" defer></script>
</head><body>
<a class="skip-link" href="#main">Skip to content</a>
<main id="main">
<a href="/ui/documents/abc">doc</a>
<a href="https://example.com/a" rel="nofollow noreferrer noopener" target="_blank">out</a>
<a href="mailto:a@example.com">mail</a>
<form action="/ui/"><input name="q" hx-get="/ui/" hx-select="#results > *"></form>
<img src="https://img.example/a.png" alt="a" loading="lazy">
<button data-method="POST" data-path="/v1/documents/a%2Fb%3F/refetch?force=1" data-body="">Refetch</button>
<form data-method="PUT" data-path="/v1/queue" data-join="-"><input type="time" name="schedule"></form>
<div hidden data-poll hx-get="/ui/status?poll=live" hx-swap="none" hx-select-oob="#queue-state"></div>
<div id="interest-map" data-src="/v1/interests/map" data-page="/ui/interests/map" data-document-page="/ui/documents/"
 data-interest-page="/ui/interests/" data-unsorted-page="/ui/interests/unsorted" data-select="area:a/b?#"></div>
</main>
</body></html>`
	assert.Empty(t, Problems(good))
}
