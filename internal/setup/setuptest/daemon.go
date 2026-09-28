package setuptest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/version"
)

const (
	// DaemonVar, set in a test binary's environment, makes it a fake
	// curio-daemon (RunDaemonIfAsked): what a test's launchd agent
	// launches, and what a controller spawns when it runs the test binary
	// as curio-daemon.
	DaemonVar = "CURIO_SETUPTEST_DAEMON"
	// DaemonVersionVar is the version the fake daemon reports; this
	// curio's when unset.
	DaemonVersionVar = "CURIO_SETUPTEST_DAEMON_VERSION"
)

// RunDaemonIfAsked runs this test binary as the fake daemon when
// DaemonVar is set, and returns the exit status for TestMain to exit with;
// asked is false, and nothing runs, otherwise. Call it first in TestMain.
//
// The fake is curio-daemon as far as curio up and the CLI see it: it
// holds the home's lock ($CURIO_HOME), and serves, on config.yaml's
// daemon.listen, a healthz naming itself, its version and config.yaml's
// writing model, and the stats and queue the status reads, until SIGTERM.
func RunDaemonIfAsked() (exitCode int, asked bool) {
	if os.Getenv(DaemonVar) == "" {
		return 0, false
	}
	if err := runDaemon(); err != nil {
		fmt.Fprintln(os.Stderr, "fake daemon:", err)
		return 1, true
	}
	return 0, true
}

func runDaemon() error {
	home, err := curiohome.Open(os.Getenv("CURIO_HOME"))
	if err != nil {
		return err
	}
	cfg, err := config.Load(home.ConfigPath())
	if err != nil {
		return err
	}
	lock, err := daemonctl.AcquireLock(home)
	if errors.Is(err, daemonctl.ErrAlreadyRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }() // the process ends with it
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", cfg.Daemon.Listen)
	if err != nil {
		return err
	}
	v := os.Getenv(DaemonVersionVar)
	if v == "" {
		v = version.String()
	}
	srv := &http.Server{Handler: daemonAPI(home.Path, v, cfg), ReadHeaderTimeout: time.Second}
	defer func() { _ = srv.Close() }() // the process ends with it
	go func() { _ = srv.Serve(ln) }()  // Serve ends when srv closes
	select {
	case <-stop:
	case <-time.After(2 * time.Minute): // never outlive the test run
	}
	return nil
}

// daemonAPI is the part of the daemon's API the fake serves.
func daemonAPI(home, v string, cfg config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/healthz":
			writeJSON(w, map[string]any{"status": "ok", "pid": os.Getpid(), "home": home, "version": v,
				"generation_model": cfg.Generation.Model, "embedding_model": cfg.Embedding.Model,
				"embedding_dim": cfg.Embedding.Dim, "ollama_reachable": true, "upstreams": []any{}})
		case r.URL.Path == "/v1/stats":
			writeJSON(w, map[string]any{"version": v, "bookmarks_total": 3, "documents_total": 3})
		case strings.HasPrefix(r.URL.Path, "/v1/queue"):
			writeJSON(w, map[string]any{"paused": false, "throttle": "normal", "state": "open",
				"kinds": []map[string]any{{"kind": "fetch", "limit": 16}, {"kind": "index", "limit": 4}}})
		default:
			http.NotFound(w, r)
		}
	})
}
