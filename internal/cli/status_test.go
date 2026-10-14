package cli

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
)

func TestHomeMismatchWarning(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(home, link))

	assert.Empty(t, homeMismatchWarning(home, home))
	assert.Empty(t, homeMismatchWarning(link, home), "a symlink to the same home is the same home")
	assert.Empty(t, homeMismatchWarning("", home), "a daemon that doesn't report its home can't be checked")

	w := homeMismatchWarning(other, home)
	assert.Contains(t, w, "serves "+other+", not "+home)
	assert.Contains(t, w, "daemon.listen")
}

// TestUpstreamCheck: doctor renders each state the daemon reports for an
// upstream, with a hint on the ones that need attention.
func TestUpstreamCheck(t *testing.T) {
	answered := time.Date(2026, 9, 27, 14, 2, 42, 0, time.UTC)
	failed := answered.Add(38 * time.Minute)
	jina := func(state string, change func(*client.UpstreamHealth)) client.UpstreamHealth {
		u := client.UpstreamHealth{Name: "jina", Enabled: state != client.UpstreamDisabled, State: state,
			WindowSeconds: 900, Recent: map[string]int{}}
		if change != nil {
			change(&u)
		}
		return u
	}
	cases := []struct {
		name     string
		upstream client.UpstreamHealth
		status   checkStatus
		detail   string
		hint     string
	}{
		{"disabled", jina(client.UpstreamDisabled, nil), statusOK, "off (fetcher.native.jina_fallback: false)", ""},
		{"idle, never called", jina(client.UpstreamIdle, nil), statusOK, "no calls in the last 15m", ""},
		{"idle", jina(client.UpstreamIdle, func(u *client.UpstreamHealth) { u.LastSuccessAt = answered }),
			statusOK, "no calls in the last 15m; last answer " + localTime(answered), ""},
		{"ok", jina(client.UpstreamOK, func(u *client.UpstreamHealth) {
			u.LastSuccessAt = answered
			u.Recent = map[string]int{client.CallOK: 7, client.CallJudged: 3, client.CallRefused: 1, client.CallNetwork: 1}
		}), statusOK, "12 calls in the last 15m (judged=3  network=1  ok=7  refused=1); last answer " + localTime(answered), ""},
		{"degraded", jina(client.UpstreamDegraded, func(u *client.UpstreamHealth) {
			u.Recent = map[string]int{client.CallOK: 8, client.CallNetwork: 3, client.CallRateLimited: 1}
			u.LastFailureClass = client.CallRateLimited
		}), statusWarn, "degraded: 4 of 12 calls in the last 15m failed (network=3  ok=8  rate_limited=1)",
			"network: curio can't reach r.jina.ai, or it doesn't answer in time; check connectivity"},
		{"paused", jina(client.UpstreamPaused, func(u *client.UpstreamHealth) {
			u.Recent = map[string]int{client.CallOK: 2, client.CallChallenged: 1}
			u.LastFailureAt, u.LastFailureClass = failed, client.CallChallenged
			u.CooldownUntil = failed.Add(10 * time.Minute)
		}), statusWarn, "paused until " + localTime(failed.Add(10*time.Minute)),
			"challenged: r.jina.ai is challenging curio; see Troubleshooting in docs/setup.md"},
		{"failing", jina(client.UpstreamFailing, func(u *client.UpstreamHealth) {
			u.LastSuccessAt = answered
			u.LastFailureAt, u.LastFailureClass = failed, client.CallAuth
			u.Recent = map[string]int{client.CallAuth: 5}
		}), statusFail, "failing: no answer since " + localTime(answered) + "; last failure auth at " + localTime(failed),
			"auth: check fetcher.native.jina_api_key or CURIO_JINA_API_KEY (401: the key is invalid, 402: it has no balance left)"},
		{"failing, never answered", jina(client.UpstreamFailing, func(u *client.UpstreamHealth) {
			u.LastFailureAt, u.LastFailureClass = failed, client.CallServerError
		}), statusFail, "failing: no answer since the daemon started; last failure server_error at " + localTime(failed),
			"server_error: r.jina.ai is failing on its side; fetches keep retrying"},
		{"a state from a newer daemon", jina("overloaded", nil), statusWarn,
			`state "overloaded", which this curio doesn't know`, "update curio"},
		{"ok, one site blocked", jina(client.UpstreamOK, func(u *client.UpstreamHealth) {
			u.Recent = map[string]int{client.CallOK: 3, client.CallRateLimited: 1}
			u.SitePauses = []client.SitePause{{Site: "twitter.com", Until: failed.Add(time.Hour)}}
		}), statusOK, "4 calls in the last 15m (ok=3  rate_limited=1); Jina Reader blocks keyless reads of 1 site " +
			"for now: twitter.com until " + localTime(failed.Add(time.Hour)), ""},
		{"idle, two sites blocked", jina(client.UpstreamIdle, func(u *client.UpstreamHealth) {
			u.SitePauses = []client.SitePause{{Site: "forbes.com", Until: failed.Add(20 * time.Minute)},
				{Site: "twitter.com", Until: failed.Add(time.Hour)}}
		}), statusOK, "no calls in the last 15m; Jina Reader blocks keyless reads of 2 sites for now: forbes.com until " +
			localTime(failed.Add(20*time.Minute)) + ", twitter.com until " + localTime(failed.Add(time.Hour)), ""},
		{"degraded, five sites blocked", jina(client.UpstreamDegraded, func(u *client.UpstreamHealth) {
			u.Recent = map[string]int{client.CallOK: 2, client.CallRateLimited: 5}
			for _, site := range []string{"alibaba.com", "forbes.com", "instagram.com", "twitter.com", "x.com"} {
				u.SitePauses = append(u.SitePauses, client.SitePause{Site: site, Until: failed})
			}
		}), statusWarn, "degraded: 5 of 7 calls in the last 15m failed (ok=2  rate_limited=5); Jina Reader blocks " +
			"keyless reads of 5 sites for now: alibaba.com until " + localTime(failed) + ", forbes.com until " +
			localTime(failed) + ", instagram.com until " + localTime(failed) + ", and 2 more",
			"rate_limited: Jina is rate-limiting curio, or blocking keyless reads of a busy site, which curio waits out; " +
				"a fetcher.native.jina_api_key raises the limit (a block names anonymous access only)"},
		{"disabled, a site blocked", jina(client.UpstreamDisabled, func(u *client.UpstreamHealth) {
			u.SitePauses = []client.SitePause{{Site: "twitter.com", Until: failed}}
		}), statusOK, "off (fetcher.native.jina_fallback: false)", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, detail, hint := upstreamCheck(tc.upstream)
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.detail, detail)
			assert.Equal(t, tc.hint, hint)
		})
	}
}

