package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/summarize"
)

// The fixes for a summary pass that doesn't move.
const (
	// summariesOutageFix is what brings a summaries' model back.
	summariesOutageFix = "start Ollama (the Ollama app, or `ollama serve`); a model that isn't pulled: `curio up` " +
		"pulls it. The pass goes on once the model answers"
	// summariesBrakeFix is what lets a braked pass go on.
	summariesBrakeFix = "check that Ollama runs and serves the summaries' model (`ollama ps`, and Ollama's log): the " +
		"daemon tries one page again at that time and goes on once one is written; `curio docs show <id>` shows a " +
		"page named, and a restart (`curio daemon stop`, then any curio command starts it) tries at once"
	// summariesCheckFix is what lets a model whose check keeps reaching no
	// verdict be checked.
	summariesCheckFix = "check that Ollama serves the summaries' model (`ollama ps`, and Ollama's log for its " +
		"errors): the daemon checks it again at that time, and the pass begins once it passes; a model that keeps " +
		"failing to answer can be replaced (generation.roles.summaries.model) or summaries.enabled: false"
	// summariesBarsFix is what to do about summaries on with a model below
	// the measured bars.
	summariesBarsFix = "set generation.roles.summaries.model to a model that meets them, or summaries.enabled: " +
		"false to write none"
	// summariesEvictionFix is what lets the pass keep its model loaded.
	summariesEvictionFix = "the models curio and other apps use don't fit in memory together (`ollama ps` lists " +
		"what is loaded): a smaller summaries model (generation.roles.summaries.model), fewer models loaded at once, " +
		"or summaries.enabled: false; the pass tries again at that time"
	// summariesRestart is how a restart resumes a pass that stopped: its
	// start counts and feeds it again.
	summariesRestart = "restart the daemon (`curio daemon stop`, then any curio command starts it): its start " +
		"resumes the summary pass"
)

// summariesWarning is the line status prints while summaries are on but
// no page gets one, for the first that holds of: pages withheld from the
// model or a provider without structured output, a model that failed its
// check, a model that doesn't answer, a pass held for a model Ollama keeps
// unloading, a model whose check reached no verdict, and a pass its brake
// stops; empty otherwise, and from a daemon that predates summaries. The
// fix is doctor's.
func summariesWarning(h *client.SummariesHealth) string {
	switch {
	case h == nil || !h.On:
		return ""
	case h.Reason != "":
		return fmt.Sprintf("warning: no page is summarized: %s; run `curio doctor`\n", h.Reason)
	case h.Check == checkUnfit:
		return fmt.Sprintf("warning: no page is summarized: %s failed its check; run `curio doctor`\n", h.Model)
	case h.Outage != nil:
		return fmt.Sprintf("warning: the summaries' model hasn't answered since %s (%s); pages wait for it; run "+
			"`curio doctor`\n", localTime(h.Outage.Since), h.Outage.Error)
	case !h.EvictionHoldUntil.IsZero():
		return fmt.Sprintf("warning: the summary pass waits until %s: Ollama keeps unloading %s for other models; "+
			"run `curio doctor`\n", localTime(h.EvictionHoldUntil), h.Model)
	case checkFailing(h):
		return fmt.Sprintf("warning: no page is summarized: %s couldn't be checked (%s)%s; run `curio doctor`\n",
			h.Model, h.LastFailure.Error, checkRetry(h))
	case h.Brake != nil:
		return fmt.Sprintf("warning: the summary pass waits until %s: the summaries of %s failed in a row (%s); run "+
			"`curio doctor`\n", localTime(h.Brake.Until), plural(h.Brake.Failures, "page"), h.Brake.Error)
	}
	return ""
}

// checkFailing reports whether h's model is unchecked because its last
// check reached no verdict for a reason of the model's: its last failure
// is the check's.
func checkFailing(h *client.SummariesHealth) bool {
	return h.Check == checkUnchecked && h.LastFailure != nil && h.LastFailure.Cause == failureCheck
}

