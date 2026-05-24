package cli

import (
	"errors"
	"fmt"
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
		state      string
		limit      int
	)
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "List indexed documents (filter with --failed, --state, --limit)",
		Long: `Show recent documents in the corpus. Each row carries the most recent
error from a failed job that targeted it, so 'curio docs --failed' is
the one-stop debug view for stuck content.

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
			s := state
			if failedOnly {
				s = "failed"
			}

			resp, err := ctx.Client.ListDocuments(cmd.Context(), client.ListDocumentsOpts{
				State: s, Limit: limit,
			})
			if err != nil {
				return err
			}
			renderDocList(resp)
			return nil
		},
	}
	cmd.Flags().BoolVar(&failedOnly, "failed", false, "Shortcut for --state=failed")
	cmd.Flags().StringVar(&state, "state", "", "pending|fetched|failed|dead")
	cmd.Flags().IntVar(&limit, "limit", 50, "Max rows (server caps at 500)")
	return cmd
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
			renderDocShow(doc, ctx.Home.ContentDir())

			if showContent {
				body, err := ctx.Client.GetDocumentContent(cmd.Context(), id)
				if err != nil {
					return err
				}
				fmt.Println("\n--- content ---")
				fmt.Println(body)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&showContent, "content", false, "Stream the extracted markdown after the metadata")
	return cmd
}

func renderDocShow(d *client.Document, contentDir string) {
	fmt.Printf("id:           %s\n", d.ID)
	fmt.Printf("url:          %s\n", d.URL)
	if d.Title != nil && *d.Title != "" {
		fmt.Printf("title:        %s\n", *d.Title)
	}
	if d.Author != nil && *d.Author != "" {
		fmt.Printf("author:       %s\n", *d.Author)
	}
	fmt.Printf("content_type: %s\n", d.ContentType)
	fmt.Printf("state:        %s\n", d.State)
	fmt.Printf("created_at:   %s\n", d.CreatedAt.Local().Format("2006-01-02 15:04:05 MST"))
	if e := d.CurrentExtraction; e != nil {
		fmt.Printf("\nlatest extraction:\n")
		fmt.Printf("  id:           %s\n", e.ID)
		fmt.Printf("  fetcher:      %s\n", e.Fetcher)
		fmt.Printf("  status:       %s\n", e.Status)
		fmt.Printf("  fetched_at:   %s\n", e.FetchedAt.Local().Format("2006-01-02 15:04:05 MST"))
		if e.MarkdownPath != "" {
			fmt.Printf("  markdown:     %s/%s\n", contentDir, e.MarkdownPath)
		}
		if e.ErrorMessage != nil && *e.ErrorMessage != "" {
			fmt.Printf("  err:          %s\n", *e.ErrorMessage)
		}
	}
}

func renderDocList(resp *client.DocumentList) {
	if len(resp.Items) == 0 {
		fmt.Println("no documents match")
		return
	}
	// Two columns: STATE + URL on one line; LAST_ERR indented below.
	for _, d := range resp.Items {
		title := d.URL
		if d.Title != nil && *d.Title != "" {
			title = *d.Title
		}
		fmt.Printf("%-8s %s\n", d.State, d.URL)
		if title != d.URL {
			fmt.Printf("         (%s)\n", truncate(title, 100))
		}
		if d.LastError != "" {
			fmt.Printf("         err: %s\n", truncate(strings.TrimSpace(d.LastError), 200))
		}
		fmt.Printf("         id: %s\n\n", d.ID)
	}
	fmt.Printf("%d document(s)\n", len(resp.Items))
}
