package cli

import (
	"os"
	"testing"

	"github.com/samsar/curio/internal/setup/setuptest"
)

// TestMain runs this test binary as the fake curio-daemon when a test's
// launchd agent or controller starts it as one (setuptest.DaemonVar), and
// otherwise runs the tests with no browser's bookmarks in sight.
func TestMain(m *testing.M) {
	if code, asked := setuptest.RunDaemonIfAsked(); asked {
		os.Exit(code)
	}
	os.Exit(setuptest.WithoutBrowsers(m.Run))
}
