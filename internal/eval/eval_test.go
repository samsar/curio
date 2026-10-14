package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeQrels(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qrels.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	return path
}

func TestLoadQuerySet_NormalizesRelevantURLs(t *testing.T) {
	qs, err := LoadQuerySet(writeQrels(t, `
queries:
  - query: "videos"
    relevant:
      - "https://youtu.be/dQw4w9WgXcQ"
  - query: "origins and tracking"
    relevant:
      - "https://Example.com"
      - "https://example.com/post?utm_source=feed&id=7#comments"
      - "https://example.com/post?id=7"
`))
	require.NoError(t, err)
	assert.Equal(t, []Judgment{{URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Grade: 1}}, qs.Queries[0].Relevant)
	assert.Equal(t, []Judgment{{URL: "https://example.com/", Grade: 1}, {URL: "https://example.com/post?id=7", Grade: 1}},
		qs.Queries[1].Relevant, "normalized duplicates are dropped")
}

func TestLoadQuerySet_InvalidRelevantURL(t *testing.T) {
	_, err := LoadQuerySet(writeQrels(t, `
queries:
  - query: "fine"
    relevant: ["https://example.com/a"]
  - query: "broken"
    relevant: ["ftp://example.com/file"]
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query 1")
	assert.Contains(t, err.Error(), "ftp://example.com/file")
}

func TestEvaluate_MatchesNormalizedURLs(t *testing.T) {
	qs, err := LoadQuerySet(writeQrels(t, `
queries:
  - query: "q"
    relevant: ["https://Example.com/a#section"]
`))
	require.NoError(t, err)
	rep := Evaluate(qs, [][]string{{"https://example.com/a"}}, 10)
	assert.InDelta(t, 1.0, rep.MeanRecall, 1e-9, "a stored URL matches the browser-pasted one")
}

// TestLoadQuerySet_TheExamples: both example files in docs/ load, and the
// one curio eval documents has its three queries, every judgment at
// grade 1.
func TestLoadQuerySet_TheExamples(t *testing.T) {
	qs, err := LoadQuerySet(filepath.Join("..", "..", "docs", "eval.example.yaml"))
	require.NoError(t, err)
	require.Len(t, qs.Queries, 3)
	assert.Equal(t, "how neural networks learn", qs.Queries[0].Query)
	assert.Equal(t, []Judgment{
		{URL: "https://colah.github.io/posts/2015-08-Backprop/", Grade: 1},
		{URL: "https://distill.pub/2017/momentum/", Grade: 1},
	}, qs.Queries[0].Relevant)
	assert.Equal(t, []Judgment{{URL: "https://doc.rust-lang.org/book/ch04-00-understanding-ownership.html", Grade: 1}},
		qs.Queries[1].Relevant)
	assert.Equal(t, []Judgment{
		{URL: "https://martinfowler.com/articles/microservices.html", Grade: 1},
		{URL: "https://aphyr.com/posts/288-the-network-is-reliable", Grade: 1},
	}, qs.Queries[2].Relevant)

	qs, err = LoadQuerySet(filepath.Join("..", "..", "docs", "search-eval.example.yaml"))
	require.NoError(t, err)
	var grades []int
	var filtered []*Filters
	for _, q := range qs.Queries {
		assert.NotEmpty(t, q.ID)
		assert.NotEmpty(t, q.Kind)
		for _, j := range q.Relevant {
			grades = append(grades, j.Grade)
		}
		if q.Filters != nil {
			filtered = append(filtered, q.Filters)
		}
	}
	assert.Contains(t, grades, 1)
	assert.Contains(t, grades, MaxGrade)
	assert.Equal(t, []*Filters{{SavedAfter: "2024-01-01", SavedBefore: "2025-01-01", Site: []string{"example.com"}}},
		filtered)
}

func TestParseQuerySet_GradedAndGrouped(t *testing.T) {
	qs, err := ParseQuerySet([]byte(`
queries:
  - id: rust-ownership
    kind: topic
    tags: [owner, recent, owner]
    note: the book's chapter is what this is after
    query: rust ownership and borrowing
    relevant:
      - https://example.com/rust/intro
      - {url: "https://example.com/rust/ownership", grade: 2}
      - url: https://Example.com/a#x
        grade: 1
      - {url: "https://example.com/a", grade: 2}
  - query: no id, kind or tags
    relevant: [https://example.com/b]
`), "set.yaml")
	require.NoError(t, err)
	require.Len(t, qs.Queries, 2)
	q := qs.Queries[0]
	assert.Equal(t, "rust-ownership", q.ID)
	assert.Equal(t, "topic", q.Kind)
	assert.Equal(t, []string{"owner", "recent"}, q.Tags, "a tag repeated counts once")
	assert.Equal(t, "the book's chapter is what this is after", q.Note)
	assert.Equal(t, []Judgment{
		{URL: "https://example.com/rust/intro", Grade: 1},
		{URL: "https://example.com/rust/ownership", Grade: 2},
		{URL: "https://example.com/a", Grade: 2},
	}, q.Relevant, "a duplicate after normalization keeps the higher grade, at the first one's place")
	assert.Equal(t, Grades{"https://example.com/rust/intro": 1, "https://example.com/rust/ownership": 2,
		"https://example.com/a": 2}, q.Grades())

	plain := qs.Queries[1]
	assert.Empty(t, plain.ID)
	assert.Empty(t, plain.Kind)
	assert.Nil(t, plain.Tags)
}

// TestParseQuerySet_Errors: each mistake names the file, the query's index
// and id, and, for a relevant entry, its line.
func TestParseQuerySet_Errors(t *testing.T) {
	const head = "queries:\n  - id: first\n    query: fine\n    relevant: [https://example.com/a]\n"
	entry := func(e string) string {
		return head + "  - id: second\n    query: q\n    relevant:\n      - " + e + "\n"
	}
	cases := []struct {
		name string
		yaml string
		says []string
	}{
		{"grade 0", entry("{url: https://example.com/b, grade: 0}"), []string{"query 1", `id "second"`, "line 8", "grade 0"}},
		{"grade -1", entry("{url: https://example.com/b, grade: -1}"), []string{"query 1", "line 8", "grade -1"}},
		{"grade 3", entry("{url: https://example.com/b, grade: 3}"), []string{"line 8", "grade 3", "from 1 to 2"}},
		{"grade 1.5", entry("{url: https://example.com/b, grade: 1.5}"), []string{"line 8", `grade "1.5"`}},
		{"grade two", entry("{url: https://example.com/b, grade: two}"), []string{"line 8", `grade "two"`}},
		{"no url", entry("{grade: 2}"), []string{"line 8", "without a url"}},
		{"no grade", entry("{url: https://example.com/b}"), []string{"line 8", "without a grade"}},
		{"url twice", entry("{url: https://example.com/b, url: https://example.com/c, grade: 2}"),
			[]string{"query 1", "line 8", `key "url" repeated`}},
		{"grade twice", entry("{url: https://example.com/b, grade: 2, grade: 1}"), []string{"line 8", `key "grade" repeated`}},
		{"a note", entry("{url: https://example.com/b, grade: 2, note: why}"), []string{"line 8", `unknown key "note"`}},
		{"a weight", entry("{url: https://example.com/b, weight: 2}"), []string{"line 8", `unknown key "weight"`}},
		{"a sequence", entry("[https://example.com/b, 2]"), []string{"line 8", "a URL or a {url, grade} mapping"}},
		{"an unparseable URL", entry("ftp://example.com/file"), []string{"query 1", "line 8", "ftp://example.com/file"}},
		{"an unknown query key", head + "  - id: second\n    query: q\n    tag: [owner]\n    relevant: [https://example.com/b]\n",
			[]string{"query 1", `id "second"`, "line 7", `unknown key "tag"`}},
		{"a query key twice", head + "  - id: second\n    query: q\n    query: r\n    relevant: [https://example.com/b]\n",
			[]string{"query 1", `id "second"`, "line 7", `key "query" repeated (first at line 6)`}},
		{"an unknown top-level key", "querys: []\n", []string{"line 1", `unknown key "querys"`}},
		{"queries twice", head + "queries:\n  - id: third\n    query: q\n    relevant: [https://example.com/c]\n",
			[]string{"line 5", `key "queries" repeated (first at line 1)`}},
		{"queries not a list", "queries: {a: 1}\n", []string{"queries is a list"}},
		{"the same id twice", head + "  - id: other\n    query: q\n    relevant: [https://example.com/b]\n" +
			"  - id: first\n    query: q\n    relevant: [https://example.com/b]\n",
			[]string{`queries 0 and 2 have the same id "first"`}},
		{"an empty tag", head + "  - id: second\n    query: q\n    tags: [owner, \"\"]\n    relevant: [https://example.com/b]\n",
			[]string{"query 1", "line 7", "an empty tag"}},
		{"a null tag", head + "  - id: second\n    query: q\n    tags: [owner, ~]\n    relevant: [https://example.com/b]\n",
			[]string{"query 1", "line 7", "an empty tag"}},
		{"empty text", head + "  - id: second\n    relevant: [https://example.com/b]\n", []string{"query 1", `id "second"`, "empty text"}},
		{"no judgments", head + "  - id: second\n    query: q\n    relevant: []\n", []string{"query 1", "no relevant documents"}},
		{"an empty file", "", []string{"has no queries"}},
		{"two documents", head + "---\n" + head, []string{"line 5", "a second YAML document"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseQuerySet([]byte(tc.yaml), "set.yaml")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "set.yaml")
			for _, s := range tc.says {
				assert.Contains(t, err.Error(), s)
			}
		})
	}
}

// TestParseQuerySet_Aliases: an anchored entry or query reused by alias
// reads as the original, as yaml.v3 decodes aliases everywhere else.
func TestParseQuerySet_Aliases(t *testing.T) {
	const data = `queries:
  - &q
    id: first
    query: one
    relevant: [&u https://example.com/a, {url: https://example.com/b, grade: 2}]
  - id: second
    query: two
    relevant: [*u]
`
	qs, err := ParseQuerySet([]byte(data), "set.yaml")
	require.NoError(t, err)
	require.Len(t, qs.Queries, 2)
	assert.Equal(t, []Judgment{{URL: "https://example.com/a", Grade: 1}}, qs.Queries[1].Relevant)

	_, err = ParseQuerySet([]byte(data+"  - *q\n"), "set.yaml")
	require.Error(t, err, "the aliased query repeats the first's id")
	assert.Contains(t, err.Error(), `queries 0 and 2 have the same id "first"`)
}

// TestParseQuerySet_Filters: a query's filters are read under the API's
// names, as written, an unquoted date as its text; a query without them
// has none.
func TestParseQuerySet_Filters(t *testing.T) {
	qs, err := ParseQuerySet([]byte(`
queries:
  - id: every
    query: what I saved about kafka
    filters:
      saved_after: 2020-01-01
      saved_before: "2021-01-01T00:00:00+01:00"
      published_after: 2019-06-01
      published_before: 2020-06-01
      site: [example.com, blog.example.org]
      folder: ["/Bookmarks Bar/Tech", "/Reading/a,b"]
      include_undated: true
    relevant: [https://example.com/a]
  - id: none
    query: kafka
    relevant: [https://example.com/b]
`), "set.yaml")
	require.NoError(t, err)
	assert.Equal(t, &Filters{SavedAfter: "2020-01-01", SavedBefore: "2021-01-01T00:00:00+01:00",
		PublishedAfter: "2019-06-01", PublishedBefore: "2020-06-01", Site: []string{"example.com", "blog.example.org"},
		Folder: []string{"/Bookmarks Bar/Tech", "/Reading/a,b"}, IncludeUndated: true}, qs.Queries[0].Filters)
	assert.Nil(t, qs.Queries[1].Filters)
}

// TestParseQuerySet_FilterErrors: a filter the format doesn't take names
// the query and the line.
func TestParseQuerySet_FilterErrors(t *testing.T) {
	const head = "queries:\n  - id: first\n    query: fine\n    relevant: [https://example.com/a]\n" +
		"  - id: second\n    query: q\n    relevant: [https://example.com/b]\n    filters:\n"
	cases := []struct {
		name, filters string
		says          []string
	}{
		{"an unknown key", "      saved_since: 2020-01-01\n", []string{"line 9", `unknown key "saved_since"`}},
		{"a key twice", "      site: [a.example]\n      site: [b.example]\n",
			[]string{"line 10", `key "site" repeated (first at line 9)`}},
		{"a list for a date", "      saved_after: [2020-01-01]\n", []string{"line 9", "saved_after is one value"}},
		{"a null date", "      published_before: ~\n", []string{"line 9", "published_before is one value"}},
		{"a string for include_undated", "      include_undated: \"yes\"\n",
			[]string{"line 9", "include_undated is true or false"}},
		{"a site, not a list", "      site: example.com\n", []string{"line 9", "site is a list"}},
		{"a folder of lists", "      folder: [[a]]\n", []string{"line 9", "folder is one value"}},
		{"an empty mapping", "      {}\n", []string{"line 9", "an empty filters mapping"}},
		{"an empty date", "      saved_after: \"\"\n", []string{"line 9", "saved_after is empty"}},
		{"a blank date", "      published_before: \" \"\n", []string{"line 9", "published_before is empty"}},
		{"an empty site list", "      site: []\n", []string{"line 9", "site is an empty list"}},
		{"an empty folder", "      folder: [\"/A\", \"\"]\n", []string{"line 9", "folder is empty"}},
		{"include_undated false alone", "      include_undated: false\n",
			[]string{"line 9", "filters that filter nothing"}},
		{"a list", "      - site\n", []string{"line 9", "filters is a mapping"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseQuerySet([]byte(head+tc.filters), "set.yaml")
			require.Error(t, err)
			assert.Contains(t, err.Error(), `query 1 (id "second")`)
			for _, s := range tc.says {
				assert.Contains(t, err.Error(), s)
			}
		})
	}
}
