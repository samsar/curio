package fetcher

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryableStatus(t *testing.T) {
	retryable := []int{408, 421, 425, 429, 500, 502, 503, 504, 520, 599}
	final := []int{400, 401, 402, 403, 404, 405, 410, 422, 451, 501, 505, 600, 999}
	for _, code := range retryable {
		assert.True(t, retryableStatus(code), "%d should be retryable", code)
	}
	for _, code := range final {
		assert.False(t, retryableStatus(code), "%d should be final", code)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"absent", "", 0, false},
		{"delta seconds", "120", 120 * time.Second, true},
		{"zero", "0", 0, true},
		{"http date", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{"date in the past", now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{"negative", "-5", 0, false},
		{"garbage", "soon", 0, false},
		{"huge is clamped", strconv.FormatInt(1<<62, 10), maxRetryAfter, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.value != "" {
				h.Set("Retry-After", tc.value)
			}
			got, ok := parseRetryAfter(h, now)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSnippet(t *testing.T) {
	assert.Equal(t, "short body", snippet([]byte("  short body\n")))

	long := strings.Repeat("é", maxErrorBody) // 2 bytes per rune
	got := snippet([]byte(long))
	assert.True(t, utf8.ValidString(got), "must cut on a rune boundary")
	assert.LessOrEqual(t, len(strings.TrimSuffix(got, "…")), maxErrorBody)
}

// TestHeldBack: a call a cooldown holds is deferred until the cooldown
// ends. The deferral reads as, and unwraps to, the error it carries, and is
// no PermanentError, however it is wrapped.
func TestHeldBack(t *testing.T) {
	fc := newFakeClock()
	var c cooldown
	c.extend(fc.now(), time.Hour)
	se := &HTTPStatusError{StatusCode: http.StatusTooManyRequests, RetryAfter: time.Hour}
	cause := fmt.Errorf("upstream: not sent: %w", se)

	de := heldBack(&c, "the upstream to answer again", cause)
	assert.Equal(t, fc.now().Add(time.Hour), de.Until)
	assert.Equal(t, "the upstream to answer again", de.Reason)
	assert.Equal(t, cause.Error(), de.Error())
	assert.Same(t, cause, errors.Unwrap(de))

	wrapped := fmt.Errorf("fetch failed: %w", de)
	got, ok := errors.AsType[*DeferError](wrapped)
	require.True(t, ok)
	assert.Same(t, de, got)
	assert.ErrorIs(t, wrapped, cause)
	gotStatus, ok := errors.AsType[*HTTPStatusError](wrapped)
	require.True(t, ok, "the 429 stays in the chain")
	assert.Same(t, se, gotStatus)
	_, permanent := errors.AsType[*PermanentError](wrapped)
	assert.False(t, permanent)
}

// requireDeferred checks that err is a deferral until until, naming what it
// waits for, and no PermanentError, and returns it.
func requireDeferred(t *testing.T, err error, until time.Time) *DeferError {
	t.Helper()
	de, ok := errors.AsType[*DeferError](err)
	require.True(t, ok, "a deferral: %v", err)
	assert.Equal(t, until, de.Until)
	assert.NotEmpty(t, de.Reason)
	_, permanent := errors.AsType[*PermanentError](err)
	assert.False(t, permanent, "a deferral is never permanent: %v", err)
	return de
}

// assertNotDeferred checks that err, which a request that was sent ended
// in, is no deferral.
func assertNotDeferred(t *testing.T, err error) {
	t.Helper()
	_, deferred := errors.AsType[*DeferError](err)
	assert.False(t, deferred, "an answer is never deferred: %v", err)
}
