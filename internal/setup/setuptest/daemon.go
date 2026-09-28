package setuptest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/importer"
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
	// YTDLPMarkerVar names a file whose presence, as the fake daemon
	// starts, makes it route YouTube videos to YTDLPPath, as a daemon
	// that found yt-dlp does: a test's installer makes it for `brew
	// install yt-dlp`, never the host's PATH.
	YTDLPMarkerVar = "CURIO_SETUPTEST_YTDLP_MARKER"
	// YTDLPPath is the yt-dlp the fake daemon reports.
	YTDLPPath = "/opt/homebrew/bin/yt-dlp"
	// requestLog is the file in the home's logs the fake daemon records
	// each request that changes something in, one line each.
	requestLog = "setuptest-daemon.log"
)

// RunDaemonIfAsked runs this test binary as the fake daemon when
// DaemonVar is set, and returns the exit status for TestMain to exit with;
// asked is false, and nothing runs, otherwise. Call it first in TestMain.
//
// The fake is curio-daemon as far as curio up and the CLI see it: it
// holds the home's lock ($CURIO_HOME), and serves, on config.yaml's
// daemon.listen, a healthz naming itself, its version, config.yaml's
// writing model and its YouTube route (YTDLPMarkerVar); stats reporting a
// library of 3 bookmarks; the queue, whose settings it keeps; and imports,
// counting every valid URL as new, until SIGTERM. What changes something
// is recorded in the home's logs (DaemonRequests).
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
	ytdlp := ""
	if marker := os.Getenv(YTDLPMarkerVar); marker != "" {
		if _, err := os.Stat(marker); err == nil {
			ytdlp = YTDLPPath
		}
	}
	fake := &fakeDaemon{home: home, version: v, cfg: cfg, ytdlp: ytdlp,
		queue: map[string]any{"paused": false, "throttle": "normal", "keep_awake": false}}
	srv := &http.Server{Handler: fake, ReadHeaderTimeout: time.Second}
	defer func() { _ = srv.Close() }() // the process ends with it
	go func() { _ = srv.Serve(ln) }()  // Serve ends when srv closes
	select {
	case <-stop:
	case <-time.After(2 * time.Minute): // never outlive the test run
	}
	return nil
}

// fakeDaemon is the part of the daemon's API the fake serves.
type fakeDaemon struct {
	home    *curiohome.Home
	version string
	cfg     config.Config
	ytdlp   string

	mu    sync.Mutex
	queue map[string]any // paused, throttle, schedule, keep_awake
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/healthz":
		health := map[string]any{"status": "ok", "pid": os.Getpid(), "home": f.home.Path, "version": f.version,
			"generation_model": f.cfg.Generation.Model, "embedding_model": f.cfg.Embedding.Model,
			"embedding_dim": f.cfg.Embedding.Dim, "ollama_reachable": true, "upstreams": []any{}}
		if f.ytdlp != "" {
			health["youtube_fetcher"] = f.ytdlp
		}
		writeJSON(w, health)
	case r.URL.Path == "/v1/stats":
		writeJSON(w, map[string]any{"version": f.version, "bookmarks_total": 3, "documents_total": 3})
	case r.URL.Path == "/v1/queue":
		f.serveQueue(w, r)
	case r.URL.Path == "/v1/bookmarks/import" && r.Method == http.MethodPost:
		f.serveImport(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveQueue reports the queue, open whatever its settings, after a PUT's
// changes.
func (f *fakeDaemon) serveQueue(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodPut {
		var u map[string]any
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		maps.Copy(f.queue, u)
		if f.queue["schedule"] == "off" {
			delete(f.queue, "schedule")
		}
		f.record(r, u)
	}
	q := maps.Clone(f.queue)
	q["state"] = "open"
	q["kinds"] = []map[string]any{{"kind": "fetch", "limit": 16}, {"kind": "index", "limit": 4}}
	writeJSON(w, q)
}

// serveImport counts an import's bookmarks as the daemon would with an
// empty library: every one the filter keeps, once, is created and fetched.
func (f *fakeDaemon) serveImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source    string `json:"source"`
		DryRun    bool   `json:"dry_run"`
		Bookmarks []struct {
			URL string `json:"url"`
		} `json:"bookmarks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := map[string]any{"source": req.Source, "total": len(req.Bookmarks), "dry_run": req.DryRun}
	var created []string
	seen := map[string]bool{}
	filtered, skipped := 0, 0
	for _, b := range req.Bookmarks {
		norm, why := importer.Classify(b.URL)
		switch {
		case why != "":
			filtered++
		case seen[norm]:
			skipped++
		default:
			seen[norm] = true
			created = append(created, norm)
		}
	}
	resp["created"], resp["skipped"], resp["filtered"], resp["jobs_enqueued"] = len(created), skipped, filtered, len(created)
	if req.DryRun {
		resp["new_urls"] = created
	}
	f.mu.Lock()
	f.record(r, map[string]any{"source": req.Source, "dry_run": req.DryRun, "created": len(created)})
	f.mu.Unlock()
	writeJSON(w, resp)
}

// record appends the request and what it carried to the request log; the
// log is the tests', and a failure to write it loses only that.
func (f *fakeDaemon) record(r *http.Request, what map[string]any) {
	line, err := json.Marshal(what)
	if err != nil {
		return
	}
	log, err := os.OpenFile(filepath.Join(f.home.Path, curiohome.LogsDirName, requestLog),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer log.Close()
	_, _ = fmt.Fprintf(log, "%s %s %s\n", r.Method, r.URL.Path, line)
}

// DaemonRequests are the requests that changed something that the fake
// daemons serving home received, in order: "PUT /v1/queue {...}", "POST
// /v1/bookmarks/import {...}".
func DaemonRequests(home string) []string {
	data, err := os.ReadFile(filepath.Join(home, curiohome.LogsDirName, requestLog))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}
