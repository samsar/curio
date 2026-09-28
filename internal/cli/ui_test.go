package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/daemonctl"
)

// recordingOpener records the URLs curio ui opens, failing with err.
type recordingOpener struct {
	urls []string
	err  error
}

func (o *recordingOpener) open(_ context.Context, url string) error {
	o.urls = append(o.urls, url)
	return o.err
}

// runUI runs curio ui with args against srv at daemonURL, opening URLs
// with opener.
func runUI(t *testing.T, srv *apitest.Server, daemonURL string, opener *recordingOpener, args ...string) (string, error) {
	t.Helper()
	d := testDeps(t)
	if opener != nil {
		d.openURL = opener.open
	}
	var stdout, stderr bytes.Buffer
	err := runCLIStreams(newRootCmdWith(d), srv.Home.Path, daemonURL, &stdout, &stderr, append([]string{"ui"}, args...)...)
	return stdout.String(), err
}

func TestUI_Print(t *testing.T) {
	srv := apitest.Start(t)
	for _, daemonURL := range []string{srv.URL, srv.URL + "/"} {
		out, err := runUI(t, srv, daemonURL, nil, "--print")
		require.NoError(t, err, daemonURL)
		assert.Equal(t, srv.URL+"/ui/\n", out, daemonURL)
	}
}

func TestUI_Opens(t *testing.T) {
	srv := apitest.Start(t)
	opener := &recordingOpener{}
	out, err := runUI(t, srv, srv.URL, opener)
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Equal(t, []string{srv.URL + "/ui/"}, opener.urls)

	opener = &recordingOpener{err: errors.New("exit status 1: no browser")}
	_, err = runUI(t, srv, srv.URL, opener)
	require.Error(t, err)
	assert.Contains(t, err.Error(), srv.URL+"/ui/", "the URL, to open by hand")
	assert.Contains(t, err.Error(), "no browser")
}

// TestUI_WhileStarting: a daemon still starting serves the page that shows
// its progress, so curio ui opens it rather than waiting for the daemon to
// be ready.
func TestUI_WhileStarting(t *testing.T) {
	starting := apitest.StartNotReady(t)
	// The daemon's lock, which a starting daemon holds, and whose PID its
	// healthz names: this process's.
	lock, err := daemonctl.AcquireLock(starting.Home)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })
	opener := &recordingOpener{}
	_, err = runUI(t, starting, starting.URL, opener)
	require.NoError(t, err)
	assert.Equal(t, []string{starting.URL + "/ui/"}, opener.urls)
}

// TestUI_Off: with daemon.ui false, curio ui says so, naming the setting
// and the file, and opens nothing.
func TestUI_Off(t *testing.T) {
	srv := apitest.Start(t)
	require.NoError(t, os.WriteFile(srv.Home.ConfigPath(), []byte("daemon:\n  ui: false\n"), 0o600))
	opener := &recordingOpener{}
	for _, args := range [][]string{nil, {"--print"}} {
		out, err := runUI(t, srv, srv.URL, opener, args...)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "daemon.ui")
		assert.Contains(t, err.Error(), srv.Home.ConfigPath())
		assert.Empty(t, out)
	}
	assert.Empty(t, opener.urls)
}