// TestUpstreamHint: each failure class gets its own advice, named by the
// class.
func TestUpstreamHint(t *testing.T) {
	wants := map[string]string{
		client.CallChallenged:  "r.jina.ai is challenging curio",
		client.CallForbidden:   "HTTP 403 without naming a target",
		client.CallRateLimited: "blocking keyless reads of a busy site, which curio waits out; a fetcher.native.jina_api_key raises the limit",
		client.CallAuth:        "check fetcher.native.jina_api_key or CURIO_JINA_API_KEY",
		client.CallServerError: "failing on its side",
		client.CallNetwork:     "check connectivity",
	}
	require.ElementsMatch(t, failureClasses, slices.Collect(maps.Keys(wants)))
	for class, want := range wants {
		hint := upstreamHint(client.UpstreamHealth{State: client.UpstreamFailing, LastFailureClass: class,
			Recent: map[string]int{class: 5}})
		assert.True(t, strings.HasPrefix(hint, class+": "), hint)
		assert.Contains(t, hint, want)
	}
}

// TestDominantFailure: the hint is about the failure class with the most
// calls in the window, or the last failure's on a tie or without any.
func TestDominantFailure(t *testing.T) {
	cases := []struct {
		name   string
		recent map[string]int
		last   string
		want   string
	}{
		{"most calls", map[string]int{client.CallNetwork: 3, client.CallRateLimited: 1, client.CallOK: 9},
			client.CallRateLimited, client.CallNetwork},
		{"a tie", map[string]int{client.CallNetwork: 2, client.CallForbidden: 2}, client.CallForbidden, client.CallForbidden},
		{"a tie the other way", map[string]int{client.CallNetwork: 2, client.CallForbidden: 2}, client.CallNetwork,
			client.CallNetwork},
		{"a tie broken by a larger class", map[string]int{client.CallChallenged: 2, client.CallForbidden: 2,
			client.CallAuth: 3}, client.CallChallenged, client.CallAuth},
		{"no failures in the window", map[string]int{client.CallOK: 4}, client.CallAuth, client.CallAuth},
		{"an empty window", map[string]int{}, client.CallChallenged, client.CallChallenged},
		{"nothing to go on", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, dominantFailure(client.UpstreamHealth{Recent: tc.recent, LastFailureClass: tc.last}))
		})
	}
}

