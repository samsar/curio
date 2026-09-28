package cli

import (
	"os"
	"testing"

	"github.com/samsar/curio/internal/setup/setuptest"
)

// TestMain runs this test binary as the fake curio-daemon when a test's
// launchd agent or controller starts it as one (setuptest.DaemonVar).
func TestMain(m *testing.M) {
	if code, asked := setuptest.RunDaemonIfAsked(); asked {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
