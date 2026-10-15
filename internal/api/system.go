package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/samsar/curio/internal/drift"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/understand"
	"github.com/samsar/curio/internal/version"
)

// Health is the /v1/healthz response. PID and Home identify which daemon
// answered: clients use them to confirm the process on the port is the one
// serving their $CURIO_HOME before trusting or signalling it.
// GenerationModel is the generation.model the daemon started with, and
// GenerationRoles each role in use with the model it writes with, which
// `curio up` compares with config.yaml's to know when a restart would
// change them. Upstreams are the services fetches depend on; like Ollama, a
// failing one doesn't make the daemon unhealthy, and neither does
// EmbeddingDrift: a change of the embedding build that a re-embedded
// sample of the library showed, or couldn't rule out, changed the vectors.
// YouTubeFetcher is the yt-dlp the daemon found when it
// started, which it routes YouTube videos to for their transcripts, or
// empty: `curio up` tells from it whether a daemon predates an install.
// GitHubToken says whether the GitHub fetcher sends a token, from
// config.yaml or its environment: without one GitHub allows 60 API
// requests an hour, which a library with many github.com pages exceeds.
// Interests is where automatic rebuilds of the interests stand, the
// scheduler's last check: interests are not health, so it never changes
// Status. DocumentFields is what the daemon's start found of the
// documents' search fields; a start that couldn't check them still
// serves, so it never changes Status either. Nor does SavedDates: what
// the start did to the bookmarks' saved-date classes, and the sources
// whose classification after an import failed, which the daemon retries.
// EmbedderHold is the hold over the index and cards jobs while the
// embedding model doesn't answer, and ReembedBrake the re-embed's brake
// while it stops the re-embed: each present only while it holds, and
// neither changes Status. Understanding is what the planner knows of its
// model, from its memory alone; it never changes Status either.
type Health struct {
	Status          string              `json:"status"`
	PID             int                 `json:"pid"`
	Home            string              `json:"home"`
	Version         string              `json:"version"`
	SchemaVersion   int                 `json:"schema_version"`
	EmbeddingModel  string              `json:"embedding_model"`
	EmbeddingDim    int                 `json:"embedding_dim"`
	EmbeddingDrift  *EmbeddingDrift     `json:"embedding_drift,omitempty"`
	GenerationModel string              `json:"generation_model"`
	GenerationRoles map[string]string   `json:"generation_roles"`
	OllamaReachable bool                `json:"ollama_reachable"`
	OllamaDetail    string              `json:"ollama_detail,omitempty"`
	Upstreams       []UpstreamHealth    `json:"upstreams"`
	YouTubeFetcher  string              `json:"youtube_fetcher,omitempty"`
	GitHubToken     bool                `json:"github_token"`
	Interests       InterestsState      `json:"interests"`
	DocumentFields  DocumentFields      `json:"document_fields"`
	SavedDates      SavedDates          `json:"saved_dates"`
	EmbedderHold    *EmbedderHold       `json:"embedder_hold,omitempty"`
	ReembedBrake    *ReembedBrake       `json:"reembed_brake,omitempty"`
	Understanding   UnderstandingHealth `json:"understanding"`
}

// UnderstandingHealth mirrors the openapi UnderstandingHealth schema: what
// the planner knows of its model, read from its memory alone, never by
// asking the model. On is search.understand. Reason says why no question
// is planned at all: understanding is off, its provider can't answer in
// JSON, or questions would leave this machine for a server the planner
// role doesn't name. Check is the fitness check's verdict (unchecked, fit
// or unfit), CheckedAt when it was reached, and Unfit why, with the fix.
// LastStatus is the last question's understanding.status, at LastAt.
// TimeoutHoldUntil and EvictionHoldUntil are present only while each
// holds: plans that kept timing out, and a model Ollama kept unloading
// within its keep-alive. LastFailure is the planner's last failure.
type UnderstandingHealth struct {
	On                bool                  `json:"on"`
	Model             string                `json:"model,omitempty"`
	Reason            string                `json:"reason,omitempty"`
	Check             string                `json:"check,omitempty"`
	CheckedAt         time.Time             `json:"checked_at,omitzero"`
	Unfit             string                `json:"unfit,omitempty"`
	LastStatus        string                `json:"last_status,omitempty"`
	LastAt            time.Time             `json:"last_at,omitzero"`
	TimeoutHoldUntil  time.Time             `json:"timeout_hold_until,omitzero"`
	EvictionHoldUntil time.Time             `json:"eviction_hold_until,omitzero"`
	LastFailure       *UnderstandingFailure `json:"last_failure,omitempty"`
}

