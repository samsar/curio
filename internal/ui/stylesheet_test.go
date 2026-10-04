package ui

import (
	"io/fs"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cssRule is one rule of a stylesheet: its selectors, its declarations,
// and the at-rule it sits in, if any.
type cssRule struct {
	selectors []string
	body      string
	at        string
}

// cssCommentRE matches a CSS comment.
var cssCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)

// cssRules lists the style rules of css, a stylesheet without braces in
// its strings, including those inside @media blocks; @keyframes are left
// out.
func cssRules(t *testing.T, css string) []cssRule {
	t.Helper()
	var rules []cssRule
	var walk func(src, at string)
	walk = func(src, at string) {
		for {
			open := strings.IndexByte(src, '{')
			if open < 0 {
				require.Empty(t, strings.TrimSpace(src), "text after the last rule")
				return
			}
			prelude := strings.TrimSpace(src[:open])
			end := matchingBrace(t, src, open)
			body := src[open+1 : end]
			switch {
			case strings.HasPrefix(prelude, "@media"):
				walk(body, prelude)
			case strings.HasPrefix(prelude, "@keyframes"):
			default:
				var selectors []string
				for s := range strings.SplitSeq(prelude, ",") {
					selectors = append(selectors, strings.Join(strings.Fields(s), " "))
				}
				rules = append(rules, cssRule{selectors: selectors, body: body, at: at})
			}
			src = src[end+1:]
		}
	}
	walk(cssCommentRE.ReplaceAllString(css, ""), "")
	return rules
}

// matchingBrace is the index of the brace closing the one at open.
func matchingBrace(t *testing.T, src string, open int) int {
	t.Helper()
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	require.Fail(t, "an unclosed brace", src[open:])
	return 0
}

// declarations are a rule's declarations, by property, whitespace
// normalized.
func declarations(body string) map[string]string {
	out := map[string]string{}
	for d := range strings.SplitSeq(body, ";") {
		prop, value, ok := strings.Cut(d, ":")
		if ok {
			out[strings.TrimSpace(prop)] = strings.Join(strings.Fields(value), " ")
		}
	}
	return out
}

// ruleFor is the rule, outside any at-rule unless at names one, whose
// selectors include selector.
func ruleFor(t *testing.T, rules []cssRule, at, selector string) cssRule {
	t.Helper()
	for _, r := range rules {
		if r.at == at && slices.Contains(r.selectors, selector) {
			return r
		}
	}
	require.Failf(t, "no rule", "%q in %q", selector, at)
	return cssRule{}
}

func stylesheet(t *testing.T) string {
	t.Helper()
	css, err := fs.ReadFile(files, "static/app.css")
	require.NoError(t, err)
	return string(css)
}

// wrapAnywhere are the only selectors that may wrap between any two
// characters: code, IDs and paths, and the titles and passages that are
// read rather than scanned. overflow-wrap: anywhere shrinks an element's
// min-content width to about one character, which squeezed the Library's
// columns to one letter when the body had it.
var wrapAnywhere = []string{
	"code", "details.more-text pre", "pre.source", "dl.facts .mono", ".doc-head h1", ".result-title a",
	".snippet", ".related a", ".bookmark-list .title", ".interest h2 a", ".cause-main h2",
}

