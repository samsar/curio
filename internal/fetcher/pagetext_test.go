package fetcher

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadPageText: a Jina answer's markdown reads as the words its lines
// show, link lines flagged and left out of the own-text offsets; plain text
// from the origin reads line for line.
func TestReadPageText(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []textLine
	}{
		{"a publishing platform's not-found navigation",
			"[Sitemap](http://blog.example/sitemap/sitemap.xml)\n\n" +
				"Sign up\n\n" +
				"[Sign in](https://publishing.example/m/signin?operation=login&source=login---not_found_layout_nav-----------------------global_nav--------------------)\n\n" +
				"[](https://publishing.example/?source=---not_found_layout_nav-------------------------------------------)\n\n" +
				"![Image 1: Unknown user](https://images.example/v2/resize:fill:64:64/1*dmbNkD5D-u45r44go_cf0g.png)\n\n" +
				"PAGE NOT FOUND\n\n" +
				"## 404",
			[]textLine{
				{text: "Sitemap", link: true, at: 0},
				{text: "Sign up", at: 0},
				{text: "Sign in", link: true, at: 8},
				{text: "PAGE NOT FOUND", at: 8},
				{text: "404", heading: true, at: 23},
			}},
		{"a social site's footer",
			"[Example](https://social.example/)\n\n" +
				"[Privacy](https://social.example/legal/privacy/?next=https%3A%2F%2Fwww.social.example%2Fchivexp%2F)\n\n" +
				"English\n\n" +
				"© 2026 Example Social",
			[]textLine{
				{text: "Example", link: true, at: 0},
				{text: "Privacy", link: true, at: 0},
				{text: "English", at: 0},
				{text: "© 2026 Example Social", at: 8},
			}},
		{"navigation items, a 404 among them",
			"*   [Inspiration](https://portfolio.example/inspiration)\n" +
				"*   [404](https://portfolio.example/inspiration/404)\n" +
				"    *   [Pricing](https://portfolio.example/pricing \"Pricing\")",
			[]textLine{
				{text: "Inspiration", link: true},
				{text: "404", link: true},
				{text: "Pricing", link: true},
			}},
		{"story cards",
			"#### [A Story](https://publishing.example/a-story)\n\n" +
				"[![Image 2: An Author](https://images.example/v2/resize:fill:80:80/1*AJ-ZDdUPFs1vJzvabcfC2w.png)](https://publishing.example/@author)\n\n" +
				"[An Author](https://publishing.example/@author)[in Example Publishing](http://blog.example/publishing)\n\n" +
				"Sep 12, 2026",
			[]textLine{
				{text: "A Story", link: true, heading: true},
				{text: "An Authorin Example Publishing", link: true},
				{text: "Sep 12, 2026"},
			}},
		{"a sign-in form's task box and rules",
			"* * *\n\nor\n\n* * *\n\nEmail or phone\n\n \n\nPassword\n\n- [x] \n\nKeep me signed in",
			[]textLine{
				{text: "or", at: 0},
				{text: "Email or phone", at: 3},
				{text: "Password", at: 18},
				{text: "Keep me signed in", at: 27},
			}},
		{"setext headings (Cloudflare's block page)",
			"Sorry, you have been blocked\n============================\n\n" +
				"You are unable to access example.com\n------------------------------------",
			[]textLine{
				{text: "Sorry, you have been blocked", heading: true, at: 0},
				{text: "You are unable to access example.com", heading: true, at: 29},
			}},
		{"prose with a link, emphasis and escapes",
			"Please try searching our site or [start again on our homepage](https://weekly-news.example/).\n" +
				"**About this page**\n" +
				"\\- Alex Writer, April 22, 2026\n" +
				"1\\. Not a list item\n" +
				"> Sorry, we can't find the page you're looking for.\n" +
				"Page not found  |  Example Cloud",
			[]textLine{
				{text: "Please try searching our site or start again on our homepage.", at: 0},
				{text: "About this page", at: 62},
				{text: "- Alex Writer, April 22, 2026", at: 78},
				{text: "1. Not a list item", at: 108},
				{text: "Sorry, we can't find the page you're looking for.", quote: true, at: 127},
				{text: "Page not found | Example Cloud", at: 177},
			}},
		{"headings, ATX and setext",
			"## 404\n\n404\n===\n\nNot Found\n---------\n\nA line\n\n---\n\n> # Quoted",
			[]textLine{
				{text: "404", heading: true, at: 0},
				{text: "404", heading: true, at: 4},
				{text: "Not Found", heading: true, at: 8},
				{text: "A line", at: 18},
				{text: "Quoted", heading: true, quote: true, at: 25},
			}},
		{"code blocks, counted as own text and left out",
			"Intro\n\n```sh\n$ curl -I https://example.com/gone\n\nHTTP/1.1 404 Not Found\n```\n\n" +
				"~~~~\n404\n~~~\n~~~~\n\n```x``` is no fence\n\nAfter",
			[]textLine{
				{text: "Intro", at: 0},
				{text: "```x``` is no fence", at: 72},
				{text: "After", at: 92},
			}},
		{"a code block left open runs to the end",
			"Intro\n\n```\nPage not found\n\nPassword",
			[]textLine{{text: "Intro", at: 0}}},
		{"plain text from the origin",
			"Page Not Found\nSorry! The page you requested was not found.\n\n\tExample Centre 100 Example Street",
			[]textLine{
				{text: "Page Not Found", at: 0},
				{text: "Sorry! The page you requested was not found.", at: 15},
				{text: "Example Centre 100 Example Street", at: 60},
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := readPageText(tc.text)
			assert.Equal(t, tc.want, got.lines)
			assert.True(t, got.whole)
		})
	}
}