// TestFailingWarning: status warns about an upstream only while it is
// failing.
func TestFailingWarning(t *testing.T) {
	answered := time.Date(2026, 9, 27, 14, 2, 42, 0, time.UTC)
	for _, state := range []string{client.UpstreamDisabled, client.UpstreamIdle, client.UpstreamOK,
		client.UpstreamDegraded, client.UpstreamPaused} {
		assert.Empty(t, failingWarning(client.UpstreamHealth{Name: "jina", State: state,
			LastFailureClass: client.CallNetwork}), state)
	}
	assert.Equal(t, "warning: jina is failing: no answer since "+localTime(answered)+
		"; last failure challenged; run `curio doctor`\n",
		failingWarning(client.UpstreamHealth{Name: "jina", State: client.UpstreamFailing,
			LastSuccessAt: answered, LastFailureClass: client.CallChallenged}))
	assert.Equal(t, "warning: jina is failing: no answer since the daemon started; last failure auth; run `curio doctor`\n",
		failingWarning(client.UpstreamHealth{Name: "jina", State: client.UpstreamFailing,
			LastFailureClass: client.CallAuth}))
}

func TestWindowText(t *testing.T) {
	for seconds, want := range map[int]string{900: "15m", 90: "1m30s", 45: "45s", 3600: "1h", 5400: "1h30m"} {
		assert.Equal(t, want, windowText(seconds), seconds)
	}
}

// TestStatus_Dashboard: status says where the dashboard is, ready or
// starting, unless config.yaml turns it off.
func TestStatus_Dashboard(t *testing.T) {
	srv := apitest.Start(t)
	line := "dashboard: " + srv.URL + "/ui/  (curio ui)\n"
	assert.Contains(t, mustRun(t, srv, "status"), line)

	starting := apitest.StartNotReady(t)
	out := mustRun(t, starting, "status")
	assert.Contains(t, out, "daemon:  starting")
	assert.Contains(t, out, "dashboard: "+starting.URL+"/ui/  (curio ui)\n")

	require.NoError(t, os.WriteFile(srv.Home.ConfigPath(), []byte("daemon:\n  ui: false\n"), 0o600))
	assert.NotContains(t, mustRun(t, srv, "status"), "dashboard:")
}

// TestStatus_SearchFields: status warns, in one line, when the daemon's
// start couldn't check the search fields, and says nothing of a start
// that checked them or rebuilt their index.
func TestStatus_SearchFields(t *testing.T) {
	checked := time.Now()
	for name, tc := range map[string]struct {
		fields api.DocumentFields
		want   string
	}{
		"checked": {api.DocumentFields{CheckedAt: checked}, ""},
		"rebuilt": {api.DocumentFields{CheckedAt: checked, Rebuilt: true}, ""},
		"failed": {api.DocumentFields{CheckedAt: checked, Error: "derive document fields: disk I/O error"},
			"warning: the daemon's start couldn't check the search fields (derive document fields: disk I/O " +
				"error); search may miss documents; run `curio doctor`\n"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := apitest.Start(t, func(d *api.Deps) { d.DocumentFields = tc.fields })
			out := mustRun(t, srv, "status")
			if tc.want == "" {
				assert.NotContains(t, out, "search fields")
				return
			}
			assert.Contains(t, out, tc.want)
			assert.Equal(t, 1, strings.Count(out, "search fields"))
		})
	}
}

