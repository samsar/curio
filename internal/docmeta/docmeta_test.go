package docmeta

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/urlutil"
)

var update = flag.Bool("update", false, "record testdata/rules_v<RulesVersion>.golden, which must not exist yet")

func TestHost(t *testing.T) {
	cases := map[string]string{
		"https://www.Example.COM:8443/a?b#c": "example.com",
		"http://blog.example.co.uk./post":    "blog.example.co.uk",
		"https://wwwexample.com/":            "wwwexample.com",
		"https://www.www.example.com/":       "www.example.com",
		"https://xn--bcher-kva.example/":     "xn--bcher-kva.example",
		"https://bücher.example/":            "bücher.example",
		"http://[::1]:8765/ui/":              "::1",
		"http://127.0.0.1/":                  "127.0.0.1",
		"mailto:someone@example.com":         "",
		"not a url":                          "",
		"https://exa mple.com/":              "",
		"":                                   "",
	}
	for in, want := range cases {
		assert.Equal(t, want, Host(in), in)
	}
}

func TestHostWords(t *testing.T) {
	cases := map[string]string{
		"stratechery.com":         "stratechery",
		"ben-evans.com":           "ben evans",
		"blog.example.co.uk":      "example blog",
		"user.github.io":          "user",
		"www.example.com":         "example",
		"a.www.b.example.com":     "example a b",
		"Docs.Example.COM.":       "example docs",
		"xn--bcher-kva.example":   "xn bcher kva",
		"xn--bcher-kva.ch":        "xn bcher kva",
		"bücher.ch":               "bücher",
		"my--odd-.example.org":    "example my odd",
		"localhost":               "localhost",
		"github.io":               "github io",
		"127.0.0.1":               "127 0 0 1",
		"::1":                     "1",
		"":                        "",
		"news.ycombinator.com":    "ycombinator news",
		"en.m.wikipedia.org":      "wikipedia en m",
		"self-hosted.lan":         "self hosted",
		"intranet":                "intranet",
		"x.y.z.example-site.blog": "example site x y z",
	}
	for host, want := range cases {
		assert.Equal(t, want, HostWords(host), host)
	}
}

func TestFolderText(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  string
	}{
		{"none", nil, ""},
		{"a root alone", []string{"/Bookmarks Bar", "/Favorites/", "/com.apple.ReadingList"}, ""},
		{"the root dropped", []string{"/Bookmarks Bar/Tech/AI"}, "Tech / AI"},
		{"each root, any case", []string{"/other bookmarks/a", "/MOBILE BOOKMARKS/b", "/Favorites/c",
			"/Bookmarks Menu/d", "/bookmarks toolbar/e", "/com.apple.readinglist/f"}, "a\nb\nc\nd\ne\nf"},
		{"a deeper root name kept", []string{"/Bookmarks Bar/Favorites/Recipes"}, "Favorites / Recipes"},
		{"no root", []string{"/Tech/Go"}, "Tech / Go"},
		{"distinct, in order", []string{"/B/x", "/Bookmarks Bar/A", "/Other Bookmarks/A", "/B/x"}, "B / x\nA"},
		{"empty segments and spaces", []string{"//Tech//  Deep   Learning /", ""}, "Tech / Deep Learning"},
		{"unicode", []string{"/Favorites/Café/日本語"}, "Café / 日本語"},
		{"a root's name inside a segment", []string{"/My Bookmarks Bar/x"}, "My Bookmarks Bar / x"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, FolderText(tc.paths), tc.name)
	}
}

func TestDeclaredSite(t *testing.T) {
	cases := []struct{ raw, host, want string }{
		{"The Example Times", "example.com", "The Example Times"},
		{"  The  Example\n Times ", "example.com", "The Example Times"},
		{"", "example.com", ""},
		{"   ", "example.com", ""},
		{"Medium", "medium.com", ""},
		{"MEDIUM", "example.com", ""},
		{"wikimedia foundation, inc.", "en.wikipedia.org", ""},
		{"Amazon Web Services, Inc.", "aws.amazon.com", ""},
		{"Substack", "x.substack.com", ""},
		{"example.com", "example.com", ""},
		{"WWW.Example.com", "example.com", ""},
		{"Example.co.uk", "blog.example.co.uk", ""},
		{"blog.example.co.uk", "blog.example.co.uk", ""},
		{"Example", "example.com", "Example"},
		{"Café Société", "example.fr", "Café Société"},
		{strings.Repeat("é", 100), "example.com", strings.Repeat("é", 60)},
		{"Name", "", "Name"},
	}
	for _, tc := range cases {
		got := DeclaredSite(tc.raw, tc.host)
		assert.Equal(t, tc.want, got, "%q on %q", tc.raw, tc.host)
		assert.LessOrEqual(t, len(got), maxDeclaredSiteBytes)
		assert.True(t, utf8.ValidString(got))
	}
}

