package jobs

import (
	"testing"
	"time"
)

// neverWithin fails t when cond holds at any tick within d. Unlike
// assert.Never, which runs each check in a goroutine of its own and
// returns at its timer without waiting for one in flight, it checks in
// the test's own goroutine: a condition that reads the database can then
// never outlive the test into the cleanup that closes it, and may use
// require.
func neverWithin(t *testing.T, d, tick time.Duration, cond func() bool, msg string) {
	t.Helper()
	window := time.NewTimer(d)
	defer window.Stop()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-window.C:
			return
		case <-ticker.C:
			if cond() {
				t.Errorf("held within %s: %s", d, msg)
				return
			}
		}
	}
}
