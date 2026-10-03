package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
)

func newInterestsCmd(env *daemonctl.Env) *cobra.Command {
	var (
		limit, offset     int
		children, members int
		flat              bool
	)
	cmd := &cobra.Command{
		Use:   "interests",
		Short: "Show the areas and interests curio found across your saved content",
		Long: "Outline the areas and interests curio found in your library, largest first: a\n" +
			"picture of what you read about. A library under about 1,000 documents gets one\n" +
			"level of interests. The daemon groups the library when it first starts, and\n" +
			"`curio interests rebuild` groups it again, taking in the documents fetched\n" +
			"since. IDs last across rebuilds: `curio interests show <id>`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			opts := client.ListInterestsOpts{Limit: limit, Offset: offset, Children: children, Members: members}
			if flat {
				opts.Level = client.LevelInterest
			}
			res, err := env.Client.ListInterests(cmd.Context(), opts)
			if err != nil {
				return err
			}
			renderOutline(cmd.OutOrStdout(), res, outlinePage{flat: flat, offset: offset}, env.Home.ConfigPath())
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "Areas to show (interests with --flat or in a library of one level)")
	cmd.Flags().IntVar(&offset, "offset", 0, "Areas (or interests) to skip, largest first")
	cmd.Flags().IntVar(&children, "children", 5, "Interests to show of each area")
	cmd.Flags().IntVar(&members, "members", 3, "Documents to show of each interest listed with its members")
	cmd.Flags().BoolVar(&flat, "flat", false, "List every interest, largest first, each with its area")
	cmd.AddCommand(newInterestsShowCmd(env), newInterestsUnsortedCmd(env), newInterestsChangesCmd(env),
		newInterestsRebuildCmd(env))
	return cmd
}

func newInterestsShowCmd(env *daemonctl.Env) *cobra.Command {
	var members, offset int
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show an area's interests, or an interest's documents",
		Long: "Show an area with every interest it holds, or an interest with a page of its\n" +
			"documents, most similar first, then its loose fits: documents close to it that\n" +
			"no interest grouped. A retired ID says what took its documents.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			in, err := env.Client.GetInterest(cmd.Context(), args[0], client.GetInterestOpts{Members: members, Offset: offset})
			if retired := client.RetiredOf(err); retired != nil {
				return retiredError(retired)
			}
			if err != nil {
				return err
			}
			renderInterest(cmd.OutOrStdout(), in, offset)
			return nil
		},
	}
	cmd.Flags().IntVar(&members, "members", 20, "Documents to show of an interest")
	cmd.Flags().IntVar(&offset, "offset", 0, "Documents to skip, most similar first")
	return cmd
}

func newInterestsUnsortedCmd(env *daemonctl.Env) *cobra.Command {
	var limit, offset int
	cmd := &cobra.Command{
		Use:   "unsorted",
		Short: "List the documents in no interest, nearest first",
		Long: "List the documents no interest holds, the one nearest an interest first, each\n" +
			"with the interest it is closest to.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			page, err := env.Client.UnsortedInterests(cmd.Context(), client.UnsortedOpts{Limit: limit, Offset: offset})
			if err != nil {
				return err
			}
			renderUnsorted(cmd.OutOrStdout(), page, offset)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Documents to show")
	cmd.Flags().IntVar(&offset, "offset", 0, "Documents to skip, nearest first")
	return cmd
}

func newInterestsChangesCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "changes",
		Short: "Show what the latest rebuild changed",
		Long: "Show what the latest rebuild of the interests did: the interests and areas it\n" +
			"split, merged, moved, dissolved and created.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			changes, err := env.Client.InterestChanges(cmd.Context())
			if err != nil {
				return err
			}
			renderChanges(cmd.OutOrStdout(), changes)
			return nil
		},
	}
}

func newInterestsRebuildCmd(env *daemonctl.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild the interests now",
		Long: "Queue a rebuild of the interests, which groups the documents fetched since the\n" +
			"last one. It starts from the current grouping and keeps the names and IDs of the\n" +
			"interests that carry over. With a rebuild already queued, it names that one\n" +
			"rather than queuing another.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			res, err := env.Client.RebuildInterests(cmd.Context())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "rebuild queued: job %s\n", res.JobID)
			fmt.Fprintf(w, "follow it with `curio jobs show %s`\n", res.JobID)
			return nil
		},
	}
}

