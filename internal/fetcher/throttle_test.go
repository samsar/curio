package fetcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

const jinaArticle = "Title: Via Jina\n\nMarkdown Content:\n"

func jinaArticleBody() string {
	return jinaArticle + strings.Repeat("Article text rendered by Jina. ", 20)
}

// gatedLimiter is a rateLimiter that holds every caller until the test
// lets it through, so a test can queue callers in the limiter and decide
// who goes when.
type gatedLimiter struct {
	queued chan struct{} // one receive per caller that started waiting
	tokens chan struct{}
}

func newGatedLimiter(maxQueued int) *gatedLimiter {
	return &gatedLimiter{queued: make(chan struct{}, maxQueued), tokens: make(chan struct{})}
}

func (l *gatedLimiter) Wait(ctx context.Context) error {
	select {
	case l.queued <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-l.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// grant lets one waiting caller through.
func (l *gatedLimiter) grant() { l.tokens <- struct{}{} }

// open lets every caller through from now on.
func (l *gatedLimiter) open() { close(l.tokens) }

// awaitQueued blocks until n more callers have started waiting.
func (l *gatedLimiter) awaitQueued(t *testing.T, n int) {
	t.Helper()
	hung := time.After(10 * time.Second)
	for range n {
		select {
		case <-l.queued:
		case <-hung:
			t.Fatalf("expected %d callers queued in the limiter", n)
		}
	}
}

// TestPace: the cooldown is checked once the limiter grants a token, a
// short one is slept through the clock and followed by a fresh token, a
// long one is reported without sleeping, and an expired one costs nothing.
func TestPace(t *testing.T) {
	const maxInline = time.Minute

	t.Run("no cooldown", func(t *testing.T) {
		fc := newFakeClock()
		lim := newGatedLimiter(4)
		lim.open()
		var c cooldown
		left, err := pace(t.Context(), lim, &c, fc.clock(), maxInline)
		require.NoError(t, err)
		assert.Zero(t, left)
		assert.Len(t, lim.queued, 1)
		assert.Empty(t, fc.slept())
	})

	t.Run("short cooldown is slept, then queued for again", func(t *testing.T) {
		fc := newFakeClock()
		lim := newGatedLimiter(4)
		lim.open()
		var c cooldown
		c.extend(fc.now(), 10*time.Second)
		c.extend(fc.now(), 5*time.Second) // never shortens
		left, err := pace(t.Context(), lim, &c, fc.clock(), maxInline)
		require.NoError(t, err)
		assert.Zero(t, left)
		assert.Equal(t, []time.Duration{10 * time.Second}, fc.slept())
		assert.Len(t, lim.queued, 2, "a caller that slept a cooldown out takes a fresh token")
	})

	t.Run("long cooldown is reported", func(t *testing.T) {
		fc := newFakeClock()
		lim := newGatedLimiter(4)
		lim.open()
		var c cooldown
		c.extend(fc.now(), 2*time.Minute)
		left, err := pace(t.Context(), lim, &c, fc.clock(), maxInline)
		require.NoError(t, err)
		assert.Equal(t, 2*time.Minute, left)
		assert.Empty(t, fc.slept(), "a cooldown over the inline cap is not slept")
	})

	t.Run("cooldown that starts while queued", func(t *testing.T) {
		fc := newFakeClock()
		lim := newGatedLimiter(4)
		var c cooldown
		type result struct {
			left time.Duration
			err  error
		}
		done := make(chan result, 1)
		go func() {
			left, err := pace(t.Context(), lim, &c, fc.clock(), maxInline)
			done <- result{left, err}
		}()
		lim.awaitQueued(t, 1)
		c.extend(fc.now(), 2*time.Minute) // another caller's rate-limit answer
		lim.grant()
		res := <-done
		require.NoError(t, res.err)
		assert.Equal(t, 2*time.Minute, res.left)
	})

	t.Run("context ends while queued", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var c cooldown
		_, err := pace(ctx, newGatedLimiter(4), &c, newFakeClock().clock(), maxInline)
		require.ErrorIs(t, err, context.Canceled)
	})
}

// TestHostGate: slots are per host, waits honor ctx, and entries go away
// once nobody holds or waits for them.
func TestHostGate(t *testing.T) {
	g := newHostGate(2)
	ctx := t.Context()

	r1, err := g.acquire(ctx, "a.example")
	require.NoError(t, err)
	r2, err := g.acquire(ctx, "a.example")
	require.NoError(t, err)

	other, err := g.acquire(ctx, "b.example")
	require.NoError(t, err, "another host is not blocked")
	other()

	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, err = g.acquire(short, "a.example")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	r1()
	r1() // idempotent
	r3, err := g.acquire(ctx, "a.example")
	require.NoError(t, err)
	r2()
	r3()

	g.mu.Lock()
	defer g.mu.Unlock()
	assert.Empty(t, g.hosts)
}

// TestNative_JinaLimiterIsShared: all Jina calls share one limiter. With
// one token an hour, two concurrent fetches make exactly one Jina request;
// the other gives up when its context runs out.
func TestNative_JinaLimiterIsShared(t *testing.T) {
	source := serveThinPage(t)
	defer source.Close()
	var jinaHits atomic.Int32
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaHits.Add(1)
		_, _ = w.Write([]byte(jinaArticleBody()))
	}))
	defer jina.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"})
	n.jinaLimiter = rate.NewLimiter(rate.Every(time.Hour), 1)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			_, errs[i] = n.Fetch(ctx, source.URL+"/"+strconv.Itoa(i))
		})
	}
	wg.Wait()

	assert.Equal(t, int32(1), jinaHits.Load())
	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
			var pe *PermanentError
			assert.False(t, errors.As(err, &pe), "our own pacing must not fail a doc permanently: %v", err)
		}
	}
	assert.Equal(t, 1, failed)
}

