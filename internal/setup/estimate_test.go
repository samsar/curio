package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/store"
)

// zone is a fixed location, so the times below read the same anywhere.
var zone = time.FixedZone("test", -4*60*60)

func at(day, hour, minute int) time.Time { return time.Date(2026, 9, day, hour, minute, 0, 0, zone) }

// TestEstimateImport: the worked example: 12,345 new pages, 16 fetches at
// once, 40 chunks a second.
func TestEstimateImport(t *testing.T) {
	e := estimateImport(12_345, 16, 40, "")
	assert.Equal(t, "4h35m–8h14m", e.Fetching())
	assert.Equal(t, "1h23m–2h58m", e.Indexing())
	assert.Equal(t, 8*time.Hour+14*time.Minute, e.Work(), "fetching is the slower half")
	assert.Equal(t, "tomorrow 05:14", CheckBackText(at(27, 21, 0), finishAt(at(27, 21, 0), e.Work(), store.DailyWindow{})))

	gentle := estimateImport(12_345, store.ThrottleGentle.Limit(store.JobKindFetch, 16), 40, "")
	assert.Equal(t, 4*e.FetchHi, gentle.FetchHi, "gently, four times slower")
	assert.Equal(t, e.FetchHi, estimateImport(12_345, 32, 40, "").FetchHi, "more than 16 workers is no faster")

	unknown := estimateImport(100, 16, 0, "measuring failed: connection refused")
	assert.Zero(t, unknown.IndexHi)
	assert.Equal(t, "unknown (measuring failed: connection refused)", unknown.Indexing())
	assert.Equal(t, unknown.FetchHi, unknown.Work(), "the fetching alone")
	assert.Equal(t, "about 1m", estimateImport(3, 16, 40, "").Fetching())
}

func TestFinishAt(t *testing.T) {
	night := store.DailyWindow{Start: 22 * 60, End: 7 * 60}
	cases := []struct {
		name   string
		now    time.Time
		work   time.Duration
		window store.DailyWindow
		want   time.Time
	}{
		{"no window", at(27, 21, 0), 3 * time.Hour, store.DailyWindow{}, at(28, 0, 0)},
		{"starts outside the window", at(27, 15, 0), 2 * time.Hour, night, at(28, 0, 0)},
		{"starts inside it", at(27, 23, 0), 2 * time.Hour, night, at(28, 1, 0)},
		{"inside, after midnight", at(28, 5, 0), time.Hour, night, at(28, 6, 0)},
		{"spans nights", at(27, 23, 0), 12 * time.Hour, night, at(28, 22, 0).Add(4 * time.Hour)},
		{"ends as a window closes", at(27, 22, 0), 9 * time.Hour, night, at(28, 7, 0)},
		{"a window that doesn't wrap", at(27, 12, 0), 5 * time.Hour,
			store.DailyWindow{Start: 60, End: 5 * 60}, at(29, 2, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, finishAt(tc.now, tc.work, tc.window))
		})
	}
}

func TestCheckBackText(t *testing.T) {
	now := at(27, 21, 0)
	assert.Equal(t, "23:30", CheckBackText(now, at(27, 23, 30)))
	assert.Equal(t, "tomorrow 05:14", CheckBackText(now, at(28, 5, 14)))
	assert.Equal(t, "Tue 29 Sep 06:00", CheckBackText(now, at(29, 6, 0)))
	assert.Equal(t, "tomorrow 05:14", CheckBackText(now, at(28, 5, 14).UTC()), "in now's location")
}

// urlsOn is n URLs on host.
func urlsOn(host string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("https://%s/%d", host, i)
	}
	return out
}

func spread(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("https://site%d.example/", i)
	}
	return out
}

// TestDominantHosts: a host is named when it holds more than 2/16 of the
// new pages and at least 100 of them; github.com never is.
func TestDominantHosts(t *testing.T) {
	cases := []struct {
		name string
		urls []string
		want []HostCount
	}{
		{"2,000 of 5,000", append(urlsOn("Blog.Example.com", 2000), spread(3000)...),
			[]HostCount{{"blog.example.com", 2000}}},
		{"300 of 5,000", append(urlsOn("blog.example.com", 300), spread(4700)...), nil},
		{"90 of 100", append(urlsOn("blog.example.com", 90), spread(10)...), nil},
		{"github.com", append(urlsOn("github.com", 3000), spread(2000)...), nil},
		{"the three largest", slicesConcat(urlsOn("a.example", 500), urlsOn("b.example", 700),
			urlsOn("c.example", 600), urlsOn("d.example", 550)),
			[]HostCount{{"b.example", 700}, {"c.example", 600}, {"d.example", 550}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, dominantHosts(tc.urls, 16))
		})
	}
}

