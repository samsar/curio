package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The vendored d3 modules the interest map reads: each the dist/*.min.js of
// its npm package, byte for byte the tarball's and jsDelivr's, with these
// SHA-256s and sizes. Updating one means replacing its file and its
// constants here and in mapScripts, and reading its changes for anything
// that evaluates code or injects style; map.js's opening comment names the
// versions and carries their notices.
var d3Modules = []struct {
	module, version, sha256 string
	size                    int
}{
	{"d3-dispatch", "3.0.1", "94b3bbdb6b98dc1325a15762b051013e8253999b0e0436b27d1da17b952ba0af", 1901},
	{"d3-selection", "3.0.0", "45daab9cf677901bcae102f3f23ca2930db3c0fb8ff9e3dbed087d9c4de921ca", 13522},
	{"d3-timer", "3.0.1", "911ceda305f014b6b53ca68d5c896a9a387da120cfd56a421a2c60cca2fc9b36", 1947},
	{"d3-color", "3.1.0", "a12639010163230b8c130fbeb92a3a49bb5f6989a566d3664d759522db458489", 10577},
	{"d3-interpolate", "3.0.1", "bfc321e4c3f3b3aadc88cfe15ccb5e443abfeadef8b75c65b41c33a4d78a98ae", 7863},
	{"d3-ease", "3.0.1", "e60a1ed750a1ad138dd18e8d3f463238113cfbf7d89685a13d19bd4e048dc3ce", 3173},
	{"d3-transition", "3.0.1", "c248d97f6c131af7f4a12a3ff1f2ed83f1cc6c0e17fc764a198b6ee452e6621b", 11718},
	{"d3-drag", "3.0.0", "158499fa8cdeef459e2ab9d01af22babd003c423a212fb24088c0f838864012b", 4186},
	{"d3-zoom", "3.0.0", "4fdce9b830b78225c58e27258296084253c41a5eaf8cad9bd9ccfe3025daf1cf", 9984},
	{"d3-quadtree", "3.0.1", "57e2ad12824ed82893ba447523f2a2fb9beeb9222aafb2c778a9f5b313348b0e", 5279},
}

// TestD3IsPinned: each vendored d3 module is the file npm publishes, under
// its upstream banner, evaluating no code.
func TestD3IsPinned(t *testing.T) {
	for _, m := range d3Modules {
		t.Run(m.module, func(t *testing.T) {
			body, err := fs.ReadFile(files, "static/"+m.module+"-"+m.version+".min.js")
			require.NoError(t, err)
			sum := sha256.Sum256(body)
			assert.Equal(t, m.sha256, hex.EncodeToString(sum[:]))
			assert.Len(t, body, m.size)
			assert.True(t, strings.HasPrefix(string(body), "// https://d3js.org/"+m.module+"/ v"+m.version+" "),
				"its upstream banner")
			for _, banned := range []string{"eval(", "Function("} {
				assert.NotContains(t, string(body), banned)
			}
		})
	}
}

// TestMapScripts: the map's page loads the d3 modules in an order where
// each comes after those it reads from the d3 global as it loads, then
// map.js; static/ holds those files, htmx's, actions.js and app.css, and
// nothing else.
func TestMapScripts(t *testing.T) {
	want := make([]string, 0, len(d3Modules)+1)
	for _, m := range d3Modules {
		want = append(want, m.module+"-"+m.version+".min.js")
	}
	want = append(want, "map.js")
	assert.Equal(t, want, mapScripts)

	entries, err := fs.ReadDir(files, "static")
	require.NoError(t, err)
	static := make([]string, 0, len(entries))
	for _, e := range entries {
		static = append(static, e.Name())
	}
	assert.ElementsMatch(t, append([]string{"app.css", "actions.js", "htmx-" + htmxVersion + ".min.js"}, want...), static)
}

// mapScriptLines is the longest map.js may be. It holds the two views,
// the panel, the search and the controller the prototype built, in the
// repository's style, in one file: assets are served under hashed names,
// which rules out imports between them. A larger map.js is a reason to cut
// code, not to raise the cap (docs/decisions.md, "Dashboard: the interest
// map").
const (
	mapScriptLines   = 1500
	mapScriptColumns = 120
)

// mapBanned is what map.js's code (its comments aside) never uses: nothing
// that evaluates code or writes markup, sets a timer, sends anything or
// loosens fetch, keeps state in the browser, styles inline, reaches a
// global through window, logs, or holds an address of its own, which come
// from its root's data attributes.
var mapBanned = []string{"eval", "Function(", "innerHTML", "outerHTML", "insertAdjacentHTML", "document.write",
	"DOMParser", "createContextualFragment", ".html(", "setTimeout", "setInterval", "import(", "XMLHttpRequest",
	"WebSocket", "EventSource", "sendBeacon", "localStorage", "sessionStorage", "indexedDB", "document.cookie",
	"window.", "globalThis", "console.", "debugger", "alert(", "confirm(", "prompt(", "javascript:", "unload",
	".style", "method:", "mode:", "credentials:", "'POST'", "'PUT'", "'PATCH'", "'DELETE'", "/ui/", "/v1/", "http:",
	"https:"}

// mapBannedRE are the banned forms a string can't name: a style or an
// event-handler attribute set, a handler property assigned, and location
// read or written (the address changes through history.replaceState).
var mapBannedRE = []*regexp.Regexp{
	regexp.MustCompile(`setAttribute\(\s*['"](style|on)`),
	regexp.MustCompile(`\.on[a-z]+\s*=[^=]`),
	regexp.MustCompile(`\blocation\s*[.\[=]`),
}

// mapProblems lists what in map.js's source breaks its rules.
func mapProblems(src string) []string {
	var problems []string
	for i, line := range strings.Split(strings.TrimSuffix(src, "\n"), "\n") {
		if n := len([]rune(line)); n > mapScriptColumns {
			problems = append(problems, fmt.Sprintf("line %d: %d columns", i+1, n))
		}
	}
	code := strings.TrimSpace(jsCode(src))
	if !strings.HasPrefix(code, "(function () {\n  'use strict';\n") {
		problems = append(problems, "not an IIFE, strict from its first line")
	}
	if !strings.HasSuffix(code, "\n})();") {
		problems = append(problems, "something after the IIFE")
	}
	for _, banned := range mapBanned {
		if strings.Contains(code, banned) {
			problems = append(problems, "uses "+banned)
		}
	}
	for _, re := range mapBannedRE {
		if m := re.FindString(code); m != "" {
			problems = append(problems, "uses "+m)
		}
	}
	if n := strings.Count(code, "fetch("); n != 1 {
		problems = append(problems, strconv.Itoa(n)+" fetch( calls, not one")
	}
	for _, required := range []string{"AbortSignal.timeout(", "textContent", "history.replaceState("} {
		if !strings.Contains(code, required) {
			problems = append(problems, "no "+required)
		}
	}
	return problems
}

// TestMapScript: map.js is one strict IIFE, at most mapScriptLines lines
// of at most mapScriptColumns columns, that makes one read, with a
// deadline, writes text, and keeps the address by replaceState; and,
// outside its comments, uses nothing mapBanned names. Like actions.js, it
// has no string or regexp holding a comment's opening, which jsCode would
// strip.
func TestMapScript(t *testing.T) {
	src, err := fs.ReadFile(files, "static/map.js")
	require.NoError(t, err)
	js := string(src)
	assert.LessOrEqual(t, strings.Count(js, "\n"), mapScriptLines, "lines")
	assert.Empty(t, mapProblems(js))
	assert.Contains(t, jsCode(js), "const fetchDeadlineMs = 30000;", "under the daemon's 2-minute write timeout")

	ok := "(function () {\n  'use strict';\n  fetch(src, {signal: AbortSignal.timeout(1)});\n" +
		"  node.textContent = t;\n  history.replaceState(null, '', page);\n})();\n"
	require.Empty(t, mapProblems(ok))
	inject := func(code string) string {
		return strings.Replace(ok, "  node.textContent", code+"\n  node.textContent", 1)
	}
	for _, bad := range append(mapBanned, "el.setAttribute('style', x);", "el.setAttribute( \"onclick\", x);",
		"el.onclick = f;", "location.href = x;", "location['href']", "location = x;", "fetch(other);") {
		assert.NotEmpty(t, mapProblems(inject("  x("+bad+");")), "caught in code: %s", bad)
		assert.Empty(t, mapProblems(inject("  // "+strings.ReplaceAll(bad, "\n", " "))), "allowed in a comment: %s", bad)
	}
	for _, bad := range []string{"var x = 1;\n" + ok, ok + "x();\n", "(function () {\n  x();\n})();\n",
		strings.Replace(ok, "AbortSignal.timeout(1)", "deadline", 1), strings.Replace(ok, "textContent", "text", 1),
		strings.Replace(ok, "history.replaceState(", "history.pushState(", 1), inject("  // " + strings.Repeat("x", 120))} {
		assert.NotEmpty(t, mapProblems(bad), bad)
	}
	assert.Empty(t, mapProblems(inject("  x.onclick === f;")), "a comparison is no assignment")
}

// mapConstantRE finds a numeric constant of map.js.
var mapConstantRE = regexp.MustCompile(`(?m)^  const ([A-Za-z]+) = ([0-9]+);`)

// TestMapScript_Palette: map.js's palette, so many hue families in so
// many tiers, is app.css's --area-N tokens, one for each slot, and a
// swatch class for each.
func TestMapScript_Palette(t *testing.T) {
	src, err := fs.ReadFile(files, "static/map.js")
	require.NoError(t, err)
	consts := map[string]int{}
	for _, m := range mapConstantRE.FindAllStringSubmatch(string(src), -1) {
		n, err := strconv.Atoi(m[2])
		require.NoError(t, err)
		consts[m[1]] = n
	}
	require.Equal(t, 10, consts["paletteFamilies"])
	require.Equal(t, 3, consts["paletteTiers"])
	slots := consts["paletteFamilies"] * consts["paletteTiers"]

	rules := cssRules(t, stylesheet(t))
	light := declarations(ruleFor(t, rules, "", ":root").body)
	var tokens []string
	for name := range light {
		if strings.HasPrefix(name, "--area-") {
			tokens = append(tokens, name)
		}
	}
	assert.Len(t, tokens, slots, "a token for each slot")
	for s := range slots {
		assert.Contains(t, light, "--area-"+strconv.Itoa(s))
		swatch := declarations(ruleFor(t, rules, "", ".a"+strconv.Itoa(s)).body)
		assert.Equal(t, "var(--area-"+strconv.Itoa(s)+")", swatch["background"])
	}
}

// TestMapScript_Notices: map.js's opening comment names every vendored
// module at its version and carries d3's ISC notice and d3-ease's BSD
// 3-Clause notice, which asks for its notice where a binary ships it.
func TestMapScript_Notices(t *testing.T) {
	src, err := fs.ReadFile(files, "static/map.js")
	require.NoError(t, err)
	head, _, found := strings.Cut(string(src), "*/")
	require.True(t, found)
	head = strings.Join(strings.Fields(head), " ")
	for _, m := range d3Modules {
		assert.Contains(t, head, m.module+" "+m.version)
	}
	for _, sentence := range []string{
		"Copyright 2010-2021 Mike Bostock (d3-color: Copyright 2010-2022 Mike Bostock)",
		"Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is " +
			"hereby granted, provided that the above copyright notice and this permission notice appear in all copies.",
		"THE SOFTWARE IS PROVIDED \"AS IS\" AND THE AUTHOR DISCLAIMS ALL WARRANTIES",
		"Copyright 2001 Robert Penner All rights reserved.",
		"Redistributions of source code must retain the above copyright notice, this list of conditions and the " +
			"following disclaimer.",
		"Redistributions in binary form must reproduce the above copyright notice, this list of conditions and the " +
			"following disclaimer in the documentation and/or other materials provided with the distribution.",
		"Neither the name of the author nor the names of contributors may be used to endorse or promote products " +
			"derived from this software without specific prior written permission.",
		"THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS \"AS IS\"",
	} {
		assert.Contains(t, head, sentence)
	}
}