// TestStylesheet: the rules the design language keeps, which no test of a
// page's markup can see.
func TestStylesheet(t *testing.T) {
	css := stylesheet(t)
	rules := cssRules(t, css)

	body := declarations(ruleFor(t, rules, "", "body").body)
	assert.Equal(t, "break-word", body["overflow-wrap"], "long strings wrap without shrinking columns")
	assert.Equal(t, "var(--bg)", body["background"])
	for _, r := range rules {
		if declarations(r.body)["overflow-wrap"] != "anywhere" {
			continue
		}
		for _, s := range r.selectors {
			assert.Contains(t, wrapAnywhere, s, "overflow-wrap: anywhere")
		}
	}

	assert.Equal(t, "fixed", declarations(ruleFor(t, rules, "", "table.data").body)["table-layout"])
	assert.Equal(t, "hidden", declarations(ruleFor(t, rules, "", "table.data td").body)["overflow"],
		"a cell's content stays in its column")
	for col, width := range map[string]string{"col.c-state": "7rem", "col.c-type": "6.5rem", "col.c-when": "7.5rem",
		"col.c-sim": "9.5rem", "col.c-rank": "3.5rem", "col.c-nearest": "16rem"} {
		assert.Equal(t, width, declarations(ruleFor(t, rules, "", col).body)["width"], col)
	}
	const phone = "@media (max-width: 48rem)"
	for _, folded := range []string{"col.c-type", "col.c-when", "th.c-type", "td.c-type", "th.c-when", "td.c-when",
		"col.c-nearest", "th.c-nearest", "td.c-nearest"} {
		assert.Equal(t, "none", declarations(ruleFor(t, rules, phone, folded).body)["display"], folded)
	}
	// Unsorted's nearest interest folds under the document's name, as the
	// Library's type and time do.
	assert.Equal(t, "none", declarations(ruleFor(t, rules, "", ".nearest-sub").body)["display"])
	assert.Equal(t, "5.75rem", declarations(ruleFor(t, rules, phone, "col.c-state").body)["width"])
	assert.Equal(t, "inline", declarations(ruleFor(t, rules, phone, ".doc-sub .narrow").body)["display"])
	assert.Equal(t, "none", declarations(ruleFor(t, rules, phone, ".doc-sub .wide").body)["display"],
		"a save's browser shows on wide screens only")
	fallback := declarations(ruleFor(t, rules, "", ".doc-title.from-bookmark").body)
	assert.Equal(t, "italic", fallback["font-style"], "a bookmark's title stands in for the document's")
	assert.NotContains(t, fallback, "font-family", "in the title's own font, unlike an address")
	assert.Equal(t, "italic", declarations(ruleFor(t, rules, "", ".doc-head h1.from-bookmark").body)["font-style"])
	// An interest card's untitled members, as the Library names documents.
	assert.Equal(t, "italic",
		declarations(ruleFor(t, rules, "", ".interest .members a.from-bookmark").body)["font-style"])
	assert.Equal(t, "var(--mono)",
		declarations(ruleFor(t, rules, "", ".interest .members a.untitled").body)["font-family"])
	// A search result or a related document is named the same way.
	for _, selector := range []string{".result-title a.from-bookmark", ".related a.from-bookmark"} {
		fallback := declarations(ruleFor(t, rules, "", selector).body)
		assert.Equal(t, "italic", fallback["font-style"], selector)
		assert.NotContains(t, fallback, "font-family", "%s: in the title's own font", selector)
	}
	for _, selector := range []string{".result-title a.untitled", ".related a.untitled"} {
		assert.Equal(t, "var(--mono)", declarations(ruleFor(t, rules, "", selector).body)["font-family"],
			"%s: an address in monospace", selector)
	}
	// Show scores is a checkbox the stylesheet alone reads: scores are
	// hidden until it is checked.
	assert.Equal(t, "none", declarations(ruleFor(t, rules, "", ".score").body)["display"])
	assert.Equal(t, "inline", declarations(ruleFor(t, rules, "", "body:has(#show-scores:checked) .score").body)["display"])
	assert.NotEmpty(t, declarations(ruleFor(t, rules, "", ".results-note").body), "the capped note is styled here")
	assert.Equal(t, "none", declarations(ruleFor(t, rules, "", ".load-more:empty").body)["display"])
	assert.Equal(t, "pre-wrap", declarations(ruleFor(t, rules, "", "pre.source").body)["white-space"])
	ruleFor(t, rules, "@media (max-width: 64rem)", ".header-search")
	assert.Equal(t, "100%", declarations(ruleFor(t, rules, "", ".results").body)["width"],
		"the results column never grows past the screen to fit a one-line address")
	assert.Equal(t, "100%", declarations(ruleFor(t, rules, "", ".explore-line").body)["width"],
		"the interests line never grows past the screen to fit a long name")
	explore := declarations(ruleFor(t, rules, "", ".explore-line a").body)
	assert.Equal(t, "ellipsis", explore["text-overflow"], "an interest's name never widens the page")
	assert.Equal(t, "hidden", explore["overflow"])
	assert.NotEmpty(t, explore["max-width"])
	// A label is a model's, of any length: on one line, cut, wherever a
	// line names one: the crumbs, an area card's interests, Unsorted's
	// nearest interest and every other identity a line links (.ref), the
	// document page's line, and a rebuild's error in the Interests' head.
	for _, selector := range []string{".truncate", ".interest .members a", ".ref", ".state-error"} {
		cut := declarations(ruleFor(t, rules, "", selector).body)
		assert.Equal(t, "nowrap", cut["white-space"], selector)
		assert.Equal(t, "hidden", cut["overflow"], selector)
		assert.Equal(t, "ellipsis", cut["text-overflow"], selector)
	}
	assert.Equal(t, "min(28rem, 100%)", declarations(ruleFor(t, rules, "", ".ref").body)["max-width"])
	assert.Equal(t, "100%", declarations(ruleFor(t, rules, "", ".state-error").body)["max-width"])
	// A cut name is an atomic inline: in a line of text that doesn't fit,
	// the line drops it whole, so the lines that hold names lay them out
	// as flex items, which shrink, and are cut, instead.
	where := declarations(ruleFor(t, rules, "", ".doc-place .where").body)
	assert.Equal(t, map[string]string{"display": "flex", "flex-wrap": "wrap", "min-width": "0"},
		map[string]string{"display": where["display"], "flex-wrap": where["flex-wrap"], "min-width": where["min-width"]})
	assert.Equal(t, "flex", declarations(ruleFor(t, rules, phone, ".nearest-sub").body)["display"])
	assert.Equal(t, "0", declarations(ruleFor(t, rules, "", ".nearest-sub").body)["min-width"])
	assert.Equal(t, "minmax(0, 1fr) 3rem 2.5rem",
		declarations(ruleFor(t, rules, "", ".interest .children > li").body)["grid-template-columns"],
		"an area card's interest: its name gives way to its bar and size")
	// The Interests' head and its rebuild line never grow past the screen.
	for _, selector := range []string{".page-head .actions", ".rebuild-state"} {
		box := declarations(ruleFor(t, rules, "", selector).body)
		assert.Equal(t, "0", box["min-width"], selector)
		assert.Equal(t, "100%", box["max-width"], selector)
	}
	// A failure group's host tag never grows past its card: the host gives
	// way, cut with an ellipsis, and its count stays.
	assert.Equal(t, "100%", declarations(ruleFor(t, rules, "", ".hosts .tag").body)["max-width"])
	hostName := declarations(ruleFor(t, rules, "", ".hosts .tag .name").body)
	assert.Equal(t, map[string]string{"min-width": "0", "overflow": "hidden", "text-overflow": "ellipsis",
		"white-space": "nowrap"}, hostName)
	assert.Equal(t, "none", declarations(ruleFor(t, rules, "", ".hosts .tag .n").body)["flex"])
	// A #fragment jump, Status's causes to their cards, lands below the
	// sticky header.
	assert.Equal(t, "sticky", declarations(ruleFor(t, rules, "", "header.site").body)["position"])
	assert.Equal(t, "calc(var(--header-h) + var(--s4))",
		declarations(ruleFor(t, rules, "", "html").body)["scroll-padding-top"])

	// Dark mode follows the system unless a host forces light, and a host
	// can force it: both blocks set the same tokens to the same values.
	light := declarations(ruleFor(t, rules, "", ":root").body)
	system := declarations(ruleFor(t, rules, "@media (prefers-color-scheme: dark)", `:root:not([data-theme="light"])`).body)
	forced := declarations(ruleFor(t, rules, "", `:root[data-theme="dark"]`).body)
	assert.Equal(t, "light dark", light["color-scheme"])
	assert.Equal(t, system, forced)
	for token := range system {
		assert.Contains(t, light, token, "a dark token with a light value")
	}

	// What needs JavaScript shows once actions.js marks the page, what
	// stands in for it only until then, and [hidden] hides whatever sets
	// display, as .btn does.
	for selector, display := range map[string]string{":root:not([data-js]) .js-only": "none !important",
		":root[data-js] .no-js": "none !important", "[hidden]": "none !important"} {
		assert.Equal(t, display, declarations(ruleFor(t, rules, "", selector).body)["display"], selector)
	}
	assert.NotEmpty(t, declarations(ruleFor(t, rules, "", ".btn").body)["display"], "why [hidden] needs !important")
	// The stale note shows only while the pollers go unanswered, and the
	// live badge hides then.
	assert.Equal(t, "none", declarations(ruleFor(t, rules, "", ".stale-note").body)["display"])
	assert.Equal(t, "flex", declarations(ruleFor(t, rules, "", ":root[data-stale] .stale-note").body)["display"])
	assert.Equal(t, "none", declarations(ruleFor(t, rules, "", ":root[data-stale] .badge-live").body)["display"])

	motion := ruleFor(t, rules, "@media (prefers-reduced-motion: reduce)", "*")
	assert.Equal(t, "none !important", declarations(motion.body)["animation"])
	assert.Equal(t, "none !important", declarations(motion.body)["transition"])

	// It loads nothing: no import, no font, no image but a data: URL.
	assert.NotContains(t, css, "@import")
	assert.NotContains(t, css, "@font-face")
	for _, m := range regexp.MustCompile(`url\(\s*["']?([^"')]*)`).FindAllStringSubmatch(css, -1) {
		assert.True(t, strings.HasPrefix(m[1], "data:"), "url(%s", m[1])
	}
}