// TestReadPageText_Bounds: the own-text total counts every line but link
// lines, and a text over pageTextScanBytes is read up to its last line
// break within them.
func TestReadPageText_Bounds(t *testing.T) {
	got := readPageText("Log in\n\n[Forgot password?](https://example.com/reset)\n\nPassword")
	assert.Equal(t, len("Log in\n")+len("Password\n"), got.own)
	assert.True(t, got.whole)

	nav := navigation(pageTextScanBytes - len(navItem))
	require.Less(t, len(nav), pageTextScanBytes)
	cut := pageTextScanBytes - len(nav)
	got = readPageText(nav + strings.Repeat("x", cut-1) + "\n" + "Page not found\n")
	assert.False(t, got.whole)
	assert.Equal(t, strings.Repeat("x", cut-1), got.lines[len(got.lines)-1].text,
		"the last whole line within the limit")

	got = readPageText(nav + strings.Repeat("x", cut+10) + "\n")
	assert.False(t, got.whole)
	assert.Equal(t, "Jobs", got.lines[len(got.lines)-1].text, "a line the limit cuts is left out")
}

// TestNotFoundSentenceRE: a line opening with a not-found sentence, each
// form of it, matches; a line that only resembles one doesn't.
func TestNotFoundSentenceRE(t *testing.T) {
	notFound := []string{
		// Representative strings.
		"We can’t find the page you’re looking for.The page you’re looking for may have been moved, or may no longer exist.",
		"This track was not found. Maybe it has been removed Learn more",
		"Sorry! The page you requested was not found.",
		// Can't find it, with or without "we", each way it is written.
		"We couldn't find that page!",
		"We could not find this article. Try the search.",
		"Cannot find the requested item. It may have sold.",
		"We cant find the profile you were looking for.",
		// The thing gone, and how.
		"Oops, the video you tried to access is no longer available. Watch another.",
		"That post has been deleted by its author. Read more posts.",
		"The requested listing does not exist.",
		"This group isn't available. Join another",
		"The story you tried to reach is missing!",
		"Uh-oh. This content no longer exists.",
		"Whoops! The product you tried to visit could not be found.",
		"This user doesn’t exist.",
		"This item was removed. Browse similar items.",
	}
	for _, line := range notFound {
		assert.True(t, notFoundSentenceRE.MatchString(line), "should match %q", line)
	}
	prose := []string{
		"Page not found. Please check the URL.", // no determiner
		"The page you requested was not found on our old server, so we moved it.",
		"This page was not found useful by most readers.",
		"“This page could not be found.” That is what visitors see.",
		"We can’t find that idea!",
		"Oops! We can't seem to find the page you're looking for.",
		"The page you are reading was not found by search engines.",
		"Why the page was not found.",
	}
	for _, line := range prose {
		assert.False(t, notFoundSentenceRE.MatchString(line), "should NOT match %q", line)
	}
}

