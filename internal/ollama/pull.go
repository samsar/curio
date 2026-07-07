// Package ollama holds small helpers shared by curio's two Ollama clients
// (internal/embedder and internal/generator), which otherwise stay independent.
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

// PullModel streams POST {baseURL}/api/pull and blocks until the model is
// downloaded. A model pull can take minutes, so it uses its own client without
// a short per-request timeout; cancellation rides on ctx. Progress is logged
// roughly every 10%.
func PullModel(ctx context.Context, baseURL, model string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	body, err := json.Marshal(map[string]any{"model": model, "stream": true})
	if err != nil {
		return fmt.Errorf("ollama pull: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ollama pull: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("ollama pull: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 2048)
		n, _ := resp.Body.Read(buf)
		return fmt.Errorf("ollama pull: HTTP %d: %s", resp.StatusCode, string(buf[:n]))
	}

	dec := json.NewDecoder(resp.Body)
	lastPct := -1
	for {
		var msg struct {
			Status    string `json:"status"`
			Error     string `json:"error"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
		}
		if derr := dec.Decode(&msg); derr != nil {
			if errors.Is(derr, io.EOF) {
				return nil // stream ended cleanly
			}
			return fmt.Errorf("ollama pull: decode stream: %w", derr)
		}
		if msg.Error != "" {
			return fmt.Errorf("ollama pull: %s", msg.Error)
		}
		if msg.Total > 0 {
			if pct := int(msg.Completed * 100 / msg.Total); pct >= lastPct+10 {
				log.Info("pulling model", "model", model, "percent", pct)
				lastPct = pct
			}
		}
		if msg.Status == "success" {
			return nil
		}
	}
}
