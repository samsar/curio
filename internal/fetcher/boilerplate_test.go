package fetcher

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prose is a line of an article's own text longer than bodyLineBytes: the
// page's body runs from the first such line to the last.
func prose(topic string) string {
	return "This paragraph is part of the article about " + topic + ", and it goes on for long enough " +
		"to be prose rather than a label, a button or a line of a site's footer, which are all short, " +
		"so the text rules read it as a line of the page's body."
}

// TestDropBoilerplate: a Jina answer loses its menus, story cards, empty
// links, images and, outside its body, its site's chrome lines; its own
// text, headings, quotes, code and the links a section holds stay.
func TestDropBoilerplate(t *testing.T) {
	assert.Greater(t, len(prose("a")), bodyLineBytes, "prose is a line of the body")
	const target = "https://news.example/2026/a-story"
	cases := []struct {
		name string
		md   string
		want string // "" for md unchanged
	}{
		{"a menu block", "[Home](https://news.example/)\n[World](https://news.example/world)\n\n" + prose("rivers"),
			prose("rivers")},
		{"a menu of list items, nested", "*   [Home](https://news.example/)\n    *   [World](https://news.example/world)\n" +
			"1.   [Sport](https://news.example/sport)\n\n" + prose("rivers"),
			prose("rivers")},
		{"list items of links among text items",
			prose("rivers") + "\n\n*   Plain item\n*   [Linked item](https://news.example/x)\n*   Another plain item",
			prose("rivers") + "\n\n*   Plain item\n*   Another plain item"},
		{"a row of share buttons on one line",
			prose("rivers") + "\n\n[Share](https://x.example/share)[Post](https://y.example/post)\n\n" + prose("lakes"),
			prose("rivers") + "\n\n" + prose("lakes")},
		{"a story card under a heading of links",
			prose("rivers") + "\n\n#### [Another story](https://news.example/other)\n\n" +
				"[![Image 2: An author](https://img.example/a.png)](https://news.example/@a)\n\n" +
				"[An author](https://news.example/@a)[in A Column](https://news.example/c)",
			prose("rivers") + "\n\n#### [Another story](https://news.example/other)"},
		{"images and empty links that show nothing",
			"![Image 1: site logo](https://img.example/logo.png)\n\n[](https://news.example/#top)\n\n" + prose("rivers"),
			prose("rivers")},
		{"an image inside a paragraph", "Some words.\n![Image 3](https://img.example/x.png)\nMore words.",
			"Some words.\nMore words."},
		{"a skip link and a sign-up dialog before the body",
			"[Skip to main content](https://news.example/#main)\n\nJoin Example News\n\n" +
				"By clicking “Sign up”, you agree to our terms of service and acknowledge you have read our privacy policy.\n\n" +
				"Sign up with Google\n\nSign up with GitHub\n\n# The story\n\n" + prose("rivers"),
			"Join Example News\n\n# The story\n\n" + prose("rivers")},
		{"a legal footer after the body",
			prose("rivers") + "\n\nPrivacy Policy\n\nTerms of Service\n\nYour Privacy Choices\n\n" +
				"Do Not Sell or Share My Personal Information\n\n© 2026 Example News LLC. All rights reserved.\n\n" +
				"By continuing to use this website, you agree we can store cookies on your device.\n\n" +
				"Necessary cookies only\n\nAccept all cookies",
			prose("rivers")},
		{"an article that discusses cookies and privacy policies in its body",
			prose("cookie banners") + "\n\nWe use cookies to measure you, the banner said.\n\n" +
				"Privacy Policy\n\n© 2026 is when the rule took effect.\n\n" + prose("consent"),
			""},
		{"chrome words inside a line that isn't a template",
			"Our privacy policy changed in March, and here is how.\n\n" + prose("policy"), ""},
		{"a line of links inside a wrapped paragraph",
			"Read the guide at\n[the official documentation](https://docs.example/)\nbefore you start.", ""},
		{"a table of contents under a heading of plain text",
			"## Table of Contents\n\n*   [Introduction](https://docs.example/intro)\n*   Part one\n" +
				"    *   [Setting up](https://docs.example/setup)\n\n*   [Appendix](https://docs.example/a)\n\n" + prose("threads"),
			""},
		{"a table of contents under a heading that links to the page itself",
			"## [Contents](https://news.example/2026/a-story#toc)\n\n*   [One](https://news.example/one)\n" +
				"*   [Two](https://news.example/two)\n\n" + prose("specs"),
			""},
		{"a section's links end at its next paragraph",
			"## Further reading\n\n*   [A book](https://books.example/a)\n\n" + prose("rivers") +
				"\n\n*   [Elsewhere](https://news.example/e)",
			"## Further reading\n\n*   [A book](https://books.example/a)\n\n" + prose("rivers")},
		{"a setext heading of plain text holds its links",
			"Contents\n========\n\n*   [One](https://docs.example/one)\n\n" + prose("specs"), ""},
		{"quoted links and fenced code stay",
			"> [A quoted link](https://quotes.example/)\n\n```\n[not a menu](https://code.example/)\n```\n\n" + prose("quotes"),
			""},
		{"links after a lead-in ending with a colon",
			"This guide covers the following topics:\n\n*   [Signing](https://docs.example/sign)\n" +
				"*   [Policies](https://docs.example/policy)\n\n" + prose("guides"),
			""},
		{"links after a bold lead-in",
			"**Contents**\n\n*   [One](https://docs.example/one)\n*   [Two](https://docs.example/two)\n\n" + prose("guides"),
			""},
		{"links after a lead-in in the same block", "See:\n* [One](https://docs.example/one)\n* [Two](https://docs.example/two)", ""},
		{"links after a list item ending with a colon",
			"*   Next steps:\n    *   [Install](https://docs.example/install)\n\n" + prose("guides"), ""},
		{"a sentence that is no lead-in ends a section",
			"**Contents**\n\n*   [One](https://docs.example/one)\n\nThat was the list.\n\n*   [Menu](https://news.example/)",
			"**Contents**\n\n*   [One](https://docs.example/one)\n\nThat was the list."},
		{"a dropped item's indented paragraph is dedented",
			prose("rivers") + "\n\n*   [Courses](https://news.example/courses)\n\n    A description under the item.\n\n" +
				"    *   Plain sub item\n\nBack at the margin.",
			prose("rivers") + "\n\nA description under the item.\n\n*   Plain sub item\n\nBack at the margin."},
		{"a dropped nested item's paragraph joins its parent",
			"*   Parent item\n    *   [Child](https://news.example/c)\n\n        The child's note.\n\n" + prose("rivers"),
			"*   Parent item\n\n    The child's note.\n\n" + prose("rivers")},
		{"hard-wrapped prose keeps a chrome-like line, a footer's goes",
			"The company updated its\nPrivacy Policy\nlast week, and the change\nreaches every user.\n\nTerms of Service",
			"The company updated its\nPrivacy Policy\nlast week, and the change\nreaches every user."},
		{"a PDF-like answer keeps its section titles",
			"Annual Report\n\n" + strings.Repeat("Revenue grew in every region this year,\nled by new markets.\n", 4) +
				"\nPrivacy Policy\n\n" + strings.Repeat("We describe here how the\nfund handles records.\n", 6) +
				"\nTerms of Use\n\n" + strings.Repeat("The terms below apply\nto every holder of a unit.\n", 6),
			""},
		{"a footer after wrapped prose",
			strings.Repeat("Revenue grew in every region this year,\nled by new markets.\n", 4) +
				"\nPrivacy Policy\n\n© 2026 Example LLC. All rights reserved.",
			strings.TrimSuffix(strings.Repeat("Revenue grew in every region this year,\nled by new markets.\n", 4), "\n")},
		{"a photo credit in the block of a caption",
			prose("rivers") + "\n\nThe river at dawn.\n© 2019 A Photographer", ""},
		{"runs of blank lines become one",
			prose("rivers") + "\n\n\n[Menu](https://news.example/)\n\n\n\n" + prose("lakes"),
			prose("rivers") + "\n\n" + prose("lakes")},
		{"a page of links alone stays whole", "*   [One](https://a.example/)\n*   [Two](https://b.example/)", ""},
		{"nothing to drop", "# Title\n\n" + prose("rivers"), ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == "" {
				want = tc.md
			}
			assert.Equal(t, want, dropBoilerplate(tc.md, target))
		})
	}
}

