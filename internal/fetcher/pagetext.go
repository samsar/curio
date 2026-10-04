package fetcher

import (
	"iter"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The text rules judge a page by what it says, where its title, URL and
// size give it away no better than an article: a not-found page titled
// with the site's name, a parked domain, a sign-in form. They read a page's
// own words: its lines with markdown reduced to the words they show, less
// the lines that hold only links (navigation, cards, footers), which Jina's
// answers keep and Readability mostly drops, and the code blocks, which
// show code, never what the page is. A notice counts only in the page's
// opening, where it is what the page is; further in, a page discusses or
// quotes it. A quoted line (a markdown blockquote) is another page's words
// wherever it stands: a question quoting the error it got is no notice.
//
// A page's text is go-readability's RenderText from the origin, plain text
// with each block on a line of its own, or the markdown body of Jina's
// answer. Plain text has no link markup, code fences or headings, so every
// line of it is own text, and it passes through the markdown reduction
// unchanged but for a line that opens like a marker ("1. ", "- ").

const (
	// pageTextScanBytes is how much of a page's text the text rules read.
	// The furthest notice in the library sits 19.3 KB into a Jina answer,
	// behind Advisor Perspectives' navigation.
	pageTextScanBytes = 64 << 10

	// openingBytes is how far into a page's own text a line of its opening
	// may begin. The library's notices begin at most 857 bytes in
	// (bomatoronto.org's from the origin; Quartz's at 495). The nearest line
	// of a stored page that would match begins 6.9 KB in, after a 591-byte
	// line (a Home Depot category's "Related Searches"), and the nearest
	// article's 7.9 KB in, after a 693-byte paragraph ("404 Not Found" in
	// HTTP Made Really Easy).
	openingBytes = 2 << 10

	// proseLineBytes is the length past which a line is prose: the opening
	// ends with its first such line, which belongs to it. The longest line
	// before any of the library's notices is 86 bytes; LinkedIn's notice is
	// itself the first long line (217 bytes).
	proseLineBytes = 200

	// parkedNoticeBytes is how far into a page's own text a parked domain's
	// notice may begin. The library's parking pages say so at most 54
	// bytes in (flappyroyale.io's, under its sign-in box); a search
	// results page puts its "Related searches" after its results, within
	// openingBytes.
	parkedNoticeBytes = 256

	// signInPageBytes is the most own text a page that is only a sign-in
	// form holds. The library's sign-in walls hold 99, 171 and 506 bytes.
	// The other pages with a password field in their first signInFormBytes
	// of own text hold 577 (a sign-up form, with no sign-in line), 628 (a
	// parking page, a dead link first) and then 7.7 KB or more (Stack
	// Overflow's questions under their sign-up dialog among them).
	signInPageBytes = 1 << 10

	// signInFormBytes is how far into a page's own text its password field
	// and sign-in line must begin. The library's walls put the password
	// field at 70, 98 and 209 bytes; pages that are stored put it at 331
	// (Pinterest's sign-in modal over a deleted pin), 511 (an image page
	// whose only text is a portfolio sidebar) and 541 (a public Facebook
	// page, its intro and posts first).
	signInFormBytes = 256

	// maxQuotedLine is the most of a line a reason quotes, in bytes.
	maxQuotedLine = 120
)

// pageText is the text rules' view of a page's text, from at most its first
// pageTextScanBytes.
type pageText struct {
	lines []textLine
	// own is the page's own text read, in bytes: every line that isn't a
	// link line, each counted with its line break.
	own int
	// whole reports whether the whole text was read.
	whole bool
}

// textLine is a non-empty line of a page's text, reduced to the words it
// shows.
type textLine struct {
	text string
	// link reports a line that holds only links; its words are no part of
	// the page's own text.
	link bool
	// heading reports a markdown heading: "## 404", or a line over a
	// setext underline.
	heading bool
	// quote reports a blockquoted line ("> 404 Not Found"): another page's
	// words, which the notice rules never read.
	quote bool
	// at is the page's own text before the line, in bytes.
	at int
}

const (
	// mdLinkText is a markdown link's text, its first group, escapes and
	// all: "[Sign in]".
	mdLinkText = `\[((?:\\.|[^\\\]])*)\]`
	// mdLinkDest is a link's destination, title included, holding at most
	// one level of parentheses: "(https://example.com/a_(b) "Home")".
	mdLinkDest = `\((?:[^()]|\([^()]*\))*\)`
)

var (
	// mdImageRE matches a markdown image, which shows no words.
	mdImageRE = regexp.MustCompile(`!` + mdLinkText + mdLinkDest)
	// mdLinkRE matches a markdown link, which shows its text.
	mdLinkRE = regexp.MustCompile(mdLinkText + mdLinkDest)
	// linkLineRE matches a line that holds only links, after an optional
	// list, heading or emphasis marker: "*   [Jobs](…)", "#### [Title](…)",
	// "[Join now](…)[Sign in](…)".
	linkLineRE = regexp.MustCompile(`^\s*(?:[*+-]\s+|\d{1,3}[.)]\s+)?(?:#{1,6}\s+)?(?:\*\*|__)?` +
		`(?:` + mdLinkText + mdLinkDest + `\s*)+(?:\*\*|__)?\s*$`)
	// ruleLineRE matches a thematic break ("* * *") or a setext heading's
	// underline ("====").
	ruleLineRE = regexp.MustCompile(`^(?:(?:[*_-]\s*){3,}|=+|-+)$`)
	// setextUnderlineRE matches a setext heading's underline, which makes
	// the line right above it a heading.
	setextUnderlineRE = regexp.MustCompile(`^(?:=+|-+)$`)
	// codeFenceRE matches the fence opening a code block: three or more
	// backticks with none after them ("```go"), its first group, or three or
	// more tildes, its second.
	codeFenceRE = regexp.MustCompile("^(?:(`{3,})[^`]*|(~{3,}).*)$")
	// blockMarkRE matches the markers opening a line: headings,
	// blockquotes, list items and task boxes ("- [x]").
	blockMarkRE = regexp.MustCompile(`^(?:#{1,6}(?:\s+|$)|>\s?|[*+-]\s+|\d{1,3}[.)]\s+|\[[ xX]\]\s*)+`)
	// mdEscapeRE matches a backslash escape of a punctuation mark.
	mdEscapeRE = regexp.MustCompile(`\\([!-/:-@\[-` + "`" + `{-~])`)
	// emphasisMarks drops strong emphasis.
	emphasisMarks = strings.NewReplacer("**", "", "__", "")
)

// readPageText reads at most the first pageTextScanBytes of a page's text
// into lines. It stops at the last line break within them, if there is one:
// part of a line would read as a shorter line. A fenced code block's lines
// count as own text, the page going on, but no rule reads them.
func readPageText(text string) pageText {
	t := pageText{whole: true}
	if len(text) > pageTextScanBytes {
		text, t.whole = text[:pageTextScanBytes], false
		if i := strings.LastIndexByte(text, '\n'); i >= 0 {
			text = text[:i]
		}
	}
	var fence string // the open code block's fence; "" outside one
	above := -1      // the index in t.lines of the line just above; -1 if none
	for raw := range strings.Lines(text) {
		if fence != "" {
			if closesFence(raw, fence) {
				fence = ""
			} else if code := plain(raw); code != "" {
				t.own += len(code) + 1
			}
			continue
		}
		if fence = codeFence(raw); fence != "" {
			above = -1
			continue
		}
		if above >= 0 && setextUnderlineRE.MatchString(strings.TrimSpace(raw)) {
			t.lines[above].heading = true
		}
		line, link, heading, quote := readLine(raw)
		if line == "" {
			above = -1
			continue
		}
		above = len(t.lines)
		t.lines = append(t.lines, textLine{text: line, link: link, heading: heading, quote: quote, at: t.own})
		if !link {
			t.own += len(line) + 1
		}
	}
	return t
}

// readLine reduces a line of a page's text to the words it shows, with its
// whitespace made plain, and reports whether it holds only links, whether
// it is an ATX heading ("## 404") and whether it is blockquoted ("> 404").
// A line that shows nothing, a thematic break or a setext underline among
// them, reads as "".
func readLine(raw string) (line string, link, heading, quote bool) {
	line = mdImageRE.ReplaceAllString(raw, "")
	link = linkLineRE.MatchString(line)
	line = strings.TrimSpace(mdLinkRE.ReplaceAllString(line, "$1"))
	if ruleLineRE.MatchString(line) {
		return "", false, false, false
	}
	marks := blockMarkRE.FindString(line)
	// Of the markers, only a heading's holds a "#" and only a blockquote's
	// a ">".
	heading = strings.Contains(marks, "#")
	quote = strings.Contains(marks, ">")
	line = emphasisMarks.Replace(line[len(marks):])
	line = mdEscapeRE.ReplaceAllString(line, "$1")
	return plain(line), link, heading, quote
}

// plain makes a line's whitespace plain: each run of it, no-break spaces
// included, one space, and none at the ends.
func plain(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// codeFence returns the fence a line opens a code block with, or the
// empty string.
func codeFence(raw string) string {
	m := codeFenceRE.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}

// closesFence reports whether a line closes the code block fence opened:
// the fence's mark, as many times or more, and nothing else.
func closesFence(raw, fence string) bool {
	s := strings.TrimSpace(raw)
	return len(s) >= len(fence) && strings.Trim(s, fence[:1]) == ""
}

// opening yields the page's opening: its own lines that begin within its
// first openingBytes of own text, up to and including its first line of
// prose (longer than proseLineBytes).
func (t *pageText) opening() iter.Seq[textLine] {
	return func(yield func(textLine) bool) {
		for _, l := range t.lines {
			if l.link {
				continue
			}
			if l.at >= openingBytes || !yield(l) || len(l.text) > proseLineBytes {
				return
			}
		}
	}
}

// notFoundNotice reports a not-found notice in the page's opening
// (notFound). It returns the reason, or the empty string.
func (t *pageText) notFoundNotice() string {
	for l := range t.opening() {
		if !l.quote && l.notFound() {
			return "text reads like a not-found page: " + quoteLine(l.text)
		}
	}
	return ""
}

// notFound reports whether a line is a not-found notice: a not-found
// template as a whole, as a title would be (notFoundLineRE), or a line
// opening with a not-found sentence (notFoundSentenceRE). A status code
// alone (statusCodeLineRE) counts only as a heading: on a line of its own,
// "404" is as often a question's score, a profile's followers or a line of
// a command's output.
func (l textLine) notFound() bool {
	if notFoundSentenceRE.MatchString(l.text) {
		return true
	}
	return notFoundLineRE.MatchString(l.text) && (l.heading || !statusCodeLineRE.MatchString(l.text))
}

// parkedDomain reports a parked domain's notice in the lines of the page's
// opening that begin within its first parkedNoticeBytes of own text, for a
// request for target (parkedNotice). It returns the reason, or the empty
// string.
func (t *pageText) parkedDomain(target string) string {
	host := strings.TrimPrefix(hostOf(target), "www.")
	for l := range t.opening() {
		if l.at >= parkedNoticeBytes {
			break
		}
		if !l.quote && parkedNotice(l.text, host) {
			return "text reads like a parked domain: " + quoteLine(l.text)
		}
	}
	return ""
}

// parkedNotice reports whether line, on a page of host, is a parked
// domain's notice: a search-ads parking page's heading, or a match of
// parkedDomainRE. A notice naming a domain counts only when it names host
// or its site (siteOf): a page may report another site's sale. Without a
// subject, the predicate must be the whole line, as GoDaddy's "is for
// sale!" under the domain's name; a line that goes on after it is about
// something else ("is parked. The owner left."). An expiry counts only
// with its subject: "Registration has expired." alone is as likely an
// event's sign-up page.
func parkedNotice(line, host string) bool {
	if parkingHeadingRE.MatchString(line) {
		return true
	}
	m := parkedDomainRE.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	group := func(name string) string { return m[parkedDomainRE.SubexpIndex(name)] }
	if domain := group("domain"); domain != "" {
		domain = strings.TrimPrefix(strings.ToLower(domain), "www.")
		return domain == host || domain == siteOf(host)
	}
	if group("subject") != "" {
		return true
	}
	return group("expired") == "" && strings.Trim(group("rest"), ".!") == ""
}

// signInForm reports a page that is only a sign-in form: read whole, with
// at most signInPageBytes of own text, whose lines beginning within its
// first signInFormBytes of own text include a password field
// (passwordFieldRE) and a sign-in line (loginTitleRE: "Log in", "Sign
// in"). A page that shows its content first and a sign-in box after it is
// the content's. It returns the reason, or the empty string.
func (t *pageText) signInForm() string {
	if !t.whole || t.own > signInPageBytes {
		return ""
	}
	var password, signIn bool
	for _, l := range t.lines {
		if l.at >= signInFormBytes {
			break
		}
		if !l.link {
			password = password || passwordFieldRE.MatchString(l.text)
			signIn = signIn || loginTitleRE.MatchString(l.text)
		}
	}
	if password && signIn {
		return "page is a sign-in form"
	}
	return ""
}

// quoteLine quotes a line of page text for a reason, cut on a rune boundary
// to at most maxQuotedLine bytes, an ellipsis included.
func quoteLine(s string) string {
	if len(s) > maxQuotedLine {
		cut := maxQuotedLine - len("…")
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return strconv.Quote(s)
}

// domainName is a domain name of two labels or more.
const domainName = `[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+`

var (
	// notFoundSentenceRE matches a line that opens with a not-found
	// sentence, in soft404TitleRE's words: "We can't find (this|that|the)
	// <thing> [you're looking for]" or "(This|That|The) [requested] <thing>
	// [you requested] <gone>", ended by "." or "!". Whatever follows is the
	// page going on: "We can’t find the page you’re looking for.The page…",
	// "This track was not found. Maybe it has been removed", "Sorry! The
	// page you requested was not found.". Without its end, the sentence may
	// go on to say something else ("The page you requested was not found
	// in 2019, but…"); a line that is the template alone is notFoundLineRE's.
	notFoundSentenceRE = regexp.MustCompile(`(?i)^` + notFoundLead + `(?:` +
		notFoundCantFind + `\s+(?:this|that|the)\s+(?:requested\s+)?` + notFoundThing + notFoundYouWanted +
		`|(?:this|that|the)\s+(?:requested\s+)?` + notFoundThing + notFoundYouWanted + `\s+` + notFoundGone +
		`)[.!]`)

	// parkedDomainRE matches a parked domain's notice: an optional subject
	// ("this domain", "the domain name", "domain", or a domain name), one
	// predicate, and the rest of the line, which must open with ".", "!" or
	// ":" ("This domain is for sale: $5,795", "This domain name may be for
	// sale. Click here…"). The predicates are what registrars and parking
	// services say: for sale, parked, expired (a group of its own, which
	// parkedNotice holds to a subject), just registered, Hover's "is a
	// totally awesome idea still being worked on" and easyDNS's "is yet
	// another domain managed by easyDNS". The list is explicit: a predicate
	// added to it needs a test row.
	parkedDomainRE = regexp.MustCompile(`(?i)^` +
		`(?:(?:(?P<subject>(?:(?:this|the)\s+)?domain(?:\s+name)?)|(?P<domain>` + domainName + `))\s+)?` +
		`(?:(?:is|may\s+be)\s+for\s+sale` +
		`|is\s+parked` +
		`|(?P<expired>(?:registration\s+)?has\s+expired)` +
		`|has\s+been\s+(?:recently\s+)?registered\s+(?:with|at)\s+` + domainName +
		`|is\s+a\s+totally\s+awesome\s+idea\s+still\s+being\s+worked\s+on` +
		`|is\s+yet\s+another\s+domain\s+managed\s+by\s+\S+` +
		`)(?P<rest>[.!:].*)?$`)

	// parkingHeadingRE matches the heading of a search-ads parking page,
	// over its ad links: "Related Searches:", "Related Search Topics".
	parkingHeadingRE = regexp.MustCompile(`(?i)^related\s+search(?:es|\s+topics)\s*:?$`)

	// passwordFieldRE matches a password field's label: "Password",
	// "Password:", "Password *".
	passwordFieldRE = regexp.MustCompile(`(?i)^password\s*[:*]?$`)
)
