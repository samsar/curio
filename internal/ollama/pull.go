package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	// maxPullLine bounds one line of /api/pull's stream. A progress line
	// is a couple of hundred bytes; a longer one is not Ollama's.
	maxPullLine = 64 << 10
	// defaultPullIdle is how long a pull may go without a line before it
	// fails. Ollama reports progress several times a second while it
	// downloads; minutes of silence is a server or a connection that hung,
	// and a pull left waiting on it would never be retried.
	defaultPullIdle = 5 * time.Minute
)

// errPullStalled is the cause a pull's context is cancelled with when its
// stream goes quiet for pullIdle.
var errPullStalled = errors.New("no progress from Ollama")

type pullRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

// pullLine is one line of /api/pull's stream.
type pullLine struct {
	Status    string `json:"status"`
	Error     string `json:"error"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
}

// PullProgress is one line of a pull's progress, as Ollama streams it:
// what it is doing ("pulling manifest", "verifying sha256 digest") and,
// while it downloads a layer, the layer's digest, its size and how much of
// it has arrived. A model is several layers, each counted on its own.
type PullProgress struct {
	Status    string
	Digest    string
	Total     int64
	Completed int64
}

// Pull streams POST /api/pull and blocks until Ollama reports the model
// downloaded with a "success" line, passing each line before it to
// progress (nil takes none). A pull can take minutes, so it ignores the
// client's per-request timeout: it is bounded by ctx, and fails, naming
// the model, when the stream goes quiet for pullIdle or sends a line over
// maxPullLine.
func (c *Client) Pull(ctx context.Context, progress func(PullProgress)) error {
	body, err := json.Marshal(pullRequest{Model: c.model, Stream: true})
	if err != nil {
		return fmt.Errorf("ollama pull %s: encode request: %w", c.model, err)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	idle := time.AfterFunc(c.pullIdle, func() { cancel(errPullStalled) })
	defer idle.Stop()

	untimed := &http.Client{Transport: c.http.Transport}
	resp, err := c.sendWith(ctx, untimed, http.MethodPost, "/api/pull", bytes.NewReader(body))
	if err != nil {
		return c.pullError(ctx, err)
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return fmt.Errorf("ollama pull %s: %w", c.model, statusError(resp))
	}

	lines := bufio.NewScanner(resp.Body)
	lines.Buffer(make([]byte, 0, 4<<10), maxPullLine)
	for lines.Scan() {
		idle.Reset(c.pullIdle)
		raw := bytes.TrimSpace(lines.Bytes())
		if len(raw) == 0 {
			continue
		}
		var line pullLine
		if err := json.Unmarshal(raw, &line); err != nil {
			return fmt.Errorf("ollama pull %s: decode stream: %w", c.model, err)
		}
		if line.Error != "" {
			return fmt.Errorf("ollama pull %s: %s", c.model, line.Error)
		}
		if progress != nil {
			progress(PullProgress{Status: line.Status, Digest: line.Digest, Total: line.Total, Completed: line.Completed})
		}
		if line.Status == "success" {
			return nil
		}
	}
	if err := lines.Err(); err != nil {
		return c.pullError(ctx, err)
	}
	// Only a "success" line means the model is there; a stream that just
	// stops is a dropped connection or a crashed server.
	return fmt.Errorf("ollama pull %s: stream ended before success: %w", c.model, io.ErrUnexpectedEOF)
}

// pullError describes a pull that failed on its way: stalled, a line too
// long, or the transport's error.
func (c *Client) pullError(ctx context.Context, err error) error {
	switch {
	case errors.Is(context.Cause(ctx), errPullStalled):
		return fmt.Errorf("ollama pull %s: %w for %s", c.model, errPullStalled, c.pullIdle)
	case errors.Is(err, bufio.ErrTooLong):
		return fmt.Errorf("ollama pull %s: a line of the stream exceeds %d bytes: %w", c.model, maxPullLine, err)
	default:
		return fmt.Errorf("ollama pull %s: %w", c.model, err)
	}
}

// logPullProgress is KeepPulled's progress callback: it logs the pull of
// model about every 10%, as each line reports its own layer's share.
func logPullProgress(ctx context.Context, log *slog.Logger, model string) func(PullProgress) {
	lastPct := -1
	return func(p PullProgress) {
		if p.Total <= 0 {
			return
		}
		if pct := int(p.Completed * 100 / p.Total); pct >= lastPct+10 {
			log.InfoContext(ctx, "pulling ollama model", "model", model, "percent", pct)
			lastPct = pct
		}
	}
}