// checkRetry is when h's model is checked again, as "; checked again at
// <time>", or "" when the daemon doesn't say: it says the time only while
// it is ahead, so a check kept behind a pause reads right.
func checkRetry(h *client.SummariesHealth) string {
	if h.CheckRetryAt.IsZero() {
		return ""
	}
	return "; checked again at " + localTime(h.CheckRetryAt)
}

// summariesLines are status's lines about the summaries: how many are
// written of the fetched documents, how many to write and when they are
// done at the rate measured, with the model; then what is outdated, failed
// or not the page, when any is. While summaries are off, one line when
// they are off by default, saying how to turn them on, else nothing.
func summariesLines(s *client.SummaryStats, h *client.SummariesHealth) string {
	if s == nil {
		if h != nil && !h.On && h.OffByDefault {
			return "summaries: off by default; " + summarize.OnFix + "\n"
		}
		return ""
	}
	total := s.Written + s.ToWrite + s.Outdated + s.Failed + s.NotThePage
	var b strings.Builder
	fmt.Fprintf(&b, "summaries: %d of %d written", s.Written+s.NotThePage, total)
	if h != nil && h.Model != "" {
		fmt.Fprintf(&b, " (%s)", h.Model)
	}
	if s.ToWrite > 0 {
		fmt.Fprintf(&b, " · %d to write%s", s.ToWrite, summariesETA(s))
	}
	b.WriteString("\n")
	if s.Outdated > 0 {
		fmt.Fprintf(&b, "           %d by another model or prompt: `curio summarize --outdated` writes them again\n",
			s.Outdated)
	}
	if s.Failed > 0 {
		fmt.Fprintf(&b, "           %d failed for good (tried again in a week, when the page, model or prompt "+
			"changes, or at once with `curio summarize --all`)\n", s.Failed)
	}
	if s.NotThePage > 0 {
		fmt.Fprintf(&b, "           %d not the page saved (a sign-in wall, an error page)\n", s.NotThePage)
	}
	return b.String()
}

// summariesETA is how long the pages to write take at the pass's rate:
// ", about 3 h 20 min left at 10.5 a minute", or "" before it is measured.
func summariesETA(s *client.SummaryStats) string {
	if s.ETASeconds <= 0 || s.PerMinute <= 0 {
		return ""
	}
	return fmt.Sprintf(", %s left at %.1f a minute", roughly(time.Duration(s.ETASeconds)*time.Second), s.PerMinute)
}

// summariesCheckOf asks c whether a summarize job is pending or running
// when pages are left to write, and checks the summaries (summariesCheck).
func summariesCheckOf(ctx context.Context, c *client.Client, s *client.SummaryStats,
	h client.SummariesHealth) (status checkStatus, detail, hint string) {
	queued := false
	if s != nil && s.ToWrite > 0 && h.Outage == nil && h.Brake == nil && h.EvictionHoldUntil.IsZero() {
		for _, status := range []store.JobStatus{store.JobStatusRunning, store.JobStatusPending} {
			jobs, err := newestJobs(ctx, c, store.JobKindSummarize, status)
			if err != nil {
				return statusWarn, "couldn't list the summarize jobs: " + err.Error(), ""
			}
			queued = queued || len(jobs) > 0
		}
	}
	return summariesCheck(s, h, queued)
}

