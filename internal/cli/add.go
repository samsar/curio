package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
)

func newAddCmd(env *daemonctl.Env) *cobra.Command {
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
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}

			res, err := env.Client.CreateBookmark(cmd.Context(), client.CreateBookmarkRequest{
				URL:        args[0],
				Title:      title,
				FolderPath: folder,
				Tags:       tags,
			})
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "added bookmark %s\n", res.Bookmark.ID)
			if res.JobID != "" {
				fmt.Fprintf(w, "  fetch job: %s\n", res.JobID)
			}

			if wait {
				if res.Bookmark.DocumentID == nil {
					return errors.New("server returned no document id to wait on")
				}
				if err := waitForFetch(cmd.Context(), env.Client, *res.Bookmark.DocumentID, time.Duration(waitSec)*time.Second); err != nil {
					return err
				}
				fmt.Fprintln(w, "fetched and indexed")
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

// fetchPollInterval is how often waitForFetch checks the document.
const fetchPollInterval = 500 * time.Millisecond

// waitForFetch polls the document until it is fetched (nil), fails (an
// error naming its state), timeout passes, or ctx is cancelled (ctx's
// error). Each check reads the one document, so it costs the same however
// large the corpus is.
func waitForFetch(ctx context.Context, c *client.Client, docID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout,
		fmt.Errorf("timed out after %s waiting for the fetch", timeout))
	defer cancel()
	tick := time.NewTicker(fetchPollInterval)
	defer tick.Stop()
	for {
		doc, err := c.GetDocument(ctx, docID)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err != nil {
			return err
		}
		switch doc.State {
		case "fetched":
			return nil
		case "failed", "dead":
			return fmt.Errorf("document state: %s", doc.State)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-tick.C:
		}
	}
}
