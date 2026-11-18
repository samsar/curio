package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/textutil"
)

// askPredates is what curio ask says to a daemon that has no /v1/ask:
// one from before Ask, running until it restarts after an upgrade.
const askPredates = "this curio-daemon predates curio ask; restart it (curio daemon stop) and the next command " +
	"starts the current one"

// The done statuses that are answers, or an honest none: curio ask exits 0
// for them, and 1 for every other.
var askAnswered = []string{"answered", "truncated", "no_sources"}

func newAskCmd(env *daemonctl.Env) *cobra.Command {
	var (
		literal, noRerank, doRerank, asJSON bool
		sources                             int
		mode                                string
		filters                             client.SearchFilters
	)
	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Answer a question from your saved pages, citing them",
		Long: "Answer a question from the pages you saved, every claim citing its page as [n].\n\n" +
			"The question is searched as curio search searches it (understood, then reranked as\n" +
			"ask.rerank says), the best pages are read, and the daemon's answer writer (ask.enabled,\n" +
			"generation.roles.answers) streams an answer from them alone, then the pages it was\n" +
			"written from. A question your pages don't answer is answered so. Pages curio found but\n" +
			"couldn't read are listed after them: they are never a source.\n\n" +
			"A question about a claim (\"is it true that...\", \"evidence for...\") is answered from\n" +
			"the passages that support or contradict it; one about how your interests changed\n" +
			"(\"how have my interests changed\") from your interests' saves over time; one for\n" +
			"everything on a topic (\"everything I saved about...\") from the pages found, grouped by\n" +
			"interest. --mode picks the route instead of the question's words.\n\n" +
			"--rerank, --no-rerank and --literal are curio search's; the filters narrow the pages\n" +
			"searched. --json writes the daemon's events as they arrive, one JSON object a line.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			req := client.AskRequest{Question: strings.Join(args, " "), Sources: sources, Mode: mode}
			understand := !literal
			req.Understand = &understand
			req.Rerank = askRerank(cmd.Flags(), literal, noRerank, doRerank)
			if !filters.IsEmpty() {
				req.Filters = &filters
			}
			out := textutil.NewTerminalWriter(cmd.OutOrStdout())
			var p askPrinter
			if asJSON {
				p = &jsonAsk{w: out}
			} else {
				p = &textAsk{out: out, errOut: cmd.ErrOrStderr()}
			}
			err := env.Client.Ask(cmd.Context(), req, p.event)
			if client.IsNotFound(err) {
				return errors.New(askPredates)
			}
			if err != nil {
				return err
			}
			return p.end()
		},
	}
	f := cmd.Flags()
	f.BoolVar(&literal, "literal", false, "Search the question's words alone, without understanding it or reranking")
	f.BoolVar(&noRerank, "no-rerank", false, "Understand the question, but keep the first order of the pages found")
	f.BoolVar(&doRerank, "rerank", false, "Rerank the pages found, whatever the daemon's ask.rerank says")
	cmd.MarkFlagsMutuallyExclusive("rerank", "no-rerank", "literal")
	f.IntVar(&sources, "sources", 0, "How many pages to answer from, 1-12 (default: the daemon's ask.sources)")
	f.StringVar(&mode, "mode", "", "How to answer: answer (from the pages the search finds), evidence (for and "+
		"against a claim), interests (how your interests changed over time) or list (everything on a topic, "+
		"grouped by interest); by default, the question's words decide")
	f.BoolVar(&asJSON, "json", false, "Write the daemon's events as JSON lines")
	addFilterFlags(f, &filters)
	return cmd
}

// askRerank is the rerank an ask's flags ask for: off for --literal and
// --no-rerank, on for --no-rerank=false, as --rerank says whenever it is
// given (--rerank=false included), and nil, the daemon's ask.rerank, when
// none of them says (--literal=false only says the question is understood).
func askRerank(f *pflag.FlagSet, literal, noRerank, doRerank bool) *bool {
	switch {
	case literal || noRerank:
		return new(false)
	case f.Changed("rerank"):
		return new(doRerank)
	case f.Changed("no-rerank"):
		return new(true)
	}
	return nil
}