// TestPageText_NotFoundNotice: a line of the opening is a not-found notice
// when it is a not-found template as a whole or opens with a not-found
// sentence; a status code alone, site names around it, only when it is a
// heading. A code block holds none.
func TestPageText_NotFoundNotice(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string // the line the reason quotes; "" for no notice
	}{
		{"a template", "Page not found", "Page not found"},
		{"a status with its words", "404 Error: Not Found", "404 Error: Not Found"},
		{"a status named an error", "Error 410", "Error 410"},
		{"a status code as a heading", "# 404", "404"},
		{"a status code as a setext heading", "410\n===", "410"},
		{"a status code and a site's name, as a heading", "## 404 - หน้าไม่พบ", "404 - หน้าไม่พบ"},
		{"a sentence", "The page you're looking for doesn't exist. Try the search.",
			"The page you're looking for doesn't exist. Try the search."},

		{"a status code alone: a question's score",
			"This question shows research effort; it is useful and clear\n\n404\n\n" +
				"This question does not show any research effort; it is unclear or not useful", ""},
		{"a status code alone: a profile's counts", "404\n\nFollowing\n\n410\n\nFollowers", ""},
		{"a status code and a label", "Votes: 404", ""},
		{"a status code and a separator", "410 · Followers", ""},
		{"a heading's status code from the origin, its markers gone", "404\nWhat the page is about.", ""},
		{"a status code in a menu", "*   [404](https://portfolio.example/inspiration/404)", ""},
		// Link lines that would be notices if their text were read: a
		// story card's heading, and a menu item.
		{"a status code as a story card's heading", "#### [404](https://publishing.example/@writer/404-5f1c2a7b9e3d)", ""},
		{"a template in a menu", "*   [Page not found](https://portfolio.example/inspiration/page-not-found)", ""},
		{"a template in a code block", "What nginx answers:\n\n```\n404 Not Found\n```", ""},
		// A question quoting the error it got, as a Q&A site's come
		// through Jina.
		{"a template quoted", "When I open the admin page I get:\n\n> HTTP Status 404 - Not Found", ""},
		{"a status line quoted", "Every request answers\n\n> 404 Not Found", ""},
		{"a status code quoted as a heading", "> ## 404", ""},
		{"a sentence quoted", "> The page you requested was not found.", ""},
		{"a sentence going on", "The page you're looking for was not found on our old server, so we moved it.", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := readPageText(tc.text)
			reason := text.notFoundNotice()
			if tc.want == "" {
				assert.Empty(t, reason)
				return
			}
			assert.Equal(t, "text reads like a not-found page: "+strconv.Quote(tc.want), reason)
		})
	}
}