// UnderstandingFailure is the planner's last failure: its cause, as the
// daemon's log records name it (timeout, unavailable:generate, unfit,
// evicted, ...), when, and its error.
type UnderstandingFailure struct {
	Cause string    `json:"cause"`
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
}

// understanding is the planner's state on healthz; with no planner, off.
// Times are in UTC.
func (d Deps) understanding() UnderstandingHealth {
	if d.Planner == nil {
		return UnderstandingHealth{Reason: understand.OffReason}
	}
	s := d.Planner.State()
	h := UnderstandingHealth{On: s.On, Model: s.Model, Reason: s.Reason, Check: s.Check, Unfit: s.Unfit,
		LastStatus: string(s.Last), CheckedAt: utc(s.CheckedAt), LastAt: utc(s.LastAt),
		TimeoutHoldUntil: utc(s.TimeoutHoldUntil), EvictionHoldUntil: utc(s.EvictionHoldUntil)}
	if f := s.Failure; f.Cause != "" {
		h.LastFailure = &UnderstandingFailure{Cause: f.Cause, At: f.At.UTC(), Error: f.Err}
	}
	return h
}

// utc is t in UTC, the zero time kept zero.
func utc(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}

// EmbedderHold is the hold over the index and cards jobs while it holds
// them (jobs.EmbedderHold): since when the embedding model hasn't answered,
// and the error of the last embed that got no answer. The jobs it holds
// wait, failing nothing, and resume at the model's first answer.
type EmbedderHold struct {
	Since time.Time `json:"since"`
	Error string    `json:"error"`
}

// EmbedderHoldState reports the embedder hold; *jobs.EmbedderHold is one.
type EmbedderHoldState interface {
	State() jobs.HoldState
}

// ReembedBrake is the re-embed's brake while it stops the re-embed
// (jobs.ReembedBrake): until when no document is queued to re-embed, how
// many documents' re-embeds failed in a row, the first few of them by ID,
// and the last one's error. They keep their earlier vectors meanwhile.
type ReembedBrake struct {
	Until     time.Time `json:"until"`
	Failures  int       `json:"failures"`
	Documents []string  `json:"documents"`
	Error     string    `json:"error"`
}

// embedderHold is the embedder hold while it holds, or nil.
func (d Deps) embedderHold() *EmbedderHold {
	if d.Hold == nil {
		return nil
	}
	st := d.Hold.State()
	if !st.Held {
		return nil
	}
	return &EmbedderHold{Since: st.Since.UTC(), Error: st.Err}
}

// reembedBrake is the re-embed's brake while it is on, or nil: never one
// whose time has passed, which the re-embed turns off when it is read.
func (d Deps) reembedBrake() *ReembedBrake {
	if d.Reembed == nil {
		return nil
	}
	b := d.Reembed.Progress().Brake
	if !b.On() || !time.Now().Before(b.Until) {
		return nil
	}
	return &ReembedBrake{Until: b.Until.UTC(), Failures: b.Failures, Documents: b.Documents, Error: b.Err}
}

// DocumentFields is what the daemon's start did to the documents' search
// fields (sqlite.EnsureDocumentFields), fixed when it started: when it
// checked them, whether it rebuilt their index, which disagreed with
// them, and the error that stopped it, if one did. Search may miss
// documents until a start succeeds; a restart checks them again.
type DocumentFields struct {
	CheckedAt time.Time `json:"checked_at"`
	Rebuilt   bool      `json:"rebuilt"`
	Error     string    `json:"error,omitempty"`
}

