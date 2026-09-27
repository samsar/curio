package cli

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
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
		client.CallRateLimited: "a fetcher.native.jina_api_key raises the limit",
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
