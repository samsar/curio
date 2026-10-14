package indexer

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/store"
)

// contextCase is a document and the headings of one of its chunks.
type contextCase struct {
	name     string
	doc      DocumentContext
	headings []string
	want     string // for TestContext; "" leaves the case to the golden alone
}

// site is a fields row's site.
func site(name, host string) *store.DocumentSite { return &store.DocumentSite{Name: name, Host: host} }

// contextCases cover every rule of Context, for its test and its golden.
var contextCases = []contextCase{
	{name: "titled, its site named, a path", want: "Why queues fail — Example Times — Consumers › Groups",
		doc:      DocumentContext{URL: "https://example.com/q", Title: "Why queues fail", Site: site("Example Times", "example.com")},
		headings: []string{"Queues", "Consumers", "Groups"}},
	{name: "untitled, no bookmarks", want: "example.com",
		doc: DocumentContext{URL: "https://example.com/a", Site: site("", "example.com")}},
	{name: "untitled: the newest titled bookmark", want: "Newest titled — example.com — Intro",
		doc: DocumentContext{URL: "https://example.com/a", Site: site("", "example.com"),
			Bookmarks: []docmeta.Bookmark{{Title: " \t"}, {Title: "Newest  titled"}, {Title: "Older"}}},
		headings: []string{"Intro"}},
	{name: "a title of whitespace is none", want: "Saved — example.com",
		doc: DocumentContext{URL: "https://example.com/a", Title: " \n\t ", Site: site("", "example.com"),
			Bookmarks: []docmeta.Bookmark{{Title: "Saved"}}}},
	{name: "a title's whitespace collapsed", want: "Two lines — example.com",
		doc: DocumentContext{URL: "https://example.com/a", Title: " Two\n lines ", Site: site("", "example.com")}},
	{name: "no site name: the host", want: "Notes — blog.example.org",
		doc: DocumentContext{URL: "https://blog.example.org/n", Title: "Notes", Site: site("", "blog.example.org")}},
	{name: "no fields row: the URL's host", want: "Notes — example.com",
		doc: DocumentContext{URL: "https://www.Example.COM:8080/n", Title: "Notes"}},
	{name: "no fields row and no host", want: "Notes",
		doc: DocumentContext{URL: "not a url", Title: "Notes"}},
	{name: "nothing at all", want: ""},
	{name: "a long title and site, cut",
		doc: DocumentContext{URL: "https://example.com/l", Title: strings.Repeat("long title ", 30),
			Site: site(strings.Repeat("Site ", 40), "example.com")}, headings: []string{"Section"}},
	{name: "a heading equal to the title", want: "Why Queues Fail — Example Times — Consumers",
		doc:      DocumentContext{URL: "https://example.com/q", Title: "Why Queues Fail", Site: site("Example Times", "example.com")},
		headings: []string{"why  queues FAIL", "Consumers"}},
	{name: "a heading the title begins with, then a separator", want: "Why queues fail | Example Times — Example Times — Consumers",
		doc: DocumentContext{URL: "https://example.com/q", Title: "Why queues fail | Example Times",
			Site: site("Example Times", "example.com")},
		headings: []string{"Why queues fail", "Consumers"}},
	{name: "a heading the title begins with, no separator, is kept",
		want: "Why queues fail today — example.com — Why queues fail",
		doc: DocumentContext{URL: "https://example.com/q", Title: "Why queues fail today",
			Site: site("", "example.com")}, headings: []string{"Why queues fail"}},
	{name: "a heading equal to the site", want: "Post — Example Times — Comments",
		doc:      DocumentContext{URL: "https://example.com/p", Title: "Post", Site: site("Example Times", "example.com")},
		headings: []string{"example times", "Comments"}},
	{name: "a heading equal to the host", want: "Post — example.com — Comments",
		doc:      DocumentContext{URL: "https://example.com/p", Title: "Post", Site: site("", "example.com")},
		headings: []string{"Example.com", "Comments"}},
	{name: "a path over 120 bytes: the nearest alone",
		doc:      DocumentContext{URL: "https://example.com/p", Title: "Post", Site: site("", "example.com")},
		headings: []string{strings.Repeat("parent words ", 6), strings.Repeat("nearest words ", 5)}},
	{name: "a nearest heading over 120 bytes, cut",
		doc:      DocumentContext{URL: "https://example.com/p", Title: "Post", Site: site("", "example.com")},
		headings: []string{"Parent", strings.Repeat("nearest words ", 12)}},
	{name: "unicode", want: "Bücher über Bücher — Bücherei — 第一章 › 日本語の見出し",
		doc:      DocumentContext{URL: "https://bücher.example/ü", Title: "Bücher über Bücher", Site: site("Bücherei", "bücher.example")},
		headings: []string{"本", "第一章", "日本語の見出し"}},
	{name: "a multi-byte cut",
		doc: DocumentContext{URL: "https://example.com/j", Title: strings.Repeat("日本語", 30),
			Site: site(strings.Repeat("語", 50), "example.com")}, headings: []string{strings.Repeat("見出し", 20)}},
	{name: "hostile values",
		doc: DocumentContext{URL: "javascript:alert(1)", Title: "<script>alert(1)</script> — \u202e — ",
			Bookmarks: []docmeta.Bookmark{{Title: "\x00"}}, Site: site(" — ", "")},
		headings: []string{" — ", "<b>bold</b>", "›"}},
}