// TestStylesheet_InterestMap: the map's palette is legible as small marks
// in both themes, every colour 3:1 or more against the surface, Unsorted's
// neutral too, and no two slots alike; its canvases leave touch to the
// map; what map.js fills hides while empty; the tabs are the segmented
// control; and on a phone the panel is a bottom sheet.
func TestStylesheet_InterestMap(t *testing.T) {
	rules := cssRules(t, stylesheet(t))
	light := declarations(ruleFor(t, rules, "", ":root").body)
	dark := declarations(ruleFor(t, rules, "@media (prefers-color-scheme: dark)", `:root:not([data-theme="light"])`).body)
	hexRE := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	for name, theme := range map[string]map[string]string{"light": light, "dark": dark} {
		seen := map[string]string{}
		for s := range 30 {
			token := "--area-" + strconv.Itoa(s)
			colour := theme[token]
			require.Regexp(t, hexRE, colour, "%s %s", name, token)
			assert.Empty(t, seen[colour], "%s %s repeats %s", name, token, seen[colour])
			seen[colour] = token
			assert.GreaterOrEqual(t, contrast(t, colour, theme["--surface"]), 3.0, "%s %s", name, token)
		}
		assert.GreaterOrEqual(t, contrast(t, theme["--neutral-dot"], theme["--surface"]), 3.0, "%s unsorted", name)
	}

	assert.Equal(t, "none", declarations(ruleFor(t, rules, "", ".map-canvas").body)["touch-action"])
	for _, selector := range []string{".map-hits:empty", ".map-crumbs:empty", ".map-legend:empty", ".map-status:empty",
		".map-kicker:empty", ".map-panel-head h2:empty", ".map-panel-body:empty"} {
		assert.Equal(t, "none", declarations(ruleFor(t, rules, "", selector).body)["display"], selector)
	}
	// Until the map is drawn, or when it can't be, the panel's title is empty: the zoom buttons and the
	// panel's head wait, and so does a phone's sheet.
	for _, selector := range []string{".interest-map:has(.map-panel-head h2:empty) .map-zoom",
		".map-panel-head:has(h2:empty)"} {
		assert.Equal(t, "none", declarations(ruleFor(t, rules, "", selector).body)["display"], selector)
	}
	ruleFor(t, rules, "", `.segmented [aria-selected="true"]`)
	const phone = "@media (max-width: 48rem)"
	assert.Equal(t, "none",
		declarations(ruleFor(t, rules, phone, ".map-panel:has(.map-panel-head h2:empty)").body)["display"])
	assert.Equal(t, "fixed", declarations(ruleFor(t, rules, phone, ".map-panel").body)["position"])
	assert.Equal(t, "none", declarations(ruleFor(t, rules, phone, ".map-panel:not(.is-open) .map-panel-body").body)["display"])
	assert.Equal(t, "55vh", declarations(ruleFor(t, rules, phone, ".map-panel.is-open").body)["max-height"])
	assert.Equal(t, "6rem", declarations(ruleFor(t, rules, phone, "main.page:has(.interest-map)").body)["padding-bottom"],
		"the page's foot clears the closed sheet")
}

// contrast is WCAG 2's contrast ratio of two #rrggbb colours.
func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// luminance is a #rrggbb colour's relative luminance.
func luminance(t *testing.T, colour string) float64 {
	t.Helper()
	rgb, err := strconv.ParseUint(strings.TrimPrefix(colour, "#"), 16, 32)
	require.NoError(t, err, colour)
	channel := func(shift uint) float64 {
		c := float64(rgb>>shift&0xff) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(16) + 0.7152*channel(8) + 0.0722*channel(0)
}

// TestStylesheet_Parse: the rule reader finds rules inside at-rules and
// skips comments and keyframes.
func TestStylesheet_Parse(t *testing.T) {
	rules := cssRules(t, `/* a { x } */ a, b > c { color: red; } @keyframes k { to { x: 1 } }
@media (max-width: 1px) { d { overflow-wrap: anywhere } }`)
	require.Len(t, rules, 2)
	assert.Equal(t, cssRule{selectors: []string{"a", "b > c"}, body: " color: red; "}, rules[0])
	assert.Equal(t, "@media (max-width: 1px)", rules[1].at)
	assert.Equal(t, map[string]string{"overflow-wrap": "anywhere"}, declarations(rules[1].body))
}
