package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/textutil"
)

func newSearchCmd(env *daemonctl.Env) *cobra.Command {
	var (
		k       int
		filters client.SearchFilters
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Hybrid BM25 + vector search across indexed bookmarks",
		Long: "Search the library by its text and by what each page was saved as.\n\n" +
			"Filters narrow the documents searched; every filter given applies, and the values of a\n" +
			"repeated one are alternatives. Dates are a day (2024-03-01, midnight UTC) or an RFC 3339\n" +
			"date-time with its zone (2024-03-01T00:00:00+01:00 for a local day); a window is\n" +
			"[after, before). A saved date counts only when it is known: a browser's bulk import\n" +
			"stamp, or a bookmark whose source kept no date, is undated, and so is a published date\n" +
			"more than a day after the page was first saved. Undated documents are left out of a\n" +
			"date-filtered search unless --include-undated, and the search says how many there are.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			req := client.SearchRequest{Query: strings.Join(args, " "), K: k}
			if !filters.IsEmpty() {
				req.Filters = &filters
			}
			res, err := env.Client.Search(cmd.Context(), req)
			if err != nil {
				return err
			}
			for _, w := range res.Warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
			}
			renderSearchResults(cmd.OutOrStdout(), res)
			renderUndated(cmd.OutOrStdout(), res.Undated, filters.IncludeUndated)
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVarP(&k, "k", "k", 0, "Number of results to return, 1-100 (default: the daemon's search.default_k)")
	f.StringSliceVar(&filters.ContentType, "type", nil,
		"Filter by content type (article|repo|video|pdf|thread|unknown); repeatable")
	f.StringSliceVar(&filters.Source, "source", nil,
		"Filter by bookmark source (chrome|safari|firefox|html|manual); repeatable")
	f.StringSliceVar(&filters.Host, "host", nil, "Filter by URL host, exactly, e.g. github.com; repeatable")
	f.StringSliceVar(&filters.Site, "site", nil,
		"Filter by site, a domain and every subdomain of it, e.g. example.com; repeatable")
	// A folder's name may hold a comma, which a slice flag would split on.
	f.StringArrayVar(&filters.Folder, "folder", nil,
		`Filter by bookmark folder and its subfolders, as stored, e.g. "/Bookmarks Bar/Tech"; repeatable`)
	f.StringVar(&filters.SavedAfter, "saved-after", "", "Saved on or after this date or date-time")
	f.StringVar(&filters.SavedBefore, "saved-before", "", "Saved before this date or date-time")
	f.StringVar(&filters.PublishedAfter, "published-after", "", "Published on or after this date or date-time")
	f.StringVar(&filters.PublishedBefore, "published-before", "", "Published before this date or date-time")
	f.BoolVar(&filters.IncludeUndated, "include-undated", false,
		"Keep the documents a date filter can't judge, which it leaves out by default")
	return cmd
}

// renderUndated says, for a date-filtered search, how many documents the
// date filters couldn't judge, and what the search did with them. A search
// without a date filter has nothing to say.
func renderUndated(w io.Writer, undated *int, kept bool) {
	if undated == nil {
		return
	}
	n := *undated
	documents := "documents have"
	if n == 1 {
		documents = "document has"
	}
	if kept {
		fmt.Fprintf(w, "%d %s no date the date filters can judge; kept (--include-undated)\n", n, documents)
		return
	}
	fmt.Fprintf(w, "%d %s no date the date filters can judge; left out (--include-undated keeps them)\n", n,
		documents)
}

func renderSearchResults(w io.Writer, res *client.SearchResponse) {
	if len(res.Items) == 0 {
		fmt.Fprintf(w, "no results for %q\n", res.Query)
		return
	}
	fmt.Fprintf(w, "%d results for %q  (BM25: %d, vector: %d)\n\n",
		len(res.Items), res.Query, res.BM25Hits, res.VectorHits)
	for i, hit := range res.Items {
		title := hit.Document.URL
		switch {
		case hit.Document.Title != nil && *hit.Document.Title != "":
			title = *hit.Document.Title
		case hit.BookmarkTitle != "":
			title = hit.BookmarkTitle
		}
		fmt.Fprintf(w, "%2d. %s\n", i+1, title)
		fmt.Fprintf(w, "    %s   (score %.4f)\n", hit.Document.URL, hit.Score)
		// Doc ID and on-disk path for direct follow-up: open the file,
		// run `curio docs show <id>`, or `curio refetch <id>` without
		// going hunting.
		fmt.Fprintf(w, "    doc_id: %s\n", hit.Document.ID)
		if hit.MarkdownPath != "" {
			fmt.Fprintf(w, "    path:   %s\n", hit.MarkdownPath)
		}
		if len(hit.Matches) > 0 {
			m := hit.Matches[0]
			snippet := m.Snippet
			if snippet == "" {
				snippet = m.Text
			}
			// Wrap on word boundaries so long matches aren't cut off
			// at 180 chars. 100 cols keeps it readable in narrow
			// terminals; the `--content` flag on `curio docs show`
			// is the right tool for the full body.
			for _, line := range wrapLines(snippet, 100) {
				fmt.Fprintf(w, "    %s\n", line)
			}
		} else {
			renderSavedAs(w, hit)
		}
		fmt.Fprintln(w)
	}
}

// renderSavedAs says what a hit without a matching passage matched on:
// the bookmark it was saved as, and for a failed or dead document, why it
// has no page to read.
func renderSavedAs(w io.Writer, hit client.SearchHit) {
	if saved := savedAs(hit.Bookmarks); saved != "" {
		for _, line := range wrapLines("saved as: "+saved, 100) {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	if why := docmeta.Unavailable(hit.Document.State); why != "" {
		fmt.Fprintf(w, "    %s (%s); its bookmark matched\n", why, hit.Document.State)
	}
}

// savedAs is what the bookmark docmeta.SavedAs picks says: its title and
// its folder, the parts it has, joined by " · ".
func savedAs(bookmarks []client.SearchHitBookmark) string {
	saved, _ := docmeta.SavedAs(docmetaBookmarks(bookmarks))
	var parts []string
	for _, p := range []string{saved.Title, saved.Folder} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// docmetaBookmarks are bookmarks as docmeta reads them.
func docmetaBookmarks(bookmarks []client.SearchHitBookmark) []docmeta.Bookmark {
	out := make([]docmeta.Bookmark, len(bookmarks))
	for i, b := range bookmarks {
		out[i] = docmeta.Bookmark{Title: b.Title, FolderPath: b.FolderPath}
	}
	return out
}

// truncate caps s at n runes, marking a cut with "...". Widths count runes,
// not terminal cells: wide CJK glyphs still render wider than n columns.
func truncate(s string, n int) string {
	t := textutil.TruncateRunes(s, n)
	if len(t) == len(s) {
		return s
	}
	return t + "..."
}