// TestPageText_ParkedDomain: each subject, predicate and the parking-page
// heading, in the library's words, is a parked domain's notice on the page
// it names; a line that only resembles one, an expiry without its subject,
// or a notice naming another site isn't.
func TestPageText_ParkedDomain(t *testing.T) {
	cases := []struct {
		name   string
		target string
		text   string
		want   bool
	}{
		// A registrar's sale page, the domain's name dropped.
		{"no subject, the whole line", "https://example.io/", "The domain name\n\nis for sale!", true},
		{"this domain, a price after a colon", "http://shop.example/", "This domain is for sale: $5,795", true},
		{"this domain name, a sentence after", "https://game-site.example/",
			"This domain name may be for sale. Click here to find out or call +1-866-284-4125", true},
		{"may be for sale (Sedo)", "http://example.org/", "This domain may be for sale!", true},
		{"the domain name", "https://example.io/", "The domain name is for sale.", true},
		{"its own name", "http://coding-blog.example/?p=46", "EN/中\n\ncoding-blog.example is for sale!", true},
		{"its own name, requested with www.", "http://www.coding-blog.example/x", "coding-blog.example is for sale!", true},
		{"its own name, given with www.", "http://coding-blog.example/x", "www.coding-blog.example is for sale!", true},
		{"its own name, in any case", "http://coding-blog.example/x", "Coding-Blog.example is for sale!", true},
		{"its site's name, from a subdomain", "http://blog.coding-blog.example/x", "coding-blog.example is for sale!", true},
		{"domain, registration expired", "http://www.streaming.example/", "Domain registration has expired.", true},
		{"expired", "http://www.streaming.example/", "This domain has expired.", true},
		{"expired, its own name", "http://www.streaming.example/", "streaming.example has expired.", true},
		{"registered with", "http://images.example/", "has been recently registered with example-registrar.example", true},
		{"registered at", "http://images.example/", "This domain has been registered at example-registrar.example.", true},
		{"parked (a registrar's link)", "http://www.finance-site.example/", "This Domain is Parked: Learn more", true},
		{"hover", "http://rental.example/", "## rental.example\n\n## is a totally awesome idea still being worked on.", true},
		{"ExampleDNS", "http://www.finance-site.example/", "finance-site.example\n\nis yet another domain managed by ExampleDNS", true},
		{"search-ads heading", "http://www.wallpaper.example/", "Related Search Topics", true},
		{"search-ads heading with a colon (Sedo)", "http://example.org/", "Related Searches:", true},
		{"the domain and its own name", "http://example.org/", "The domain example.org is for sale.", true},
		{"the domain name and its own name, may be", "http://www.example.org/", "The domain name example.org may be for sale!", true},
		{"available for purchase", "http://example.org/", "This domain is available for purchase.", true},
		{"available for sale, its own name", "http://example.org/", "example.org is available for sale!", true},
		{"available for lease, no subject", "http://example.org/", "is available for lease", true},
		{"listed for sale", "http://example.org/", "This domain is listed for sale: make an offer", true},
		{"for lease", "http://example.org/", "This domain name is for lease.", true},
		{"buy this domain", "http://example.org/", "example.org\n\nBuy this domain", true},
		{"buy this domain name now", "http://example.org/", "Buy this domain name now!", true},
		{"get this domain", "http://example.org/", "Get this domain", true},
		{"lease this domain", "http://example.org/", "Lease this domain today.", true},
		{"make an offer on this domain", "http://example.org/", "Make an offer on this domain!", true},
		{"inquire about this domain", "http://example.org/", "Inquire about this domain", true},

		{"a question", "https://blog.example/expired", "Domain registration has expired? Here's what to do", false},
		{"another site's sale", "https://news.example/a-sale", "Social.example is for sale", false},
		{"another site's sale, a headline", "https://news.example/a-sale", "Social.example is for sale: what it means for users", false},
		{"a predicate going on", "https://cars.example/", "is for sale for $10 million", false},
		{"an expiry without its subject", "https://events.example/group", "Registration has expired.", false},
		{"an expiry alone", "https://shop.example/coupon", "has expired", false},
		{"a predicate going on with a comma", "https://blog.example/", "This domain is for sale, says the registrar", false},
		{"no subject, a sentence after", "https://cars.example/", "is parked. The owner left.", false},
		{"other related links", "https://blog.example/", "Related Links", false},
		{"the domain and another site's name", "https://news.example/sale", "The domain example.com is for sale.", false},
		{"available for purchase, going on", "https://shop.example/", "is available for purchase in three colours", false},
		{"available, said of a product", "https://shop.example/", "This lamp is available for purchase.", false},
		{"for lease, said of an office", "https://realty.example/", "The office is for lease.", false},
		{"make an offer, a shop's button", "https://shop.example/", "Make an offer", false},
		{"buy this domain, a headline going on", "https://blog.example/", "Buy this domain before your rival does: a guide", false},
		{"get this domain, a question", "https://blog.example/", "How do I get this domain?", false},
		{"buy this book", "https://shop.example/", "Buy this book", false},
		{"a notice quoted", "http://example.org/", "The registrar's page said:\n\n> This domain is for sale!", false},
		{"registered with no registrar's domain", "http://images.example/", "This domain has been registered with the city.", false},
		{"a totally awesome idea, said of something else", "http://rental.example/", "Our app is a totally awesome idea still being worked on.", false},
		{"managed by ExampleDNS, another site", "http://www.finance-site.example/", "example.com is yet another domain managed by ExampleDNS", false},
		{"managed by ExampleDNS, going on", "http://www.finance-site.example/", "finance-site.example is yet another domain managed by ExampleDNS, and here is our blog", false},
		{"after an intro paragraph", "https://blog.example/expired",
			strings.Repeat("Domains lapse more often than you think. ", 6) + "\n\nThis domain is for sale!", false},
		{"past the opening (a retailer's category pages)", "https://www.home-improvement.example/en/home/categories/x.html",
			strings.Repeat("Paint sprayers for every job, from fences to cabinets.\n", 40) + "Related Searches", false},
		{"a notice beginning a byte inside parkedNoticeBytes", "http://example.org/",
			strings.Repeat("a", 126) + "\n" + strings.Repeat("b", 127) + "\nThis domain is for sale!", true},
		{"a notice beginning at parkedNoticeBytes", "http://example.org/",
			strings.Repeat("a", 127) + "\n" + strings.Repeat("b", 127) + "\nThis domain is for sale!", false},
		{"a search results page's related searches", "https://www.search.example/search?q=nginx+404+page", searchResults(), false},
	}
	results, inOpening := readPageText(searchResults()), false
	for l := range results.opening() {
		inOpening = inOpening || l.text == "Related searches"
	}
	require.True(t, inOpening, "the results page's related searches are in its opening")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := readPageText(tc.text)
			reason := text.parkedDomain(tc.target)
			assert.Equal(t, tc.want, reason != "", "reason %q", reason)
			if tc.want {
				assert.Contains(t, reason, "text reads like a parked domain: \"")
			}
		})
	}
}