// EmbeddingDrift says what changed in the build that makes the home's
// embeddings (drift.Report), present only while it differs from the build
// recorded when the library was indexed and a sample of the library,
// re-embedded by the build serving now, doesn't match the stored vectors
// or couldn't be checked. Verification is that evidence.
type EmbeddingDrift struct {
	Changes      []DriftChange     `json:"changes"`
	Verification DriftVerification `json:"verification"`
	Fix          string            `json:"fix"`
	CheckedAt    time.Time         `json:"checked_at"`
}

// DriftVerification is what a drift was reported on (drift.Evidence):
// the sample's comparison when Verified, and Detail, the daemon's wording
// of it, which clients print as it is. MinCosine is set only when
// Verified.
type DriftVerification struct {
	Verified  bool      `json:"verified"`
	Sampled   int       `json:"sampled"`
	Changed   int       `json:"changed"`
	MinCosine *float64  `json:"min_cosine,omitempty"`
	Detail    string    `json:"detail"`
	SampledAt time.Time `json:"sampled_at"`
}

// DriftChange is one changed part of the build (drift.Change).
type DriftChange struct {
	What     string `json:"what"`
	Recorded string `json:"recorded"`
	Current  string `json:"current"`
}

// DriftMonitor watches the build that makes the home's embeddings: the
// daemon's is a *drift.Monitor.
type DriftMonitor interface {
	// Report is the last check's finding; no Ollama call.
	Report() drift.Report
	// Rebaseline makes the build serving now the recorded one.
	Rebaseline() error
}

// embeddingDrift is the drift the monitor last found, or nil when there is
// none or no monitor.
func (d Deps) embeddingDrift() *EmbeddingDrift {
	if d.Drift == nil {
		return nil
	}
	r := d.Drift.Report()
	if !r.Drifted() {
		return nil
	}
	changes := make([]DriftChange, 0, len(r.Changes))
	for _, c := range r.Changes {
		changes = append(changes, DriftChange{What: c.What, Recorded: c.Recorded, Current: c.Current})
	}
	return &EmbeddingDrift{Changes: changes, Verification: driftVerification(r.Evidence), Fix: drift.Fix,
		CheckedAt: r.CheckedAt.UTC()}
}

// driftVerification is ev on the wire.
func driftVerification(ev drift.Evidence) DriftVerification {
	v := DriftVerification{Verified: ev.Verified, Sampled: ev.Sampled, Changed: ev.Changed, Detail: ev.Detail(),
		SampledAt: ev.At.UTC()}
	if ev.Verified {
		v.MinCosine = &ev.MinCosine
	}
	return v
}

// UpstreamHealth is an upstream's health on the wire (fetcher.UpstreamHealth).
// Times are UTC, and the ones that aren't set are omitted.
type UpstreamHealth struct {
	Name             string         `json:"name"`
	Enabled          bool           `json:"enabled"`
	State            string         `json:"state"`
	LastSuccessAt    time.Time      `json:"last_success_at,omitzero"`
	LastFailureAt    time.Time      `json:"last_failure_at,omitzero"`
	LastFailureClass string         `json:"last_failure_class,omitempty"`
	WindowSeconds    int            `json:"window_seconds"`
	Recent           map[string]int `json:"recent"`
	CooldownUntil    time.Time      `json:"cooldown_until,omitzero"`
	SitePauses       []SitePause    `json:"site_pauses,omitempty"`
}

// SitePause is an upstream's block of one site's reads on the wire
// (fetcher.SitePause), until a UTC time.
type SitePause struct {
	Site  string    `json:"site"`
	Until time.Time `json:"until"`
}