func TestTitleSuffix(t *testing.T) {
	cases := []struct{ title, host, want string }{
		{"Why queues fail | The Example Times", "example.com", "The Example Times"},
		{"Why queues fail – Example Blog", "example.com", "Example Blog"},
		{"Why queues fail — Example Blog", "example.com", "Example Blog"},
		{"Why queues fail - Example Blog", "example.com", "Example Blog"},
		{"Why queues fail · Example Blog", "example.com", "Example Blog"},
		{"A - B | C · Site Name", "example.com", "Site Name"},
		{"A | B - Site", "example.com", "Site"},
		{"Site first | Then the article", "example.com", "Then the article"},
		{"No separator here", "example.com", ""},
		{"Covid-19 vaccines", "example.com", ""},
		{"9:00-17:00 hours", "example.com", ""},
		{"Article |Site", "example.com", ""},
		{"Article\n|\nSite", "example.com", "Site"},
		{"Article - 42", "example.com", ""},
		{"Article - X", "example.com", ""},
		{"Article - 2026", "example.com", ""},
		{"Article - " + strings.Repeat("x", 61), "example.com", ""},
		{"Article - " + strings.Repeat("x", 60), "example.com", strings.Repeat("x", 60)},
		{"Article | Medium", "example.com", ""},
		{"Article | example.com", "example.com", ""},
		{"Article | WWW.EXAMPLE.COM", "example.com", ""},
		{"| - |", "example.com", ""},
		{" - ", "example.com", ""},
		{"- Site", "example.com", ""},
		{"", "example.com", ""},
		{"記事 | 日本の新聞", "example.jp", "日本の新聞"},
		{"Article - Ab", "example.com", "Ab"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, TitleSuffix(tc.title, tc.host), "%q", tc.title)
	}
}

func TestAgreedName(t *testing.T) {
	cases := []struct {
		name string
		host string
		docs []HostDocument
		want string
	}{
		{"no documents", "example.com", nil, ""},
		{"one declared name is enough", "example.com",
			[]HostDocument{{DeclaredSite: "Example"}, {}, {TitleSuffixes: "Other\nThird"}, {TitleSuffixes: "Other"}},
			"Example"},
		{"the declared name most hold", "example.com",
			[]HostDocument{{DeclaredSite: "B"}, {DeclaredSite: "A"}, {DeclaredSite: "B"}}, "B"},
		{"a declared tie goes to byte order", "example.com",
			[]HostDocument{{DeclaredSite: "b"}, {DeclaredSite: "B"}, {DeclaredSite: "a"}}, "B"},
		{"a suffix needs two documents", "example.com",
			[]HostDocument{{TitleSuffixes: "Example Blog"}, {}}, ""},
		{"two documents agree on a suffix", "example.com",
			[]HostDocument{{TitleSuffixes: "Example Blog"}, {TitleSuffixes: "Example Blog\nOther"}}, "Example Blog"},
		{"a suffix tie goes to byte order", "example.com",
			[]HostDocument{{TitleSuffixes: "Zed\nAlpha"}, {TitleSuffixes: "Zed\nAlpha"}}, "Alpha"},
		{"the most-carried suffix", "example.com",
			[]HostDocument{{TitleSuffixes: "B"}, {TitleSuffixes: "B"}, {TitleSuffixes: "A"}, {TitleSuffixes: "A\nB"}},
			"B"},
		{"the curated name of the host", "news.ycombinator.com", []HostDocument{{TitleSuffixes: "Lone"}},
			"Hacker News"},
		{"the curated name of the registrable domain", "blog.stratechery.com", nil, "Stratechery"},
		{"suffixes before the curated name", "stratechery.com",
			[]HostDocument{{TitleSuffixes: "Strat"}, {TitleSuffixes: "Strat"}}, "Strat"},
		{"no name at all", "example.org", []HostDocument{{}, {}}, ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, AgreedName(tc.host, tc.docs), tc.name)
	}
}

