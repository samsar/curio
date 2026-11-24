package ui

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// askScriptLines and askScriptColumns bound ask.js, in the repository's
// style: the switch, the stream and the answer's every event took 450
// lines; the searches aborted as Ask is entered, the words #results shows
// followed as searches land, the mode's words given as the script starts,
// and Go's words, keyed, for every way an ask ends without its done event
// and for the evidence route's, took 43 more, and telling a landed search
// by its sender and leaving Ask's history entry to #results 5 more;
// stopping an ask through end, on a note of its own when Ask is left,
// took 3 fewer, and the cap leaves 5 for a fix. A larger ask.js is a
// reason to cut code, not to raise the cap.
const (
	askScriptLines   = 500
	askScriptColumns = 120
)

// askBanned is what ask.js's code (its comments aside) never uses, beside
// scriptBanned but its "eval", which askEvalRE bans as a word, so that the
// retrieval event's name passes: nothing that sends anything but its one
// POST, keeps state in the browser, reaches a global through window, logs,
// styles inline, navigates, draws a frame on a timer, or holds an address
// of its own, which come from its shell's data attributes.
var askBanned = append(slices.DeleteFunc(slices.Clone(scriptBanned), func(s string) bool { return s == "eval" }),
	"WebSocket", "EventSource", "sendBeacon", "indexedDB", "document.cookie", "window.", "globalThis", "console.",
	"debugger", "alert(", "confirm(", "prompt(", ".style", "pushState", "setHTML", "requestAnimationFrame",
	"'GET'", "'PUT'", "'PATCH'", "'DELETE'", "/ui/", "/v1/", "http:", "https:")

// askEvalRE is eval as a word.
var askEvalRE = regexp.MustCompile(`\beval\b`)

// askDaemonTextRE finds a read of words the daemon or the browser wrote,
// which ask.js never shows: an error event's error, an event's reason, a
// problem's detail or title, an error's message, the degraded search's
// warnings, a response's status text or body as text. htmx's event.detail
// is the event's own, and an unreadable page's reason is docmeta's words
// for its state, which every list of the dashboard shows.
var askDaemonTextRE = regexp.MustCompile(`(\.(error|detail|title|message|warnings|statusText)|\be\.reason)\b|` +
	`\.text\(\)|String\(err`)

// askRequired is what ask.js's code must hold: its idle bound, through
// AbortSignal.timeout alone, the CLI's line cap and its own total cap, an
// abortable request, text alone, escaped IDs, the address kept by
// replaceState, stored strings in their own direction, htmx's searches
// dropped in Ask mode and those in flight aborted as it is entered (htmx's
// body listener reads the event's detail, which must not be null), the
// words searched again through the box's own trigger, the submit heard
// before htmx hears it, the mode's words given as the script starts, and
// an ask that ends without its done event named by the request's ID and,
// refused with a 503, told starting by the problem type the shell holds.
// askSenders holds the words #results shows, followed as the box's and
// the form's searches land.
var askRequired = []string{"AbortSignal.timeout(idleMs)", "const idleMs = 30000;", "const maxLineChars = 1048576;",
	"const maxStreamBytes = 4194304;", "new AbortController()", ".abort(", "textContent", "encodeURIComponent(",
	"history.replaceState(history.state,", "dir: 'auto'", "'htmx:confirm'",
	"page.dispatchEvent(new CustomEvent('htmx:abort', {bubbles: true, detail: {}}));", "'curio:search'",
	"stopPropagation()", "}, {capture: true});", "words(page.dataset.mode);", "resp.headers.get('X-Request-Id')",
	"shell.dataset.startingType"}

