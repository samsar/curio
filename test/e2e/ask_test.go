//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/cli"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/store"
)

// TestDaemon_AnswersAQuestion: curio ask, against the built daemon, waits
// for the writer's model to pass its fitness check, then streams an answer
// citing the page saved, and lists it with its doc_id; healthz says the
// model is fit.
func TestDaemon_AnswersAQuestion(t *testing.T) {
	ctx := context.Background()
	ollama, ollamaURL := serveOllama(t, digestA, "0.34.4")
	ollama.answers = true
	pages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, articleHTML())
	}))
	t.Cleanup(pages.Close)
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen, ollamaURL)
	cfg, err := os.ReadFile(home.ConfigPath())
	require.NoError(t, err)
	// Ask is off by default for the default model, which isn't measured
	// as the answer writer: the test turns it on, as an owner would.
	answering := strings.Replace(string(cfg), "generation:\n", "generation:\n  base_url: "+strconv.Quote(ollamaURL)+"\n", 1) +
		"ask:\n  enabled: true\n"
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(answering), 0o600))
	baseURL := "http://" + listen
	ctl := daemonctl.New(home, daemonBin, baseURL)
	t.Cleanup(func() {
		if st, err := ctl.Status(context.Background()); err == nil && st.State == daemonctl.Running && st.PID > 0 {
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	})
	require.NoError(t, ctl.EnsureRunning(ctx), logTail(home))
	c := client.New(baseURL)
	health, err := c.Healthz(ctx)
	require.NoError(t, err)
	assert.Contains(t, health.GenerationRoles, "answers")

	created, err := c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: pages.URL + "/zymurgy"})
	require.NoError(t, err)
	docID := *created.Bookmark.DocumentID
	require.Eventually(t, func() bool {
		d, err := c.GetDocument(ctx, docID)
		return err == nil && d.State == string(store.DocStateFetched)
	}, 30*time.Second, 50*time.Millisecond, logTail(home))

	t.Setenv("CURIO_HOME", home.Path)
	t.Setenv("CURIO_DAEMON_BIN", daemonBin)
	var out, stderr strings.Builder
	require.Zero(t, cli.Run(ctx, []string{"ask", "what is the chemistry of zymurgy"}, &out, &stderr),
		"%s\n%s", stderr.String(), logTail(home))
	assert.Contains(t, out.String(), "Your page says so [1].\n")
	assert.Contains(t, out.String(), "[1] An Introduction to Zymurgy (cited)\n")
	assert.Contains(t, out.String(), "doc_id: "+docID+"\n")
	assert.Contains(t, stderr.String(), "checking "+health.GenerationModel+" before its first answer")

	health, err = c.Healthz(ctx)
	require.NoError(t, err)
	require.NotNil(t, health.Asking)
	assert.Equal(t, "fit", health.Asking.Check)
	assert.Equal(t, "answered", health.Asking.LastStatus)
}