// TestAgreedName_OrderFree: the name agreed doesn't depend on the order of
// the host's documents.
func TestAgreedName_OrderFree(t *testing.T) {
	docs := []HostDocument{{TitleSuffixes: "B\nA"}, {TitleSuffixes: "A"}, {TitleSuffixes: "B"},
		{TitleSuffixes: "C"}, {TitleSuffixes: "C"}}
	r := rand.New(rand.NewPCG(1, 2))
	for range 50 {
		r.Shuffle(len(docs), func(i, j int) { docs[i], docs[j] = docs[j], docs[i] })
		assert.Equal(t, "A", AgreedName("example.com", docs))
	}
}

// TestAgreedNameOf: the leaders decide as AgreedName's counts do: one
// declared name is enough, a suffix takes two documents, and the curated
// name comes last.
func TestAgreedNameOf(t *testing.T) {
	cases := []struct {
		name             string
		host             string
		declared, suffix Leader
		want             string
	}{
		{"nothing held", "example.com", Leader{}, Leader{}, ""},
		{"one declaration", "example.com", Leader{"Declared", 1}, Leader{"Suffix", 9}, "Declared"},
		{"one suffix is not enough", "example.com", Leader{}, Leader{"Suffix", 1}, ""},
		{"two suffixes", "example.com", Leader{}, Leader{"Suffix", 2}, "Suffix"},
		{"the curated name, below the threshold", "stratechery.com", Leader{}, Leader{"Strat", 1}, "Stratechery"},
		{"the curated name of the registrable domain", "blog.stratechery.com", Leader{}, Leader{}, "Stratechery"},
		{"a declaration before the curated name", "hbr.org", Leader{"HBR", 1}, Leader{}, "HBR"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, AgreedNameOf(tc.host, tc.declared, tc.suffix), tc.name)
	}
}

// TestAgreedName_IsAgreedNameOfItsLeaders: over random hosts' documents,
// AgreedName is AgreedNameOf the leaders a store counting votes reads,
// the most held name first and ties in byte order: what sorting by votes
// descending, then name, gives.
func TestAgreedName_IsAgreedNameOfItsLeaders(t *testing.T) {
	hosts := []string{"example.com", "stratechery.com", "news.ycombinator.com"}
	names := []string{"", "A", "B", "a", "Beta", "B\u00e9ta", "Zed"}
	r := rand.New(rand.NewPCG(7, 11))
	for range 2000 {
		docs := make([]HostDocument, r.IntN(6))
		for i := range docs {
			var suffixes []string
			for _, n := range names[1:] {
				if r.IntN(3) == 0 {
					suffixes = append(suffixes, n)
				}
			}
			r.Shuffle(len(suffixes), func(i, j int) { suffixes[i], suffixes[j] = suffixes[j], suffixes[i] })
			docs[i] = HostDocument{DeclaredSite: names[r.IntN(len(names))], TitleSuffixes: strings.Join(suffixes, "\n")}
		}
		host := hosts[r.IntN(len(hosts))]
		declared, suffix := countedLeaders(docs)
		assert.Equal(t, AgreedNameOf(host, declared, suffix), AgreedName(host, docs), "%s %q", host, docs)
	}
}

// countedLeaders counts docs' votes as site_name_votes holds them and
// reads each kind's first by votes descending, then name.
func countedLeaders(docs []HostDocument) (declared, suffix Leader) {
	declaredVotes, suffixVotes := map[string]int{}, map[string]int{}
	for _, d := range docs {
		if d.DeclaredSite != "" {
			declaredVotes[d.DeclaredSite]++
		}
		for _, s := range lines(d.TitleSuffixes) {
			suffixVotes[s]++
		}
	}
	first := func(votes map[string]int) Leader {
		ranked := slices.SortedFunc(maps.Keys(votes), func(a, b string) int {
			if votes[a] != votes[b] {
				return votes[b] - votes[a]
			}
			return strings.Compare(a, b)
		})
		if len(ranked) == 0 {
			return Leader{}
		}
		return Leader{Name: ranked[0], Votes: votes[ranked[0]]}
	}
	return first(declaredVotes), first(suffixVotes)
}

