package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/keepawake"
	"github.com/samsar/curio/internal/store"
)

// windowExcludingNow is a daily window on the local clock, the daemon's in
// these tests, from two hours from now to three, and when it opens, as
// HH:MM. The opening comes from NextStart, not the window's start: a
// daylight-saving gap can move it.
func windowExcludingNow() (window, opens string) {
	now := time.Now()
	m := now.Hour()*60 + now.Minute()
	const day = 24 * 60
	w := store.DailyWindow{Start: (m + 120) % day, End: (m + 180) % day}
	return w.String(), w.NextStart(now).Format("15:04")
}

// oneLine returns out's only line, failing the test if it has another.
func oneLine(t *testing.T, out string) string {
	t.Helper()
	require.Equal(t, 1, strings.Count(out, "\n"), "one line:\n%s", out)
	return strings.TrimSuffix(out, "\n")
}

func TestQueueCommands(t *testing.T) {
	srv := apitest.Start(t)
	gate := srv.Deps.Gate
	status := func() string {
		t.Helper()
		out := mustRun(t, srv, "status")
		for line := range strings.SplitSeq(out, "\n") {
			if strings.HasPrefix(line, "queue:") {
				return line
			}
		}
		t.Fatalf("status has no queue line:\n%s", out)
		return ""
	}
	assert.Equal(t, "queue:     open", status())

	line := oneLine(t, mustRun(t, srv, "pause"))
	assert.Equal(t, "queue: paused; no jobs running, nothing new starts (curio resume)", line)
	assert.True(t, gate.State(time.Now()).Settings.Paused)
	assert.Contains(t, status(), "paused")

	line = oneLine(t, mustRun(t, srv, "throttle", "gentle"))
	assert.Equal(t, "queue: paused, throttled gentle (fetch 4, index 1 at once); no jobs running, "+
		"nothing new starts (curio resume)", line)
	assert.Contains(t, status(), "paused, throttled gentle (fetch 4, index 1 at once)")

	line = oneLine(t, mustRun(t, srv, "resume"))
	assert.Equal(t, "queue: open, throttled gentle (fetch 4, index 1 at once)", line)
	assert.Equal(t, "queue:     open, throttled gentle (fetch 4, index 1 at once)", status())

	window, opens := windowExcludingNow()
	line = oneLine(t, mustRun(t, srv, "schedule", window))
	assert.Equal(t, fmt.Sprintf("queue: closed outside schedule %s, throttled gentle (fetch 4, index 1 at once); "+
		"opens %s (curio schedule off runs it now)", window, opens), line)
	assert.Contains(t, status(), "closed outside schedule "+window)

	line = oneLine(t, mustRun(t, srv, "throttle", "normal"))
	assert.Equal(t, fmt.Sprintf("queue: closed outside schedule %s; opens %s (curio schedule off runs it now)",
		window, opens), line)
	line = status()
	assert.Contains(t, line, "closed outside schedule "+window)
	assert.NotContains(t, line, "throttled")

	line = oneLine(t, mustRun(t, srv, "schedule", "off"))
	assert.Equal(t, "queue: open", line)
	assert.Equal(t, "queue:     open", status())
}

// TestQueueCommands_Refused: the daemon validates, and its reason is the
// error.
func TestQueueCommands_Refused(t *testing.T) {
	srv := apitest.Start(t)
	_, err := runCLI(t, srv, "throttle", "fast")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `throttle "fast" must be one of: normal, gentle`)

	_, err = runCLI(t, srv, "schedule", "25:00-07:00")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `schedule "25:00-07:00"`)
	assert.Contains(t, err.Error(), "22:00-07:00, or off")

	_, err = runCLI(t, srv, "throttle")
	require.Error(t, err, "throttle takes a profile")
	assert.Equal(t, "normal", string(srv.Deps.Gate.State(time.Now()).Settings.Throttle))
}

