package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// templateCommentRE matches a template comment, which may mention what the
// templates must not contain.
var templateCommentRE = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)

// inlineCode is what no template may contain: inline style, event
// handlers, htmx attributes that evaluate code, and script URLs.
var inlineCode = map[string]*regexp.Regexp{
	"a <style> element":          regexp.MustCompile(`(?i)<style`),
	"a style attribute":          regexp.MustCompile(`(?i)\sstyle\s*=`),
	"an event-handler attribute": regexp.MustCompile(`(?i)\son[a-z]+\s*=`),
	"hx-on":                      regexp.MustCompile(`(?i)hx-on`),
	"hx-vars":                    regexp.MustCompile(`(?i)hx-vars`),
	"a javascript: URL":          regexp.MustCompile(`(?i)javascript:`),
}

// scriptRE matches a script element, capturing its attributes and content.
var scriptRE = regexp.MustCompile(`(?is)<script(\s[^>]*)?>(.*?)</script>`)

// inlineCodeProblems lists what in a template's source the CSP would block
// or htmx would evaluate: inlineCode, and a <script> without src or with
// content.
func inlineCodeProblems(src string) []string {
	text := templateCommentRE.ReplaceAllString(src, "")
	var problems []string
	for what, re := range inlineCode {
		if m := re.FindString(text); m != "" {
			problems = append(problems, what+": "+m)
		}
	}
	scripts := scriptRE.FindAllStringSubmatch(text, -1)
	if opened := strings.Count(strings.ToLower(text), "<script"); opened != len(scripts) {
		problems = append(problems, "a <script> that isn't closed")
	}
	for _, m := range scripts {
		if !strings.Contains(m[1], " src=") || strings.TrimSpace(m[2]) != "" {
			problems = append(problems, "a <script> without src or with content: "+m[0])
		}
	}
	return problems
}

// TestTemplatesHaveNoInlineCode: the CSP allows no inline script or style,
// so the templates have none, and nothing that makes htmx evaluate code.
func TestTemplatesHaveNoInlineCode(t *testing.T) {
	paths, err := fs.Glob(files, "templates/*.html")
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		src, err := fs.ReadFile(files, path)
		require.NoError(t, err)
		assert.Empty(t, inlineCodeProblems(string(src)), path)
	}

	for _, bad := range []string{
		`<style>p{}</style>`, `<p style="x">`, `<a href="/ui/" onclick="x()">`, `<img ONERROR = "x">`,
		`<button hx-on:click="x()">`, `<button data-hx-on-click="x()">`, `<div hx-vars="a:1">`,
		`<a href="javascript:x()">`, `<script>x()</script>`, `<script src="/ui/static/a.js">x()</script>`,
		`<script src="/ui/static/a.js">`,
	} {
		assert.NotEmpty(t, inlineCodeProblems(bad), bad)
	}
	assert.Empty(t, inlineCodeProblems(`{{/* no hx-on, no <style>, no onclick= */}}`), "comments may name them")
}

// trustedHTMLTypes are html/template's types that mark content as safe,
// bypassing its escaping.
var trustedHTMLTypes = []string{"HTML", "JS", "JSStr", "CSS", "URL", "HTMLAttr", "Srcset"}

// TestTrustedHTMLOnlyFromTheSanitizer: a conversion to one of
// html/template's trusted types appears in exactly one non-test place in
// the repository, the markdown sanitizer's output. Anywhere else it would
// let stored content past the escaping.
func TestTrustedHTMLOnlyFromTheSanitizer(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	var sites []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "bin" ||
				name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, pos := range trustedConversions(t, path) {
			rel, err := filepath.Rel(root, pos.Filename)
			require.NoError(t, err)
			sites = append(sites, rel+":"+strconv.Itoa(pos.Line))
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, sites, 1, "conversions to trusted template types: %v", sites)
	assert.True(t, strings.HasPrefix(sites[0], filepath.Join("internal", "ui", "markdown.go")+":"), sites[0])
}

// trustedConversions finds the calls in the Go file at path that convert
// to one of html/template's trusted types, however the file names the
// package.
func trustedConversions(t *testing.T, path string) []token.Position {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	require.NoError(t, err, path)
	pkgName := ""
	for _, imp := range f.Imports {
		if imp.Path.Value != `"html/template"` {
			continue
		}
		pkgName = "template"
		if imp.Name != nil {
			pkgName = imp.Name.Name
		}
	}
	if pkgName == "" {
		return nil
	}
	var out []token.Position
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkgName && slices.Contains(trustedHTMLTypes, sel.Sel.Name) {
			out = append(out, fset.Position(call.Pos()))
		}
		return true
	})
	return out
}

// The vendored htmx: dist/htmx.min.js of the npm package htmx.org, whose
// tarball and jsDelivr copy have this SHA-256. Updating htmx means
// replacing the file and both constants, and reading its changes for
// anything that evaluates code or injects style.
const (
	htmxVersion = "2.0.11"
	htmxSHA256  = "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717"
)

func TestHTMXIsPinned(t *testing.T) {
	body, err := fs.ReadFile(files, "static/htmx-"+htmxVersion+".min.js")
	require.NoError(t, err)
	sum := sha256.Sum256(body)
	assert.Equal(t, htmxSHA256, hex.EncodeToString(sum[:]))
	assert.Len(t, body, 52182)

	r := newRenderer(t)
	page := render(t, r, PageError, samples(t, r)[PageError])
	assert.Contains(t, page, `<script src="/ui/static/htmx-`+htmxVersion+`.min.`+htmxSHA256[:assetHashLen]+`.js" defer>`)
}

// TestHTMXConfig: every page configures htmx to evaluate no code, inject no
// style and keep no copy of pages in localStorage.
func TestHTMXConfig(t *testing.T) {
	r := newRenderer(t)
	for page, data := range eachSample(t, r) {
		doc, err := html.Parse(strings.NewReader(render(t, r, page, data)))
		require.NoError(t, err)
		var content string
		for n := range doc.Descendants() {
			if n.Type == html.ElementNode && n.Data == "meta" && attrValue(n, "name") == "htmx-config" {
				content = attrValue(n, "content")
			}
		}
		require.NotEmpty(t, content, "%s: the htmx-config meta", page)
		var cfg map[string]any
		require.NoError(t, json.Unmarshal([]byte(content), &cfg), page)
		assert.Equal(t, false, cfg["allowEval"], page)
		assert.Equal(t, false, cfg["includeIndicatorStyles"], page)
		assert.Equal(t, false, cfg["allowScriptTags"], page)
		assert.Equal(t, true, cfg["selfRequestsOnly"], page)
		assert.EqualValues(t, 0, cfg["historyCacheSize"], page)
	}
}

func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