// upstreams reports the health of each upstream the daemon tracks: an
// empty list, never null, when it tracks none.
func (d Deps) upstreams() []UpstreamHealth {
	var tracked []fetcher.UpstreamHealth
	if d.Upstreams != nil {
		tracked = d.Upstreams()
	}
	out := make([]UpstreamHealth, 0, len(tracked))
	for _, u := range tracked {
		out = append(out, UpstreamHealth{
			Name:             u.Name,
			Enabled:          u.Enabled,
			State:            string(u.State),
			LastSuccessAt:    u.LastSuccess.UTC(),
			LastFailureAt:    u.LastFailure.UTC(),
			LastFailureClass: string(u.LastFailureClass),
			WindowSeconds:    int(u.Window / time.Second),
			Recent:           stringKeys(u.Recent),
			CooldownUntil:    u.CooldownUntil.UTC(),
			SitePauses:       sitePauses(u.SitePauses),
		})
	}
	return out
}

// sitePauses is pauses on the wire: nil, and so left out, when there are
// none.
func sitePauses(pauses []fetcher.SitePause) []SitePause {
	if len(pauses) == 0 {
		return nil
	}
	out := make([]SitePause, len(pauses))
	for i, p := range pauses {
		out[i] = SitePause{Site: p.Site, Until: p.Until.UTC()}
	}
	return out
}

// ollamaPingTimeout caps the Ollama check in /v1/healthz, the only part of
// the handler that waits on another service. Clients probe healthz with a
// much longer timeout (client.Healthz), so a slow Ollama is reported as
// ollama_reachable=false, never mistaken for a missing daemon.
const ollamaPingTimeout = 500 * time.Millisecond

func (d Deps) handleHealth(w http.ResponseWriter, r *http.Request) {
	h, err := d.health(r.Context())
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, h)
}

// health reports the daemon's health. Fail-open: an unreachable Ollama
// doesn't make the whole daemon unhealthy (the user can still list
// bookmarks, browse docs, etc.); it is reported in OllamaReachable and
// OllamaDetail, with advice.
func (d Deps) health(ctx context.Context) (Health, error) {
	meta, err := d.Home.Meta()
	if err != nil {
		return Health{}, err
	}

	reachable := true
	detail := ""
	if pinger, ok := d.Embedder.(interface {
		Ping(context.Context) error
	}); ok {
		pctx, cancel := context.WithTimeout(ctx, ollamaPingTimeout)
		defer cancel()
		if err := pinger.Ping(pctx); err != nil {
			reachable = false
			switch {
			case errors.Is(err, ollama.ErrModelNotLoaded):
				detail = "model not pulled (try `ollama pull " + meta.EmbeddingModel + "`)"
			case errors.Is(err, ollama.ErrUnreachable):
				detail = "ollama unreachable (start it with `ollama serve` or `brew services start ollama`)"
			default:
				detail = err.Error()
			}
		}
	}

	return Health{
		Status:          "ok",
		PID:             os.Getpid(),
		Home:            d.Home.Path,
		Version:         version.String(),
		SchemaVersion:   meta.SchemaVersion,
		EmbeddingModel:  meta.EmbeddingModel,
		EmbeddingDim:    meta.EmbeddingDim,
		EmbeddingDrift:  d.embeddingDrift(),
		GenerationModel: d.GenerationModel,
		GenerationRoles: d.generationRoles(),
		OllamaReachable: reachable,
		OllamaDetail:    detail,
		Upstreams:       d.upstreams(),
		YouTubeFetcher:  d.YouTubeFetcher,
		GitHubToken:     d.GitHubToken,
		Interests:       d.interestsState(),
		DocumentFields:  d.documentFields(),
		SavedDates:      d.SavedDates.State(),
		EmbedderHold:    d.embedderHold(),
		ReembedBrake:    d.reembedBrake(),
		Understanding:   d.understanding(),
	}, nil
}

// documentFields is what the daemon's start did to the documents' search
// fields, its time in UTC.
func (d Deps) documentFields() DocumentFields {
	fields := d.DocumentFields
	fields.CheckedAt = fields.CheckedAt.UTC()
	return fields
}

// generationRoles is the roles in use and their models: an empty object,
// never null, when none is.
func (d Deps) generationRoles() map[string]string {
	if d.GenerationRoles == nil {
		return map[string]string{}
	}
	return d.GenerationRoles
}

