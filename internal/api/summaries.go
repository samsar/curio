package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/summarize"
)

// SummaryWriterState is what the API reads of the summaries' writer, from
// memory alone; *summarize.Writer is one.
type SummaryWriterState interface {
	State() summarize.State
	Identity() store.SummaryWriter
}

// SummaryFeed is the summary pass the API reports and kicks; *jobs.Summaries
// is one.
type SummaryFeed interface {
	Progress() jobs.SummaryProgress
	Kick()
	// Counts are the summaries' counts, read at most every few seconds
	// unless the pass hears of a change, and when they were read.
	Counts(ctx context.Context) (store.SummaryCounts, time.Time, error)
}

// SummaryResponse mirrors the openapi DocumentSummary schema: a document's
// summary as written, who wrote it and from what, and whether it read the
// extraction now current (Current) and was written by another model or
// prompt than the writer's now (Outdated).
type SummaryResponse struct {
	Kind          string    `json:"kind"`
	Site          string    `json:"site,omitempty"`
	Author        string    `json:"author,omitempty"`
	About         string    `json:"about,omitempty"`
	Entities      []string  `json:"entities"`
	Claims        []string  `json:"claims"`
	Descriptions  []string  `json:"descriptions"`
	Model         string    `json:"model"`
	PromptVersion string    `json:"prompt_version"`
	ExtractionID  string    `json:"extraction_id"`
	WrittenAt     time.Time `json:"written_at"`
	Current       bool      `json:"current"`
	Outdated      bool      `json:"outdated"`
}

// SummaryFailureResponse mirrors the openapi SummaryFailure schema: when a
// document's summary last failed for good, why, and the extraction it read.
type SummaryFailureResponse struct {
	At           time.Time `json:"at"`
	Error        string    `json:"error"`
	ExtractionID string    `json:"extraction_id"`
}

// attachSummary adds doc's summary and its last failure to resp, when it
// has either: one read by the document's key.
func (d Deps) attachSummary(ctx context.Context, doc *store.Document, resp *DocumentResponse) error {
	if d.Summaries == nil {
		return nil
	}
	stored, err := d.Summaries.GetSummary(ctx, d.TenantID, doc.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return err
	}
	if s := stored.Summary; s != nil {
		resp.Summary = &SummaryResponse{Kind: string(s.Kind), Site: s.Site, Author: s.Author, About: s.About,
			Entities: nonNil(s.Entities), Claims: nonNil(s.Claims), Descriptions: nonNil(s.Descriptions),
			Model: s.Model, PromptVersion: s.PromptVersion, ExtractionID: s.ExtractionID, WrittenAt: s.WrittenAt.UTC(),
			Current:  doc.CurrentExtractionID != nil && *doc.CurrentExtractionID == s.ExtractionID,
			Outdated: d.SummaryWriter != nil && s.SummaryWriter != d.SummaryWriter.Identity()}
	}
	if f := stored.Failure; f != nil {
		resp.SummaryFailure = &SummaryFailureResponse{At: f.At.UTC(), Error: f.Error, ExtractionID: f.ExtractionID}
	}
	return nil
}

