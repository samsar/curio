package fetcher

import (
	"regexp"
	"strings"
)

// Jina Reader converts a whole page to markdown: its menus, story cards,
// share buttons and footer come with the article, where Readability keeps
// the article alone. Stored, they are chunked, embedded and matched like
// the article's words, so a query about nothing in particular finds a
// page by its footer. dropBoilerplate takes the lines out that are no part
// of the page's own text, in the text rules' terms (readPageText): what
// shows only links or nothing at all. It reads no meaning into a line's
// words but for a few templates of a site's chrome (chromeLineRE), and
// those only outside the page's body, in blocks of chrome alone.

const (
	// bodyLineBytes is how much own text past which a block is prose, as a
	// line past proseLineBytes ends the opening: the page's chrome blocks
	// are shorter, and its body runs from its first prose block to its
	// last.
	bodyLineBytes = proseLineBytes

	// chromeLineBytes is the most a line matching chromeLineRE may hold to
	// be dropped.
	chromeLineBytes = 300
)

var (
	// listItemRE matches a list item's marker opening a line.
	listItemRE = regexp.MustCompile(`^\s*(?:[*+-]|\d{1,3}[.)])\s+`)

	// emphasisLineRE matches a line that is strong or plain emphasis
	// alone, a colon allowed inside or after it: "**Contents**",
	// "__Resources:__", "*See also*:".
	emphasisLineRE = regexp.MustCompile(`^(?:\*\*[^*]+\*\*|__[^_]+__|\*[^*\s][^*]*\*|_[^_\s][^_]*_):?$`)

	// markupRE matches a markdown link or image's opening, which a line
	// that shows nothing must hold to be dropped: a thematic break or a
	// setext underline shows nothing too, and is the page's structure.
	markupRE = regexp.MustCompile(`\]\(`)

	// chromeLineRE matches a line of a site's chrome, as a whole, in the
	// words a line shows (readLine): a skip link, a copyright notice, a
	// cookie or sign-up consent, a sign-in button, a legal footer's
	// labels. The list is explicit, each template has a test row, and
	// dropBoilerplate reads it only outside the page's body: an article
	// that discusses cookies, copyright or privacy policies does so in its
	// body.
	chromeLineRE = regexp.MustCompile(`(?i)^(?:` +
		// Skip links: "Skip to main content", "Skip to navigation".
		`skip\s+to\s+(?:the\s+)?(?:main\s+)?(?:content|navigation|search|footer)` +
		// A copyright notice: "© 2026 Example LLC. All rights reserved.",
		// "Copyright © 2010-2026 Example", "Site design / logo © 2026 …".
		`|(?:.*\s)?©\s*\d{4}\b.*|(?:copyright|\(c\))\s*(?:©\s*)?\d{4}\b.*` +
		`|.*\ball\s+rights\s+reserved\b.*` +
		// A cookie consent: "We use cookies to …", "This site uses
		// cookies …", "By continuing to use this website, you agree …
		// cookies …", and its buttons.
		`|(?:we|this\s+(?:web)?site|our\s+(?:web)?site)\s+uses?\s+cookies\b.*` +
		`|by\s+(?:continuing\s+to\s+(?:use|browse)|using)\s+(?:this|our)\s+(?:web)?site\b.*\bcookies\b.*` +
		`|(?:accept|reject|allow)\s+(?:all\s+)?cookies|necessary\s+cookies\s+only|cookie\s+(?:settings|preferences)` +
		// A sign-up consent and its buttons: "By clicking “Sign up”, you
		// agree to our terms of service …", "Sign up with Google".
		`|by\s+(?:clicking|signing\s+up|creating\s+an\s+account|continuing)\b.*\byou\s+agree\s+to\b.*` +
		`|(?:sign\s+(?:up|in)|log\s+in|continue)\s+with\s+(?:google|github|apple|facebook|microsoft|linkedin|twitter|x|email)` +
		// A legal footer's labels, each a line of its own.
		`|privacy\s+policy|cookie\s+policy|terms\s+of\s+(?:service|use)|terms\s+(?:and|&)\s+conditions` +
		`|your\s+privacy\s+choices|do\s+not\s+(?:sell|share)(?:\s+or\s+(?:sell|share))?\s+my\s+personal\s+info(?:rmation)?` +
		`)$`)
)

