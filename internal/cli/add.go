package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
)

func newAddCmd() *cobra.Command {
	var (
		folder  string
		tags    []string
		title   string
		wait    bool
		waitSec int
	)
	cmd := &cobra.Command{
		Use:   "add <url>",
		Short: "Add a URL to your bookmarks; the daemon fetches and indexes it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}

			res, err := ctx.Client.CreateBookmark(cmd.Context(), client.CreateBookmarkRequest{
				URL:        args[0],
				Title:      title,
				FolderPath: folder,
				Tags:       tags,
			})
			if err != nil {
				return err
			}

			fmt.Printf("added bookmark %s\n", res.Bookmark.ID)
			if res.JobID != "" {
				fmt.Printf("  fetch job: %s\n", res.JobID)
			}

			if wait {
				if res.Bookmark.DocumentID == nil {
					return errors.New("server returned no document id to wait on")
				}
				if err := waitForFetch(cmd.Context(), ctx, *res.Bookmark.DocumentID, time.Duration(waitSec)*time.Second); err != nil {
					return err
				}
				fmt.Println("fetched and indexed")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&folder, "folder", "", "Folder path (e.g. /Tech/AI)")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "Tags (repeatable)")
	cmd.Flags().StringVar(&title, "title", "", "Optional title override")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for the fetch + index to complete")
	cmd.Flags().IntVar(&waitSec, "wait-timeout", 60, "Seconds to wait when --wait is set")
	return cmd
}

// waitForFetch polls the document by ID until it reaches a terminal state.
// Polling the document directly (rather than scanning the bookmark list)
// keeps this O(1) and correct regardless of corpus size — an earlier version
// listed only the first 100 bookmarks and silently timed out on large
// corpora even after the fetch had succeeded.
func waitForFetch(ctx context.Context, c *Context, docID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		doc, err := c.Client.GetDocument(ctx, docID)
		if err != nil {
			return err
		}
		switch doc.State {
		case "fetched":
			return nil
		case "failed", "dead":
			return fmt.Errorf("document state: %s", doc.State)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s waiting for fetch", timeout)
}

func ensureDaemon(c *Context) error {
	if c.Controller == nil {
		// No $CURIO_HOME yet, so we can't manage the daemon process.
		// Try a direct healthz first; if daemon is up, no need to start.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := c.Client.Healthz(ctx); err == nil {
			return nil
		}
		return errors.New("daemon not running and no $CURIO_HOME available to start it")
	}
	return c.Controller.EnsureRunning()
}