// outlinePage is the part of the outline asked for: every interest
// (flat), and how many groups come before the page.
type outlinePage struct {
	flat   bool
	offset int
}

// renderOutline writes the outline of res: its header, then its areas,
// each with its largest interests, or its interests, each with its most
// similar members; with flat, every interest with its area.
func renderOutline(w io.Writer, res *client.InterestList, page outlinePage, configPath string) {
	if res.RunID == "" || res.Total == 0 {
		renderNoInterests(w, res, configPath)
		return
	}
	if res.Shape == "areas" {
		fmt.Fprintf(w, "%s, ", plural(res.NumAreas, "area"))
	}
	fmt.Fprintf(w, "%s across %s (%s, %d unsorted)\n", plural(res.NumInterests, "interest"),
		plural(res.NumDocuments, "document"), plural(res.NumLoose, "loose fit"), res.NumUnsorted)
	fmt.Fprintln(w, rebuildLine(res.ComputedAt, res.Rebuild))
	fmt.Fprintln(w)
	for _, in := range res.Items {
		if in.Level == client.LevelArea {
			renderAreaLine(w, in)
			continue
		}
		fmt.Fprintf(w, "%s — %s  %s\n", label(in), plural(in.Size, "doc"), in.ID)
		if page.flat && in.ParentID != "" {
			fmt.Fprintf(w, "    in %s  %s\n", cmp.Or(in.ParentLabel, "(unlabeled area)"), in.ParentID)
		}
		renderMembers(w, "    ", in.Members)
	}
	if next := page.offset + len(res.Items); next < res.Total && len(res.Items) > 0 {
		more := fmt.Sprintf("curio interests --offset %d", next)
		if page.flat {
			more += " --flat"
		}
		fmt.Fprintf(w, "+ %d more: %s\n", res.Total-next, more)
	}
	fmt.Fprintf(w, "\nUnsorted — %s: curio interests unsorted\n", plural(res.NumUnsorted, "doc"))
}

// renderAreaLine writes an area and the interests it lists.
func renderAreaLine(w io.Writer, area client.Interest) {
	fmt.Fprintf(w, "%s — %s, %s  %s\n", label(area), plural(area.Size, "doc"), plural(area.NumChildren, "interest"), area.ID)
	for _, c := range area.Children {
		fmt.Fprintf(w, "    %s  %d  %s\n", label(c), c.Size, c.ID)
	}
	if more := area.NumChildren - len(area.Children); more > 0 {
		fmt.Fprintf(w, "    + %d more: curio interests show %s\n", more, area.ID)
	}
}

// renderMembers writes documents, each with its doc_id and path, indented.
func renderMembers(w io.Writer, indent string, members []client.InterestMember) {
	for _, m := range members {
		fmt.Fprintf(w, "%s• %s\n", indent, docName(m.Title, m.BookmarkTitle, m.URL))
		fmt.Fprintf(w, "%s  doc_id: %s\n", indent, m.DocID)
		if m.MarkdownPath != "" {
			fmt.Fprintf(w, "%s  path:   %s\n", indent, m.MarkdownPath)
		}
	}
}