// TestNative_JinaRateLimitCooldown: a 429's Retry-After becomes a shared
// cooldown the next Jina call waits out, whichever fetch makes it.
func TestNative_JinaRateLimitCooldown(t *testing.T) {
	source := serveThinPage(t)
	defer source.Close()

	t.Run("same fetch", func(t *testing.T) {
		var jinaHits atomic.Int32
		jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if jinaHits.Add(1) == 1 {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte(jinaArticleBody()))
		}))
		defer jina.Close()

		fc := newFakeClock()
		n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"}), fc)
		res, err := n.Fetch(context.Background(), source.URL)
		require.NoError(t, err)
		assert.Equal(t, "jina", res.Meta["via"])
		assert.Equal(t, []time.Duration{3 * time.Second}, fc.slept())
	})

	t.Run("another fetch", func(t *testing.T) {
		jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(jinaArticleBody()))
		}))
		defer jina.Close()

		fc := newFakeClock()
		n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"}), fc)
		n.jinaCooldown.extend(fc.now(), 3*time.Second) // left by another worker's 429
		_, err := n.Fetch(context.Background(), source.URL)
		require.NoError(t, err)
		assert.Equal(t, []time.Duration{3 * time.Second}, fc.slept())
	})
}

// TestNative_JinaLongCooldownFailsFast: a Retry-After beyond the inline
// cap stops all Jina calls for the cooldown. Callers get a retryable 429
// with the time left, and no host verdict is cached.
func TestNative_JinaLongCooldownFailsFast(t *testing.T) {
	var jinaHits atomic.Int32
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaHits.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer jina.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer origin.Close()

	fc := newFakeClock()
	n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"}), fc)
	for _, path := range []string{"/a", "/b", "/c"} {
		_, err := n.Fetch(context.Background(), origin.URL+path)
		require.ErrorIs(t, err, ErrAntiBot)
		var pe *PermanentError
		assert.False(t, errors.As(err, &pe), "must stay retryable: %v", err)
		assert.NotContains(t, err.Error(), "(cached:")
		var se *HTTPStatusError
		require.ErrorAs(t, err, &se, "errors.As finds Jina's status, not the origin's 403")
		assert.Equal(t, http.StatusTooManyRequests, se.StatusCode)
		assert.Equal(t, 120*time.Second, se.RetryAfter)
	}
	assert.Equal(t, int32(1), jinaHits.Load(), "no Jina requests during the cooldown")
	assert.Empty(t, fc.slept())
	_, cached := n.hostCache.Get(hostOf(origin.URL))
	assert.False(t, cached)
}

// TestNative_JinaServerErrorBackoff: 5xx answers keep the 2/4/8s backoff,
// four attempts in all.
func TestNative_JinaServerErrorBackoff(t *testing.T) {
	source := serveThinPage(t)
	defer source.Close()
	var jinaHits atomic.Int32
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaHits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer jina.Close()

	fc := newFakeClock()
	n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"}), fc)
	_, err := n.Fetch(context.Background(), source.URL)
	require.Error(t, err)
	assert.Equal(t, int32(jinaAttempts), jinaHits.Load())
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}, fc.slept())
}

