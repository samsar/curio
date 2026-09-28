package porttest

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFreeAddr: the address is loopback, in the range, and bindable.
func TestFreeAddr(t *testing.T) {
	addr := FreeAddr(t)
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host)
	n, err := strconv.Atoi(port)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, lowPort)
	assert.Less(t, n, highPort)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	require.NoError(t, err)
	require.NoError(t, ln.Close())
}