// renderInterest writes an area with its interests, or an interest with
// its page of members, then its loose fits; offset is where the page
// starts.
func renderInterest(w io.Writer, in *client.Interest, offset int) {
	if in.Level == client.LevelArea {
		fmt.Fprintf(w, "%s — area of %s, %s  %s\n", label(*in), plural(in.Size, "doc"),
			plural(in.NumChildren, "interest"), in.ID)
		renderSummary(w, in.Summary)
		fmt.Fprintln(w)
		for _, c := range in.Children {
			fmt.Fprintf(w, "    %s  %d  %s\n", label(c), c.Size, c.ID)
		}
		return
	}
	fmt.Fprintf(w, "%s — %s", label(*in), plural(in.Size, "doc"))
	if in.Loose > 0 {
		fmt.Fprintf(w, ", %s", plural(in.Loose, "loose fit"))
	}
	fmt.Fprintf(w, "  %s\n", in.ID)
	if in.ParentID != "" {
		fmt.Fprintf(w, "in %s  %s\n", cmp.Or(in.ParentLabel, "(unlabeled area)"), in.ParentID)
	}
	renderSummary(w, in.Summary)
	var members, loose []client.InterestMember
	for _, m := range in.Members {
		if m.Fit == "loose" {
			loose = append(loose, m)
		} else {
			members = append(members, m)
		}
	}
	if len(members) > 0 {
		fmt.Fprintln(w, "\nmembers:")
		renderMembers(w, "  ", members)
	}
	if len(loose) > 0 {
		fmt.Fprintln(w, "\nloose fits:")
		renderMembers(w, "  ", loose)
	}
	if next := offset + len(in.Members); next < in.Size+in.Loose && len(in.Members) > 0 {
		fmt.Fprintf(w, "\n+ %d more: curio interests show %s --offset %d\n", in.Size+in.Loose-next, in.ID, next)
	}
}

// renderSummary writes a summary wrapped, when there is one.
func renderSummary(w io.Writer, summary string) {
	for _, line := range wrapLines(summary, 96) {
		fmt.Fprintln(w, line)
	}
}

// renderUnsorted writes a page of the documents in no interest, each with
// the interest it is nearest.
func renderUnsorted(w io.Writer, page *client.UnsortedPage, offset int) {
	if page.RunID == "" {
		fmt.Fprintln(w, "no rebuild has finished yet, so no document is sorted or unsorted")
		return
	}
	if page.Total == 0 {
		fmt.Fprintln(w, "every document is in an interest or close to one")
		return
	}
	fmt.Fprintf(w, "%s in no interest, nearest first\n\n", plural(page.Total, "document"))
	for _, m := range page.Items {
		fmt.Fprintf(w, "• %s\n", docName(m.Title, m.BookmarkTitle, m.URL))
		fmt.Fprintf(w, "  doc_id: %s\n", m.DocID)
		if m.MarkdownPath != "" {
			fmt.Fprintf(w, "  path:   %s\n", m.MarkdownPath)
		}
		if m.NearestID != "" {
			fmt.Fprintf(w, "  nearest: %s (%s) %.2f\n", cmp.Or(m.NearestLabel, "(unlabeled)"), m.NearestID, m.Similarity)
		}
	}
	if next := offset + len(page.Items); next < page.Total && len(page.Items) > 0 {
		fmt.Fprintf(w, "\n+ %d more: curio interests unsorted --offset %d\n", page.Total-next, next)
	}
}

// renderChanges writes what the latest rebuild did, its events grouped by
// kind; a first grouping, where every group is new, is said in a line.
func renderChanges(w io.Writer, c *client.InterestChanges) {
	if c.RunID == "" {
		fmt.Fprintln(w, "no rebuild has finished yet")
		return
	}
	fmt.Fprintln(w, rebuildLine(c.ComputedAt, c.Rebuild))
	first := c.Rebuild != nil && c.Rebuild.Kept == 0
	for _, e := range c.Events {
		first = first && e.Event == "new"
	}
	switch {
	case len(c.Events) == 0:
		fmt.Fprintln(w, "no area or interest changed")
		return
	case first:
		fmt.Fprintln(w, "first grouping: every area and interest is new")
		return
	}
	kind := ""
	for _, e := range c.Events {
		if e.Event != kind {
			kind = e.Event
			fmt.Fprintf(w, "\n%s:\n", kind)
		}
		fmt.Fprintf(w, "  %s\n", eventLine(e))
	}
}

// eventLine is one event: the identities it names, with their IDs.
func eventLine(e client.InterestEvent) string {
	ref := func(r *client.InterestRef) string {
		return fmt.Sprintf("%s %s (%s)", e.Level, cmp.Or(r.Label, "(unlabeled)"), r.ID)
	}
	switch {
	case e.From != nil && e.To != nil && e.Area != nil:
		return fmt.Sprintf("%s → in %s (%s)", ref(e.From), cmp.Or(e.Area.Label, "(unlabeled area)"), e.Area.ID)
	case e.From != nil && e.To != nil:
		return fmt.Sprintf("%s → %s, %s shared", ref(e.From), ref(e.To), plural(e.Shared, "doc"))
	case e.From != nil:
		return ref(e.From)
	case e.To != nil:
		return ref(e.To)
	}
	return e.Event
}

