package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newClient(t *testing.T, baseURL, model string) *Client {
	t.Helper()
	c, err := New(baseURL, model, 5*time.Second)
	require.NoError(t, err)
	return c
}

// closedAddr returns a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// fakeOllama serves /api/tags from its model list and /api/pull by adding
// the requested model to it, as Ollama names it: untagged means :latest.
type fakeOllama struct {
	t          *testing.T
	mu         sync.Mutex
	models     []string
	tagsStatus int // 0: 200
	pullStatus int // 0: 200
	pulls      int
	pulled     []string // the model each pull asked for
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/tags":
		if f.tagsStatus != 0 {
			w.WriteHeader(f.tagsStatus)
			fmt.Fprint(w, "starting")
			return
		}
		var tags struct {
			Models []map[string]string `json:"models"`
		}
		for _, m := range f.models {
			tags.Models = append(tags.Models, map[string]string{"name": m})
		}
		assert.NoError(f.t, json.NewEncoder(w).Encode(tags))
	case "/api/pull":
		f.pulls++
		if f.pullStatus != 0 {
			w.WriteHeader(f.pullStatus)
			fmt.Fprint(w, `{"error":"no space left on device"}`)
			return
		}
		var req pullRequest
		assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&req))
		f.pulled = append(f.pulled, req.Model)
		f.models = append(f.models, normalizeModel(req.Model))
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"status":"success"}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOllama) pullCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pulls
}

func serveFake(t *testing.T, f *fakeOllama) string {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv.URL
}

// recorder is a slog handler that keeps every record.
type recorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler     { return r }
func (r *recorder) WithGroup(string) slog.Handler          { return r }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Clone())
	return nil
}

// messages lists the messages logged at level.
func (r *recorder) messages(level slog.Level) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, rec := range r.records {
		if rec.Level == level {
			out = append(out, rec.Message)
		}
	}
	return out
}

func TestNew(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		model   string
		want    string // the base URL; "" when New must fail
	}{
		{"default base URL", "", "m", DefaultBaseURL},
		{"trailing slashes trimmed", "http://127.0.0.1:11434//", "m", "http://127.0.0.1:11434"},
		{"https", "https://ollama.example:443", "m", "https://ollama.example:443"},
		{"model required", "", "", ""},
		{"no scheme", "localhost:11434", "m", ""},
		{"not http", "ftp://localhost", "m", ""},
		{"no host", "http://", "m", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.baseURL, tc.model, time.Second)
			if tc.want == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, c.BaseURL())
			assert.Equal(t, tc.model, c.Model())
		})
	}
}