// TestPageText_SignInForm: a short page whose text opens with a password
// field and a sign-in line is a sign-in form; a long one, one that shows
// something else first, or a sign-up form isn't.
func TestPageText_SignInForm(t *testing.T) {
	// form is a sign-in form padded to own bytes of own text with a line of
	// prose; its password field begins at at.
	form := func(at, own int) string {
		lead := "Log in\n" + strings.Repeat("x", at-len("Log in\n")-1) + "\n"
		text := lead + "Password\n"
		return text + strings.Repeat("y", own-len(text)-1)
	}
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"a social site", socialSignInBody, true},
		{"another social site", networkSignInBody, true},
		{"a professional network", professionalSignInBody, true},
		{"a field with a colon", "Sign in\n\nUsername:\n\nPassword:", true},
		{"a field marked required", "LOGIN\n\nUser Name *\n\nPassword *", true},
		{"a field in capitals", "Log In\n\nEmail\n\nPASSWORD", true},
		{"the field just inside the form's bytes", form(signInFormBytes-1, 600), true},
		{"the field at the form's bytes", form(signInFormBytes, 600), false},
		{"at the page's bytes", form(100, signInPageBytes), true},
		{"over the page's bytes", form(100, signInPageBytes+1), false},
		// A public organization page: its intro and a post, then
		// the sign-in box, 579 bytes of own text.
		{"a public page, its sign-in box last", "[Log In](https://www.social.example/login/)\n\nLog In\n\n" +
			"# Example Organization\n\n2.2K followers • 4 following\n\n## Intro\n\n" +
			"Example Organization is the local chapter of the a professional association, the professional " +
			"association for over 10,000 video and computer game developers worldwide.\n\n" +
			"Page · Nonprofit organization\n\ncontact@example.org\n\nSeptember 18 at 6:41 PM ·\n\nOur next event is up!!…\n\n" +
			"See more from Example Organization\n\nEmail or phone number\n\nPassword\n\nLog In", false},
		// A news site's: a sign-in box above 31 KB of story.
		{"a long page, a sign-in box at its top", "Log in\n\nNickname:\n\nPassword:\n\n" +
			strings.Repeat("A whitelist for phone calls would stop the robocalls. ", 30), false},
		// A form's: a sign-up form, no sign-in line.
		{"a sign-up form", "Fill in the form below to get instant access.\n\n- Username\n- Password\n- Password Confirmation\n" +
			"- First Name\n- Last Name\n- E-mail Address", false},
		{"a field that isn't one", "Log in\n\nForgot password?\n\nPassword reset", false},
		// A sign-up form whose only sign-in line is a link: read, the link
		// would make it a sign-in form.
		{"a sign-up form, its sign-in line a link", "[Log in](https://example.com/login)\n\n" +
			"Create your account\n\nEmail\n\nPassword\n\nSign up", false},
		{"read in part", "Log in\n\nPassword\n\n" + navigation(pageTextScanBytes), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := readPageText(tc.text)
			reason := text.signInForm()
			assert.Equal(t, tc.want, reason != "", "reason %q, own text %d", reason, text.own)
		})
	}
}