// askSenders are the listeners through which ask.js follows the words
// #results shows: the words of the box's or the form's search as it is
// sent, and those of the last to land. Each tells the box's and the
// form's requests by the element that sent them, detail.requestConfig.elt,
// never detail.elt: htmx gives every event the element it is dispatched
// on as detail.elt, the sender for htmx:beforeRequest but each element
// swapped in for htmx:afterSwap, never the box or the form, so a listener
// reading it there never hears a search land, and leaving Ask searches
// again words #results shows, or none when it should.
var askSenders = []string{
	"document.addEventListener('htmx:beforeRequest', function (event) {",
	"document.addEventListener('htmx:afterSwap', function (event) {",
}

// askProblems lists what in ask.js's source breaks its rules.
func askProblems(src string) []string {
	var problems []string
	if n := strings.Count(src, "\n"); n > askScriptLines {
		problems = append(problems, fmt.Sprintf("%d lines", n))
	}
	for i, line := range strings.Split(strings.TrimSuffix(src, "\n"), "\n") {
		if n := len([]rune(line)); n > askScriptColumns {
			problems = append(problems, fmt.Sprintf("line %d: %d columns", i+1, n))
		}
	}
	code := strings.TrimSpace(jsCode(src))
	if !strings.HasPrefix(code, "(function () {\n  'use strict';\n") || !strings.HasSuffix(code, "\n})();") {
		problems = append(problems, "not one IIFE, strict from its first line")
	}
	for _, banned := range askBanned {
		if strings.Contains(code, banned) {
			problems = append(problems, "uses "+banned)
		}
	}
	for _, re := range append([]*regexp.Regexp{askEvalRE}, mapBannedRE...) {
		if m := re.FindString(code); m != "" {
			problems = append(problems, "uses "+m)
		}
	}
	if m := askDaemonTextRE.FindString(strings.ReplaceAll(code, "event.detail.", "event.htmx.")); m != "" {
		problems = append(problems, "shows the daemon's or the browser's words: "+m)
	}
	if n := strings.Count(code, "'POST'"); n != 1 {
		problems = append(problems, strconv.Itoa(n)+" 'POST', not one")
	}
	for _, e := range askEntries {
		calls := askCalls(code, e.name)
		if within := askCalls(askBlock(code, e.within), e.name); len(calls) != 1 || len(within) != 1 {
			problems = append(problems, fmt.Sprintf("%d calls of %s(, not one, in %s", len(calls), e.name, e.within))
		}
	}
	for _, required := range askRequired {
		if !strings.Contains(code, required) {
			problems = append(problems, "no "+required)
		}
	}
	for _, head := range askSenders {
		switch block := askBlock(code, head); {
		case block == "":
			problems = append(problems, "no "+head)
		case !strings.Contains(block, "event.detail.requestConfig.elt") || strings.Contains(block, "event.detail.elt"):
			problems = append(problems, "not told the sender by detail.requestConfig.elt: "+head)
		}
	}
	return append(problems, askEndProblems(code)...)
}

