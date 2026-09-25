package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
)

// pendingDaemon serves a document that stays pending, calling onGet after
// each answer.
func pendingDaemon(t *testing.T, onGet func()) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"doc","url":"https://example.com/","state":"pending"}`)
		onGet()
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL)
}

// TestWaitForFetch_Cancelled: ctrl-c while the document is still pending
// ends the wait at once with the cancellation.
func TestWaitForFetch_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := pendingDaemon(t, cancel)

	start := time.Now()
	err := waitForFetch(ctx, c, "doc", time.Minute)
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 10*time.Second)
}

func TestWaitForFetch_TimesOut(t *testing.T) {
	c := pendingDaemon(t, func() {})
	err := waitForFetch(context.Background(), c, "doc", 50*time.Millisecond)
	require.EqualError(t, err, "timed out after 50ms waiting for the fetch")
}
