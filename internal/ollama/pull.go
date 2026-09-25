package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

type pullRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

// pullProgress is one line of /api/pull's stream.
type pullProgress struct {
	Status    string `json:"status"`
	Error     string `json:"error"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
}

// Pull streams POST /api/pull and blocks until Ollama reports the model
// downloaded with a "success" line. A pull can take minutes, so it ignores
// the client's per-request timeout and is bounded by ctx alone. Progress is
// logged about every 10%.
func (c *Client) Pull(ctx context.Context, log *slog.Logger) error {
	body, err := json.Marshal(pullRequest{Model: c.model, Stream: true})
	if err != nil {
		return fmt.Errorf("ollama pull %s: encode request: %w", c.model, err)
	}
	untimed := &http.Client{Transport: c.http.Transport}
	resp, err := c.sendWith(ctx, untimed, http.MethodPost, "/api/pull", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ollama pull %s: %w", c.model, err)
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return fmt.Errorf("ollama pull %s: %w", c.model, statusError(resp))
	}

	dec := json.NewDecoder(resp.Body)
	lastPct := -1
	for {
		var msg pullProgress
		switch err := dec.Decode(&msg); {
		case errors.Is(err, io.EOF):
			// Only a "success" line means the model is there; a stream that
			// just stops is a dropped connection or a crashed server.
			return fmt.Errorf("ollama pull %s: stream ended before success: %w", c.model, io.ErrUnexpectedEOF)
		case err != nil:
			return fmt.Errorf("ollama pull %s: decode stream: %w", c.model, err)
		}
		if msg.Error != "" {
			return fmt.Errorf("ollama pull %s: %s", c.model, msg.Error)
		}
		if msg.Total > 0 {
			if pct := int(msg.Completed * 100 / msg.Total); pct >= lastPct+10 {
				log.Info("pulling ollama model", "model", c.model, "percent", pct)
				lastPct = pct
			}
		}
		if msg.Status == "success" {
			return nil
		}
	}
}
