package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// TestOnePage: a page is the first limit rows, followed by the cursor of
// the last when more came back; a limit below 1 is an error, not a slice
// out of range.
func TestOnePage(t *testing.T) {
	at := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	key := func(id string) store.PageKey { return store.PageKey{At: at, ID: id} }
	rows := []string{"a", "b", "c"}

	page, next, err := onePage(rows, 2, key)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, page)
	after, err := decodeCursor(next)
	require.NoError(t, err)
	assert.Equal(t, key("b"), after, "the next page starts after the last row shown")

	page, next, err = onePage(rows, 3, key)
	require.NoError(t, err)
	assert.Equal(t, rows, page)
	assert.Empty(t, next, "the last page")

	for _, limit := range []int{0, -1} {
		_, _, err := onePage(rows, limit, key)
		assert.Error(t, err, "limit %d", limit)
		_, _, err = onePage([]string{}, limit, key)
		assert.Error(t, err, "limit %d without rows", limit)
	}
}