func TestPing(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		status  int
		body    string
		wantErr error // nil: the model is there
	}{
		{"untagged name, :latest pulled", "nomic-embed-text", 200,
			`{"models":[{"name":"nomic-embed-text:latest","model":"nomic-embed-text:latest"}]}`, nil},
		{"untagged name, only another tag pulled", "qwen3-embedding", 200,
			`{"models":[{"name":"qwen3-embedding:0.6b","model":"qwen3-embedding:0.6b"}]}`, ErrModelNotLoaded},
		{"tagged name", "qwen3-embedding:0.6b", 200,
			`{"models":[{"name":"qwen3-embedding:0.6b","model":"qwen3-embedding:0.6b"}]}`, nil},
		{"tagged name, only a longer tag pulled", "qwen3-embedding:0.6b", 200,
			`{"models":[{"name":"qwen3-embedding:0.6b-q8_0","model":"qwen3-embedding:0.6b-q8_0"}]}`, ErrModelNotLoaded},
		{"case differs", "Qwen3-Embedding:0.6B", 200, `{"models":[{"name":"qwen3-embedding:0.6b"}]}`, nil},
		{"matched on the model field", "qwen3:4b-instruct", 200,
			`{"models":[{"name":"alias","model":"qwen3:4b-instruct"}]}`, nil},
		{"a host's port is not a tag", "localhost:5000/team/m", 200,
			`{"models":[{"name":"localhost:5000/team/m:latest"}]}`, nil},
		{"other models only", "qwen3:4b-instruct", 200,
			`{"models":[{"name":"qwen3:4b-instruct-q8_0"},{"name":"qwen3"}]}`, ErrModelNotLoaded},
		{"no models", "qwen3:4b-instruct", 200, `{"models":[]}`, ErrModelNotLoaded},
		{"server error", "qwen3:4b-instruct", 500, `oops`, ErrUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/tags", r.URL.Path)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			err := newClient(t, srv.URL, tc.model).Ping(context.Background())
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestModelDigest(t *testing.T) {
	const tags = `{"models":[{"name":"nomic-embed-text:latest","digest":"0a109f422b47"},` +
		`{"name":"qwen3-embedding:0.6b","model":"qwen3-embedding:0.6b","digest":"ac6da0dfba84"},` +
		`{"name":"qwen3-embedding:8b","digest":"64b933495768"}]}`
	cases := []struct {
		name    string
		model   string
		body    string
		status  int
		want    string
		wantErr error
		errText string
	}{
		{"the configured tag's digest", "qwen3-embedding:0.6b", tags, 200, "ac6da0dfba84", nil, ""},
		{"untagged is :latest", "nomic-embed-text", tags, 200, "0a109f422b47", nil, ""},
		{"not pulled", "qwen3-embedding", tags, 200, "", ErrModelNotLoaded, ""},
		{"no digest listed", "m", `{"models":[{"name":"m:latest"}]}`, 200, "", nil, "without a digest"},
		{"server error", "m", "oops", 500, "", ErrUnreachable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/tags", r.URL.Path)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			got, err := newClient(t, srv.URL, tc.model).ModelDigest(context.Background())
			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
			case tc.errText != "":
				require.ErrorContains(t, err, tc.errText)
			default:
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    string
		wantErr error
		errText string
	}{
		{"the server's version", 200, `{"version":"0.34.4"}`, "0.34.4", nil, ""},
		{"no version", 200, `{}`, "", nil, "no version"},
		{"a reply over the limit", 200, `{"version":"` + strings.Repeat("9", MaxResponseBody) + `"}`, "", nil, "exceeds"},
		{"server error", 503, "starting", "", ErrUnreachable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/version", r.URL.Path)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			got, err := newClient(t, srv.URL, "m").Version(context.Background())
			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
			case tc.errText != "":
				require.ErrorContains(t, err, tc.errText)
			default:
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestNormalizeModel(t *testing.T) {
	cases := map[string]string{
		"qwen3-embedding":       "qwen3-embedding:latest",
		"qwen3-embedding:0.6b":  "qwen3-embedding:0.6b",
		"Qwen3:4B-Instruct":     "qwen3:4b-instruct",
		"library/qwen3":         "library/qwen3:latest",
		"localhost:5000/team/m": "localhost:5000/team/m:latest",
		"localhost:5000/m:v1":   "localhost:5000/m:v1",
	}
	for in, want := range cases {
		assert.Equal(t, want, normalizeModel(in), in)
	}
}

func TestPing_MalformedReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"models":`)
	}))
	t.Cleanup(srv.Close)
	err := newClient(t, srv.URL, "m").Ping(context.Background())
	require.ErrorContains(t, err, "decode response")
	assert.NotErrorIs(t, err, ErrModelNotLoaded, "an unreadable reply says nothing about the model")
}

// TestTransportErrorsKeepTheirCause: a refused connection is ErrUnreachable
// with the syscall error still in the chain, for Ping and requests alike.
func TestTransportErrorsKeepTheirCause(t *testing.T) {
	c := newClient(t, "http://"+closedAddr(t), "m")

	err := c.Ping(context.Background())
	require.ErrorIs(t, err, ErrUnreachable)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)

	_, err = c.Version(context.Background())
	require.ErrorIs(t, err, ErrUnreachable)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)

	_, err = c.ModelDigest(context.Background())
	require.ErrorIs(t, err, ErrUnreachable)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)

	var out struct{}
	err = c.PostJSON(context.Background(), "/api/generate", struct{}{}, &out, MaxResponseBody)
	require.ErrorIs(t, err, ErrUnreachable)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
}

func TestPostJSON(t *testing.T) {
	type reply struct {
		Text string `json:"text"`
	}
	cases := []struct {
		name         string
		status       int
		body         string
		want         string
		wantStatus   int  // 0: no StatusError
		wantNotFound bool // wraps ErrModelNotLoaded
		wantErr      string
	}{
		{name: "decodes a 2xx reply", status: 200, body: `{"text":"hi"}`, want: "hi"},
		{name: "404 is a model that isn't pulled", status: 404, body: `{"error":"model 'm' not found"}`,
			wantStatus: 404, wantNotFound: true, wantErr: "not found"},
		{name: "other statuses are only StatusErrors", status: 400, body: `{"error":"invalid options"}`,
			wantStatus: 400, wantErr: "invalid options"},
		{name: "a reply over the limit is rejected", status: 200, body: `{"text":"` + strings.Repeat("a", 64) + `"}`,
			wantErr: "exceeds 32 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				var in map[string]string
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&in))
				assert.Equal(t, "m", in["model"])
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(srv.Close)

			var out reply
			err := newClient(t, srv.URL, "m").PostJSON(context.Background(), "/api/x",
				map[string]string{"model": "m"}, &out, 32)
			if tc.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.want, out.Text)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, tc.wantNotFound, errors.Is(err, ErrModelNotLoaded))
			var se *StatusError
			if tc.wantStatus == 0 {
				assert.NotErrorAs(t, err, &se)
				return
			}
			require.ErrorAs(t, err, &se)
			assert.Equal(t, tc.wantStatus, se.Code)
		})
	}
}

// TestPostJSON_ErrorBodyIsQuotedUpToTheCap: an error body that arrives in
// several writes is read to the 2 KiB cap, not cut at the first chunk.
func TestPostJSON_ErrorBodyIsQuotedUpToTheCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		for range 3 {
			fmt.Fprint(w, strings.Repeat("e", 1<<10))
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)

	var out struct{}
	err := newClient(t, srv.URL, "m").PostJSON(context.Background(), "/api/x", struct{}{}, &out, MaxResponseBody)
	var se *StatusError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, strings.Repeat("e", maxErrorBody), se.Body)
}

func TestEnsureModel(t *testing.T) {
	t.Run("present: no pull", func(t *testing.T) {
		f := &fakeOllama{models: []string{"qwen3:4b-instruct"}}
		require.NoError(t, newClient(t, serveFake(t, f), "qwen3:4b-instruct").EnsureModel(context.Background(), quietLog()))
		assert.Zero(t, f.pullCount())
	})
	t.Run("missing: pulls it", func(t *testing.T) {
		f := &fakeOllama{}
		c := newClient(t, serveFake(t, f), "qwen3:4b-instruct")
		require.NoError(t, c.EnsureModel(context.Background(), quietLog()))
		assert.Equal(t, 1, f.pullCount())
		require.NoError(t, c.Ping(context.Background()), "pulled")
	})
	t.Run("pull fails: the error is returned", func(t *testing.T) {
		f := &fakeOllama{pullStatus: http.StatusInternalServerError}
		err := newClient(t, serveFake(t, f), "qwen3:4b-instruct").EnsureModel(context.Background(), quietLog())
		require.ErrorContains(t, err, "no space left on device")
		assert.Equal(t, 1, f.pullCount())
	})
	t.Run("unreachable: nothing to pull to", func(t *testing.T) {
		f := &fakeOllama{tagsStatus: http.StatusServiceUnavailable}
		err := newClient(t, serveFake(t, f), "qwen3:4b-instruct").EnsureModel(context.Background(), quietLog())
		require.ErrorIs(t, err, ErrUnreachable)
		assert.Zero(t, f.pullCount())
	})
}

// TestKeepPulled_WaitsForOllamaThenPulls: Ollama is down when the daemon
// starts and comes up later without the model. The helper keeps trying and
// pulls it, warning once.
func TestKeepPulled_WaitsForOllamaThenPulls(t *testing.T) {
	addr := closedAddr(t)
	c := newClient(t, "http://"+addr, "nomic-embed-text")
	fake := &fakeOllama{t: t}
	var retries []int
	c.pullBackoff = func(retry int) time.Duration {
		retries = append(retries, retry)
		if retry == 2 {
			// Ollama starts during the second wait, without the model.
			ln, err := net.Listen("tcp", addr)
			require.NoError(t, err)
			srv := httptest.NewUnstartedServer(fake)
			require.NoError(t, srv.Listener.Close())
			srv.Listener = ln
			srv.Start()
			t.Cleanup(srv.Close)
		}
		return time.Millisecond
	}
	rec := &recorder{}

	c.KeepPulled(context.Background(), slog.New(rec))

	assert.Equal(t, []int{1, 2}, retries)
	assert.Equal(t, 1, fake.pullCount())
	require.NoError(t, c.Ping(context.Background()))
	assert.Equal(t, []string{notReady}, rec.messages(slog.LevelWarn), "only the first failure warns")
	assert.Equal(t, []string{notReady, "pulling ollama model"}, rec.messages(slog.LevelDebug),
		"a retry's failure and pull are debug")
	assert.Equal(t, []string{"ollama model ready"}, rec.messages(slog.LevelInfo))
}

// TestKeepPulled_PullsAnUntaggedModelWhenAnotherTagIsPresent: an untagged
// name means :latest, so having another tag of the model is not having it,
// and KeepPulled pulls the name as configured.
func TestKeepPulled_PullsAnUntaggedModelWhenAnotherTagIsPresent(t *testing.T) {
	f := &fakeOllama{models: []string{"qwen3-embedding:0.6b"}}
	c := newClient(t, serveFake(t, f), "qwen3-embedding")

	c.KeepPulled(context.Background(), quietLog())

	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []string{"qwen3-embedding"}, f.pulled)
	assert.Contains(t, f.models, "qwen3-embedding:latest")
}

const notReady = "ollama model not ready; retrying in the background"

// TestKeepPulled_RetriedPullsAreQuiet: Ollama is up but every pull fails,
// as it does when Ollama can't reach its registry. Only the first attempt
// is announced at INFO and WARN, not one INFO line per retry.
func TestKeepPulled_RetriedPullsAreQuiet(t *testing.T) {
	fake := &fakeOllama{pullStatus: http.StatusInternalServerError}
	c := newClient(t, serveFake(t, fake), "m")
	c.pullBackoff = func(retry int) time.Duration {
		if retry == 3 {
			fake.mu.Lock()
			fake.pullStatus = 0 // the registry is reachable again
			fake.mu.Unlock()
		}
		return time.Millisecond
	}
	rec := &recorder{}

	c.KeepPulled(context.Background(), slog.New(rec))

	assert.Equal(t, 4, fake.pullCount())
	assert.Equal(t, []string{"pulling ollama model", "ollama model ready"}, rec.messages(slog.LevelInfo))
	assert.Equal(t, []string{notReady}, rec.messages(slog.LevelWarn))
	assert.Equal(t, []string{
		"pulling ollama model", notReady, // second attempt
		"pulling ollama model", notReady, // third attempt
		"pulling ollama model", // fourth attempt, which succeeds
	}, rec.messages(slog.LevelDebug))
}

// TestKeepPulled_CancelDuringBackoff: shutdown during the wait between
// attempts returns at once and adds nothing to the log.
func TestKeepPulled_CancelDuringBackoff(t *testing.T) {
	c := newClient(t, "http://"+closedAddr(t), "m")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.pullBackoff = func(int) time.Duration { return time.Hour }
	rec := &recorder{}
	done := make(chan struct{})
	go func() {
		c.KeepPulled(ctx, slog.New(rec))
		close(done)
	}()

	// The WARN is logged after the backoff is chosen and before the wait
	// starts, so it, not the backoff hook, marks the wait as begun.
	require.Eventually(t, func() bool { return len(rec.messages(slog.LevelWarn)) == 1 },
		5*time.Second, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("KeepPulled did not return after cancel")
	}
	assert.Equal(t, []string{notReady}, rec.messages(slog.LevelWarn), "no WARN for the cancelled wait")
	assert.Empty(t, rec.messages(slog.LevelDebug))
	assert.Empty(t, rec.messages(slog.LevelInfo))
}

// TestKeepPulled_ShutdownDuringPullIsNotAFailure: a pull cut short by
// shutdown is not a missing model, so nothing is logged about it.
func TestKeepPulled_ShutdownDuringPullIsNotAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[]}`)
		case "/api/pull":
			fmt.Fprintln(w, `{"status":"downloading","total":100,"completed":10}`)
			w.(http.Flusher).Flush()
			cancel()
			<-r.Context().Done()
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(t, srv.URL, "m")
	c.pullBackoff = func(int) time.Duration {
		t.Error("no retry after shutdown")
		return 0
	}
	rec := &recorder{}

	c.KeepPulled(ctx, slog.New(rec))

	assert.Empty(t, rec.messages(slog.LevelWarn))
	assert.Empty(t, rec.messages(slog.LevelDebug))
	assert.NotContains(t, rec.messages(slog.LevelInfo), "ollama model ready")
}

func TestPullBackoff(t *testing.T) {
	for retry, want := range map[int]time.Duration{
		1:   5 * time.Second,
		2:   10 * time.Second,
		3:   20 * time.Second,
		6:   160 * time.Second,
		7:   5 * time.Minute,
		100: 5 * time.Minute,
	} {
		assert.Equal(t, want, pullBackoff(retry), "retry %d", retry)
	}
}