// TestSearchFieldsCheck: doctor passes a start that checked the search
// fields, saying when, and that it rebuilt their index if it did, and
// fails one that couldn't, with the restart that fixes it.
func TestSearchFieldsCheck(t *testing.T) {
	checked := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	at := localTime(checked)
	for name, tc := range map[string]struct {
		fields               client.DocumentFields
		status               checkStatus
		detail, hintContains string
	}{
		"checked": {client.DocumentFields{CheckedAt: checked}, statusOK, "checked when the daemon started (" + at + ")", ""},
		"rebuilt": {client.DocumentFields{CheckedAt: checked, Rebuilt: true}, statusOK,
			"checked when the daemon started (" + at + "); their index didn't match them, and was rebuilt", ""},
		"failed": {client.DocumentFields{CheckedAt: checked, Rebuilt: true, Error: "disk I/O error"}, statusFail,
			"the daemon's start (" + at + ") couldn't check them: disk I/O error; search may miss documents",
			"`curio daemon stop`, then any curio command starts it again"},
	} {
		t.Run(name, func(t *testing.T) {
			status, detail, hint := searchFieldsCheck(tc.fields)
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.detail, detail)
			if tc.hintContains == "" {
				assert.Empty(t, hint)
				return
			}
			assert.Contains(t, hint, tc.hintContains)
			assert.Contains(t, hint, "docs/setup.md")
		})
	}
}

// failingClassifications is a bookmark store whose saved-date
// classifications fail.
type failingClassifications struct{ store.BookmarkStore }

func (failingClassifications) ClassifySavedDates(context.Context, string, string) (store.SavedDatesClassified,
	error) {
	return store.SavedDatesClassified{}, errors.New("database is locked")
}

// TestStatus_SavedDates: status warns, a line each, while the daemon's
// start couldn't classify the saved dates and while an import's source
// is owed a classification, and says nothing of a start that classified
// them.
func TestStatus_SavedDates(t *testing.T) {
	checked := time.Now()
	startFailed := "warning: the daemon's start couldn't classify the saved dates (classify saved dates: disk I/O " +
		"error); date filters may misjudge bookmarks; run `curio doctor`\n"
	owed := "warning: the saved dates of the chrome and safari bookmarks an import stored aren't classified yet; " +
		"date filters leave them out until a retry does; run `curio doctor`\n"
	for name, tc := range map[string]struct {
		start api.SavedDates
		owe   []string
		want  []string
	}{
		"classified": {start: api.SavedDates{CheckedAt: checked}},
		"start failed": {start: api.SavedDates{CheckedAt: checked, Error: "classify saved dates: disk I/O error"},
			want: []string{startFailed}},
		"owed": {start: api.SavedDates{CheckedAt: checked}, owe: []string{"safari", "chrome"}, want: []string{owed}},
		"both": {start: api.SavedDates{CheckedAt: checked, Error: "classify saved dates: disk I/O error"},
			owe: []string{"chrome", "safari"}, want: []string{startFailed, owed}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := apitest.Start(t, func(d *api.Deps) {
				d.SavedDates = api.NewSavedDateClassifier(context.Background(), api.SavedDateOptions{
					Bookmarks: failingClassifications{d.Bookmarks}, Start: tc.start, Log: d.Log})
				for _, source := range tc.owe {
					d.SavedDates.Classify(source)
				}
			})
			out := mustRun(t, srv, "status")
			assert.Equal(t, len(tc.want), strings.Count(out, "warning: "), out)
			for _, line := range tc.want {
				assert.Contains(t, out, line)
			}
		})
	}
}

// TestSavedDatesCheck: doctor passes a start that classified the saved
// dates, saying when, and fails one that couldn't, with the restart that
// fixes it, and a source owed a classification, naming the oldest
// failure, with the retry and the restart.
func TestSavedDatesCheck(t *testing.T) {
	checked := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	since := checked.Add(time.Hour)
	at := localTime(checked)
	for name, tc := range map[string]struct {
		saved               client.SavedDates
		status              checkStatus
		detail, hintContain string
	}{
		"classified": {client.SavedDates{CheckedAt: checked, Changed: 12}, statusOK,
			"classified when the daemon started (" + at + ")", ""},
		"start failed": {client.SavedDates{CheckedAt: checked, Error: "disk I/O error",
			Owed: []client.OwedClassification{{Source: "chrome", Since: since, Error: "database is locked"}}},
			statusFail, "the daemon's start (" + at + ") couldn't classify them: disk I/O error; date filters read " +
				"the classes the bookmarks had", "`curio daemon stop`, then any curio command starts it again"},
		"owed": {client.SavedDates{CheckedAt: checked, Owed: []client.OwedClassification{
			{Source: "chrome", Since: since.Add(time.Minute), Error: "database is locked"},
			{Source: "html", Since: since, Error: "disk I/O error"}}},
			statusFail, "the chrome and html bookmarks an import stored aren't classified, failing since " +
				localTime(since) + ": disk I/O error; date filters leave them out", "the daemon retries every minute"},
	} {
		t.Run(name, func(t *testing.T) {
			status, detail, hint := savedDatesCheck(tc.saved)
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.detail, detail)
			if tc.hintContain == "" {
				assert.Empty(t, hint)
				return
			}
			assert.Contains(t, hint, tc.hintContain)
			assert.Contains(t, hint, "its start classifies every bookmark")
		})
	}
}