// askEndProblems lists what in ask.js's code breaks the rules that keep
// one ask in flight at a time and give the page back to Search, each read
// in the block it belongs to:
//   - ask opens by stopping the ask in flight, before anything of its own
//     is shown or made current, or two asks stream into one answer;
//   - setMode's way back to Search, after its branch for Ask, stops the
//     ask in flight, whatever the page held, or the writer keeps writing
//     for a page that shows none of it, on the note that says so, or a
//     return to Ask shows half an answer as though it were whole;
//   - it puts back the address #results had, or Ask's stays the address
//     of a search's results;
//   - it searches the box's words only under the guard that they aren't
//     those #results shows, or every return to Search searches again, in
//     a history entry of its own;
//   - end passes over a run no longer in flight, or an ask stopped or
//     replaced is ended again by its own late failure, on a second note
//     or over the next ask's answer;
//   - end lets the ask go as every way an ask ends must, its request
//     aborted and its answer no longer busy, which a screen reader would
//     otherwise wait on, and stop ends the ask through end, or lets it go
//     so itself.
func askEndProblems(code string) []string {
	var problems []string
	if body := askBody(askBlock(code, "function ask(")); len(body) == 0 || body[0] != "stop();" {
		problems = append(problems, "ask doesn't open by stopping the ask in flight")
	}
	back, ok := askAfter(askBlock(code, "function setMode("), "    ", "if (mode === 'ask') {")
	if !ok {
		problems = append(problems, "setMode has no branch for Ask before its way back to Search")
	}
	switch stops := askCalls(back, "stop"); {
	case len(stops) != 1:
		problems = append(problems, fmt.Sprintf("leaving Ask makes %d calls of stop(, not one", len(stops)))
	case !strings.Contains(back, "stop(note('stopped'));"):
		problems = append(problems, "leaving Ask stops the ask in flight without the note that says so")
	case !slices.ContainsFunc(strings.Split(back, "\n"), func(l string) bool { return strings.HasPrefix(l, "    stop(") }):
		problems = append(problems, "leaving Ask stops the ask in flight only under a condition")
	}
	if !askLeftRE.MatchString(back) {
		problems = append(problems, "leaving Ask doesn't put back the address #results had")
	}
	if !askGuarded(back, "'curio:search'", askSearchedRE) {
		problems = append(problems, "leaving Ask searches the box's words without the guard that #results "+
			"doesn't show them")
	}
	if body := askBody(askBlock(code, "function end(")); len(body) < 2 || body[0] != "if (current !== run) {" ||
		body[1] != "return;" {
		problems = append(problems, "end doesn't open by passing over a run no longer in flight")
	}
	if !askLetsGo(askBlock(code, "function end(")) {
		problems = append(problems, "end doesn't abort the ask's request and leave its answer no longer busy")
	}
	if stop := askBlock(code, "function stop("); !askEndRE.MatchString(stop) && !askLetsGo(stop) {
		problems = append(problems, "stop neither ends the ask through end nor lets it go itself")
	}
	return problems
}

var (
	// askAbortRE and askUnbusyRE are an ask's request aborted and its
	// answer made no longer busy, the two things every way an ask ends
	// must do.
	askAbortRE  = regexp.MustCompile(`\.controller\.abort\(\)`)
	askUnbusyRE = regexp.MustCompile(`\banswer\.removeAttribute\('aria-busy'\)`)
	// askEndRE is stop ending the ask in flight through end.
	askEndRE = regexp.MustCompile(`(^|[^\w.$])end\(current\b`)
	// askLeftRE is the address #results had put back.
	askLeftRE = regexp.MustCompile(`(^|[^\w.$])replaceAddress\(left\)`)
	// askSearchedRE is the guard that the box's words aren't those #results
	// shows, either way round, and nothing else.
	askSearchedRE = regexp.MustCompile(`^if \((box\.value !== searched|searched !== box\.value)\) \{$`)
)

// askLetsGo reports whether block aborts an ask's request and leaves its
// answer no longer busy.
func askLetsGo(block string) bool {
	return askAbortRE.MatchString(block) && askUnbusyRE.MatchString(block)
}

// askGuarded reports whether the first line of code holding what is
// inside the statement that guard matches: the nearest line above it of a
// smaller indent, trimmed, which encloses it.
func askGuarded(code, what string, guard *regexp.Regexp) bool {
	lines := strings.Split(code, "\n")
	at := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, what) })
	if at < 0 {
		return false
	}
	indent := askIndent(lines[at])
	for i := at - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" && askIndent(lines[i]) < indent {
			return guard.MatchString(strings.TrimSpace(lines[i]))
		}
	}
	return false
}