// summariesCheck is doctor's check of the summaries, given whether a
// summarize job is queued (pending or running): fine while off, saying how
// to turn them on when they are off by default; a warning while pages are
// withheld or the provider can't answer in JSON, the model failed its
// check, doesn't answer, is held for a model Ollama keeps unloading,
// couldn't be checked, or the pass's brake stops it, each with its fix;
// while they are on with a model below the measured bars
// (config.SummariesBelowBars); and while pages are left to write with no
// job queued to write them, which a restart fixes; otherwise fine, with
// the progress, summaries by another model or prompt a note.
func summariesCheck(s *client.SummaryStats, h client.SummariesHealth, queued bool) (status checkStatus, detail,
	hint string) {
	switch {
	case !h.On && h.OffByDefault:
		return statusOK, "off by default", summarize.OnFix
	case !h.On:
		return statusOK, "off (summaries.enabled: false)", ""
	case h.Reason != "":
		return statusWarn, "no page is summarized: " + h.Reason, ""
	case h.Check == checkUnfit:
		return statusWarn, "no page is summarized: " + h.Unfit, ""
	case h.Outage != nil:
		return statusWarn, fmt.Sprintf("the summaries' model %s hasn't answered since %s: %s", h.Model,
			localTime(h.Outage.Since), h.Outage.Error), summariesOutageFix
	case !h.EvictionHoldUntil.IsZero():
		return statusWarn, fmt.Sprintf("the summary pass waits until %s: Ollama unloaded %s within its keep-alive "+
			"twice within an hour, for other models", localTime(h.EvictionHoldUntil), h.Model), summariesEvictionFix
	case checkFailing(&h):
		return statusWarn, fmt.Sprintf("no page is summarized: %s couldn't be checked: %s%s", h.Model,
			h.LastFailure.Error, checkRetry(&h)), summariesCheckFix
	case h.Brake != nil:
		return statusWarn, fmt.Sprintf("the summary pass waits until %s, the summaries of %s having failed in a "+
			"row%s: %s", localTime(h.Brake.Until), plural(h.Brake.Failures, "page"), brakeDocuments(*h.Brake),
			h.Brake.Error), summariesBrakeFix
	case config.SummariesBelowBars(h.Model) != "":
		return statusWarn, fmt.Sprintf("on with %s, which misses the summaries' measured bars: %s", h.Model,
			config.SummariesBelowBars(h.Model)), summariesBarsFix
	case s == nil:
		return statusOK, "on, with " + h.Model, ""
	}
	progress := fmt.Sprintf("%d of %d written with %s", s.Written+s.NotThePage,
		s.Written+s.ToWrite+s.Outdated+s.Failed+s.NotThePage, h.Model)
	if s.ToWrite > 0 {
		if !queued {
			return statusWarn, fmt.Sprintf("%s; %d to write, and no summarize job is queued to write them", progress,
				s.ToWrite), summariesRestart
		}
		progress += fmt.Sprintf("; %d to write%s", s.ToWrite, summariesETA(s))
	}
	if s.Outdated > 0 {
		hint = fmt.Sprintf("%d by another model or prompt are kept as they are: `curio summarize --outdated` writes "+
			"them again", s.Outdated)
	}
	return statusOK, progress, hint
}

// renderDocumentSummary writes a document's summary, for docs show: what it is
// about, its kind, site and author, the names it holds, who wrote it and
// when, and whether it is stale (it read an earlier extraction) or
// outdated (another model or prompt wrote it); and its last failure.
func renderDocumentSummary(w io.Writer, d *client.Document) {
	if s := d.Summary; s != nil {
		fmt.Fprintf(w, "\nsummary:\n")
		if s.About != "" {
			fmt.Fprintf(w, "  about:        %s\n", s.About)
		}
		fmt.Fprintf(w, "  kind:         %s\n", s.Kind)
		if s.Site != "" {
			fmt.Fprintf(w, "  site:         %s\n", s.Site)
		}
		if s.Author != "" {
			fmt.Fprintf(w, "  author:       %s\n", s.Author)
		}
		if len(s.Entities) > 0 {
			fmt.Fprintf(w, "  names:        %s\n", strings.Join(s.Entities, ", "))
		}
		fmt.Fprintf(w, "  written:      %s by %s\n", s.WrittenAt.Local().Format("2006-01-02 15:04"), s.Model)
		if !s.Current {
			fmt.Fprintf(w, "  stale:        it read an earlier extraction; the summary pass writes it again\n")
		}
		if s.Outdated {
			fmt.Fprintf(w, "  outdated:     another model or prompt wrote it; `curio summarize --outdated` "+
				"writes it again\n")
		}
	}
	if f := d.SummaryFailure; f != nil {
		fmt.Fprintf(w, "\nsummary failed %s: %s\n", f.At.Local().Format("2006-01-02 15:04"), f.Error)
	}
}

