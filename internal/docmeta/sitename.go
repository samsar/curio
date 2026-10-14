package docmeta

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/samsar/curio/internal/textutil"
	"github.com/samsar/curio/internal/urlutil"
)

// maxDeclaredSiteBytes caps a declared site name: names are a few words,
// and a page can put anything in its og:site_name.
const maxDeclaredSiteBytes = 120

// Title suffixes are 2 to 60 bytes: shorter is an initial or a number,
// longer a subtitle.
const (
	minTitleSuffixBytes = 2
	maxTitleSuffixBytes = 60
)

// titleSeparators are what a title's site name follows: "Article | Site",
// "Article - Site". Each has a space on either side, so a hyphenated word
// or a time range doesn't split.
var titleSeparators = []string{" | ", " – ", " — ", " - ", " · "}

// genericSiteNames are site names that name a platform or its owner, not
// the site: Medium's and Substack's publications, every Tumblr and Blogger
// blog, a Wikipedia in any language, a page AWS serves. Compared
// case-insensitively.
var genericSiteNames = []string{
	"Wikimedia Foundation, Inc.", "Amazon Web Services", "Amazon Web Services, Inc.", "Tumblr", "Substack",
	"Blogger", "Google Docs", "WordPress", "Medium",
}

// curatedSiteNames name a few well-known public sites whose domain doesn't
// spell their name, by host or registrable domain, lowercase and without
// "www.": the last way a host gets a name, when none of its pages declares
// one and their titles agree on none. It stays short and general; a name
// added to win a particular search belongs nowhere.
var curatedSiteNames = map[string]string{
	"stratechery.com":      "Stratechery",
	"ben-evans.com":        "Benedict Evans",
	"hbr.org":              "Harvard Business Review",
	"fs.blog":              "Farnam Street",
	"paulgraham.com":       "Paul Graham",
	"news.ycombinator.com": "Hacker News",
	"nytimes.com":          "The New York Times",
	"wsj.com":              "The Wall Street Journal",
	"ft.com":               "Financial Times",
	"washingtonpost.com":   "The Washington Post",
	"newyorker.com":        "The New Yorker",
	"theatlantic.com":      "The Atlantic",
	"theverge.com":         "The Verge",
	"arstechnica.com":      "Ars Technica",
	"technologyreview.com": "MIT Technology Review",
	"a16z.com":             "Andreessen Horowitz",
	"martinfowler.com":     "Martin Fowler",
	"joelonsoftware.com":   "Joel on Software",
	"stackoverflow.com":    "Stack Overflow",
	"danluu.com":           "Dan Luu",
}

// Host is rawURL's host as a document's site is found by: lowercased,
// without its port, a trailing dot or a leading "www.", or "" when the URL
// has none.
func Host(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	return strings.TrimPrefix(host, "www.")
}

// HostWords is what a host says of its site, as words: the label its
// registrable domain is named by, then each subdomain label but "www", in
// order, hyphens read as spaces ("blog.example.co.uk" is "example blog",
// "ben-evans.com" "ben evans"). A host with no registrable domain (an IP
// address, a name under no public suffix) gives all of its labels.
func HostWords(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	var words []string
	if site, ok := urlutil.RegistrableDomain(host); ok {
		label, _, _ := strings.Cut(site, ".")
		words = append(words, label)
		if sub, ok := strings.CutSuffix(host, "."+site); ok {
			for l := range strings.SplitSeq(sub, ".") {
				if l != "www" {
					words = append(words, l)
				}
			}
		}
	} else {
		words = strings.FieldsFunc(host, func(r rune) bool { return r == '.' || r == ':' })
	}
	return collapse(strings.ReplaceAll(strings.Join(words, " "), "-", " "))
}

// DeclaredSite is the site name raw, a page's og:site_name, gives a
// document on host: whitespace collapsed, at most 120 bytes cut on a rune
// boundary, and empty when it says nothing a search can use, a platform's
// name (genericSiteNames) or the host itself.
func DeclaredSite(raw, host string) string {
	name := strings.TrimSpace(textutil.TruncateBytes(collapse(raw), maxDeclaredSiteBytes))
	if name == "" || isGeneric(name) || namesHost(name, host) {
		return ""
	}
	return name
}

