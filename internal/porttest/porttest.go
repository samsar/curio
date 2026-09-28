// Package porttest finds loopback addresses for tests whose daemon binds a
// port it is told, later, in another process.
package porttest

import (
	"context"
	"math/rand/v2"
	"net"
	"strconv"
	"testing"
)

// The ports FreeAddr picks from: below the ephemeral ranges Linux
// (32768-60999) and macOS (49152-65535) hand out for a port-0 listen and
// for outgoing connections.
const (
	lowPort  = 20000
	highPort = 32768 // exclusive
)

// FreeAddr returns a 127.0.0.1 address whose port nothing listens on, for
// a daemon the test starts later to bind. A port-0 listen's port, closed
// and handed on, can go to another test's listener or connection before
// the daemon binds it (CI saw "address already in use"); the kernel never
// hands out one of these on its own, so only another FreeAddr could take
// it, one pick in 12,768.
func FreeAddr(t testing.TB) string {
	t.Helper()
	for range 100 {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(lowPort+rand.IntN(highPort-lowPort))) //nolint:gosec // a port, not a secret
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
		if err != nil {
			continue // taken
		}
		if err := ln.Close(); err != nil {
			t.Fatalf("porttest: close %s: %v", addr, err)
		}
		return addr
	}
	t.Fatalf("porttest: no free port in [%d, %d) after 100 picks", lowPort, highPort)
	return ""
}