// askPrinter shows an ask's events as they arrive, and says at the end how
// it went: nil for an answer, else the error curio ask exits 1 with.
type askPrinter interface {
	event(e client.AskEvent) error
	end() error
}

// jsonAsk writes each event as the daemon sent it, one a line, with every
// rune a terminal would act on escaped inside its JSON strings. done is
// the event the stream ended with, a done or an error.
type jsonAsk struct {
	w    io.Writer
	done *client.AskEvent
}

func (j *jsonAsk) event(e client.AskEvent) error {
	if e.Type == "done" || e.Type == "error" {
		j.done = &e
	}
	if _, err := fmt.Fprintf(j.w, "%s\n", textutil.TerminalJSON(e.Raw)); err != nil {
		return fmt.Errorf("write an event: %w", err)
	}
	return nil
}

func (j *jsonAsk) end() error { return askEnd(j.done) }

// askEnd is how an ask that ended with done exits: nil for an answer, its
// reason for anything else, a stream without a done line included.
func askEnd(done *client.AskEvent) error {
	switch {
	case done == nil:
		return errors.New("curio-daemon ended the answer without saying how it went")
	case done.Type == "error":
		return askEventError(*done)
	case slices.Contains(askAnswered, done.Status):
		return nil
	}
	return errors.New(done.Reason)
}

// askEventError is an error event as an error.
func askEventError(e client.AskEvent) error {
	if e.RequestID == "" {
		return fmt.Errorf("%s (see `curio daemon logs`)", e.Error)
	}
	return fmt.Errorf("%s (request %s; see `curio daemon logs`)", e.Error, e.RequestID)
}

// textAsk prints an ask for a person: what the search understood, an
// interests table, the answer as it streams, then its sources (cited ones
// marked, an evidence source's side and quote too) or its groups, and the
// pages curio couldn't read, on stdout; what the ask waits on and notes,
// a fallback of the understanding or the rerank among them, on stderr.
type textAsk struct {
	out, errOut io.Writer
	waited      map[string]bool
	retrieved   *client.AskEvent
	evidence    *client.AskEvent
	table       *client.AskEvent
	groups      *client.AskEvent
	answer      strings.Builder
	done        *client.AskEvent
}

func (a *textAsk) event(e client.AskEvent) error {
	switch e.Type {
	case "progress":
		a.progress(e)
	case "retrieval":
		a.retrieved = &e
		for _, w := range e.Warnings {
			fmt.Fprintln(a.errOut, "warning:", textutil.TerminalLine(w))
		}
		askHeader(a.out, a.errOut, e.Understanding, e.Reranking, time.Local)
	case "evidence":
		a.evidence = &e
		if e.Status != "judged" && e.Status != "none" {
			fmt.Fprintln(a.errOut, "note: the passages weren't judged ("+textutil.TerminalLine(e.Reason)+
				"); the claim is answered from the pages found")
		}
	case "table":
		a.table = &e
		renderTable(a.out, e)
	case "groups":
		a.groups = &e
	case "delta":
		a.answer.WriteString(e.Text)
		fmt.Fprint(a.out, e.Text)
	case "done", "error":
		a.done = &e
	}
	return nil
}

// askHeader writes how an ask's question was understood and reranked on
// out, and why either fell back on errOut, each a note.
func askHeader(out, errOut io.Writer, u *client.Understanding, r *client.Reranking, loc *time.Location) {
	var lines []string
	for _, l := range headerLines(u, r, loc) {
		if l.fallback {
			fmt.Fprintln(errOut, "note: "+textutil.TerminalLine(l.text))
			continue
		}
		lines = append(lines, l.text)
	}
	if len(lines) > 0 {
		fmt.Fprint(out, strings.Join(lines, "\n")+"\n\n")
	}
}

// progress says, once each, what the ask waits on: the model loading or
// being checked, another ask's answer, or the judgment of a claim's
// passages.
func (a *textAsk) progress(e client.AskEvent) {
	var what string
	switch e.Stage {
	case "loading":
		what = "loading " + e.Model
	case "checking":
		what = "checking " + e.Model + " before its first answer"
	case "waiting":
		what = "waiting for another answer to finish"
	case "judging":
		what = "judging " + plural(e.Pages, "page")
	default:
		return
	}
	if a.waited == nil {
		a.waited = map[string]bool{}
	}
	if !a.waited[e.Stage] {
		a.waited[e.Stage] = true
		fmt.Fprintln(a.errOut, textutil.TerminalLine(what)+"…")
	}
}