func slicesConcat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// TestImportNotes_GitHub: GitHub pages without a token are mentioned once,
// pointing at config.yaml, which the daemon reads; with a token they
// aren't.
func TestImportNotes_GitHub(t *testing.T) {
	pages := append(urlsOn("github.com", 40), spread(60)...)
	cfg := config.Default()
	e := estimateImport(len(pages), cfg.Daemon.FetchWorkers, 40, "")
	notes := strings.Join(importNotes(e, 40, pages, cfg, "/Users/x/.curio/config.yaml", true), "\n")
	assert.Contains(t, notes, "40 pages on github.com: without a token GitHub allows 60 API requests an hour, 2 a repository")
	assert.Contains(t, notes, "Set fetcher.github.token in /Users/x/.curio/config.yaml")
	assert.Contains(t, notes, "`curio refetch --all --state=failed`")
	assert.Contains(t, notes, "launchd keeps the daemon running")
	assert.Contains(t, notes, "`curio throttle gentle`")
	assert.Contains(t, notes, "A sleeping Mac pauses the import.")

	cfg.Fetcher.GitHub.Token = "ghp_example"
	notes = strings.Join(importNotes(e, 40, pages, cfg, "/Users/x/.curio/config.yaml", false), "\n")
	assert.NotContains(t, notes, "github.com")
	assert.Contains(t, notes, "the next curio command starts the daemon again")
}

// fakeEmbedder embeds by advancing a fake clock by took per call.
type fakeEmbedder struct {
	clock *time.Time
	took  time.Duration
	err   error
	texts [][]string
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.texts = append(f.texts, texts)
	*f.clock = f.clock.Add(f.took)
	return make([][]float32, len(texts)), f.err
}

func (*fakeEmbedder) Dimensions() int { return 1024 }
func (*fakeEmbedder) Model() string   { return "fake" }

// TestMeasureIndexRate: a warm-up batch, then a timed one, of 32 prefixed
// chunks of 2,300 to 2,500 characters, all different.
func TestMeasureIndexRate(t *testing.T) {
	clock := at(27, 12, 0)
	emb := &fakeEmbedder{clock: &clock, took: 800 * time.Millisecond}
	rate, err := measureIndexRate(context.Background(), emb, "passage: ", func() time.Time { return clock })
	require.NoError(t, err)
	assert.InDelta(t, 40, rate, 0.001, "32 chunks in 0.8s")
	require.Len(t, emb.texts, 2)
	seen := map[string]bool{}
	for _, batch := range emb.texts {
		require.Len(t, batch, 32)
		for _, text := range batch {
			assert.True(t, strings.HasPrefix(text, "passage: "))
			assert.GreaterOrEqual(t, len(text), 2300)
			assert.LessOrEqual(t, len(text), 2500)
			assert.False(t, seen[text], "no two chunks alike")
			seen[text] = true
		}
	}

	emb = &fakeEmbedder{clock: &clock}
	_, err = measureIndexRate(context.Background(), emb, "", func() time.Time { return clock })
	require.Error(t, err, "no time measured")
	emb = &fakeEmbedder{clock: &clock, took: time.Second, err: errors.New("model not found")}
	_, err = measureIndexRate(context.Background(), emb, "", func() time.Time { return clock })
	require.ErrorContains(t, err, "model not found")
}

// TestPaceUpdate: each pace is one queue update, its JSON pinned.
func TestPaceUpdate(t *testing.T) {
	cases := map[Pace]string{
		PaceFull:      `{"paused":false,"throttle":"normal","schedule":"off","keep_awake":false}`,
		PaceGentle:    `{"paused":false,"throttle":"gentle","schedule":"off","keep_awake":false}`,
		PaceOvernight: `{"paused":false,"throttle":"normal","schedule":"22:00-07:00","keep_awake":false}`,
	}
	for pace, want := range cases {
		got, err := json.Marshal(paceUpdate(pace, false))
		require.NoError(t, err)
		assert.JSONEq(t, want, string(got), pace.String())
	}
	got, err := json.Marshal(paceUpdate(PaceOvernight, true))
	require.NoError(t, err)
	assert.JSONEq(t, `{"paused":false,"throttle":"normal","schedule":"22:00-07:00","keep_awake":true}`, string(got))
}

func TestParseImportSpec(t *testing.T) {
	good := map[string]importSpec{
		"chrome":           {kind: "chrome"},
		"chrome:Profile 1": {kind: "chrome", arg: "Profile 1"},
		"safari":           {kind: "safari"},
		"firefox":          {kind: "firefox"},
		"html:~/b.html":    {kind: "html", arg: "~/b.html"},
		"html:C:/x:y.html": {kind: "html", arg: "C:/x:y.html"},
	}
	for v, want := range good {
		got, err := parseImportSpec(v)
		require.NoError(t, err, v)
		assert.Equal(t, want, *got, v)
		assert.Equal(t, v, got.String())
	}
	for _, v := range []string{"bogus", "html", "html:", "safari:x", "Chrome"} {
		_, err := parseImportSpec(v)
		require.Error(t, err, v)
	}
	none, err := parseImportSpec("")
	require.NoError(t, err)
	assert.Nil(t, none)
}
