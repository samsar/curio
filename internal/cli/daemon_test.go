package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, describeDaemonStatus(tc.st, home))
		})
	}
}