// TestStatus_Cards: status counts the cards left to embed, and prints no
// line while none is.
func TestStatus_Cards(t *testing.T) {
	srv := apitest.Start(t)
	assert.NotContains(t, mustRun(t, srv, "status"), "cards:")
	srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	srv.AddDocument(t, "https://example.com/b", store.DocStatePending)
	assert.Contains(t, mustRun(t, srv, "status"), "\ncards:     2 to embed\n")
}

// TestStatus_ChunkContext: status counts the chunks left to re-embed for
// the chunk context and their documents, of all the fetched ones, with the
// time left while the daemon has measured the re-embed's rate, and prints
// no line while none is.
func TestStatus_ChunkContext(t *testing.T) {
	srv := apitest.Start(t)
	srv.AddContent(t, srv.AddDocument(t, "https://example.com/a", store.DocStateFetched), "indexed")
	srv.AddDocument(t, "https://example.com/b", store.DocStateFetched)
	assert.NotContains(t, mustRun(t, srv, "status"), "chunks:")
	_, err := srv.DB.Exec(`UPDATE chunks SET context_version = 0`)
	require.NoError(t, err)
	assert.Contains(t, mustRun(t, srv, "status"),
		"\nchunks:    1 chunk in 1 of 2 documents to re-embed (new chunk context)\n")

	running := apitest.Start(t, func(d *api.Deps) {
		d.Reembed = reembedAt{jobs.ReembedProgress{Active: true, ChunksPerSecond: 0.5}}
	})
	running.AddContent(t, running.AddDocument(t, "https://example.com/a", store.DocStateFetched), "indexed")
	_, err = running.DB.Exec(`UPDATE chunks SET context_version = 0`)
	require.NoError(t, err)
	assert.Contains(t, mustRun(t, running, "status"), "\nchunks:    1 chunk in 1 of 1 documents to re-embed "+
		"(new chunk context), under a minute left at 0.5 chunks/s\n")
}

// reembedAt is a re-embed that reports progress.
type reembedAt struct{ progress jobs.ReembedProgress }

func (r reembedAt) Progress() jobs.ReembedProgress { return r.progress }

