package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/version"
)

// getStarting sends req to a starting server and checks what every
// starting answer has in common: 503, the starting problem type and
// Retry-After. It returns the body decoded as a map, so a test can tell a
// member that is absent from one that is zero.
func getStarting(t *testing.T, s *testServer, req request) map[string]any {
	t.Helper()
	r, err := http.NewRequest(req.method, s.base+req.path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	got := newResponse(t, resp, r.URL.Path)

	p := assertProblem(t, got, http.StatusServiceUnavailable)
	assert.Equal(t, StartingProblemType, p.Type)
	assert.Equal(t, startingRetryAfter, resp.Header.Get("Retry-After"))
	var members map[string]any
	require.NoError(t, json.Unmarshal([]byte(got.body), &members))
	return members
}

// TestStarting_HealthzNamesTheDaemon: a starting daemon's healthz answer is
// a 503 problem that names the daemon and says how far along it is, with
// migration progress exactly while it migrates.
func TestStarting_HealthzNamesTheDaemon(t *testing.T) {
	s := newStartingTestServer(t)
	healthz := request{method: http.MethodGet, path: "/v1/healthz"}

	body := getStarting(t, s, healthz)
	assert.EqualValues(t, os.Getpid(), body["pid"])
	assert.Equal(t, s.deps.Home.Path, body["home"])
	assert.Equal(t, version.String(), body["version"])
	assert.Equal(t, "initializing", body["phase"])
	assert.NotContains(t, body, "migrations")
	assert.Equal(t, "curio-daemon is starting: initializing", body["detail"])

	s.startup.SetMigrating(6)
	body = getStarting(t, s, healthz)
	assert.Equal(t, "migrating", body["phase"])
	assert.Equal(t, map[string]any{"applied": 0.0, "total": 6.0}, body["migrations"],
		"applied is sent when it is 0")
	assert.Equal(t, "curio-daemon is starting: migrating the database, 0 of 6 migrations applied", body["detail"])

	s.startup.MigrationApplied()
	body = getStarting(t, s, healthz)
	assert.Equal(t, map[string]any{"applied": 1.0, "total": 6.0}, body["migrations"])

	s.startup.SetInitializing()
	body = getStarting(t, s, healthz)
	assert.Equal(t, "initializing", body["phase"])
	assert.NotContains(t, body, "migrations")
}

// TestStarting_RefusesEverythingElse: every other request, whatever its
// method or path, is a 503 starting problem without the daemon's identity,
// and none reaches a handler.
func TestStarting_RefusesEverythingElse(t *testing.T) {
	s := newStartingTestServer(t)
	for _, req := range []request{
		{method: http.MethodGet, path: "/v1/stats"},
		{method: http.MethodPost, path: "/v1/documents/refetch-all"},
		{method: http.MethodDelete, path: "/v1/jobs?status=failed"},
		{method: http.MethodPost, path: "/v1/healthz"},
		{method: http.MethodGet, path: "/v1/nope"},
		{method: http.MethodGet, path: "/"},
	} {
		t.Run(req.method+" "+req.path, func(t *testing.T) {
			body := getStarting(t, s, req)
			for _, identity := range []string{"pid", "home", "version", "phase", "migrations"} {
				assert.NotContains(t, body, identity)
			}
		})
	}

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/bookmarks", contentType: "application/json",
		body: `{"url":"https://example.com/a"}`})
	assert.Equal(t, http.StatusServiceUnavailable, resp.status, resp.body)
	assert.Zero(t, s.count(t, "bookmarks"), "nothing a starting daemon refused has run")
	assert.Zero(t, s.count(t, "jobs"))
}

// TestStarting_AccessPolicy: the access checks apply while starting, before
// the daemon says who it is, and every answer carries a request ID and an
// access-log line.
func TestStarting_AccessPolicy(t *testing.T) {
	var rec logRecorder
	s := newStartingTestServer(t, func(d *Deps) { d.Log = slog.New(&rec) })
	cases := []struct {
		name   string
		req    request
		status int
	}{
		{"foreign host", request{method: http.MethodGet, path: "/v1/healthz", host: "attacker.example:" + s.port},
			http.StatusForbidden},
		{"foreign origin", request{method: http.MethodGet, path: "/v1/healthz", origin: "https://attacker.example"},
			http.StatusForbidden},
		{"null origin", request{method: http.MethodGet, path: "/v1/healthz", origin: "null"}, http.StatusForbidden},
		{"body that isn't JSON", request{method: http.MethodPost, path: "/v1/bookmarks", contentType: "text/plain",
			body: `{"url":"https://example.com/a"}`}, http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.do(t, tc.req)
			p := assertProblem(t, resp, tc.status)
			assert.Equal(t, "about:blank", p.Type)
			assert.NotContains(t, resp.body, `"pid"`)
			assert.NotContains(t, resp.body, `"home"`)
		})
	}

	var logged int
	for _, r := range rec.at(slog.LevelInfo) {
		if r["msg"] == "http" {
			logged++
		}
	}
	assert.Equal(t, len(cases), logged, "one access-log line per request")
}

// TestStarting_ReadyKeepsTheConnection: a client that asked a starting
// daemon gets the full API's answer on the same keep-alive connection once
// the daemon is ready.
func TestStarting_ReadyKeepsTheConnection(t *testing.T) {
	s := newStartingTestServer(t)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	c := &http.Client{Transport: transport}

	get := func() (status int, reused bool) {
		t.Helper()
		ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
		})
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/v1/healthz", nil)
		require.NoError(t, err)
		resp, err := c.Do(r)
		require.NoError(t, err)
		defer resp.Body.Close()
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, reused
	}

	status, _ := get()
	assert.Equal(t, http.StatusServiceUnavailable, status)
	s.ready(t)
	status, reused := get()
	assert.Equal(t, http.StatusOK, status)
	assert.True(t, reused, "the same connection serves the full API")
}

// TestStarting_RequestsRacingReady: requests in flight while the daemon
// swaps in the full API each get one answer or the other, whole.
func TestStarting_RequestsRacingReady(t *testing.T) {
	s := newStartingTestServer(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				resp := s.do(t, request{method: http.MethodGet, path: "/v1/healthz"})
				if resp.status == http.StatusOK {
					var h Health
					assert.NoError(t, json.Unmarshal([]byte(resp.body), &h))
					assert.Equal(t, "ok", h.Status)
					continue
				}
				p := assertProblem(t, resp, http.StatusServiceUnavailable)
				assert.Equal(t, StartingProblemType, p.Type)
			}
		})
	}
	s.ready(t)
	wg.Wait()
	assert.Equal(t, http.StatusOK, s.do(t, request{method: http.MethodGet, path: "/v1/healthz"}).status)
}