// TestContext: a chunk's context by each rule.
func TestContext(t *testing.T) {
	for _, tc := range contextCases {
		if tc.want == "" && tc.name != "nothing at all" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Context(tc.doc, tc.headings))
		})
	}
	t.Run("every title separator", func(t *testing.T) {
		for _, sep := range titleSeparators {
			doc := DocumentContext{URL: "https://example.com/q", Title: "Queues" + sep + "a guide"}
			assert.Equal(t, doc.Title+" — example.com", Context(doc, []string{"queues"}), "%q", sep)
		}
	})
	t.Run("blank and padded headings", func(t *testing.T) {
		doc := DocumentContext{URL: "https://example.com/a", Title: "Post"}
		for _, tc := range []struct {
			headings []string
			want     string
		}{
			{[]string{"A", ""}, "Post — example.com — A"},
			{[]string{"", "B"}, "Post — example.com — B"},
			{[]string{"A", " \t"}, "Post — example.com — A"},
			{[]string{"A", "B "}, "Post — example.com — A › B"},
			{[]string{" A\n a ", "B"}, "Post — example.com — A a › B"},
		} {
			assert.Equal(t, tc.want, Context(doc, tc.headings), "%q", tc.headings)
		}
	})
	t.Run("the parts are cut", func(t *testing.T) {
		got := Context(contextCases[9].doc, contextCases[9].headings)
		parts := strings.Split(got, contextSeparator)
		assert.Len(t, parts, 3, got)
		assert.LessOrEqual(t, len(parts[0]), maxContextTitleBytes)
		assert.LessOrEqual(t, len(parts[1]), maxContextSiteBytes)
		assert.Equal(t, "Section", parts[2])
	})
}

// TestContextGolden: the context rules' limits, the headings the chunker
// gives the corpus's chunks and the context of every case are those
// recorded for ContextVersion. A change to any of them needs a new
// version, so the daemon re-embeds every home's chunks: bump
// ContextVersion, then record its golden with -update (which never
// overwrites one).
func TestContextGolden(t *testing.T) {
	golden(t, filepath.Join("testdata", fmt.Sprintf("context_v%d.golden", ContextVersion)), renderContext(),
		"the chunks' context changed: bump ContextVersion and record its golden")
}

// renderContext writes the rules' limits and separators, every corpus
// chunk's headings, and each case's context, as text.
func renderContext() string {
	var b strings.Builder
	fmt.Fprintf(&b, "indexer context v%d\n", ContextVersion)
	fmt.Fprintf(&b, "\nlimits title %d site %d path %d heading %d context %d\n", maxContextTitleBytes,
		maxContextSiteBytes, maxContextPathBytes, maxHeadingBytes, maxContextBytes)
	fmt.Fprintf(&b, "separators part %q path %q title %q heading trim %q\n", contextSeparator, pathSeparator,
		titleSeparators, headingTrim)
	for _, doc := range corpus() {
		for _, set := range doc.optionSets() {
			fmt.Fprintf(&b, "\nheadings of %q options %q\n", doc.name, set.name)
			for i, c := range ChunkText(doc.markdown, set.opts) {
				fmt.Fprintf(&b, "  %d %q\n", i, c.Headings)
			}
		}
	}
	for _, tc := range contextCases {
		fmt.Fprintf(&b, "\ncontext %q\n  url %q title %q bookmarks %q site %s\n  headings %q\n  = %q\n", tc.name,
			tc.doc.URL, tc.doc.Title, tc.doc.Bookmarks, siteText(tc.doc.Site), tc.headings,
			Context(tc.doc, tc.headings))
	}
	return b.String()
}

// siteText is a fields row's site as the golden shows it.
func siteText(s *store.DocumentSite) string {
	if s == nil {
		return "none"
	}
	return fmt.Sprintf("%q %q", s.Name, s.Host)
}

// FuzzContext: a context is valid UTF-8 for valid input, at most
// maxContextBytes, with no space at either end, and its heading path holds
// no empty or padded heading.
func FuzzContext(f *testing.F) {
	f.Add("https://example.com/a", "Why queues fail | Example", "Saved", "Example", "example.com", "Queues",
		"Consumers")
	f.Add("", "", "", "", "", "", "")
	f.Add("x", strings.Repeat("日本語", 100), "", strings.Repeat("語", 80), "h", strings.Repeat("見", 50), " — ")
	f.Fuzz(func(t *testing.T, url, title, saved, name, host, parent, nearest string) {
		var s *store.DocumentSite
		if host != "" {
			s = site(name, host)
		}
		got := Context(DocumentContext{URL: url, Title: title, Bookmarks: []docmeta.Bookmark{{Title: saved}}, Site: s},
			[]string{parent, nearest})
		if len(got) > maxContextBytes {
			t.Fatalf("context of %d bytes: %q", len(got), got)
		}
		if strings.TrimSpace(got) != got {
			t.Fatalf("context with space at an end: %q", got)
		}
		if p := headingPath([]string{parent, nearest}, collapse(title), name); p != "" {
			for h := range strings.SplitSeq(p, pathSeparator) {
				if h == "" || strings.TrimSpace(h) != h {
					t.Fatalf("heading path %q holds the heading %q", p, h)
				}
			}
		}
		for _, in := range []string{url, title, saved, name, host, parent, nearest} {
			if !utf8.ValidString(in) {
				return
			}
		}
		if !utf8.ValidString(got) {
			t.Fatalf("context is not valid UTF-8: %q", got)
		}
	})
}
