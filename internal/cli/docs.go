package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
)

func newDocsCmd() *cobra.Command {
	cmd := newDocsListCmd()
	cmd.AddCommand(newDocsShowCmd())
	return cmd
}

func newDocsListCmd() *cobra.Command {
	var (
		failedOnly bool
		showAll    bool
		state      string
		limit      int
	)
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "List indexed documents (defaults to successfully fetched; --failed / --all to widen)",
		Long: `Show documents in the corpus. By default lists only state=fetched —
the "happy path" view, what's actually searchable. Add --failed to
debug stuck content, --all to see every state, or --state for
exact filtering.

Each row carries the most recent error from a failed job that
targeted it AND the on-disk markdown path (when present), so most
follow-ups (cat the file, run curio refetch, etc.) don't need
another lookup.

Cross-reference: 'curio jobs --failed' shows the underlying job rows
with full error messages and attempt counts.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}
			s := resolveDocsState(state, failedOnly, showAll)

			resp, err := ctx.Client.ListDocuments(cmd.Context(), client.ListDocumentsOpts{
				State: s, Limit: limit,
			})
			if err != nil {
				return err
			}
			renderDocList(cmd.OutOrStdout(), resp)
			return nil
		},
	}
	cmd.Flags().BoolVar(&failedOnly, "failed", false, "Shortcut for --state=failed")
	cmd.Flags().BoolVar(&showAll, "all", false, "Show every state instead of just fetched")
	cmd.Flags().StringVar(&state, "state", "", "pending|fetched|failed|dead (overrides defaults)")
	cmd.Flags().IntVar(&limit, "limit", 50, "Max rows (server caps at 500)")
	return cmd
}

// resolveDocsState resolves the three flags into a single state filter.
// Precedence: --state > --failed > --all > default(fetched).
func resolveDocsState(state string, failedOnly, all bool) string {
	switch {
	case state != "":
		return state
	case failedOnly:
		return "failed"
	case all:
		return ""
	default:
		return "fetched"
	}
}

func newDocsShowCmd() *cobra.Command {
	var showContent bool
	cmd := &cobra.Command{
		Use:   "show <document-id>",
		Short: "Show metadata + (optionally) content for one document",
		Long: `Print metadata for a single document by ID, including URL, title,
extraction info, and the on-disk markdown path so you can grep/edit
directly. Pass --content to also stream the markdown to stdout.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, ok := getCtx(cmd.Context())
			if !ok {
				return errors.New("no context")
			}
			if err := ensureDaemon(ctx); err != nil {
				return err
			}

			id := args[0]
			doc, err := ctx.Client.GetDocument(cmd.Context(), id)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			renderDocShow(w, doc)

			if showContent {
				body, err := ctx.Client.GetDocumentContent(cmd.Context(), id)
				if err != nil {
					return err
				}
				fmt.Fprintln(w, "\n--- content ---")
				fmt.Fprintln(w, body)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&showContent, "content", false, "Stream the extracted markdown after the metadata")
	return cmd
}

func renderDocShow(w io.Writer, d *client.Document) {
	fmt.Fprintf(w, "id:           %s\n", d.ID)
	fmt.Fprintf(w, "url:          %s\n", d.URL)
	if d.Title != nil && *d.Title != "" {
		fmt.Fprintf(w, "title:        %s\n", *d.Title)
	}
	if d.Author != nil && *d.Author != "" {
		fmt.Fprintf(w, "author:       %s\n", *d.Author)
	}
	fmt.Fprintf(w, "content_type: %s\n", d.ContentType)
	fmt.Fprintf(w, "state:        %s\n", d.State)
	fmt.Fprintf(w, "created_at:   %s\n", d.CreatedAt.Local().Format("2006-01-02 15:04:05 MST"))
	if e := d.CurrentExtraction; e != nil {
		fmt.Fprintf(w, "\nlatest extraction:\n")
		fmt.Fprintf(w, "  id:           %s\n", e.ID)
		fmt.Fprintf(w, "  fetcher:      %s\n", e.Fetcher)
		fmt.Fprintf(w, "  status:       %s\n", e.Status)
		fmt.Fprintf(w, "  fetched_at:   %s\n", e.FetchedAt.Local().Format("2006-01-02 15:04:05 MST"))
		if e.MarkdownPath != "" {
			fmt.Fprintf(w, "  markdown:     %s\n", e.MarkdownPath)
		}
		if e.ErrorMessage != nil && *e.ErrorMessage != "" {
			fmt.Fprintf(w, "  err:          %s\n", *e.ErrorMessage)
		}
	}
}

func renderDocList(w io.Writer, resp *client.DocumentList) {
	if len(resp.Items) == 0 {
		fmt.Fprintln(w, "no documents match")
		return
	}
	// Two columns: STATE + URL on one line; LAST_ERR indented below.
	for _, d := range resp.Items {
		title := d.URL
		if d.Title != nil && *d.Title != "" {
			title = *d.Title
		}
		fmt.Fprintf(w, "%-8s %s\n", d.State, d.URL)
		if title != d.URL {
			fmt.Fprintf(w, "         (%s)\n", truncate(title, 100))
		}
		if d.LastError != "" {
			fmt.Fprintf(w, "         err: %s\n", truncate(strings.TrimSpace(d.LastError), 200))
		}
		fmt.Fprintf(w, "         doc_id: %s\n", d.ID)
		if d.MarkdownPath != "" {
			fmt.Fprintf(w, "         path:   %s\n", d.MarkdownPath)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%d document(s)\n", len(resp.Items))
}