// TestQuoteLine: a quoted line is cut on a rune boundary, its ellipsis
// within the limit.
func TestQuoteLine(t *testing.T) {
	assert.Equal(t, `"PAGE NOT FOUND"`, quoteLine("PAGE NOT FOUND"))
	exact := strings.Repeat("a", maxQuotedLine)
	assert.Equal(t, `"`+exact+`"`, quoteLine(exact))

	thai := strings.Repeat("หน้าไม่พบ ", 20) // 3-byte runes
	got := quoteLine(thai)
	require.True(t, strings.HasSuffix(got, `…"`), got)
	inner := got[1 : len(got)-1]
	assert.LessOrEqual(t, len(inner), maxQuotedLine)
	assert.True(t, utf8.ValidString(inner))
	assert.True(t, strings.HasPrefix(thai, strings.TrimSuffix(inner, "…")))

	assert.Equal(t, `"say \"hi\"\tnow"`, quoteLine("say \"hi\"\tnow"), "quoted as Go quotes it")
}

// searchResults is a search results page as Jina renders a search engine's: ten
// results, each a heading of links, its address and a snippet, then the
// related searches.
func searchResults() string {
	var b strings.Builder
	for i := range 10 {
		fmt.Fprintf(&b, "### [Custom error pages in nginx, part %d](https://example.com/nginx/%d)\n\n"+
			"[example.com › nginx › %d](https://example.com/nginx/%d)\n\n", i, i, i, i)
		b.WriteString("Sep 4, 2026 — How to serve a custom 404 page from nginx with the error_page directive, " +
			"check it with curl, and keep the status a real 404 so that search engines drop the page.\n\n")
	}
	b.WriteString("Related searches\n\n*   [nginx 404 page](https://www.search.example/search?q=nginx+404+page)\n")
	return b.String()
}

// navItem is a line of a menu, as Jina renders it.
const navItem = "*   [Jobs](https://example.com/jobs)\n"

// navigation is a menu of navItem lines, the most that fit in size bytes.
func navigation(size int) string {
	return strings.Repeat(navItem, size/len(navItem))
}