// TestDropBoilerplate_ChromeOnlyOutsideTheBody: a chrome line between two
// lines of prose is the article's; before the first or after the last, it
// is the site's. A page with no prose has no body, and its chrome lines go.
func TestDropBoilerplate_ChromeOnlyOutsideTheBody(t *testing.T) {
	const chrome = "Privacy Policy"
	md := chrome + "\n\n" + prose("a") + "\n\n" + chrome + "\n\n" + prose("b") + "\n\n" + chrome
	assert.Equal(t, prose("a")+"\n\n"+chrome+"\n\n"+prose("b"), dropBoilerplate(md, ""))
	assert.Equal(t, "A short page.", dropBoilerplate("A short page.\n\n"+chrome, ""))
	atCap := "© 2026 " + strings.Repeat("x", chromeLineBytes-len("© 2026 "))
	assert.Equal(t, "Short.", dropBoilerplate("Short.\n\n"+atCap, ""), "a chrome line of chromeLineBytes goes")
	over := "Short.\n\n" + atCap + "x"
	assert.Equal(t, over, dropBoilerplate(over, ""), "a line over chromeLineBytes stays")
}

// TestChromeLineRE: every template of a site's chrome as a whole line,
// and lines that only mention its words.
func TestChromeLineRE(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"Skip to main content", true},
		{"Skip to content", true},
		{"SKIP TO THE MAIN CONTENT", true},
		{"Skip to navigation", true},
		{"Skip to search", true},
		{"Skip to footer", true},
		{"© 2026 Example Media LLC. All rights reserved.", true},
		{"©2005-2026 Example Forum. All Rights Reserved.", true},
		{"Site design / logo © 2026 Example Inc; user contributions licensed under CC BY-SA.", true},
		{"Copyright © 2010 - 2026 Example GmbH", true},
		{"Copyright 1998-2026 Example LLC.", true},
		{"(c) 2026 Example", true},
		{"All rights reserved.", true},
		{"© Example Conferences, LLC. All rights reserved.", true},
		{"We use cookies to personalise the content you see.", true},
		{"This website uses cookies", true},
		{"This site uses cookies from a provider to deliver its services.", true},
		{"Our website uses cookies.", true},
		{"By continuing to use this website, you agree we can store cookies on your device.", true},
		{"By using our site, you consent to cookies.", true},
		{"Accept all cookies", true},
		{"Reject cookies", true},
		{"Allow all cookies", true},
		{"Necessary cookies only", true},
		{"Cookie settings", true},
		{"Cookie Preferences", true},
		{"By clicking “Sign up”, you agree to our terms of service and acknowledge you have read our privacy policy.", true},
		{"By signing up, you agree to our Terms.", true},
		{"By creating an account you agree to the rules.", true},
		{"By continuing, you agree to our Terms of Service and Privacy Policy.", true},
		{"Sign up with Google", true},
		{"Sign in with GitHub", true},
		{"Log in with Apple", true},
		{"Continue with email", true},
		{"Continue with Microsoft", true},
		{"Sign up with Facebook", true},
		{"Sign in with LinkedIn", true},
		{"Continue with Twitter", true},
		{"Continue with X", true},
		{"Privacy Policy", true},
		{"Cookie Policy", true},
		{"Terms of Service", true},
		{"Terms of Use", true},
		{"Terms & Conditions", true},
		{"Terms and Conditions", true},
		{"Your Privacy Choices", true},
		{"Do Not Sell My Personal Information", true},
		{"Do not sell or share my personal information", true},
		{"Do Not Share My Personal Info", true},

		{"Skip to the good part: the results", false},
		{"Copyright law changed in 1976.", false},
		{"Our copyright 1976 act summary", false},
		{"The © symbol is a letter C in a circle.", false},
		{"We use cookie cutters for the dough.", false},
		{"Cookies are small files a site stores in your browser.", false},
		{"Browsers block third-party cookies by default.", false},
		{"Privacy policies are long and nobody reads them.", false},
		{"Our privacy policy changed in March.", false},
		{"Sign up with a friend and save", false},
		{"Continue with the next step", false},
		{"Terms of service matter", false},
		{"By clicking the button you start the timer.", false},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			assert.Equal(t, tc.want, chromeLineRE.MatchString(tc.line))
		})
	}
}