// askIndent is how many spaces line opens with.
func askIndent(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

// askBody is the statements of block, a function's, between its head and
// its closing line, trimmed, its blank lines (a comment's, once jsCode has
// taken it out) left out.
func askBody(block string) []string {
	lines := strings.Split(block, "\n")
	if len(lines) < 2 {
		return nil
	}
	var body []string
	for _, line := range lines[1 : len(lines)-1] {
		if line = strings.TrimSpace(line); line != "" {
			body = append(body, line)
		}
	}
	return body
}

// askAfter is what follows, in block, the statement that opens with
// indent and head, through block's end; ok is false when none opens so.
func askAfter(block, indent, head string) (string, bool) {
	lines := strings.Split(block, "\n")
	_, to, ok := askStatement(lines, indent, head)
	if !ok {
		return "", false
	}
	return strings.Join(lines[to+1:], "\n"), true
}

// askEntries hold ask.js to one way to the model: the form's submit,
// heard before htmx hears it, alone calls ask, which alone calls read,
// which alone fetches. So nothing asks as the page loads, which would let
// another site's link to an address in Ask mode load the writer's model,
// nor on any event but the person's Enter.
var askEntries = []struct{ name, within string }{
	{"ask", "document.addEventListener('submit', function (event) {"},
	{"read", "function ask("},
	{"fetch", "async function read("},
}

// askCalls are the calls of the function name in code: name( where no
// identifier or member access precedes it (reader.read() is the stream's),
// nor the function keyword, which defines it.
func askCalls(code, name string) []string {
	var calls []string
	for _, m := range regexp.MustCompile(`(?:^|[^\w.$])(function\s+)?`+name+`\(`).FindAllStringSubmatch(code, -1) {
		if m[1] == "" {
			calls = append(calls, m[0])
		}
	}
	return calls
}

// askBlock is the top-level statement of ask.js's IIFE that opens with
// head, through the line that closes it (askStatement, at the IIFE's
// indent). "" when no statement opens so.
func askBlock(code, head string) string {
	lines := strings.Split(code, "\n")
	if from, to, ok := askStatement(lines, "  ", head); ok {
		return strings.Join(lines[from:to+1], "\n")
	}
	return ""
}

// askStatement is where the statement of lines that opens with indent and
// head begins and ends: the line that closes it is the first after it
// that opens with indent and a "}". ok is false when no statement opens
// so.
func askStatement(lines []string, indent, head string) (from, to int, ok bool) {
	for i, line := range lines {
		if !strings.HasPrefix(line, indent+head) {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], indent+"}") {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

// askMutate is code with from replaced by to in the top-level statement
// that opens with head (askBlock), "" when that statement doesn't hold
// from.
func askMutate(code, head, from, to string) string {
	block := askBlock(code, head)
	if block == "" || !strings.Contains(block, from) {
		return ""
	}
	return strings.Replace(code, block, strings.Replace(block, from, to, 1), 1)
}

// askEndMutations break one rule of askEndProblems each, by a change in
// the block that opens with head.
var askEndMutations = []struct{ name, head, from, to string }{
	{"a new ask that doesn't stop the last", "function ask(", "    stop();\n", ""},
	{"leaving Ask with the answer still coming", "function setMode(", "    stop(note('stopped'));\n", ""},
	{"leaving Ask without the note", "function setMode(", "stop(note('stopped'))", "stop()"},
	{"leaving Ask with Ask's address kept", "function setMode(", "replaceAddress(left);", ""},
	{"leaving Ask always searching", "function setMode(", "box.value !== searched", "true"},
	{"leaving Ask with a guard that always passes", "function setMode(", "box.value !== searched",
		"box.value !== searched || true"},
	{"leaving Ask stopping the ask only under a condition", "function setMode(", "    stop(note('stopped'));\n",
		"    if (left !== '') {\n      stop(note('stopped'));\n    }\n"},
	{"an ask ended twice", "function end(", "    if (current !== run) {\n      return;\n    }\n", ""},
	{"an ended ask left busy", "function end(", "answer.removeAttribute('aria-busy');", ""},
	{"an ended ask's request left going", "function end(", "run.controller.abort();", ""},
	{"a stopped ask forgotten, still going and busy", "function stop(", "end(current, lines);",
		"current = null;"},
	{"a stopped ask aborted but left busy", "function stop(", "end(current, lines);",
		"current.controller.abort();"},
}

// TestAskScript: ask.js is one strict IIFE, at most askScriptLines lines
// of at most askScriptColumns columns, that makes one kind of request, a
// POST with a deadline between its lines, sent only from the form's
// submit (askEntries), writes text, keeps the address by replaceState and
// hears the form's submit first; keeps one ask in flight at a time and
// gives the page back to Search as askEndProblems says, which each of
// askEndMutations, made in ask.js and in a sample, fails; and, outside its
// comments, uses nothing askBanned names. Like actions.js, it has no
// string or regexp holding a comment's opening, which jsCode would strip.
func TestAskScript(t *testing.T) {
	src, err := fs.ReadFile(files, "static/ask.js")
	require.NoError(t, err)
	assert.Empty(t, askProblems(string(src)))

	const submit = "  document.addEventListener('submit', function (event) {\n    ask(box.value);\n  }, {capture: true});\n"
	const landed = "    if ([box, form].includes(event.detail.requestConfig.elt)) {\n      searched = sending;\n    }\n"
	const ends = "  function setMode(mode) {\n    if (mode === 'ask') {\n      left = document.URL;\n      return;\n" +
		"    }\n    stop(note('stopped'));\n    replaceAddress(left);\n    if (box.value !== searched) {\n" +
		"      box.dispatchEvent(new CustomEvent('curio:search'));\n    }\n  }\n" +
		"  function stop(...lines) {\n    if (current !== null) {\n      end(current, lines);\n    }\n  }\n" +
		"  function end(run, lines) {\n    if (current !== run) {\n      return;\n    }\n" +
		"    run.controller.abort();\n    answer.removeAttribute('aria-busy');\n  }\n"
	ok := "(function () {\n  'use strict';\n" + strings.Join(askRequired, "\n") + "\n" + ends +
		"  function ask(question) {\n    stop();\n    read(question);\n  }\n" +
		"  async function read(question) {\n    await reader.read();\n    fetch(src, {method: 'POST'});\n  }\n" +
		submit + "  " + askSenders[0] + "\n    if ([box, form].includes(event.detail.requestConfig.elt)) {\n" +
		"      sending = box.value;\n    }\n  });\n  " + askSenders[1] + "\n" + landed + "  });\n" +
		"  const events = ['retrieval'];\n})();\n"
	require.Empty(t, askProblems(ok))
	inject := func(code string) string { return strings.Replace(ok, "  const events", code+"\n  const events", 1) }
	for _, bad := range []string{"x.innerHTML = y;", "setTimeout(f, 1);", "fetch(other);", "history.pushState(s, '', a);",
		"fetch('/v1/ask');", "location.href = x;", "el.style.x = 1;", "new URLSearchParams({q: q, mode: 'ask'});",
		"eval(x);", "x.onclick = f;", "el.setAttribute('onclick', x);", "window.open(x);",
		// An ask as the page loads, which another site's link could make.
		"if (page.dataset.mode === 'ask' && box.value.trim() !== '') { ask(box.value.trim()); }",
		"document.addEventListener('DOMContentLoaded', () => ask(box.value));", "read(box.value);",
		"Promise.resolve().then(() => read(box.value));",
		// The daemon's or the browser's words.
		"end(run, [e.error]);", "end(run, [e.reason]);", "end(run, [problem.detail || problem.title]);",
		"end(run, [err.message]);", "end(run, [String(err)]);", "end(run, ['HTTP ' + resp.statusText]);",
		"end(run, [await resp.text()]);", "end(run, e.warnings);"} {
		assert.NotEmpty(t, askProblems(inject("  "+bad)), "caught in code: %s", bad)
		assert.Empty(t, askProblems(inject("  // "+bad)), "allowed in a comment: %s", bad)
	}
	for _, bad := range []string{"var x = 1;\n" + ok, ok + "x();\n", strings.ReplaceAll(ok, "{capture: true}", "true"),
		strings.Replace(ok, "AbortSignal.timeout(idleMs)", "deadline", 1),
		strings.Replace(ok, "'POST'", "'GET'", 1), inject("  // " + strings.Repeat("x", askScriptColumns)),
		inject(strings.Repeat("\n", askScriptLines)),
		// The submit's ask moved out of its listener, and the fetch out of read.
		strings.Replace(ok, submit, "", 1) + "  ask(box.value);\n",
		strings.Replace(strings.Replace(ok, "    fetch(src, {method: 'POST'});\n", "", 1), "  const events",
			"  fetch(src, {method: 'POST'});\n  const events", 1),
		// A search landing told by the element swapped in, which is never the
		// box or the form, or not followed at all.
		strings.Replace(ok, landed, "    if (event.detail.elt === box || event.detail.elt === form) {\n"+
			"      searched = sending;\n    }\n", 1),
		strings.Replace(ok, landed, "    if ([box, form].includes(event.detail.elt)) {\n      searched = sending;\n    }\n", 1),
		strings.Replace(ok, askSenders[1], "  document.addEventListener('htmx:afterSettle', function (event) {", 1)} {
		assert.NotEmpty(t, askProblems(bad), bad)
	}
	for _, m := range askEndMutations {
		for name, code := range map[string]string{"ask.js": string(src), "the sample": ok} {
			bad := askMutate(code, m.head, m.from, m.to)
			require.NotEmpty(t, bad, "%s: %s's %s holds %q", m.name, name, m.head, m.from)
			assert.NotEmpty(t, askEndProblems(jsCode(bad)), "%s, in %s", m.name, name)
		}
	}
}

// askNoteKeyRE finds a note ask.js names by its key: note('key'), or
// fail(run, 'key'), which shows it.
var askNoteKeyRE = regexp.MustCompile(`(?:\bnote\(|\bfail\(run, )'([a-z_]+)'\)`)

// TestAskScript_NotesWorded: every note ask.js names by its key is one
// the shell words, an ask's status or one of askNotes, so none it shows
// is empty.
func TestAskScript_NotesWorded(t *testing.T) {
	src, err := fs.ReadFile(files, "static/ask.js")
	require.NoError(t, err)
	code := jsCode(string(src))
	worded := append(slices.Clone(sampleStatuses), askNotes...)
	named := askNoteKeyRE.FindAllStringSubmatch(code, -1)
	require.NotEmpty(t, named)
	for _, m := range named {
		assert.Contains(t, worded, m[1], "the shell words %s", m[1])
	}
	assert.True(t, strings.Contains(code, "note('stopped')"), "an answer left for Search ends on its note")
}

// TestAskShell_OneAnnouncement: wherever the search page holds the answer's
// shell, it is outside #results, whose live region would read an answer
// out piece by piece, and the only element in it that announces is
// #ask-status, the end of an answer; each note ask.js may show is a
// template of words, keyed.
func TestAskShell_OneAnnouncement(t *testing.T) {
	r := newRenderer(t)
	shells := 0
	for _, data := range allSamples(t, r)[PageSearch] {
		s := data.(Search)
		doc := parse(t, render(t, r, PageSearch, s))
		shell := byID(doc, "ask")
		if s.Shell() == nil {
			assert.Nil(t, shell, "a page that can't ask has no shell")
			assert.Empty(t, withClass(doc, "search-modes"), "nor a switch")
			continue
		}
		shells++
		require.NotNil(t, shell)
		for p := shell.Parent; p != nil; p = p.Parent {
			assert.NotEqual(t, "results", attrValue(p, "id"), "the shell is outside #results")
		}
		var live []string
		for n := range shell.Descendants() {
			if n.Type == html.ElementNode && (hasAttr(n, "aria-live") || attrValue(n, "role") == "status" ||
				attrValue(n, "role") == "alert") {
				live = append(live, attrValue(n, "id"))
			}
		}
		assert.Equal(t, []string{"ask-status"}, live)
		status := byID(doc, "ask-status")
		assert.Equal(t, "polite", attrValue(status, "aria-live"))
		assert.Equal(t, "action-status", attrValue(status, "class"))
		keys := map[string]string{}
		for _, n := range withAttr(doc, "data-note") {
			assert.Equal(t, "template", n.Data)
			keys[attrValue(n, "data-note")] = textOf(n)
		}
		for _, key := range append(slices.Clone(sampleStatuses), askNotes...) {
			assert.NotEmpty(t, keys[key], "a note for %s", key)
		}
		assert.Equal(t, startingProblem, attrValue(shell, "data-starting-type"))
	}
	assert.Equal(t, 3, shells, "the samples that can ask")
}

// TestSearch_ModeSwitch: a page that can ask has the switch, in its mode,
// each button carrying the words of its own mode, and the box and its
// button Search's words in either mode, since without the script, which
// gives them Ask's, Enter searches; the box searches as it is typed in,
// or when ask.js leaves Ask mode with other words.
func TestSearch_ModeSwitch(t *testing.T) {
	r := newRenderer(t)
	for _, tc := range []struct {
		mode               string
		label, placeholder string
	}{
		{ModeSearch, "Search", "Search your 12 documents"},
		{ModeAsk, "Search", "Search your 12 documents"},
	} {
		s := Search{Layout: Layout{Nav: NavSearch}, Mode: tc.mode, Home: &SearchHome{Searchable: 12},
			Asking: sampleAsking(AskCan)}
		doc := parse(t, render(t, r, PageSearch, s))
		page := withClass(doc, "search-page")
		require.Len(t, page, 1)
		assert.Equal(t, tc.mode, attrValue(page[0], "data-mode"))
		for _, toggle := range []struct{ id, mode, label, placeholder string }{
			{"mode-search", ModeSearch, "Search", "Search your 12 documents"},
			{"mode-ask", ModeAsk, "Ask", "Ask your library a question"},
		} {
			b := byID(doc, toggle.id)
			require.NotNil(t, b, toggle.id)
			assert.Equal(t, "button", attrValue(b, "type"))
			assert.Equal(t, strconv.FormatBool(toggle.mode == tc.mode), attrValue(b, "aria-pressed"))
			assert.Equal(t, toggle.label, attrValue(b, "data-label"))
			assert.Equal(t, toggle.placeholder, attrValue(b, "data-placeholder"))
			assert.Equal(t, toggle.mode, attrValue(b, "data-mode"))
		}
		group := byID(doc, "mode-search").Parent
		assert.Equal(t, "group", attrValue(group, "role"))
		assert.Equal(t, "segmented search-modes js-only", attrValue(group, "class"))
		q := byID(doc, "q")
		assert.Equal(t, tc.placeholder, attrValue(q, "placeholder"))
		assert.Equal(t, "input changed delay:400ms, curio:search", attrValue(q, "hx-trigger"))
		submit := withAttr(doc, "aria-label")
		var button *html.Node
		for _, n := range submit {
			if attrValue(n, "type") == "submit" {
				button = n
			}
		}
		require.NotNil(t, button)
		assert.Equal(t, tc.label, attrValue(button, "aria-label"))
		assert.Equal(t, tc.label, textOf(button))
	}
	doc := parse(t, render(t, r, PageSearch, Search{Layout: Layout{Nav: NavSearch}, Home: &SearchHome{}}))
	assert.Equal(t, "search", attrValue(withClass(doc, "search-page")[0], "data-mode"), "a page without Ask")
	assert.Nil(t, byID(doc, "mode-ask"))
}

// TestSearch_AskShellAddresses: the shell's addresses are the dashboard's
// own, built in Go: where it asks, the search page it keeps its address
// on, and the prefixes of the pages it links.
func TestSearch_AskShellAddresses(t *testing.T) {
	shell := Search{Asking: sampleAsking(AskCan)}.Shell()
	require.NotNil(t, shell)
	assert.Equal(t, "/v1/ask", shell.Src())
	assert.Equal(t, navHref(NavSearch), shell.PageHref())
	assert.Equal(t, documentHref(""), shell.DocumentPrefix())
	assert.Equal(t, interestHref(""), shell.InterestPrefix())
	assert.Equal(t, unsortedHref(1, ""), shell.UnsortedPage())
}

// TestSearch_AskOffNote: a page whose address asked for Ask mode but that
// can't ask says why in its kind's words, and none of the daemon's; one
// that can, or whose address didn't ask, says nothing.
func TestSearch_AskOffNote(t *testing.T) {
	for off, want := range map[AskOff]string{
		AskOffSet: "Searched your words: Ask is off (ask.enabled: false).",
		AskOffByDefault: "Searched your words: Ask is off by default for m:1, which isn't measured for writing " +
			"answers; ask.enabled: true turns it on (then restart).",
		AskWithheld: "Searched your words: Ask would send your question and pages off this machine, or to another " +
			"provider, which generation.roles.answers doesn't name; name it there, or ask.enabled: false.",
		AskNoStream: "Searched your words: Ask is off: the answers role's provider can't stream answers.",
		AskUnfit: "Searched your words: m:1 didn't pass curio's check for writing answers; set " +
			"generation.roles.answers.model to a model that passes, or ask.enabled: false.",
	} {
		s := Search{Asked: true, Asking: &Asking{Off: off, Model: "m:1"}}
		assert.Equal(t, want, s.AskOffNote(), off)
		s.Asked = false
		assert.Empty(t, s.AskOffNote(), "%s: an address that didn't ask", off)
	}
	assert.Empty(t, Search{Asked: true, Asking: &Asking{Model: "m:1"}}.AskOffNote(), "it can ask")
	assert.Empty(t, Search{Asked: true}.AskOffNote(), "an htmx request, which reads nothing of Ask")
}

// TestAsking_Notes: the words of what an ask ends on beside its status
// are Go's alone. No saved page to answer from is said so it holds on
// every route, the evidence route's pages judged with none for or
// against the claim included, which has words of its own; a refusal of
// the question names the longest one curio takes, when the page knows it;
// an answer left for Search says what stopped it, and how to have it
// whole.
func TestAsking_Notes(t *testing.T) {
	a := Asking{Model: "m:1", MaxQuestion: 512}
	for key, want := range map[string]string{
		"no_sources": "curio found nothing in your library to answer this from.",
		askNoteUnsided: "None of the pages curio judged supports or contradicts the claim, so there is nothing to " +
			"answer from.",
		askNoteUnjudged: "The pages couldn't be judged for and against the claim, so it is answered from the pages " +
			"found.",
		askNoteFailed:   "curio failed while answering; the daemon's log says what went wrong.",
		askNoteQuestion: "curio couldn't take the question as written: a question is at most 512 bytes.",
		askNoteBusy:     "curio is answering as many questions as it takes at once; ask again once one of them ends.",
		askNoteStarting: "curio is starting; ask again in a moment.",
		askNoteRefused:  "curio didn't take the question.",
		askNoteLost: "The connection to curio was lost before the answer ended; curio status says whether the " +
			"daemon runs.",
		askNoteStopped: "Switching to Search stopped the answer before it ended; press Enter to ask again.",
		askNoteRequest: "Its request ID in the daemon's log:",
	} {
		assert.Equal(t, want, a.note(key), key)
	}
	assert.Equal(t, "curio couldn't take the question as written.", Asking{}.note(askNoteQuestion))
	assert.Empty(t, a.note("no-such-note"))
}
