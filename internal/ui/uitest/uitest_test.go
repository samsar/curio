package uitest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestProblems: the check finds each way HTML can do something, and passes
// what the pages are made of.
func TestProblems(t *testing.T) {
	bad := map[string]string{
		"inline script":           `<script>alert(1)</script>`,
		"foreign script":          `<script src="https://cdn.example/x.js"></script>`,
		"asset script with code":  `<script src="/ui/static/app.js">alert(1)</script>`,
		"style element":           `<style>body{}</style>`,
		"style attribute":         `<p style="color:red">x</p>`,
		"event handler":           `<img src="/ui/static/x.png" onerror="alert(1)">`,
		"event handler in caps":   `<body ONLOAD="alert(1)">`,
		"hx-on":                   `<button hx-on:click="alert(1)">x</button>`,
		"data-hx-on":              `<button data-hx-on-click="alert(1)">x</button>`,
		"javascript link":         `<a href="javascript:alert(1)">x</a>`,
		"data link":               `<a href="data:text/html,<script>alert(1)</script>">x</a>`,
		"relative link":           `<a href="../admin">x</a>`,
		"fragment link":           `<a href="#top">x</a>`,
		"link without rel":        `<a href="https://example.com/">x</a>`,
		"link with half a rel":    `<a href="https://example.com/" rel="noopener">x</a>`,
		"form to another site":    `<form action="https://evil.example/"></form>`,
		"iframe":                  `<iframe src="https://example.com/"></iframe>`,
		"object":                  `<object data="x"></object>`,
		"base":                    `<base href="https://evil.example/">`,
		"data image":              `<img src="data:image/png;base64,AAAA">`,
		"html/template's refusal": `<a href="#ZgotmplZ">x</a>`,
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
<a href="/ui/documents/abc">doc</a>
<a href="https://example.com/a" rel="nofollow noreferrer noopener" target="_blank">out</a>
<a href="mailto:a@example.com">mail</a>
<form action="/ui/search"><input name="q" hx-get="/ui/search" hx-select="#results > *"></form>
<img src="https://img.example/a.png" alt="a" loading="lazy">
</body></html>`
	assert.Empty(t, Problems(good))
}
