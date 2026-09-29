package api

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// TestOnePage: a page is the first limit rows, followed by the cursor of
// the last when more came back, in the order the page was walked; a limit
// below 1 is an error, not a slice out of range.
func TestOnePage(t *testing.T) {
	at := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	key := func(id string) store.PageKey { return store.PageKey{At: at, ID: id} }
	rows := []string{"a", "b", "c"}

	for _, order := range []string{"", "saved"} {
		page, next, err := onePage(rows, 2, order, key)
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b"}, page)
		after, err := decodeCursor(next, order)
		require.NoError(t, err)
		assert.Equal(t, key("b"), after, "the next page starts after the last row shown")
	}

	page, next, err := onePage(rows, 3, "", key)
	require.NoError(t, err)
	assert.Equal(t, rows, page)
	assert.Empty(t, next, "the last page")

	for _, limit := range []int{0, -1} {
		_, _, err := onePage(rows, limit, "", key)
		assert.Error(t, err, "limit %d", limit)
		_, _, err = onePage([]string{}, limit, "", key)
		assert.Error(t, err, "limit %d without rows", limit)
	}
}

// TestCursor_Order: a cursor records its list's order only when it isn't
// the default, so cursors issued before orders existed still decode, and
// a cursor pages only the order that issued it.
func TestCursor_Order(t *testing.T) {
	key := store.PageKey{At: time.Date(2026, 9, 29, 9, 0, 0, 123_000_000, time.UTC), ID: "b1"}
	plain, err := encodeCursor(key, "")
	require.NoError(t, err)
	assert.Equal(t, base64.RawURLEncoding.EncodeToString([]byte(`{"t":"2026-09-29T09:00:00.123Z","id":"b1"}`)), plain,
		"a default order's cursor is encoded as before")
	saved, err := encodeCursor(key, "saved")
	require.NoError(t, err)
	assert.Equal(t, base64.RawURLEncoding.EncodeToString([]byte(`{"t":"2026-09-29T09:00:00.123Z","id":"b1","o":"saved"}`)),
		saved)

	for _, tc := range []struct {
		cursor, order string
	}{{plain, ""}, {saved, "saved"}} {
		got, err := decodeCursor(tc.cursor, tc.order)
		require.NoError(t, err, tc.order)
		assert.Equal(t, key, got, "round-trips in order %q", tc.order)
	}
	for _, tc := range []struct {
		cursor, order string
	}{{plain, "saved"}, {saved, ""}, {saved, "created"}} {
		_, err := decodeCursor(tc.cursor, tc.order)
		assert.ErrorIs(t, err, errOtherOrder, "a cursor walks only its own order: %q", tc.order)
	}
}