// TitleSuffix is the site name title ends with: the text after its
// rightmost separator (titleSeparators) with text before it, when that is
// 2 to 60 bytes with a letter in it, and is neither a platform's name nor
// the host itself; otherwise "". The title's whitespace is collapsed
// first.
func TitleSuffix(title, host string) string {
	t := collapse(title)
	cut, sepLen := -1, 0
	for _, sep := range titleSeparators {
		// t has no space at either end, so a separator found is never at
		// its start: there is always text before it.
		if i := strings.LastIndex(t, sep); i > cut {
			cut, sepLen = i, len(sep)
		}
	}
	if cut <= 0 {
		return ""
	}
	suffix := strings.TrimSpace(t[cut+sepLen:])
	if len(suffix) < minTitleSuffixBytes || len(suffix) > maxTitleSuffixBytes || !strings.ContainsFunc(suffix, unicode.IsLetter) ||
		isGeneric(suffix) || namesHost(suffix, host) {
		return ""
	}
	return suffix
}

// HostDocument is what one document of a host contributes to the name the
// host agrees on.
type HostDocument struct {
	DeclaredSite  string // Own.DeclaredSite
	TitleSuffixes string // Own.TitleSuffixes, one a line
}

// AgreedName is the site name host's documents agree on: the declared name
// the most of them hold, one being enough; else the title suffix the most
// of them carry, each counted once, at least two; else the curated name of
// the host, or of its registrable domain; else "". Ties go to the name
// first in byte order. It is AgreedNameOf over the leaders it counts.
func AgreedName(host string, docs []HostDocument) string {
	declared := map[string]int{}
	suffixes := map[string]int{}
	for _, d := range docs {
		if d.DeclaredSite != "" {
			declared[d.DeclaredSite]++
		}
		// Own.TitleSuffixes holds each suffix once, so a document counts
		// once for each.
		for _, s := range lines(d.TitleSuffixes) {
			suffixes[s]++
		}
	}
	return AgreedNameOf(host, leader(declared), leader(suffixes))
}

// Leader is the name of one kind, declared names or title suffixes, held
// by the most of a host's documents, ties going to the first in byte
// order, and how many hold it; the zero Leader when none holds any.
type Leader struct {
	Name  string
	Votes int
}

// AgreedNameOf is the name host agrees on from the leaders of its
// documents' declared names and title suffixes, as AgreedName counts them
// or a store that keeps the counts reads them: the declared leader when any
// document declares a name, else the suffix leader when at least two carry
// it, else the curated name of the host or of its registrable domain, else
// "".
func AgreedNameOf(host string, declared, suffix Leader) string {
	if declared.Votes >= 1 {
		return declared.Name
	}
	if suffix.Votes >= 2 {
		return suffix.Name
	}
	return curatedName(host)
}

// leader is the name counts holds the most, ties going to the first in
// byte order.
func leader(counts map[string]int) Leader {
	var best Leader
	for name, n := range counts {
		if n > best.Votes || (n == best.Votes && name < best.Name) {
			best = Leader{Name: name, Votes: n}
		}
	}
	return best
}

// curatedName is the curated name of host, or of its registrable domain.
func curatedName(host string) string {
	if name, ok := curatedSiteNames[host]; ok {
		return name
	}
	if site, ok := urlutil.RegistrableDomain(host); ok {
		return curatedSiteNames[site]
	}
	return ""
}

func isGeneric(name string) bool {
	for _, g := range genericSiteNames {
		if strings.EqualFold(name, g) {
			return true
		}
	}
	return false
}

// namesHost reports whether name is host written out: the host, with
// "www.", or its registrable domain.
func namesHost(name, host string) bool {
	if host == "" {
		return false
	}
	return strings.EqualFold(name, host) || strings.EqualFold(name, "www."+host) ||
		strings.EqualFold(name, urlutil.Site(host))
}
