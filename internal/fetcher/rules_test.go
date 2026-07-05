package fetcher

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRules_Valid(t *testing.T) {
	rf, err := ParseRules([]byte(`
rules:
  - match: { host: "github.com" }
    fetcher: github
  - match: { host_suffix: ".youtube.com" }
    fetcher: youtube
  - match: { host_in: ["nytimes.com", "wsj.com"] }
    fetcher: native
  - match: {}
    fetcher: native
`))
	require.NoError(t, err)
	require.Len(t, rf.Rules, 4)
	assert.Equal(t, "github.com", rf.Rules[0].Match.Host)
	assert.Equal(t, ".youtube.com", rf.Rules[1].Match.HostSuffix)
	assert.Equal(t, []string{"nytimes.com", "wsj.com"}, rf.Rules[2].Match.HostIn)
	assert.Equal(t, "native", rf.Rules[3].Fetcher)
}

func TestParseRules_Errors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"bad yaml", "rules: [", "parse yaml"},
		{"missing fetcher", "rules:\n  - match: { host: \"x.com\" }\n", "missing fetcher"},
		{"content_type unsupported", "rules:\n  - match: { content_type: \"application/pdf\" }\n    fetcher: native\n", "content_type matching is not supported"},
		{"multiple matchers", "rules:\n  - match: { host: \"x.com\", host_suffix: \".x.com\" }\n    fetcher: native\n", "only one of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRules([]byte(tc.yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestRuleSpec_MatchesHost(t *testing.T) {
	cases := []struct {
		name  string
		match MatchSpec
		host  string
		want  bool
	}{
		{"exact hit", MatchSpec{Host: "github.com"}, "github.com", true},
		{"exact case-insensitive", MatchSpec{Host: "GitHub.com"}, "github.com", true},
		{"exact miss on subdomain", MatchSpec{Host: "github.com"}, "gist.github.com", false},
		{"suffix with dot hits subdomain", MatchSpec{HostSuffix: ".youtube.com"}, "www.youtube.com", true},
		{"suffix with dot hits apex", MatchSpec{HostSuffix: ".youtube.com"}, "youtube.com", true},
		{"suffix without dot hits subdomain", MatchSpec{HostSuffix: "youtube.com"}, "m.youtube.com", true},
		{"suffix respects label boundary", MatchSpec{HostSuffix: ".youtube.com"}, "evilyoutube.com", false},
		{"host_in hit", MatchSpec{HostIn: []string{"a.com", "b.com"}}, "b.com", true},
		{"host_in miss", MatchSpec{HostIn: []string{"a.com", "b.com"}}, "c.com", false},
		{"catch-all", MatchSpec{}, "anything.example", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := RuleSpec{Match: tc.match}
			assert.Equal(t, tc.want, r.matchesHost(tc.host))
		})
	}
}

// writeRules writes content to path and nudges mtime forward so the
// dispatcher's stat-based change detection always sees an edit, even on
// filesystems with coarse mtime granularity.
func writeRules(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	now := time.Now()
	require.NoError(t, os.Chtimes(path, now, now.Add(2*time.Second)))
}

func newRulesTestFetchers() (native, github, youtube Fetcher, registry map[string]Fetcher) {
	native = &fakeRuleFetcher{name: "native"}
	github = &fakeRuleFetcher{name: "github"}
	youtube = &fakeRuleFetcher{name: "youtube"}
	registry = map[string]Fetcher{"native": native, "github": github, "youtube": youtube}
	return
}

type fakeRuleFetcher struct{ name string }

func (f *fakeRuleFetcher) Name() string { return f.name }
func (f *fakeRuleFetcher) Fetch(_ context.Context, _ string) (*Result, error) {
	return &Result{Markdown: "x"}, nil
}

func TestRulesDispatcher_FileAbsentUsesDefaults(t *testing.T) {
	native, github, _, registry := newRulesTestFetchers()

	d := NewRulesDispatcher(RulesDispatcherOptions{
		Path:          filepath.Join(t.TempDir(), "fetcher_rules.yaml"),
		Registry:      registry,
		DefaultRules:  []Rule{{Hosts: []string{"github.com"}, Fetcher: github}},
		Fallback:      native,
		CheckInterval: -1,
		Log:           slog.Default(),
	})

	f, err := d.For("https://github.com/o/r")
	require.NoError(t, err)
	assert.Equal(t, "github", f.Name())

	f, err = d.For("https://example.com/article")
	require.NoError(t, err)
	assert.Equal(t, "native", f.Name())
}