// TestReembedCheck: doctor passes documents left to re-embed while index
// jobs are queued to re-embed them, with the time left once measured, and
// warns when none is, with the restart that resumes the re-embed, and while
// the re-embed's brake stops it, with what fixes the model.
func TestReembedCheck(t *testing.T) {
	const done = "every document's chunks are embedded with their context"
	until := time.Date(2026, 10, 14, 14, 30, 0, 0, time.UTC)
	brake := &client.ReembedBrake{Until: until, Failures: 3, Documents: []string{"d1", "d2", "d3"},
		Error: "HTTP 500: llama runner process has terminated"}
	for name, tc := range map[string]struct {
		left         *client.ReembedStats
		queued       bool
		brake        *client.ReembedBrake
		status       checkStatus
		detail, hint string
	}{
		"none left":               {nil, false, nil, statusOK, done, ""},
		"none left, jobs":         {nil, true, nil, statusOK, done, ""},
		"none left, the brake on": {nil, false, brake, statusOK, done, ""},
		"the brake on": {&client.ReembedStats{Documents: 12, Chunks: 140}, true, brake, statusWarn,
			"12 documents (140 chunks) to re-embed for the new chunk context; the re-embed waits until " +
				localTime(until) + ", the re-embeds of 3 documents having failed in a row (d1, d2 and d3): HTTP 500: " +
				"llama runner process has terminated; until then each keeps its earlier vectors", reembedBrakeFix},
		"some left, jobs": {&client.ReembedStats{Documents: 12, Chunks: 140}, true, nil, statusOK,
			"12 documents (140 chunks) to re-embed for the new chunk context; index jobs are queued to re-embed " +
				"them", ""},
		"measured": {&client.ReembedStats{Documents: 12, Chunks: 140, ChunksPerSecond: 0.5, ETASeconds: 280},
			true, nil, statusOK, "12 documents (140 chunks) to re-embed for the new chunk context, about 5 min left at " +
				"0.5 chunks/s; index jobs are queued to re-embed them", ""},
		"one left, no job": {&client.ReembedStats{Documents: 1, Chunks: 1}, false, nil, statusWarn,
			"1 document (1 chunk) to re-embed for the new chunk context, and no index job is queued to re-embed it",
			reembedRestart},
		"several left, no job": {&client.ReembedStats{Documents: 7, Chunks: 70, ChunksPerSecond: 2, ETASeconds: 35},
			false, nil, statusWarn, "7 documents (70 chunks) to re-embed for the new chunk context, about 1 min left at " +
				"2.0 chunks/s, and no index job is queued to re-embed them", reembedRestart},
	} {
		t.Run(name, func(t *testing.T) {
			status, detail, hint := reembedCheck(tc.left, tc.queued, tc.brake)
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.detail, detail)
			assert.Equal(t, tc.hint, hint)
		})
	}
	assert.Contains(t, reembedRestart, "tries a document that fails on its own up to 3 times",
		"the hint says what the restart does")
	for want, b := range map[string]client.ReembedBrake{
		"":                                 {Failures: 3},
		" (d1)":                            {Failures: 1, Documents: []string{"d1"}},
		" (d1 and d2)":                     {Failures: 2, Documents: []string{"d1", "d2"}},
		" (d1, d2, d3, d4, d5 and 2 more)": {Failures: 7, Documents: []string{"d1", "d2", "d3", "d4", "d5"}},
	} {
		assert.Equal(t, want, brakeDocuments(b))
	}
	for d, want := range map[time.Duration]string{0: "under a minute", 29 * time.Second: "under a minute",
		90 * time.Second: "about 2 min", 59 * time.Minute: "about 59 min", 85 * time.Minute: "about 1 h 25 min"} {
		assert.Equal(t, want, roughly(d), d)
	}
}

// TestCardsCheck: doctor passes cards left to embed while a cards job runs
// or waits to run, and warns while the job waiting carries an error or
// none is queued.
func TestCardsCheck(t *testing.T) {
	due := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	waiting := "waiting for the embedding model to answer"
	for name, tc := range map[string]struct {
		stale            int
		pending, running []client.Job
		status           checkStatus
		detail, hint     string
	}{
		"none stale": {0, nil, nil, statusOK, "every card is embedded", ""},
		"none stale, a job deferred": {0, []client.Job{{RunAfter: due, LastError: &waiting}}, nil, statusOK,
			"every card is embedded", ""},
		"a job running": {12, nil, []client.Job{{}}, statusOK, "12 cards to embed; the cards job is running", ""},
		"a job pending": {12, []client.Job{{RunAfter: due}}, nil, statusOK,
			"12 cards to embed; the cards job runs at " + localTime(due), ""},
		"a job deferred": {12, []client.Job{{RunAfter: due, LastError: &waiting}}, nil, statusWarn,
			"12 cards to embed; the cards job waits to run at " + localTime(due) + ": " + waiting,
			"`curio jobs --status pending` shows it; it runs again by itself"},
		"a job running, another deferred": {12, []client.Job{{RunAfter: due, LastError: &waiting}}, []client.Job{{}},
			statusOK, "12 cards to embed; the cards job is running", ""},
		"no job": {12, nil, nil, statusWarn, "12 cards to embed, and no cards job is queued to embed them",
			"restart the daemon (`curio daemon stop`, then any curio command starts it): its start queues the job"},
	} {
		t.Run(name, func(t *testing.T) {
			status, detail, hint := cardsCheck(tc.stale, tc.pending, tc.running)
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.detail, detail)
			assert.Equal(t, tc.hint, hint)
		})
	}
}