// TestKeepAwakeCommand: keep-awake on and off change the stored setting;
// anything else is refused before the daemon is asked.
func TestKeepAwakeCommand(t *testing.T) {
	srv := apitest.Start(t)
	gate := srv.Deps.Gate

	line := oneLine(t, mustRun(t, srv, "keep-awake", "on"))
	assert.Equal(t, "keep-awake: on (the daemon keeps the Mac from idle sleep while jobs are queued "+
		"and it runs on AC power)", line)
	assert.True(t, gate.State(time.Now()).Settings.KeepAwake)

	line = oneLine(t, mustRun(t, srv, "keep-awake", "off"))
	assert.Equal(t, "keep-awake: off", line)
	assert.False(t, gate.State(time.Now()).Settings.KeepAwake)

	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	for _, args := range [][]string{{"keep-awake", "maybe"}, {"keep-awake"}, {"keep-awake", "on", "off"}} {
		_, err := runCLIAt(t, srv.Home.Path, down.URL, args...)
		require.Error(t, err, args)
		assert.NotContains(t, err.Error(), "daemon", "refused before the daemon is asked: %v", args)
	}
	_, err := runCLIAt(t, srv.Home.Path, down.URL, "keep-awake", "maybe")
	assert.Contains(t, err.Error(), `invalid argument "maybe"`)
}

// TestStatus_QueueLoad: status shows each pool's running jobs against its
// limit, and what waits.
func TestStatus_QueueLoad(t *testing.T) {
	srv := apitest.Start(t)
	mustRun(t, srv, "add", "https://example.com/a")
	out := mustRun(t, srv, "status")
	assert.Contains(t, out, "queue:     open\n"+
		"           fetch 0/16 running, 1 pending   index 0/4 running, 0 pending   cluster 0/1 running, 0 pending\n")
}

func TestDescribeKeepAwake(t *testing.T) {
	kinds := func(pending, running int) []client.QueueKind {
		return []client.QueueKind{{Kind: "fetch", Pending: pending, Running: running}, {Kind: "index"}}
	}
	cases := []struct {
		name string
		q    client.Queue
		want string
	}{
		{"off", client.Queue{Kinds: kinds(3, 1)}, "off"},
		{"holding", client.Queue{KeepAwake: true, KeepAwakeActive: true, PowerSource: "ac", Kinds: kinds(3, 1)},
			"on, holding the Mac awake (AC power, 4 jobs queued)"},
		{"holding for one", client.Queue{KeepAwake: true, KeepAwakeActive: true, PowerSource: "ac", Kinds: kinds(0, 1)},
			"on, holding the Mac awake (AC power, 1 job queued)"},
		{"paused", client.Queue{KeepAwake: true, Paused: true, PowerSource: "ac", Kinds: kinds(3, 0)},
			"on, not holding: the queue is paused"},
		{"nothing queued", client.Queue{KeepAwake: true, PowerSource: "ac", Kinds: kinds(0, 0)},
			"on, not holding: nothing queued"},
		{"on battery", client.Queue{KeepAwake: true, PowerSource: "battery", Kinds: kinds(3, 0)},
			"on, not holding: on battery power"},
		{"power unknown", client.Queue{KeepAwake: true, PowerSource: "unknown", Kinds: kinds(3, 0)},
			"on, not holding: power source unknown"},
		{"about to hold", client.Queue{KeepAwake: true, PowerSource: "ac", Kinds: kinds(3, 0)},
			"on, not holding yet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, describeKeepAwake(&tc.q))
		})
	}
}

// keepAwakeStub is a keeper reporting a fixed state.
type keepAwakeStub keepawake.State

func (s keepAwakeStub) State() keepawake.State { return keepawake.State(s) }

// TestStatus_KeepAwake: status says after the pool loads whether
// keep-awake is on and whether the daemon holds the Mac awake.
func TestStatus_KeepAwake(t *testing.T) {
	keepAwakeLine := func(t *testing.T, srv *apitest.Server) string {
		t.Helper()
		for line := range strings.SplitSeq(mustRun(t, srv, "status"), "\n") {
			if strings.HasPrefix(line, "keep-awake:") {
				return line
			}
		}
		t.Fatal("status has no keep-awake line")
		return ""
	}

	srv := apitest.Start(t)
	assert.Equal(t, "keep-awake: off", keepAwakeLine(t, srv))

	srv = apitest.Start(t, func(d *api.Deps) {
		d.KeepAwake = keepAwakeStub{Enabled: true, Active: true, Power: keepawake.PowerAC}
	})
	mustRun(t, srv, "add", "https://example.com/a")
	mustRun(t, srv, "keep-awake", "on")
	out := mustRun(t, srv, "status")
	assert.Contains(t, out, "cluster 0/1 running, 0 pending\n"+
		"keep-awake: on, holding the Mac awake (AC power, 1 job queued)\n", "right after the pool loads")

	srv = apitest.Start(t, func(d *api.Deps) {
		d.KeepAwake = keepAwakeStub{Enabled: true, Power: keepawake.PowerBattery}
	})
	mustRun(t, srv, "add", "https://example.com/a")
	mustRun(t, srv, "keep-awake", "on")
	assert.Equal(t, "keep-awake: on, not holding: on battery power", keepAwakeLine(t, srv))
}