// TestLinksHere: a heading's links lead to the page itself when they are
// fragments, or the page's URL whatever its scheme, "www.", fragment and
// trailing slash.
func TestLinksHere(t *testing.T) {
	const target = "https://www.docs.example/spec/"
	cases := []struct {
		raw  string
		want bool
	}{
		{"## [Contents](#toc)", true},
		{"## [Contents](http://docs.example/spec#toc)", true},
		{"## [Contents](https://www.docs.example/spec/ \"Contents\")", true},
		{"## [Contents](https://docs.example/other)", false},
		{"## [Contents](#toc) [Elsewhere](https://docs.example/other)", false},
		{"## [](https://docs.example/spec)", true},
		{"## No links", false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			assert.Equal(t, tc.want, linksHere(tc.raw, target))
		})
	}
}

// TestNative_JinaAnswerStoredWithoutChrome: a Jina answer that passes its
// judgement is stored less its menu and footer.
func TestNative_JinaAnswerStoredWithoutChrome(t *testing.T) {
	origin := serveThinPage(t)
	defer origin.Close()
	article := strings.TrimSpace(strings.Repeat("Article text rendered by Jina. ", 20))
	menu := "*   [Home](https://news.example/)\n*   [World](https://news.example/world)\n"
	footer := "Privacy Policy\n\n© 2026 Example News LLC. All rights reserved."
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, jinaArticle+menu+"\n"+article+"\n\n"+footer)
	}))
	defer jina.Close()

	n := unpaced(newTestNative(NativeOptions{
		Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/",
	}), newFakeClock())
	res, err := n.Fetch(t.Context(), origin.URL)
	require.NoError(t, err)
	require.Equal(t, "jina", res.Meta["via"])
	assert.Equal(t, article, res.Markdown)
}

// TestNative_JinaJudgedBeforeCleaning: the judgement sees Jina's answer as
// Reader sent it, menus and all. An answer whose own text is short of the
// thin floor, carried past it by a menu, passes and is stored without the
// menu; the cleaning never makes it thin.
func TestNative_JinaJudgedBeforeCleaning(t *testing.T) {
	origin := serveThinPage(t)
	defer origin.Close()
	own := "A short note that the page itself holds."
	menu := strings.Repeat("*   [A menu item](https://news.example/section/with/a/long/path)\n", 12)
	require.Less(t, len(own), minArticleBytes)
	require.GreaterOrEqual(t, len(menu+own), minArticleBytes)
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, jinaArticle+menu+"\n"+own)
	}))
	defer jina.Close()

	n := unpaced(newTestNative(NativeOptions{
		Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/",
	}), newFakeClock())
	res, err := n.Fetch(t.Context(), origin.URL)
	require.NoError(t, err)
	assert.Equal(t, own, res.Markdown)
}