// TestCuratedSiteNames: the table is short, and keyed the way curatedName
// looks it up.
func TestCuratedSiteNames(t *testing.T) {
	assert.LessOrEqual(t, len(curatedSiteNames), 25)
	for key, name := range curatedSiteNames {
		assert.Equal(t, strings.ToLower(key), key)
		assert.False(t, strings.HasPrefix(key, "www."), key)
		assert.NotEmpty(t, strings.TrimSpace(name), key)
		site, ok := urlutil.RegistrableDomain(key)
		assert.True(t, ok, "%s has a registrable domain", key)
		assert.True(t, key == site || strings.HasSuffix(key, "."+site), "%s is a host or a registrable domain", key)
		assert.Equal(t, key, Host("https://"+key+"/"), "%s is a host as Host gives it", key)
	}
}

func TestDerive(t *testing.T) {
	own := Derive(Document{
		URL:      "https://www.Example.com/queues",
		Title:    "  Why queues   fail | The Example Times ",
		Author:   " Ada \n Lovelace ",
		SiteName: "The Example Times",
		Bookmarks: []Bookmark{
			{Title: "Queues - Example Times", FolderPath: "/Bookmarks Bar/Tech/Queues"},
			{Title: "  ", FolderPath: "/Other Bookmarks"},
			{Title: "Queues  - Example Times", FolderPath: "/Bookmarks Bar/Tech/Queues"},
			{Title: "Read later — Example Times", FolderPath: "/Favorites/Later"},
		},
	})
	assert.Equal(t, Own{
		Host:           "example.com",
		DeclaredSite:   "The Example Times",
		TitleSuffixes:  "The Example Times\nExample Times",
		Title:          "Why queues fail | The Example Times",
		BookmarkTitles: "Queues - Example Times\nRead later — Example Times",
		Folders:        "Tech / Queues\nLater",
		Author:         "Ada Lovelace",
	}, own)
	f := own.Fields("Agreed")
	assert.Equal(t, "The Example Times", f.SiteName, "its own declared name wins")
	assert.Equal(t, "The Example Times example", f.Site)
	assert.Equal(t, "Why queues fail | The Example Times — Queues - Example Times | Read later — Example Times — "+
		"The Example Times — Ada Lovelace — Tech / Queues | Later", f.Card())

	bare := Derive(Document{URL: "https://docs.example.org/x"})
	assert.Equal(t, Own{Host: "docs.example.org"}, bare)
	assert.Equal(t, Fields{Own: bare, Site: "example docs"}, bare.Fields(""))
	assert.Equal(t, "docs.example.org", bare.Fields("").Card(), "the host names a card with nothing else")
	assert.Equal(t, Fields{Own: bare, SiteName: "Example Docs", Site: "Example Docs example docs"},
		bare.Fields("Example Docs"), "the host's agreed name, without one of its own")
}

func TestDerive_CapsEveryField(t *testing.T) {
	long := strings.Repeat("ü", MaxFieldBytes) // two bytes each
	bookmarks := make([]Bookmark, 0, 100)
	for i := range 100 {
		bookmarks = append(bookmarks, Bookmark{Title: fmt.Sprint(i, long[:200]), FolderPath: fmt.Sprint("/F", i, "/", long[:200])})
	}
	f := Derive(Document{URL: "https://example.com/" + long, Title: long, Author: long, SiteName: long,
		Bookmarks: bookmarks}).Fields(long)
	for name, v := range map[string]string{"title": f.Title, "bookmark titles": f.BookmarkTitles, "folders": f.Folders,
		"author": f.Author, "site": f.Site} {
		assert.LessOrEqual(t, len(v), MaxFieldBytes, name)
		assert.True(t, utf8.ValidString(v), name)
	}
	assert.LessOrEqual(t, len(f.DeclaredSite), maxDeclaredSiteBytes)
	assert.LessOrEqual(t, len(f.Card()), MaxCardBytes)
}

