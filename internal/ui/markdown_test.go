package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/ui/uitest"
)

// docURL is the URL the markdown tests' document was fetched from.
const docURL = "https://site.example/articles/post"

func renderMD(t *testing.T, src string, images bool) Text {
	t.Helper()
	out, err := newRenderer(t).RenderMarkdown([]byte(src), docURL, images)
	require.NoError(t, err)
	return out
}

// TestMarkdown_Hostile: whatever a stored page holds, both modes render it
// inert: no script, event handler, style, frame or form survives, and no
// link or image uses a scheme other than http, https or mailto.
func TestMarkdown_Hostile(t *testing.T) {
	cases := map[string]string{
		"script block":          "<script>alert(1)</script>\n\ntext",
		"inline script":         "a <script>alert(1)</script> b",
		"img onerror":           `<img src=x onerror=alert(1)>`,
		"javascript link":       "[x](javascript:alert(1))",
		"mixed-case javascript": "[x](JaVaScRiPt:alert(1))",
		"entity javascript":     "[x](&#106;avascript:alert(1))",
		"data html link":        "[x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)",
		"vbscript link":         "[x](vbscript:msgbox(1))",
		"javascript autolink":   "<javascript:alert(1)>",
		"inline style":          `<p style="position:fixed;top:0">x</p>`,
		"raw html block":        "<div onclick=\"alert(1)\">\n\n*x*\n\n</div>",
		"iframe":                `<iframe src="https://evil.example/"></iframe>`,
		"svg onload":            `<svg onload=alert(1)><circle r=1 /></svg>`,
		"form":                  `<form action="https://evil.example/"><input name=q><button>go</button></form>`,
		"reference link":        "[x][r]\n\n[r]: javascript:alert(1)",
		"image with script url": "![a](javascript:alert(1))",
	}
	for name, src := range cases {
		for _, images := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				out := string(renderMD(t, src, images).HTML)
				uitest.AssertInert(t, out)
				lower := strings.ToLower(out)
				for _, bad := range []string{"<script", "onerror=", "onclick=", "onload=", `="javascript:`,
					`="vbscript:`, `="data:`, "<iframe", "<form", "<svg", "style="} {
					assert.NotContains(t, lower, bad, "%s (images %v): %s", name, images, out)
				}
			})
		}
	}
}

// TestMarkdown_Links: every link points at the original site or another
// http(s) or mailto URL, and opens without a Referer.
func TestMarkdown_Links(t *testing.T) {
	cases := []struct {
		src, href string
	}{
		{"[rel](other)", "https://site.example/articles/other"},
		{"[root](/about)", "https://site.example/about"},
		{"[up](../index.html)", "https://site.example/index.html"},
		{"[frag](#section-2)", "https://site.example/articles/post#section-2"},
		{"[abs](https://elsewhere.example/x?a=1&b=2)", "https://elsewhere.example/x?a=1&amp;b=2"},
		{"[proto-relative](//cdn.example/x)", "https://cdn.example/x"},
		{"[mail](mailto:someone@example.com)", "mailto:someone@example.com"},
		{"<https://auto.example/x>", "https://auto.example/x"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			out := string(renderMD(t, tc.src, false).HTML)
			uitest.AssertInert(t, out)
			assert.Contains(t, out, `href="`+tc.href+`"`)
		})
	}
	out := string(renderMD(t, "[a link](https://elsewhere.example/)", false).HTML)
	assert.Contains(t, out, `rel="nofollow noreferrer noopener"`)
	assert.Contains(t, out, `target="_blank"`)

	// A link that can't be kept is its text.
	for _, src := range []string{"[kept text](javascript:alert(1))", "[kept text](&#106;avascript:alert(1))",
		"[kept text](ftp://files.example/x)", "[kept text](%zz)"} {
		out := string(renderMD(t, src, false).HTML)
		assert.Contains(t, out, "kept text", src)
		assert.NotContains(t, out, "<a", src)
	}
}