// TestNative_JinaAPIKey: a configured key (or CURIO_JINA_API_KEY) goes out
// as a bearer token and nowhere else: not in logs, not in errors.
func TestNative_JinaAPIKey(t *testing.T) {
	const key = "jina_secret_key_123"
	cases := []struct {
		name     string
		optKey   string
		envKey   string
		wantAuth string
	}{
		{"no key", "", "", ""},
		{"configured key", key, "", "Bearer " + key},
		{"env fallback", "", key, "Bearer " + key},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CURIO_JINA_API_KEY", tc.envKey)
			source := serveThinPage(t)
			defer source.Close()
			var gotAuth atomic.Value
			jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth.Store(r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer jina.Close()

			var logs bytes.Buffer
			n := unpaced(NewNative(NativeOptions{
				Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/",
				JinaAPIKey: tc.optKey, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
			}), newFakeClock())
			_, err := n.Fetch(context.Background(), source.URL)
			require.Error(t, err)

			assert.Equal(t, tc.wantAuth, gotAuth.Load())
			assert.NotContains(t, err.Error(), key)
			assert.NotContains(t, logs.String(), key)
		})
	}
}

// TestNative_OriginConcurrencyPerHost: at most originRequestsPerHost
// requests are in flight to one host; other hosts aren't held up, a fetch
// waiting for a slot honors its context, and the gate ends empty.
func TestNative_OriginConcurrencyPerHost(t *testing.T) {
	var inFlight, peak atomic.Int32
	release := make(chan struct{})
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		<-release
		_, _ = w.Write([]byte(makeArticleHTML("Busy host", "")))
	}))
	defer busy.Close()
	calm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(makeArticleHTML("Calm host", "")))
	}))
	defer calm.Close()

	n := NewNative(NativeOptions{Timeout: 30 * time.Second})
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Go(func() { _, errs[i] = n.Fetch(context.Background(), busy.URL+"/"+strconv.Itoa(i)) })
	}
	require.Eventually(t, func() bool { return inFlight.Load() == originRequestsPerHost }, 5*time.Second, time.Millisecond)

	_, err := n.Fetch(context.Background(), localhostURL(t, calm.URL))
	require.NoError(t, err, "another host must not wait behind the busy one")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = n.Fetch(ctx, busy.URL+"/impatient")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)

	close(release)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(originRequestsPerHost), peak.Load())
	n.originSlots.mu.Lock()
	defer n.originSlots.mu.Unlock()
	assert.Empty(t, n.originSlots.hosts)
}

// TestNative_QueuedFetchesSeeFreshVerdict: fetches queued behind the ones
// that got a host cached as anti-bot fail from the cache when their turn
// comes, without contacting the origin.
func TestNative_QueuedFetchesSeeFreshVerdict(t *testing.T) {
	var hits, inFlight atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		inFlight.Add(1)
		<-release
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 30 * time.Second})
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Go(func() { _, errs[i] = n.Fetch(context.Background(), srv.URL+"/"+strconv.Itoa(i)) })
	}
	require.Eventually(t, func() bool { return inFlight.Load() == originRequestsPerHost }, 5*time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int32(originRequestsPerHost), hits.Load(), "queued fetches must not reach the origin")
	cached := 0
	for _, err := range errs {
		require.ErrorIs(t, err, ErrAntiBot)
		if strings.Contains(err.Error(), "(cached:") {
			cached++
		}
	}
	assert.Equal(t, len(errs)-originRequestsPerHost, cached)
}

// TestNative_OriginSlotFreeDuringJina: a fetch waiting on Jina doesn't hold
// its origin slot, on the thin-page path and the unreadable-PDF path, so a
// healthy page on the same host goes through meanwhile.
func TestNative_OriginSlotFreeDuringJina(t *testing.T) {
	pages := map[string]func(w http.ResponseWriter){
		"thin page": func(w http.ResponseWriter) { _, _ = w.Write([]byte(thinPage)) },
		"unreadable pdf": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4\nnot a parseable pdf body"))
		},
	}
	for name, page := range pages {
		t.Run(name, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthy" {
					_, _ = w.Write([]byte(makeArticleHTML("Healthy", "")))
					return
				}
				page(w)
			}))
			defer origin.Close()

			var inJina atomic.Int32
			release := make(chan struct{})
			jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				inJina.Add(1)
				<-release
				_, _ = w.Write([]byte(jinaArticleBody()))
			}))
			defer jina.Close()

			n := unpaced(NewNative(NativeOptions{Timeout: 30 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"}), newFakeClock())
			var wg sync.WaitGroup
			for i := range originRequestsPerHost {
				wg.Go(func() {
					_, err := n.Fetch(context.Background(), origin.URL+"/needs-jina-"+strconv.Itoa(i))
					assert.NoError(t, err)
				})
			}
			require.Eventually(t, func() bool { return inJina.Load() == originRequestsPerHost }, 5*time.Second, time.Millisecond)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := n.Fetch(ctx, origin.URL+"/healthy")
			assert.NoError(t, err, "the origin slot must be free while Jina works")

			close(release)
			wg.Wait()
		})
	}
}
