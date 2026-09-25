package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/store"
)

func TestDescribeDaemonStatus(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir()
	cases := []struct {
		name string
		st   daemonctl.Status
		want string
	}{
		{
			name: "starting",
			st:   daemonctl.Status{State: daemonctl.Running},
			want: "starting (lock held, pid not recorded yet)",
		},
		{
			name: "starting, migrating",
			st: daemonctl.Status{State: daemonctl.Running, PID: 42,
				Startup: &client.Startup{PID: 42, Home: home, Version: "v1.2.3", Phase: client.PhaseMigrating,
					Migrations: &client.MigrationProgress{Applied: 2, Total: 6}}},
			want: "starting (pid 42, home " + home + ", version v1.2.3): migrating the database, 2 of 6 migrations applied",
		},
		{
			name: "starting, initializing",
			st: daemonctl.Status{State: daemonctl.Running, PID: 42,
				Startup: &client.Startup{PID: 42, Home: home, Version: "v1.2.3", Phase: client.PhaseInitializing}},
			want: "starting (pid 42, home " + home + ", version v1.2.3): initializing",
		},
		{
			name: "running, not serving yet",
			st:   daemonctl.Status{State: daemonctl.Running, PID: 42},
			want: "running (pid 42), not answering HTTP yet",
		},
		{
			name: "running",
			st: daemonctl.Status{State: daemonctl.Running, PID: 42,
				Health: &client.Health{PID: 42, Home: home, Version: "v1.2.3"}},
			want: "running (pid 42, home " + home + ", version v1.2.3)",
		},
		{
			name: "stale PID file",
			st:   daemonctl.Status{State: daemonctl.Stale, PID: 42},
			want: "not running (stale PID file: pid 42 is left over from an earlier run and is ignored)",
		},
		{
			name: "legacy daemon",
			st:   daemonctl.Status{State: daemonctl.Legacy, Health: &client.Health{Version: "v0.2.0"}},
			want: "legacy daemon from an older curio is answering (version v0.2.0); " +
				"run `curio daemon stop` for how to retire it",
		},
		{
			name: "not running",
			st:   daemonctl.Status{State: daemonctl.NotRunning},
			want: "not running",
		},
		{
			name: "not running, port served for another home",
			st: daemonctl.Status{State: daemonctl.NotRunning,
				Health: &client.Health{PID: 7, Home: other}},
			want: "not running (the port is served by the daemon for " + other + ")",
		},
		{
			name: "not running, port held by another home's starting daemon",
			st: daemonctl.Status{State: daemonctl.NotRunning,
				Startup: &client.Startup{PID: 7, Home: other, Phase: client.PhaseMigrating}},
			want: "not running (the port is served by the daemon for " + other + ")",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, describeDaemonStatus(tc.st, home))
		})
	}
}

// TestDaemonStop_NotRunning: stopping a home with no daemon says so rather
// than claiming to have stopped one.
func TestDaemonStop_NotRunning(t *testing.T) {
	home, err := curiohome.Init(t.TempDir(), "nomic-embed-text", store.EmbeddingDim)
	require.NoError(t, err)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	out, err := runCLIAt(t, home.Path, down.URL, "daemon", "stop")
	require.NoError(t, err)
	assert.Equal(t, "daemon not running\n", out)
}

// syncBuffer is a bytes.Buffer safe to read while a command writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestDaemonStart_WaitsOutAMigration: `curio daemon start` against a
// daemon that is migrating says once, on stderr, why it waits, and reports
// the daemon running once it is ready.
func TestDaemonStart_WaitsOutAMigration(t *testing.T) {
	srv := apitest.StartNotReady(t)
	srv.Startup.SetMigrating(6)
	// The test process is the daemon holding the lock: healthz names it.
	lock, err := daemonctl.AcquireLock(srv.Home)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, lock.Release()) })

	var stdout, stderr syncBuffer
	done := make(chan error, 1)
	go func() { done <- runCLIStreams(srv.Home.Path, srv.URL, &stdout, &stderr, "daemon", "start") }()

	notice := "curio-daemon is migrating the database (6 migrations); this can take a minute on a large library; " +
		"`curio daemon logs -f` shows progress\n"
	require.Eventually(t, func() bool { return stderr.String() == notice }, 5*time.Second, 10*time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("daemon start returned while the daemon was migrating: %v", err)
	case <-time.After(1500 * time.Millisecond): // past a poll or two
	}
	require.NoError(t, srv.Ready())

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon start did not return once the daemon was ready")
	}
	assert.Equal(t, "daemon running\n", stdout.String())
	assert.Equal(t, notice, stderr.String(), "said once")
}