func TestRulesDispatcher_FileRoutesAndReloads(t *testing.T) {
	native, github, _, registry := newRulesTestFetchers()
	path := filepath.Join(t.TempDir(), "fetcher_rules.yaml")

	writeRules(t, path, `
rules:
  - match: { host_suffix: ".youtube.com" }
    fetcher: youtube
  - match: { host: "github.com" }
    fetcher: github
`)

	d := NewRulesDispatcher(RulesDispatcherOptions{
		Path:          path,
		Registry:      registry,
		DefaultRules:  []Rule{{Hosts: []string{"github.com"}, Fetcher: github}},
		Fallback:      native,
		CheckInterval: -1, // recheck on every dispatch
		Log:           slog.Default(),
	})

	f, err := d.For("https://m.youtube.com/watch?v=abc")
	require.NoError(t, err)
	assert.Equal(t, "youtube", f.Name())

	// First match wins: youtube.com routed by the file, not the fallback.
	f, err = d.For("https://github.com/o/r")
	require.NoError(t, err)
	assert.Equal(t, "github", f.Name())

	// Edit the file: route github.com to native now.
	writeRules(t, path, `
rules:
  - match: { host: "github.com" }
    fetcher: native
`)
	f, err = d.For("https://github.com/o/r")
	require.NoError(t, err)
	assert.Equal(t, "native", f.Name(), "edit should be picked up without restart")

	// YouTube rule is gone from the file → falls to the fallback.
	f, err = d.For("https://m.youtube.com/watch?v=abc")
	require.NoError(t, err)
	assert.Equal(t, "native", f.Name())
}

func TestRulesDispatcher_InvalidEditKeepsLastGood(t *testing.T) {
	native, _, _, registry := newRulesTestFetchers()
	path := filepath.Join(t.TempDir(), "fetcher_rules.yaml")

	writeRules(t, path, `
rules:
  - match: { host: "github.com" }
    fetcher: github
`)
	d := NewRulesDispatcher(RulesDispatcherOptions{
		Path:          path,
		Registry:      registry,
		Fallback:      native,
		CheckInterval: -1,
		Log:           slog.Default(),
	})

	f, err := d.For("https://github.com/o/r")
	require.NoError(t, err)
	assert.Equal(t, "github", f.Name())

	// Break the file: last good rules must keep serving.
	writeRules(t, path, "rules: [broken")
	f, err = d.For("https://github.com/o/r")
	require.NoError(t, err)
	assert.Equal(t, "github", f.Name(), "invalid edit must keep the last good rules")
}

func TestRulesDispatcher_FileDeletedRevertsToDefaults(t *testing.T) {
	native, github, youtube, registry := newRulesTestFetchers()
	path := filepath.Join(t.TempDir(), "fetcher_rules.yaml")

	writeRules(t, path, `
rules:
  - match: { host: "github.com" }
    fetcher: youtube
`)
	d := NewRulesDispatcher(RulesDispatcherOptions{
		Path:          path,
		Registry:      registry,
		DefaultRules:  []Rule{{Hosts: []string{"github.com"}, Fetcher: github}},
		Fallback:      native,
		CheckInterval: -1,
		Log:           slog.Default(),
	})
	_ = youtube

	f, err := d.For("https://github.com/o/r")
	require.NoError(t, err)
	assert.Equal(t, "youtube", f.Name())

	require.NoError(t, os.Remove(path))
	f, err = d.For("https://github.com/o/r")
	require.NoError(t, err)
	assert.Equal(t, "github", f.Name(), "deleting the file reverts to built-in rules")
}

func TestRulesDispatcher_UnknownFetcherSkipsRule(t *testing.T) {
	native, _, _, registry := newRulesTestFetchers()
	delete(registry, "youtube") // simulate yt-dlp not installed
	path := filepath.Join(t.TempDir(), "fetcher_rules.yaml")

	writeRules(t, path, `
rules:
  - match: { host_suffix: ".youtube.com" }
    fetcher: youtube
  - match: { host: "example.com" }
    fetcher: native
`)
	d := NewRulesDispatcher(RulesDispatcherOptions{
		Path:          path,
		Registry:      registry,
		Fallback:      native,
		CheckInterval: -1,
		Log:           slog.Default(),
	})

	// The youtube rule is skipped (unavailable); URL degrades to fallback.
	f, err := d.For("https://www.youtube.com/watch?v=abc")
	require.NoError(t, err)
	assert.Equal(t, "native", f.Name())

	// Later rules still apply.
	f, err = d.For("https://example.com/x")
	require.NoError(t, err)
	assert.Equal(t, "native", f.Name())
}

func TestRulesDispatcher_CatchAllStopsFallback(t *testing.T) {
	native, _, _, registry := newRulesTestFetchers()
	path := filepath.Join(t.TempDir(), "fetcher_rules.yaml")

	writeRules(t, path, `
rules:
  - match: {}
    fetcher: github
`)
	d := NewRulesDispatcher(RulesDispatcherOptions{
		Path:          path,
		Registry:      registry,
		Fallback:      native,
		CheckInterval: -1,
		Log:           slog.Default(),
	})

	f, err := d.For("https://anything.example/x")
	require.NoError(t, err)
	assert.Equal(t, "github", f.Name(), "catch-all rule wins over the fallback")
}