// rebuildLine says when the interests were rebuilt, how, and what the
// rebuild changed.
func rebuildLine(at *time.Time, r *client.InterestRebuild) string {
	var b strings.Builder
	b.WriteString("rebuilt")
	if at != nil {
		b.WriteString(" " + at.Local().Format("2006-01-02 15:04"))
	}
	if r == nil {
		return b.String()
	}
	fmt.Fprintf(&b, " (%s, %s", r.Kind, r.Trigger)
	if r.ChangedDocuments > 0 {
		fmt.Fprintf(&b, ", %d changed: %d new, %d split, %d merged, %d dissolved",
			r.ChangedDocuments, r.Created, r.Split, r.Merged, r.Dissolved)
	}
	b.WriteString(")")
	return b.String()
}

// renderNoInterests explains a list without groups: no rebuild done yet,
// by where the next one stands, or a rebuild that grouped nothing.
func renderNoInterests(w io.Writer, res *client.InterestList, configPath string) {
	if res.RunID != "" {
		when := "the last rebuild"
		if res.ComputedAt != nil {
			when = "the rebuild of " + res.ComputedAt.Local().Format("2006-01-02 15:04")
		}
		if res.NumDocuments == 0 {
			fmt.Fprintf(w, "no interests: %s found no fetched, indexed documents\n", when)
			fmt.Fprintln(w, "run `curio interests rebuild` once `curio status` shows documents fetched")
			return
		}
		where := "they are all in Unsorted"
		if res.NumDocuments == 1 {
			where = "it is in Unsorted"
		}
		fmt.Fprintf(w, "no interests: %s grouped none of its %s; %s\n", when, plural(res.NumDocuments, "document"), where)
		fmt.Fprintln(w, "list them with `curio interests unsorted`")
		return
	}
	switch res.Next.State {
	case client.StateQueued, client.StateRebuilding:
		fmt.Fprintln(w, "your library is being grouped for the first time")
		fmt.Fprintln(w, "its interests show here once the rebuild finishes; follow it with `curio jobs --kind cluster --all`")
	case client.StateFailing:
		fmt.Fprintf(w, "the last rebuild failed: %s\n", res.Next.LastError)
		fmt.Fprintln(w, "`curio interests rebuild` tries again")
	case client.StateOff:
		fmt.Fprintf(w, "interests are turned off: set insight.enabled: true in %s and restart the daemon\n", configPath)
	default:
		fmt.Fprintln(w, "no interests yet: `curio interests rebuild` groups the library")
	}
}

// retiredError is the error `show` exits with for a retired ID: when it
// was retired, and the interests that took its documents, each with the
// command that shows it.
func retiredError(r *client.RetiredInterest) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s was retired by the rebuild of %s", r.Level, cmp.Or(r.Label, r.ID),
		r.RetiredAt.Local().Format("2006-01-02 15:04"))
	if len(r.Successors) == 0 {
		b.WriteString(": it dissolved, its documents going to other interests or to Unsorted " +
			"(`curio interests unsorted`)")
		return errors.New(b.String())
	}
	b.WriteString(": its documents went to")
	for _, s := range r.Successors {
		fmt.Fprintf(&b, "\n  %s (%s, %s shared): curio interests show %s", cmp.Or(s.Label, "(unlabeled)"), s.Event,
			plural(s.Shared, "doc"), s.ID)
	}
	return errors.New(b.String())
}

// label is an area's or interest's label, or says it has none.
func label(in client.Interest) string {
	if in.Level == client.LevelArea {
		return cmp.Or(in.Label, "(unlabeled area)")
	}
	return cmp.Or(in.Label, "(unlabeled)")
}

// docName names a document: its title, its bookmark's, or its URL.
func docName(title, bookmarkTitle, url string) string { return cmp.Or(title, bookmarkTitle, url) }