// Stats is the /v1/stats response. CardsStale is how many of the
// documents' cards are left to embed: the cards job's backlog. Reembed is
// what is left of the re-embed for a new chunk context, absent when
// nothing is.
type Stats struct {
	Version          string         `json:"version"`
	BookmarksTotal   int            `json:"bookmarks_total"`
	DocumentsTotal   int            `json:"documents_total"`
	DocumentsByState map[string]int `json:"documents_by_state,omitempty"`
	JobsByStatus     map[string]int `json:"jobs_by_status,omitempty"`
	CardsStale       int            `json:"cards_stale"`
	Reembed          *ReembedStats  `json:"reembed,omitempty"`
}

// ReembedStats is what is left to re-embed for a new chunk context
// (indexer.ContextVersion): the fetched documents holding chunks embedded
// under an older one, and those chunks. ChunksPerSecond is the re-embed's
// rate over its last 10 minutes, and ETASeconds how long the chunks left
// take at it; both are absent until it has been measured, and while the
// re-embed doesn't move (a paused queue, an Ollama that doesn't answer).
type ReembedStats struct {
	Documents       int     `json:"documents"`
	Chunks          int     `json:"chunks"`
	ChunksPerSecond float64 `json:"chunks_per_second,omitempty"`
	ETASeconds      int64   `json:"eta_seconds,omitempty"`
}

// ReembedProgress reports how far the re-embed got, and its brake;
// *jobs.Reembed is one.
type ReembedProgress interface {
	Progress() jobs.ReembedProgress
}

func (d Deps) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := d.stats(r.Context())
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, st)
}

// stats reports corpus and queue counts. A count that can't be read fails
// the whole answer: `curio import --follow` and `curio status` read absent
// fields as zero.
func (d Deps) stats(ctx context.Context) (Stats, error) {
	bookmarks, err := d.Bookmarks.Count(ctx, d.TenantID)
	if err != nil {
		return Stats{}, err
	}
	docsByState, err := d.Documents.CountByState(ctx, d.TenantID)
	if err != nil {
		return Stats{}, err
	}
	jobsByStatus, err := d.Queue.CountByStatus(ctx, d.TenantID)
	if err != nil {
		return Stats{}, err
	}
	cardsStale, err := d.cardsStale(ctx)
	if err != nil {
		return Stats{}, err
	}
	reembed, err := d.reembedStats(ctx)
	if err != nil {
		return Stats{}, err
	}

	docsTotal := 0
	for _, n := range docsByState {
		docsTotal += n
	}
	return Stats{
		Version:          version.String(),
		BookmarksTotal:   bookmarks,
		DocumentsTotal:   docsTotal,
		DocumentsByState: stringKeys(docsByState),
		JobsByStatus:     stringKeys(jobsByStatus),
		CardsStale:       cardsStale,
		Reembed:          reembed,
	}, nil
}

// reembedStats counts what is left to re-embed, with the re-embed's rate
// and the time left at it while it runs; nil when nothing is left.
func (d Deps) reembedStats(ctx context.Context) (*ReembedStats, error) {
	left, err := d.Documents.CountToReembed(ctx, d.TenantID, indexer.ContextVersion)
	if err != nil || left.Documents == 0 {
		return nil, err
	}
	out := &ReembedStats{Documents: left.Documents, Chunks: left.Chunks}
	if d.Reembed != nil {
		if p := d.Reembed.Progress(); p.Active && p.ChunksPerSecond > 0 {
			out.ChunksPerSecond = p.ChunksPerSecond
			out.ETASeconds = int64(p.ETA(left.Chunks) / time.Second)
		}
	}
	return out, nil
}

// cardsStale counts the cards left to embed; none without a document
// index, as reindex-all makes none stale without one.
func (d Deps) cardsStale(ctx context.Context) (int, error) {
	if d.Index == nil {
		return 0, nil
	}
	return d.Index.CountStaleCards(ctx, d.TenantID)
}

// stringKeys converts a count map keyed by a store enum to the wire shape.
func stringKeys[K ~string](m map[K]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, n := range m {
		out[string(k)] = n
	}
	return out
}