// newSummarizeCmd builds `curio summarize`, which owes summaries a
// rewrite.
func newSummarizeCmd(env *daemonctl.Env) *cobra.Command {
	var outdated, all bool
	cmd := &cobra.Command{
		Use:   "summarize (--outdated | --all)",
		Short: "Write the documents' summaries again",
		Long: `The daemon writes each fetched page's summary once, with the summaries
role's model (generation.model unless generation.roles.summaries names
another), and again when the page is refetched with new content. A summary
another model or prompt wrote is kept as it is, counted outdated in
` + "`curio status`" + `: a new model or an upgrade never starts the whole pass
again unasked.

--outdated writes the outdated summaries again; --all every one, and tries
again each page whose summary failed for good. The pass writes them one
page at a time under the queue's gate (curio pause, curio schedule, curio
throttle), yielding to your searches, at the rate ` + "`curio status`" + `
shows.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if outdated == all {
				return errors.New("pass --outdated or --all")
			}
			if err := env.Controller.EnsureRunning(cmd.Context()); err != nil {
				return err
			}
			scope := string(store.SummaryScopeOutdated)
			if all {
				scope = string(store.SummaryScopeAll)
			}
			resp, err := env.Client.SummarizeAll(cmd.Context(), scope)
			if err != nil {
				return err
			}
			return printSummarizeAll(cmd.Context(), cmd.OutOrStdout(), env.Client, resp.SummariesOwed)
		},
	}
	cmd.Flags().BoolVar(&outdated, "outdated", false, "Write again the summaries another model or prompt wrote")
	cmd.Flags().BoolVar(&all, "all", false, "Write every summary again, and try again the pages whose summary failed")
	return cmd
}

// printSummarizeAll says how many pages were owed a summary, theirs
// written again or, with --all, one that failed for good tried again, and
// how long the pages to write take: at the pass's rate while it is
// measured, else at the summaries' model's time a page as curio up
// measured it, else that the time isn't known yet.
func printSummarizeAll(ctx context.Context, w io.Writer, c *client.Client, owed int) error {
	fmt.Fprintf(w, "%s to summarize again\n", plural(owed, "page"))
	if owed == 0 {
		return nil
	}
	stats, err := c.Stats(ctx)
	if err != nil {
		return fmt.Errorf("read the pass's rate: %w", err)
	}
	s := stats.Summaries
	if s == nil {
		return nil
	}
	if s.ETASeconds > 0 {
		fmt.Fprintf(w, "%s to write in all%s\n", plural(s.ToWrite, "page"), summariesETA(s))
		return nil
	}
	h, err := c.Healthz(ctx)
	if err != nil {
		return fmt.Errorf("read the summaries' model: %w", err)
	}
	if h.Summaries != nil {
		if c, ok := config.SummaryCostOf(h.Summaries.Model); ok {
			spp := c.SecondsPerPage
			took := time.Duration(float64(s.ToWrite) * spp * float64(time.Second))
			fmt.Fprintf(w, "%s to write in all, %s at %s's %.1f s a page (normal throttle, as measured on a "+
				"64 GB M4 Max; searches pause it)\n", plural(s.ToWrite, "page"), roughly(took), h.Summaries.Model, spp)
			return nil
		}
	}
	fmt.Fprintf(w, "%s to write in all, one at a time; the time a page isn't measured yet: `curio status` "+
		"shows the rate once the pass runs\n", plural(s.ToWrite, "page"))
	return nil
}