// TestMarkdown_Images: without images, each image is its alt text linking
// to it, or plain text when it isn't http(s) or is inside a link already;
// with images, https images show, lazily, and nothing else does.
func TestMarkdown_Images(t *testing.T) {
	src := "![an https image](https://img.example/a.png \"title\")\n\n" +
		"![an http image](http://img.example/b.png)\n\n" +
		"![a data image](data:image/png;base64,iVBORw0KGgo=)\n\n" +
		"![a relative image](../img/r.png)\n\n" +
		"![](https://img.example/no-alt.png)\n\n" +
		"[![a linked image](https://img.example/c.png)](https://site.example/big)\n"

	off := renderMD(t, src, false)
	html := string(off.HTML)
	uitest.AssertInert(t, html)
	assert.NotContains(t, html, "<img")
	assert.Equal(t, 5, off.RemoteImages, "every http(s) image, the relative one resolved to https")
	for _, want := range []string{
		`<a href="https://img.example/a.png" rel="nofollow noreferrer noopener" target="_blank">[image: an https image]</a>`,
		`<a href="http://img.example/b.png" rel="nofollow noreferrer noopener" target="_blank">[image: an http image]</a>`,
		`<p>[image: a data image]</p>`,
		`<a href="https://site.example/img/r.png" rel="nofollow noreferrer noopener" target="_blank">[image: a relative image]</a>`,
		`<a href="https://img.example/no-alt.png" rel="nofollow noreferrer noopener" target="_blank">[image]</a>`,
		`<a href="https://site.example/big" rel="nofollow noreferrer noopener" target="_blank">[image: a linked image]</a>`,
	} {
		assert.Contains(t, html, want)
	}
	assert.Equal(t, 1, strings.Count(html, "[image: a linked image]"), "no nested anchor")

	on := renderMD(t, src, true)
	html = string(on.HTML)
	uitest.AssertInert(t, html)
	assert.Equal(t, 5, on.RemoteImages)
	for _, want := range []string{
		`<img src="https://img.example/a.png" alt="an https image" title="title" loading="lazy">`,
		`<img src="https://site.example/img/r.png" alt="a relative image" loading="lazy">`,
		`<img src="https://img.example/no-alt.png" alt="" loading="lazy">`,
		`<a href="https://site.example/big" rel="nofollow noreferrer noopener" target="_blank"><img src="https://img.example/c.png" alt="a linked image" loading="lazy"></a>`,
		`[image: an http image]`,
		`<p>[image: a data image]</p>`,
	} {
		assert.Contains(t, html, want)
	}
	assert.Equal(t, 4, strings.Count(html, "<img"), "https images only")
	assert.NotContains(t, html, `src="http:`)
	assert.NotContains(t, html, `src="data:`)
}

// TestMarkdown_NoBase: a document whose URL doesn't parse leaves relative
// links nothing to resolve against, so they are text.
func TestMarkdown_NoBase(t *testing.T) {
	out, err := newRenderer(t).RenderMarkdown([]byte("[rel](/about) [abs](https://x.example/)"), "::not a url", false)
	require.NoError(t, err)
	html := string(out.HTML)
	uitest.AssertInert(t, html)
	assert.NotContains(t, html, "/about")
	assert.Contains(t, html, `href="https://x.example/"`)
}

func TestCutMarkdown(t *testing.T) {
	short := []byte("# short\n")
	got, cut := CutMarkdown(short)
	assert.False(t, cut)
	assert.Equal(t, short, got)

	line := bytes.Repeat([]byte("x"), 1000)
	var long []byte
	for len(long) <= MaxRenderedMarkdown {
		long = append(append(long, line...), '\n')
	}
	got, cut = CutMarkdown(long)
	assert.True(t, cut)
	assert.LessOrEqual(t, len(got), MaxRenderedMarkdown)
	assert.Equal(t, byte('\n'), got[len(got)-1], "cut after a whole line")
	assert.Zero(t, len(got)%(len(line)+1), "whole lines only")

	// One line longer than the cap, of three-byte runes: cut on a rune.
	runes := bytes.Repeat([]byte("€"), MaxRenderedMarkdown)
	got, cut = CutMarkdown(runes)
	assert.True(t, cut)
	assert.LessOrEqual(t, len(got), MaxRenderedMarkdown)
	assert.Zero(t, len(got)%3)
}
