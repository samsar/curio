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
// answers keep and Readability mostly drops. A notice counts only in the
// page's opening, where it is what the page is; further in, a page
// discusses or quotes it.
//
// A page's text is go-readability's RenderText from the origin, plain text
// with each block on a line of its own, or the markdown body of Jina's
// answer. Plain text has no link markup, so every line of it is own text,
// and it passes through the markdown reduction unchanged but for a line
// that opens like a marker ("1. ", "- ").

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
// part of a line would read as a shorter line.
func readPageText(text string) pageText {
	t := pageText{whole: true}
	if len(text) > pageTextScanBytes {
		text, t.whole = text[:pageTextScanBytes], false
		if i := strings.LastIndexByte(text, '\n'); i >= 0 {
			text = text[:i]
		}
	}
	for raw := range strings.Lines(text) {
		line, link := readLine(raw)
		if line == "" {
			continue
		}
		t.lines = append(t.lines, textLine{text: line, link: link, at: t.own})
		if !link {
			t.own += len(line) + 1
		}
	}
	return t
}

// readLine reduces a line of a page's text to the words it shows, with its
// whitespace made plain, and reports whether it holds only links. A line
// that shows nothing, a thematic break or a setext underline among them,
// reads as "".
func readLine(raw string) (line string, link bool) {
	line = mdImageRE.ReplaceAllString(raw, "")
	link = linkLineRE.MatchString(line)
	line = strings.TrimSpace(mdLinkRE.ReplaceAllString(line, "$1"))
	if ruleLineRE.MatchString(line) {
		return "", false
	}
	line = emphasisMarks.Replace(blockMarkRE.ReplaceAllString(line, ""))
	line = mdEscapeRE.ReplaceAllString(line, "$1")
	return strings.Join(strings.Fields(line), " "), link
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

// notFoundNotice reports a not-found notice in the page's opening: a line
// that is a not-found template as a whole, as a title would be
// (soft404TitleRE), or that opens with a not-found sentence
// (notFoundSentenceRE). It returns the reason, or the empty string.
func (t *pageText) notFoundNotice() string {
	for l := range t.opening() {
		if soft404TitleRE.MatchString(l.text) || notFoundSentenceRE.MatchString(l.text) {
			return "text reads like a not-found page: " + quoteLine(l.text)
		}
	}
	return ""
}

// parkedDomain reports a parked domain's notice in the page's opening, for
// a request for target (parkedNotice). It returns the reason, or the empty
// string.
func (t *pageText) parkedDomain(target string) string {
	host := strings.TrimPrefix(hostOf(target), "www.")
	for l := range t.opening() {
		if parkedNotice(l.text, host) {
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
// something else ("is for sale for $10 million").
func parkedNotice(line, host string) bool {
	if parkingHeadingRE.MatchString(line) {
		return true
	}
	m := parkedDomainRE.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	if domain := m[parkedDomainRE.SubexpIndex("domain")]; domain != "" {
		domain = strings.TrimPrefix(strings.ToLower(domain), "www.")
		return domain == host || domain == siteOf(host)
	}
	return m[parkedDomainRE.SubexpIndex("subject")] != "" ||
		strings.Trim(m[parkedDomainRE.SubexpIndex("rest")], ".!") == ""
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
	// in 2019, but…"); a line that is the template alone is soft404TitleRE's.
	notFoundSentenceRE = regexp.MustCompile(`(?i)^` + notFoundLead + `(?:` +
		notFoundCantFind + `\s+(?:this|that|the)\s+(?:requested\s+)?` + notFoundThing + notFoundYouWanted +
		`|(?:this|that|the)\s+(?:requested\s+)?` + notFoundThing + notFoundYouWanted + `\s+` + notFoundGone +
		`)[.!]`)

	// parkedDomainRE matches a parked domain's notice: an optional subject
	// ("this domain", "the domain name", "domain", or a domain name), one
	// predicate, and the rest of the line, which must open with ".", "!" or
	// ":" ("This domain is for sale: $5,795", "This domain name may be for
	// sale. Click here…"). The predicates are what registrars and parking
	// services say: for sale, parked, expired, just registered, Hover's "is
	// a totally awesome idea still being worked on" and easyDNS's "is yet
	// another domain managed by easyDNS". The list is explicit: a predicate
	// added to it needs a test row.
	parkedDomainRE = regexp.MustCompile(`(?i)^` +
		`(?:(?:(?P<subject>(?:(?:this|the)\s+)?domain(?:\s+name)?)|(?P<domain>` + domainName + `))\s+)?` +
		`(?:(?:is|may\s+be)\s+for\s+sale` +
		`|is\s+parked` +
		`|(?:registration\s+)?has\s+expired` +
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