// dropBoilerplate returns a Jina answer's markdown less its boilerplate:
//
//   - a block (lines between blank lines) of which every line shows only
//     links, or links and images that show nothing: a menu, a story card,
//     a row of share buttons;
//   - in any other block, a list item that shows only links; a line of
//     links that isn't a list item stays there, since a wrapped paragraph
//     can break before a link and after it;
//   - a line that shows nothing but holds a link or an image ("![Image
//     3](…)", "[](…)");
//   - outside the page's body, a line that is a template of a site's
//     chrome as a whole (chromeLine), in a block whose every line is such
//     a template, a link or shows nothing: the body runs from the first
//     block of prose (more than bodyLineBytes of own text, however it is
//     wrapped) to the last.
//
// Links under a heading of plain text, or one whose links lead to a place
// on the page (target) itself, or under a lead-in (a line ending with a
// colon, or emphasis alone: "This guide covers:", "**Contents**"), are
// that section's content (a table of contents, a reading list), not a
// menu: they stay, up to the next line of own text that isn't a list item,
// heading of other links (a story card's), quote or code block. A
// heading, a quoted line and a fenced code block always stay. The lines
// indented under a dropped list item lose its marker's width, so they
// don't turn into a code block. A line it keeps is otherwise kept byte for
// byte; runs of blank lines become one. It returns md unchanged when it
// drops nothing, and when it would leave no own text (a page of links that
// passed judgePage is the page asked for, all of it).
func dropBoilerplate(md, target string) string {
	lines := strings.Split(md, "\n")
	kind := make([]lineKind, len(lines))
	texts := make([]string, len(lines)) // the words each line shows (readLine)
	var fence string
	for i, raw := range lines {
		switch {
		case fence != "":
			kind[i] = bpCode
			if closesFence(raw, fence) {
				fence = ""
			}
			continue
		case strings.TrimSpace(raw) == "":
			kind[i] = bpBlank
			continue
		}
		if fence = codeFence(raw); fence != "" {
			kind[i] = bpCode
			continue
		}
		kind[i], texts[i] = classifyLine(raw, target)
		if i+1 < len(lines) && setextUnderlineRE.MatchString(strings.TrimSpace(lines[i+1])) {
			// A setext heading's text.
			kind[i] = kind[i].asHeading()
		}
	}
	blocks := splitBlocks(kind)
	// The body: from the first block of prose to the last.
	first, last := -1, -1
	for bi, bl := range blocks {
		if bl.prose(kind, texts) {
			if first < 0 {
				first = bi
			}
			last = bi
		}
	}
	drop := make([]bool, len(lines))
	section := false // under a heading or a lead-in, in its links
	for bi, bl := range blocks {
		whole, chrome := true, true
		for i := bl.start; i < bl.end; i++ {
			switch kind[i] {
			case bpLinks, bpListLinks, bpEmptyMarkup:
			case bpRule:
				whole = false
			case bpText:
				whole = false
				chrome = chrome && chromeLine(texts[i])
			case bpBlank, bpCode, bpListText, bpLead, bpHeading, bpKept:
				whole, chrome = false, false
			}
		}
		outside := first < 0 || bi < first || bi > last
		for i := bl.start; i < bl.end; i++ {
			switch k := kind[i]; k {
			case bpHeading, bpLead:
				section = true
			case bpEmptyMarkup:
				drop[i] = true
			case bpLinks, bpListLinks:
				drop[i] = !section && (whole || k == bpListLinks)
			case bpListText:
				section = section || strings.HasSuffix(texts[i], ":")
			case bpRule:
			case bpText:
				section = false
				drop[i] = outside && chrome && chromeLine(texts[i])
			case bpKept, bpCode, bpBlank:
				section = false
			}
		}
	}
	var b strings.Builder
	dropped, own, blank := false, false, false
	cont := continuation{}
	for i, raw := range lines {
		switch {
		case drop[i]:
			dropped = true
			cont = cont.after(raw, kind[i])
			continue
		case kind[i] == bpBlank:
			blank = b.Len() > 0
			continue
		}
		cont, raw = cont.dedent(raw)
		if blank {
			b.WriteString("\n\n")
		} else if b.Len() > 0 {
			b.WriteByte('\n')
		}
		blank = false
		b.WriteString(raw)
		own = own || kind[i].own()
	}
	if !dropped || !own {
		return md
	}
	return b.String()
}

// block is a run of lines between blank lines, [start, end).
type block struct{ start, end int }

// splitBlocks returns the blocks of lines of kind.
func splitBlocks(kind []lineKind) []block {
	var blocks []block
	for start := 0; start < len(kind); {
		if kind[start] == bpBlank {
			start++
			continue
		}
		end := start
		for end < len(kind) && kind[end] != bpBlank {
			end++
		}
		blocks = append(blocks, block{start, end})
		start = end
	}
	return blocks
}

// prose reports whether a block is prose: more than bodyLineBytes of own
// text, chrome lines aside, however its lines are wrapped.
func (bl block) prose(kind []lineKind, texts []string) bool {
	n := 0
	for i := bl.start; i < bl.end; i++ {
		switch kind[i] {
		case bpText, bpListText, bpLead, bpHeading, bpKept:
			if !chromeLine(texts[i]) {
				n += len(texts[i]) + 1
			}
		case bpBlank, bpCode, bpLinks, bpListLinks, bpEmptyMarkup, bpRule:
		}
	}
	return n > bodyLineBytes
}

