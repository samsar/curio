package fetcher

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
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