func (a *textAsk) end() error {
	if a.answer.Len() > 0 && !strings.HasSuffix(a.answer.String(), "\n") {
		fmt.Fprintln(a.out)
	}
	if a.done != nil && a.done.Type == "done" {
		a.notes(*a.done)
	}
	var cited []int
	var markers []string
	if a.done != nil {
		cited, markers = a.done.Citations, a.done.Markers
	}
	if a.table != nil {
		renderTableRows(a.out, *a.table, markers)
	}
	if a.groups != nil {
		renderGroups(a.out, a.groups.Groups, markers)
	}
	if a.retrieved != nil {
		renderAskSources(a.out, a.retrieved.Sources, a.retrieved.Unreadable, cited, a.evidence)
	}
	return askEnd(a.done)
}

// notes says on stderr what a reader should know of the answer: cut at its
// cap, stopped part way, citing what it wasn't given, citing nothing, or
// why there was nothing to answer from.
func (a *textAsk) notes(done client.AskEvent) {
	switch {
	case done.Status == "truncated":
		fmt.Fprintln(a.errOut, "note: "+textutil.TerminalLine(done.Reason))
	case done.Status == "no_sources":
		fmt.Fprintln(a.errOut, "note: "+textutil.TerminalLine(done.Reason))
	case done.Partial:
		fmt.Fprintln(a.errOut, "note: the answer stopped part way")
	}
	if len(done.InvalidCitations) > 0 {
		marks := make([]string, len(done.InvalidCitations))
		for i, c := range done.InvalidCitations {
			marks[i] = "[" + textutil.TerminalLine(c) + "]"
		}
		fmt.Fprintf(a.errOut, "note: the answer cites %s, which it wasn't given: nothing backs those claims\n",
			strings.Join(marks, ", "))
	}
	if done.Uncited {
		fmt.Fprintln(a.errOut, "note: nothing is cited; check the sources before relying on it")
	}
}