// olderDaemon answers as this test's daemon for home, serving healthz,
// stats and metrics but no /v1/queue, as a daemon from before the queue
// controls does.
func olderDaemon(t *testing.T, home string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/healthz":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"ok","pid":%d,"home":%q,"version":"v0.9.0","schema_version":10,`+
				`"embedding_model":"nomic-embed-text","embedding_dim":768,"ollama_reachable":true,"upstreams":[]}`,
				os.Getpid(), home)
		case "/v1/stats":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"version":"v0.9.0","bookmarks_total":0,"documents_total":0}`)
		case "/v1/metrics":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"window_seconds":3600,"by_kind":[]}`)
		default:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"title":"not found","status":404,"detail":"no route for %s"}`, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestQueueCommands_OlderDaemon: after an upgrade the daemon still running
// may predate the queue controls; the commands say to restart it.
func TestQueueCommands_OlderDaemon(t *testing.T) {
	home := apitest.Start(t).Home.Path
	daemon := olderDaemon(t, home)
	const hint = "the running curio-daemon has no queue controls: it predates them. " +
		"Restart it: run `curio daemon stop`, and the next command starts the installed one"

	for _, args := range [][]string{{"pause"}, {"resume"}, {"throttle", "gentle"}, {"schedule", "off"}} {
		_, err := runCLIAt(t, home, daemon, args...)
		require.Error(t, err, args)
		assert.Contains(t, err.Error(), hint, args)
		assert.True(t, client.IsNotFound(err), "the daemon's answer is kept")
	}
	out, err := runCLIAt(t, home, daemon, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "queue:     unavailable: "+hint)
}

func TestDescribeQueue(t *testing.T) {
	kinds := func(fetch, index int) []client.QueueKind {
		return []client.QueueKind{{Kind: "fetch", Limit: fetch, Running: 2}, {Kind: "index", Limit: index, Running: 1},
			{Kind: "cluster", Limit: 1}}
	}
	opensAt := time.Date(2026, 9, 27, 22, 0, 0, 0, time.Local)
	cases := []struct {
		name string
		q    client.Queue
		want string
	}{
		{"open", client.Queue{Throttle: "normal", State: "open", Kinds: kinds(16, 4)}, "open"},
		{"gentle and scheduled", client.Queue{Throttle: "gentle", Schedule: "22:00-07:00", State: "open", Kinds: kinds(4, 1)},
			"open, throttled gentle (fetch 4, index 1 at once), schedule 22:00-07:00"},
		{"paused", client.Queue{Paused: true, Throttle: "normal", State: "closed", Reason: "paused", Kinds: kinds(16, 4)},
			"paused; 3 running jobs finish, nothing new starts (curio resume)"},
		{"paused and scheduled", client.Queue{Paused: true, Throttle: "normal", Schedule: "22:00-07:00", State: "closed",
			Reason: "paused", Kinds: []client.QueueKind{{Kind: "fetch", Limit: 16, Running: 1}}},
			"paused, schedule 22:00-07:00; 1 running job finishes, nothing new starts (curio resume)"},
		{"outside the schedule", client.Queue{Throttle: "normal", Schedule: "22:00-07:00", State: "closed",
			Reason: "outside_schedule", OpensAt: opensAt.UTC(), Kinds: kinds(16, 4)},
			"closed outside schedule 22:00-07:00; opens 22:00 (curio schedule off runs it now)"},
		{"a reason this client doesn't know", client.Queue{Throttle: "normal", State: "closed", Reason: "on_battery"},
			"closed (on_battery)"},
		{"a state this client doesn't know", client.Queue{Throttle: "normal", State: "draining"}, "draining"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, describeQueue(&tc.q))
		})
	}
}