// continuation is what dropBoilerplate does to the lines indented under a
// list item it dropped: they would read as a code block, so they lose the
// item's marker width (by) while they are indented at least to its
// content (from).
type continuation struct{ from, by int }

// after is the continuation after dropping raw, of kind k: a dropped list
// item's, or the one before for any other line.
func (c continuation) after(raw string, k lineKind) continuation {
	if k != bpListLinks {
		return c
	}
	marker := listItemRE.FindString(raw)
	indent := len(marker) - len(strings.TrimLeft(marker, " "))
	return continuation{from: len(marker), by: len(marker) - indent}
}

// dedent returns raw, a kept line, less the continuation's indent when it
// is indented under the dropped item, and the continuation that holds
// after it: none once a line is indented less.
func (c continuation) dedent(raw string) (continuation, string) {
	if c.from == 0 {
		return c, raw
	}
	if len(raw)-len(strings.TrimLeft(raw, " ")) < c.from {
		return continuation{}, raw
	}
	return c, raw[c.by:]
}

// lineKind is what a line of a Jina answer is to dropBoilerplate.
type lineKind int

const (
	// bpBlank is empty or holds only whitespace.
	bpBlank lineKind = iota
	// bpCode is a fenced code block's line, its fences included.
	bpCode
	// bpText shows words of its own.
	bpText
	// bpListText is a list item that shows words of its own.
	bpListText
	// bpLead is a line of own text that introduces what follows: it ends
	// with a colon ("This guide covers the following topics:") or is
	// emphasis alone ("**Contents**").
	bpLead
	// bpHeading is a heading that shows words of its own.
	bpHeading
	// bpKept always stays: a heading of links, a quote.
	bpKept
	// bpLinks shows only links' words.
	bpLinks
	// bpListLinks is a list item that shows only links' words.
	bpListLinks
	// bpEmptyMarkup shows nothing but holds a link or an image.
	bpEmptyMarkup
	// bpRule shows nothing: a thematic break, a setext underline, a lone
	// marker.
	bpRule
)

// own reports whether a line of kind k shows own text.
func (k lineKind) own() bool {
	return k == bpText || k == bpListText || k == bpLead || k == bpHeading || k == bpKept || k == bpCode
}

// asHeading is kind k of a line over a setext underline.
func (k lineKind) asHeading() lineKind {
	switch k {
	case bpText, bpListText, bpLead:
		return bpHeading
	case bpLinks, bpListLinks:
		return bpKept
	case bpBlank, bpCode, bpHeading, bpKept, bpEmptyMarkup, bpRule:
	}
	return k
}

// classifyLine reads one line outside a code block that isn't blank, of
// the answer for target: its kind, and the words it shows (readLine).
func classifyLine(raw, target string) (lineKind, string) {
	text, link, heading, quote := readLine(raw)
	list := listItemRE.MatchString(raw)
	switch {
	case text == "" && markupRE.MatchString(raw):
		return bpEmptyMarkup, text
	case text == "":
		return bpRule, text
	case heading && (!link || linksHere(raw, target)):
		return bpHeading, text
	case quote, heading:
		return bpKept, text
	case link && list:
		return bpListLinks, text
	case link:
		return bpLinks, text
	case list:
		return bpListText, text
	case strings.HasSuffix(text, ":") || emphasisLineRE.MatchString(strings.TrimSpace(raw)):
		return bpLead, text
	}
	return bpText, text
}

// linksHere reports whether every link on a line leads to the page
// requested as target, a fragment of it included: a heading's own anchor
// ("## [Contents](https://example.com/spec.html#toc)"). Scheme, a trailing
// slash and "www." aside.
func linksHere(raw, target string) bool {
	here := samePage(target)
	links := mdLinkDestRE.FindAllStringSubmatch(mdImageRE.ReplaceAllString(raw, ""), -1)
	for _, m := range links {
		dest, _, _ := strings.Cut(strings.TrimSpace(m[2]), " ")
		if !strings.HasPrefix(dest, "#") && (here == "" || samePage(dest) != here) {
			return false
		}
	}
	return len(links) > 0
}

// samePage is a URL less its scheme, "www.", fragment and trailing slash,
// lowercased: what two links to one page share.
func samePage(u string) string {
	u, _, _ = strings.Cut(u, "#")
	if _, rest, ok := strings.Cut(u, "://"); ok {
		u = rest
	}
	return strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(u), "www."), "/")
}

// mdLinkDestRE matches a markdown link, its text the first group, its
// destination (title and all) the second.
var mdLinkDestRE = regexp.MustCompile(mdLinkText + `\(((?:[^()]|\([^()]*\))*)\)`)

// chromeLine reports whether text, the words a line shows, is a template
// of a site's chrome as a whole (chromeLineRE) in at most chromeLineBytes.
func chromeLine(text string) bool {
	return len(text) <= chromeLineBytes && chromeLineRE.MatchString(text)
}