func TestCard(t *testing.T) {
	cases := []struct {
		name string
		f    Fields
		want string
	}{
		{"nothing", Fields{}, ""},
		{"a title alone", Fields{Own: Own{Title: "Title"}}, "Title"},
		{"bookmark titles the title repeats are left out",
			Fields{Own: Own{Title: "Same Title", BookmarkTitles: "same  TITLE\nOther\nother"}, SiteName: "Site"},
			"Same Title — Other — Site"},
		{"an untitled document's bookmark titles",
			Fields{Own: Own{Host: "example.com", BookmarkTitles: "One\nTwo", Folders: "A / B\nC"}},
			"One | Two — example.com — A / B | C"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.f.Card(), tc.name)
	}
	long := Fields{Own: Own{Title: strings.Repeat("t", 500), BookmarkTitles: strings.Repeat("b", 500),
		Author: strings.Repeat("a", 500), Folders: strings.Repeat("f", 500)}, SiteName: strings.Repeat("s", 500)}
	card := long.Card()
	assert.Len(t, card, MaxCardBytes, "every part at its cap")
	for part := range strings.SplitSeq(card, cardSeparator) {
		assert.Len(t, part, maxCardPartBytes)
	}
}

// TestCard_Bounded: whatever a document holds, its card is at most
// MaxCardBytes, and valid UTF-8 when what it was made from is.
func TestCard_Bounded(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	alphabet := []string{"a", "Z", " ", "\n", "\t", "|", " | ", " — ", "é", "日本", "😀", "<script>", "\"", "/", "-",
		"\u200b", "\u00a0", "Bookmarks Bar", "www.", ".", ":"}
	text := func(maxParts int) string {
		var b strings.Builder
		for range r.IntN(maxParts) {
			b.WriteString(alphabet[r.IntN(len(alphabet))])
		}
		return b.String()
	}
	for range 2000 {
		d := Document{URL: "https://" + text(20) + ".example/" + text(5), Title: text(400), Author: text(300),
			SiteName: text(200)}
		for range r.IntN(6) {
			d.Bookmarks = append(d.Bookmarks, Bookmark{Title: text(400), FolderPath: text(300)})
		}
		own := Derive(d)
		f := own.Fields(text(100))
		card := f.Card()
		require.LessOrEqual(t, len(card), MaxCardBytes)
		require.True(t, utf8.ValidString(card), "%q", card)
	}
	// Invalid UTF-8 in, a bounded card out.
	f := Derive(Document{URL: "https://example.com/", Title: string(bytes.Repeat([]byte{0xff, 'x'}, 2000))}).Fields("")
	assert.LessOrEqual(t, len(f.Card()), MaxCardBytes)
}

// goldenDocuments are the synthetic library TestRulesGolden derives,
// covering every rule: declared names good and generic, suffix agreement,
// the curated table, roots, unicode and hostile values.
var goldenDocuments = []Document{
	{URL: "https://www.example.com/one", Title: "Why queues fail | Example Times", SiteName: "Example Times",
		Author: "Ada", Bookmarks: []Bookmark{{Title: "Queues", FolderPath: "/Bookmarks Bar/Tech"}}},
	{URL: "https://example.com/two", Title: "Backpressure - Example Times",
		Bookmarks: []Bookmark{{Title: "Backpressure - Example Times", FolderPath: "/Other Bookmarks/Tech/Queues"}}},
	{URL: "https://blog.example.org/a", Title: "Notes on caching · Example Notes", SiteName: "Medium"},
	{URL: "https://blog.example.org/b", Title: "More caching · Example Notes", SiteName: "blog.example.org"},
	{URL: "https://blog.example.org/c", Title: "Untitled"},
	{URL: "https://news.ycombinator.com/item?id=1", Title: "Show: a thing",
		Bookmarks: []Bookmark{{Title: "Show HN", FolderPath: "/Favorites"}}},
	{URL: "https://ben-evans.com/essays", Title: "", Bookmarks: []Bookmark{{Title: "Essays – Benedict", FolderPath: "/Bookmarks Menu/Reading/Essays"},
		{Title: "essays – benedict", FolderPath: "/Bookmarks Toolbar/Reading/Essays"}}},
	{URL: "https://bücher.example/ü", Title: "Bücher über Bücher — Bücherei", Author: "Ünal",
		Bookmarks: []Bookmark{{Title: "Bücher", FolderPath: "/com.apple.ReadingList"}}},
	{URL: "https://xn--bcher-kva.ch/", Title: "Punycode — xn--bcher-kva.ch", SiteName: "XN--BCHER-KVA.CH"},
	{URL: "http://127.0.0.1:8765/ui/", Title: "Dashboard - Local"},
	{URL: "https://user.github.io/project", Title: "Project · user.github.io",
		Bookmarks: []Bookmark{{Title: "<b>\"Project\"</b> | GitHub Pages", FolderPath: "/Mobile Bookmarks/Code/<i>"}}},
	{URL: "not a url", Title: "  |  —  ·  ", Author: strings.Repeat("long author ", 30)},
}