// nonNil is items, or an empty list for none, so a list is never null.
func nonNil(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// SummaryStats mirrors the openapi SummaryStats schema: the fetched
// documents by their summary under the writer now (store.SummaryCounts),
// when they were counted, and, while the pass measures one, its rate and
// the time the pages left take at it.
type SummaryStats struct {
	Written    int       `json:"written"`
	ToWrite    int       `json:"to_write"`
	Outdated   int       `json:"outdated"`
	Failed     int       `json:"failed"`
	NotThePage int       `json:"not_the_page"`
	CountedAt  time.Time `json:"counted_at"`
	PerMinute  float64   `json:"per_minute,omitempty"`
	ETASeconds int64     `json:"eta_seconds,omitempty"`
}

// summariesOn reports whether summaries are on, which their counts and
// a rewrite need.
func (d Deps) summariesOn() bool {
	return d.Summaries != nil && d.SummaryWriter != nil && d.SummaryWriter.State().On
}

// summaryStats counts the summaries, through the pass when there is one
// (SummaryFeed.Counts, which reads them at most every few seconds), with
// its rate.
func (d Deps) summaryStats(ctx context.Context) (*SummaryStats, error) {
	var (
		n   store.SummaryCounts
		at  = time.Now()
		err error
	)
	if d.SummaryFeed != nil {
		n, at, err = d.SummaryFeed.Counts(ctx)
	} else {
		n, err = d.Summaries.CountSummaries(ctx, d.TenantID, d.SummaryWriter.Identity())
	}
	if err != nil {
		return nil, err
	}
	out := &SummaryStats{Written: n.Written, ToWrite: n.ToWrite, Outdated: n.Outdated, Failed: n.Failed,
		NotThePage: n.NotThePage, CountedAt: at.UTC()}
	if d.SummaryFeed != nil {
		if p := d.SummaryFeed.Progress(); p.PerMinute > 0 {
			out.PerMinute = p.PerMinute
			out.ETASeconds = int64(p.ETA(n.ToWrite) / time.Second)
		}
	}
	return out, nil
}

// SummariesHealth mirrors the openapi SummariesHealth schema: what the
// writer knows of its model, from memory alone (on is whether summaries
// are written, and off_by_default whether they are off because
// config.yaml leaves summaries.enabled out; reason why no page is written
// at all: off, off by default, no structured output, or withheld; the
// fitness check's verdict, and when an unchecked model's check runs again
// after one that reached no verdict), the outage while its model doesn't
// answer, until when the pass is held for a model Ollama keeps unloading,
// the pass's brake while it is on, until when it yields to the person's
// use of the models, and the writer's last failure. Like understanding,
// it never changes Status.
type SummariesHealth struct {
	On                bool                  `json:"on"`
	OffByDefault      bool                  `json:"off_by_default,omitempty"`
	Model             string                `json:"model,omitempty"`
	PromptVersion     string                `json:"prompt_version,omitempty"`
	Reason            string                `json:"reason,omitempty"`
	Check             string                `json:"check,omitempty"`
	CheckedAt         time.Time             `json:"checked_at,omitzero"`
	Unfit             string                `json:"unfit,omitempty"`
	CheckRetryAt      time.Time             `json:"check_retry_at,omitzero"`
	Outage            *SummaryOutage        `json:"outage,omitempty"`
	EvictionHoldUntil time.Time             `json:"eviction_hold_until,omitzero"`
	Brake             *ReembedBrake         `json:"brake,omitempty"`
	YieldingUntil     time.Time             `json:"yielding_until,omitzero"`
	LastFailure       *SummaryWriterFailure `json:"last_failure,omitempty"`
}

// SummaryOutage is the summaries' model not answering: since when, and the
// last error.
type SummaryOutage struct {
	Since time.Time `json:"since"`
	Error string    `json:"error"`
}

// SummaryWriterFailure is the writer's last failure: its cause as its log
// records name it, when, its error, and the document it was writing.
type SummaryWriterFailure struct {
	Cause      string    `json:"cause"`
	At         time.Time `json:"at"`
	Error      string    `json:"error,omitempty"`
	DocumentID string    `json:"document_id,omitempty"`
}

// summaries is the writer's state on healthz; with no writer, off.
func (d Deps) summaries() SummariesHealth {
	if d.SummaryWriter == nil {
		return SummariesHealth{Reason: summarize.OffReason}
	}
	s := d.SummaryWriter.State()
	h := SummariesHealth{On: s.On, OffByDefault: s.OffByDefault, Model: s.Model, PromptVersion: s.PromptVersion,
		Reason: s.Reason, Check: s.Check, CheckedAt: utc(s.CheckedAt), Unfit: s.Unfit,
		CheckRetryAt: utc(s.CheckRetryAt), EvictionHoldUntil: utc(s.EvictionHoldUntil)}
	if !s.Outage.Since.IsZero() {
		h.Outage = &SummaryOutage{Since: s.Outage.Since.UTC(), Error: s.Outage.Err}
	}
	if f := s.Failure; f.Cause != "" {
		h.LastFailure = &SummaryWriterFailure{Cause: f.Cause, At: f.At.UTC(), Error: f.Err, DocumentID: f.DocumentID}
	}
	now := time.Now()
	if d.SummaryFeed != nil {
		if b := d.SummaryFeed.Progress().Brake; b.On() && now.Before(b.Until) {
			h.Brake = &ReembedBrake{Until: b.Until.UTC(), Failures: b.Failures, Documents: b.Documents, Error: b.Err}
		}
	}
	if s.On && s.Reason == "" {
		h.YieldingUntil = utc(d.Foreground.YieldingUntil(now))
	}
	return h
}

// handleSummarizeAll marks fetched documents' summaries owed, so the pass
// writes them again: those another model or prompt wrote (scope outdated,
// the default), or every one, failures for good included (all). It
// answers 202 with how many it marked, and 409 while summaries are off.
func (d Deps) handleSummarizeAll(w http.ResponseWriter, r *http.Request) {
	scope := store.SummaryScope(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = store.SummaryScopeOutdated
	}
	if !scope.Valid() {
		writeProblem(w, r, http.StatusBadRequest, "invalid scope",
			fmt.Sprintf("scope %q must be %s or %s", scope, store.SummaryScopeOutdated, store.SummaryScopeAll))
		return
	}
	if !d.summariesOn() {
		reason := summarize.OffReason
		if d.SummaryWriter != nil && d.SummaryWriter.State().OffByDefault {
			reason = d.SummaryWriter.State().Reason
		}
		writeProblem(w, r, http.StatusConflict, "summaries off", reason+"; nothing is written to owe")
		return
	}
	n, err := d.Summaries.OweSummaries(r.Context(), d.TenantID, scope, d.SummaryWriter.Identity())
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	if d.SummaryFeed != nil {
		d.SummaryFeed.Kick()
	}
	d.writeJSON(w, r, http.StatusAccepted, map[string]int{"summaries_owed": n})
}
