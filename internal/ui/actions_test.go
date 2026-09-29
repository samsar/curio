package ui

import (
	"bytes"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui/uitest"
)

// TestActions pins every Action constructor's request: its method, path and
// body, exactly, and where it reports; one kind each, and every kind made
// by one.
func TestActions(t *testing.T) {
	for _, tc := range []struct {
		action              Action
		method, path, body  string
		field, join, status string
		kind                ActionKind
		done, doneOff       string
	}{
		{refetchAction("d1"), "POST", "/v1/documents/d1/refetch", "", "", "", documentStatus, ActionRefetch,
			"Refetch requested", ""},
		{forcedRefetchAction("d1"), "POST", "/v1/documents/d1/refetch?force=1", "", "", "", documentStatus,
			ActionForcedRefetch, "Refetch requested", ""},
		{reindexAction("d1"), "POST", "/v1/documents/d1/reindex", "", "", "", documentStatus, ActionReindex,
			"Reindex requested", ""},
		{rebuildAction(), "POST", "/v1/interests/rebuild", "", "", "", rebuildStatus, ActionRebuild, "Rebuild requested", ""},
		{pauseAction(), "PUT", "/v1/queue", `{"paused":true}`, "", "", queueStatus, ActionPause, "Queue paused", ""},
		{resumeAction(), "PUT", "/v1/queue", `{"paused":false}`, "", "", queueStatus, ActionResume, "Queue resumed", ""},
		{throttleAction(store.ThrottleGentle), "PUT", "/v1/queue", `{"throttle":"gentle"}`, "", "", queueStatus,
			ActionThrottle, "Throttle set to gentle", ""},
		{keepAwakeAction(), "PUT", "/v1/queue", "", "keep_awake", "", queueStatus, ActionKeepAwake,
			"Keep awake turned on", "Keep awake turned off"},
		{scheduleAction(), "PUT", "/v1/queue", "", "", "-", queueStatus, ActionSchedule, "Schedule saved", ""},
		{scheduleOffAction(), "PUT", "/v1/queue", `{"schedule":"off"}`, "", "", queueStatus, ActionScheduleOff,
			"Schedule turned off", ""},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			a := tc.action
			body, err := a.Body()
			require.NoError(t, err)
			assert.Equal(t, []string{tc.method, tc.path, tc.body, tc.field, tc.join, tc.status},
				[]string{a.Method, a.Path, body, a.Field, a.Join, a.Status})
			assert.Equal(t, tc.kind, a.Kind)
			assert.Equal(t, []string{tc.done, tc.doneOff}, []string{a.Done, a.DoneOff})
		})
	}
	normal, err := throttleAction(store.ThrottleNormal).Body()
	require.NoError(t, err)
	assert.Equal(t, `{"throttle":"normal"}`, normal)
	assert.Len(t, ActionKinds, 10, "a kind for each constructor above")
}

// TestActions_EscapeIDs: a document's ID, however odd, is one escaped
// segment of an action's path.
func TestActions_EscapeIDs(t *testing.T) {
	for id, want := range map[string]string{
		"a/b?#":   "/v1/documents/a%2Fb%3F%23/refetch",
		evilAttr:  "/v1/documents/%22%20onerror=%22alert%281%29/refetch",
		"../../x": "/v1/documents/..%2F..%2Fx/refetch",
	} {
		assert.Equal(t, want, refetchAction(id).Path, id)
	}
	assert.Equal(t, "/v1/documents/a%2Fb/refetch?force=1", forcedRefetchAction("a/b").Path)
	assert.Equal(t, "/v1/documents/a%2Fb/reindex", reindexAction("a/b").Path)
}

// TestActions_Render: an Action's every value is escaped into its
// control's attributes, and the control is inert.
func TestActions_Render(t *testing.T) {
	set := newRenderer(t).pages[PageDocument]
	hostile := Action{Kind: evilAttr, Method: evilScript, Path: evilURL, body: map[string]any{evilAttr: evilScript},
		Field: evilQuotes, Join: evilAttr, Status: evilAttr, Done: evilScript, DoneOff: evilQuotes}
	var attrs bytes.Buffer
	require.NoError(t, set.ExecuteTemplate(&attrs, "action", hostile))

	button := byID(parse(t, `<button id="control"`+attrs.String()+">x</button>"), "control")
	require.NotNil(t, button)
	body, err := hostile.Body()
	require.NoError(t, err)
	for attr, want := range map[string]string{"data-action": evilAttr, "data-method": evilScript,
		"data-path": evilURL, "data-body": body, "data-field": evilQuotes, "data-join": evilAttr,
		"data-status": evilAttr, "data-done": evilScript, "data-done-off": evilQuotes} {
		assert.Equal(t, want, attrValue(button, attr), attr)
	}
	assert.Len(t, button.Attr, 10, "no attribute broke out")
	assert.NotEmpty(t, uitest.Problems("<button"+attrs.String()+">x</button>"),
		"uitest refuses its method and path")

	// A real one is inert.
	attrs.Reset()
	require.NoError(t, set.ExecuteTemplate(&attrs, "action", pauseAction()))
	uitest.AssertInert(t, "<button"+attrs.String()+">Pause</button>")
}

// TestActionsScript: actions.js is one strict IIFE with no globals, under
// 200 lines, and, outside its comments, uses nothing that evaluates or
// injects markup, sets a timer, loosens fetch or keeps state in the
// browser.
func TestActionsScript(t *testing.T) {
	src, err := fs.ReadFile(files, "static/actions.js")
	require.NoError(t, err)
	js := string(src)
	assert.LessOrEqual(t, strings.Count(js, "\n"), 200, "lines")

	code := strings.TrimSpace(jsCode(js))
	assert.True(t, strings.HasPrefix(code, "(function () {\n  'use strict';\n"), "an IIFE, strict from its first line")
	assert.True(t, strings.HasSuffix(code, "\n})();"), "nothing after the IIFE")
	for _, banned := range []string{"eval", "Function(", "innerHTML", "outerHTML", "insertAdjacentHTML",
		"document.write", "DOMParser", "createContextualFragment", "setTimeout", "setInterval", "mode:",
		"credentials:", "javascript:", "localStorage", "sessionStorage", "XMLHttpRequest", "import("} {
		assert.NotContains(t, code, banned)
	}
	assert.Contains(t, code, "AbortSignal.timeout(timeoutMs)", "every change has a deadline")
	assert.Contains(t, code, "const timeoutMs = 15000;")
	assert.Contains(t, code, "textContent", "it writes text")

	for bad, what := range map[string]string{
		"x.innerHTML = y;": "innerHTML",
		"/* ok */ eval(x)": "eval",
	} {
		assert.Contains(t, jsCode(bad), what, "comments aside, code is checked")
	}
	assert.NotContains(t, jsCode("// no innerHTML\n/* nor eval */ x();\n"), "innerHTML")
}

// jsCommentRE matches a JavaScript comment, block or line. actions.js has
// no string or regexp holding one's opening.
var jsCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`)

// jsCode is src without its comments, whose text may name what the code
// must not use.
func jsCode(src string) string {
	return jsCommentRE.ReplaceAllString(src, "")
}