// TestRulesGolden: the rules' tables and limits, and every function's
// output over goldenDocuments, are the ones recorded for RulesVersion. A
// change to any of them needs a new version, so the store re-derives
// every home's rows: bump RulesVersion, then record its golden with
// -update (which never overwrites one).
func TestRulesGolden(t *testing.T) {
	path := filepath.Join("testdata", fmt.Sprintf("rules_v%d.golden", RulesVersion))
	got := renderGolden()
	if *update {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists: a change to the rules' output needs a new RulesVersion, recorded in a new file", path)
		}
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "record the golden of RulesVersion %d with -update", RulesVersion)
	assert.Equal(t, string(want), got, "the rules' output changed: bump RulesVersion and record its golden")
}

// renderGolden writes the rules' tables and limits, then derives
// goldenDocuments, each with its own functions' outputs, then each host's
// agreed name and every document's fields and card, as text.
func renderGolden() string {
	var b strings.Builder
	fmt.Fprintf(&b, "docmeta rules v%d\n", RulesVersion)
	renderTables(&b)
	owns := make([]Own, len(goldenDocuments))
	byHost := map[string][]HostDocument{}
	var hosts []string
	for i, d := range goldenDocuments {
		owns[i] = Derive(d)
		host := owns[i].Host
		if _, ok := byHost[host]; !ok {
			hosts = append(hosts, host)
		}
		byHost[host] = append(byHost[host], HostDocument{DeclaredSite: owns[i].DeclaredSite,
			TitleSuffixes: owns[i].TitleSuffixes})
		paths := make([]string, len(d.Bookmarks))
		for j, bm := range d.Bookmarks {
			paths[j] = bm.FolderPath
		}
		fmt.Fprintf(&b, "\ndocument %d %q\n", i, d.URL)
		fmt.Fprintf(&b, "  Host %q\n  HostWords %q\n  DeclaredSite %q\n  TitleSuffix %q\n  FolderText %q\n",
			host, HostWords(host), DeclaredSite(d.SiteName, host), TitleSuffix(d.Title, host), FolderText(paths))
		o := owns[i]
		fmt.Fprintf(&b, "  Derive declared_site %q title_suffixes %q title %q bookmark_titles %q folders %q author %q\n",
			o.DeclaredSite, o.TitleSuffixes, o.Title, o.BookmarkTitles, o.Folders, o.Author)
	}
	agreed := map[string]string{}
	for _, host := range hosts {
		agreed[host] = AgreedName(host, byHost[host])
		fmt.Fprintf(&b, "\nhost %q agrees on %q\n", host, agreed[host])
	}
	for i, own := range owns {
		f := own.Fields(agreed[own.Host])
		fmt.Fprintf(&b, "\nfields %d site_name %q site %q\n  card %q\n", i, f.SiteName, f.Site, f.Card())
	}
	return b.String()
}

// renderTables writes the tables and limits the rules apply, so editing
// one changes the golden even where no golden document meets the edit: a
// curated host, a generic name, a root label, a separator or a cap.
func renderTables(b *strings.Builder) {
	fmt.Fprintf(b, "\nlimits field %d declared_site %d title_suffix %d-%d card_part %d card %d\n", MaxFieldBytes,
		maxDeclaredSiteBytes, minTitleSuffixBytes, maxTitleSuffixBytes, maxCardPartBytes, MaxCardBytes)
	fmt.Fprintf(b, "card_separator %q\ntitle_separators %q\n", cardSeparator, titleSeparators)
	fmt.Fprintf(b, "root_labels %q\ngeneric_site_names %q\n", rootLabels, genericSiteNames)
	for _, host := range slices.Sorted(maps.Keys(curatedSiteNames)) {
		fmt.Fprintf(b, "curated %q %q\n", host, curatedSiteNames[host])
	}
}