// renderTable writes an interests table before the answer: each period's
// saves and its three largest areas (a flat grouping's interests) with
// their counts, then the saves with no known date.
func renderTable(w io.Writer, e client.AskEvent) {
	t := e.Table()
	if t == nil {
		return
	}
	top := "area"
	if !slices.ContainsFunc(t.Rows, func(r client.TimelineRow) bool { return r.Kind == "area" }) {
		top = "interest"
	}
	fmt.Fprintf(w, "Saves by %s:\n", textutil.TerminalLine(t.Period))
	for i, period := range t.Periods {
		total := 0
		type count struct {
			label string
			n     int
		}
		var counts []count
		for _, r := range t.Rows {
			if i >= len(r.Counts) {
				continue
			}
			if r.Kind == top || r.Kind == "unsorted" || r.Kind == "other_areas" ||
				(top == "interest" && r.Kind == "other_interests") {
				total += r.Counts[i]
			}
			if r.Kind == top && r.Counts[i] > 0 {
				counts = append(counts, count{rowLabel(r), r.Counts[i]})
			}
		}
		slices.SortStableFunc(counts, func(a, b count) int { return b.n - a.n })
		parts := make([]string, 0, 3)
		for _, c := range counts[:min(len(counts), 3)] {
			parts = append(parts, fmt.Sprintf("%s %d", textutil.TerminalLine(c.label), c.n))
		}
		line := fmt.Sprintf("  %s: %d", textutil.TerminalLine(period), total)
		if len(parts) > 0 {
			line += " — " + strings.Join(parts, ", ")
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "  undated: %d\n\n", t.Undated)
}

// rowLabel names a table row on a line.
func rowLabel(r client.TimelineRow) string {
	switch {
	case r.Kind == "unsorted":
		return "in no interest"
	case r.Kind == "other_areas":
		return fmt.Sprintf("other areas (%d)", r.Merged)
	case r.Kind == "other_interests":
		return fmt.Sprintf("other interests (%d)", r.Merged)
	case r.Label == "":
		return "(unnamed)"
	}
	return r.Label
}

// renderTableRows lists, after the answer, the table rows it cites, by
// their markers, with their totals, and the undated count's.
func renderTableRows(w io.Writer, e client.AskEvent, cited []string) {
	t := e.Table()
	if t == nil || len(cited) == 0 {
		return
	}
	fmt.Fprint(w, "\nRows cited:\n")
	for _, m := range cited {
		if m == e.UndatedMarker {
			fmt.Fprintf(w, "[%s] undated: %s\n", textutil.TerminalLine(m), plural(t.Undated, "save"))
			continue
		}
		for _, r := range t.Rows {
			if r.Marker == m {
				fmt.Fprintf(w, "[%s] %s: %s\n", textutil.TerminalLine(m), textutil.TerminalLine(rowLabel(r)),
					plural(r.Total, "save"))
			}
		}
	}
}

// renderGroups lists, after the answer, the groups the list route found,
// cited ones marked, each with its documents' titles, URLs and doc_ids.
func renderGroups(w io.Writer, groups []client.AskGroup, cited []string) {
	if len(groups) == 0 {
		return
	}
	fmt.Fprint(w, "\nGroups:\n")
	for _, g := range groups {
		mark := ""
		if slices.Contains(cited, g.Marker) {
			mark = " (cited)"
		}
		label := g.Label
		switch {
		case g.InterestID == "" && label == "":
			label = "in no interest"
		case label == "":
			label = "(unnamed)"
		}
		if g.AreaLabel != "" {
			label += " (in " + g.AreaLabel + ")"
		}
		fmt.Fprintf(w, "[%s] %s (%d)%s\n", textutil.TerminalLine(g.Marker), textutil.TerminalLine(label), g.Count,
			mark)
		for _, d := range g.Documents {
			fmt.Fprintf(w, "  - %s\n    %s\n    doc_id: %s\n", textutil.TerminalLine(d.Title),
				textutil.TerminalLine(d.URL), textutil.TerminalLine(d.DocID))
		}
	}
}

// renderAskSources lists the pages an answer was written from, each with
// its number, marked when the answer cites it, an evidence source's side
// and quote, its URL and its doc_id, then the pages curio found but
// couldn't read.
func renderAskSources(w io.Writer, sources []client.AskSource, unreadable []client.AskUnreadable, cited []int,
	evidence *client.AskEvent) {
	if len(sources) > 0 {
		fmt.Fprint(w, "\nSources:\n")
		for _, s := range sources {
			mark := ""
			if slices.Contains(cited, s.N) {
				mark = " (cited)"
			}
			if side := sideOf(evidence, s.N); side != "" {
				mark += " — " + side
			}
			fmt.Fprintf(w, "[%d] %s%s\n", s.N, textutil.TerminalLine(s.Title), mark)
			if s.Quote != "" {
				fmt.Fprintf(w, "    \u201c%s\u201d\n", textutil.TerminalLine(s.Quote))
			}
			fmt.Fprintf(w, "    %s\n    doc_id: %s\n", textutil.TerminalLine(s.URL), textutil.TerminalLine(s.DocID))
		}
	}
	if len(unreadable) > 0 {
		fmt.Fprint(w, "\nSaved, but curio couldn't read:\n")
		for _, u := range unreadable {
			fmt.Fprintf(w, "- %s (%s)\n    %s\n    doc_id: %s\n", textutil.TerminalLine(u.Title),
				textutil.TerminalLine(u.Reason), textutil.TerminalLine(u.URL), textutil.TerminalLine(u.DocID))
		}
	}
}

// sideOf is the side source n takes on an evidence ask's claim, "" when
// it takes none or the ask judged none.
func sideOf(evidence *client.AskEvent, n int) string {
	switch {
	case evidence == nil || evidence.Status != "judged":
		return ""
	case slices.Contains(evidence.Supports, n):
		return "supports"
	case slices.Contains(evidence.Contradicts, n):
		return "contradicts"
	}
	return ""
}
