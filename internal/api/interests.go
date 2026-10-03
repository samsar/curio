package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
)

// InterestMember is one document of an interest: a member, a loose fit, or
// a document placed into it since the rebuild (Fit says which).
// BookmarkTitle names an untitled one, as DocumentListItem's does.
type InterestMember struct {
	DocID         string  `json:"doc_id"`
	Title         string  `json:"title,omitempty"`
	BookmarkTitle string  `json:"bookmark_title,omitempty"`
	URL           string  `json:"url"`
	State         string  `json:"state"`
	MarkdownPath  string  `json:"markdown_path,omitempty"`
	Similarity    float64 `json:"similarity"`
	Fit           string  `json:"fit"`
}

// The fits a document is listed with: a member, a loose fit, unsorted, or
// placed since the rebuild (new).
const (
	fitMember   = string(store.InterestFitMember)
	fitLoose    = string(store.InterestFitLoose)
	fitUnsorted = string(store.InterestFitUnsorted)
	fitNew      = "new"
)

// InterestRef names an identity an event involves.
type InterestRef struct {
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	Retired bool   `json:"retired"`
}

// InterestEvent is what a rebuild did to an identity: kept, moved (Area is
// the area the interest is in now), split or merged, from an old identity
// to a new one, sharing Shared of its members; dissolved, from one alone;
// or new, to one alone.
type InterestEvent struct {
	Event  string       `json:"event"`
	Level  string       `json:"level"`
	From   *InterestRef `json:"from,omitempty"`
	To     *InterestRef `json:"to,omitempty"`
	Area   *InterestRef `json:"area,omitempty"`
	Shared int          `json:"shared,omitempty"`
}

// The events that have no lineage row.
const (
	eventDissolved = "dissolved"
	eventNew       = "new"
)

// InterestResponse is an area or an interest as the latest rebuild found
// it. An area carries NumChildren and some of its interests; an interest
// some of its members, and on its own page the documents placed into it
// since (NewMembers) and its events.
type InterestResponse struct {
	ID          string             `json:"id"`
	RunID       string             `json:"run_id"`
	Level       string             `json:"level"`
	ParentID    string             `json:"parent_id,omitempty"`
	ParentLabel string             `json:"parent_label,omitempty"`
	Label       string             `json:"label,omitempty"`
	Summary     string             `json:"summary,omitempty"`
	Size        int                `json:"size"`
	Loose       int                `json:"loose"`
	New         int                `json:"new"`
	Cohesion    float64            `json:"cohesion"`
	NumChildren int                `json:"num_children,omitempty"`
	Children    []InterestResponse `json:"children,omitempty"`
	Members     []InterestMember   `json:"members,omitempty"`
	NewMembers  []InterestMember   `json:"new_members,omitempty"`
	Events      []InterestEvent    `json:"events,omitempty"`
}

// InterestRebuild is what the latest rebuild was and did.
type InterestRebuild struct {
	Trigger          string `json:"trigger"`
	Kind             string `json:"kind"`
	SplitCheck       bool   `json:"split_check"`
	ChangedDocuments int    `json:"changed_documents"`
	Kept             int    `json:"kept"`
	Created          int    `json:"created"`
	Split            int    `json:"split"`
	Merged           int    `json:"merged"`
	Moved            int    `json:"moved"`
	Dissolved        int    `json:"dissolved"`
}

// InterestsState is where automatic rebuilds of the interests stand, as
// the interest scheduler's last check found them (insight.Snapshot): on
// healthz, and as GET /v1/interests' next. State is one of the scheduler's
// (insight.RebuildState), or off with the insight layer off; it is unknown
// only before the daemon's first check, and clients read a state they
// don't know as current. The fields that don't apply are left out.
type InterestsState struct {
	State string `json:"state"`
	// The done rebuild's finish, kind and trigger.
	LastRebuildAt time.Time `json:"last_rebuild_at,omitzero"`
	LastKind      string    `json:"last_kind,omitempty"`
	LastTrigger   string    `json:"last_trigger,omitempty"`
	// ChangedDocuments have changed since the done rebuild, and RebuildAt
	// make the next due; before the first, the fetched documents and how
	// many the first waits for.
	ChangedDocuments int       `json:"changed_documents,omitempty"`
	RebuildAt        int       `json:"rebuild_at,omitempty"`
	DueSince         time.Time `json:"due_since,omitzero"`
	FreshOwed        string    `json:"fresh_owed,omitempty"`
	HeldReason       string    `json:"held_reason,omitempty"`
	RetryAt          time.Time `json:"retry_at,omitzero"`
	LastError        string    `json:"last_error,omitempty"`
}

// stateOff is the state of rebuilds with the insight layer off: there is
// no scheduler.
const stateOff = "off"

// InterestScheduler queues the interests' rebuilds as the library changes:
// the daemon's is an *insight.Scheduler.
type InterestScheduler interface {
	// Snapshot is where rebuilds stood at the last check; no database
	// read.
	Snapshot() insight.Snapshot
	// Kick asks for a check now, without waiting for it.
	Kick()
}

// interestsState is where automatic rebuilds stand: the scheduler's
// snapshot, never a query.
func (d Deps) interestsState() InterestsState {
	switch {
	case !d.InsightEnabled:
		return InterestsState{State: stateOff}
	case d.Interests == nil:
		return InterestsState{State: string(insight.StateUnknown)}
	}
	s := d.Interests.Snapshot()
	return InterestsState{
		State: string(s.State), LastRebuildAt: s.LastRebuildAt.UTC(), LastKind: string(s.LastKind),
		LastTrigger: string(s.LastTrigger), ChangedDocuments: s.Changed, RebuildAt: s.RebuildAt,
		DueSince: s.DueSince.UTC(), FreshOwed: s.FreshOwed, HeldReason: s.HeldReason, RetryAt: s.RetryAt.UTC(),
		LastError: s.LastError,
	}
}

// kickInterests asks the scheduler to check now, when there is one: after
// a change of what it decides on.
func (d Deps) kickInterests() {
	if d.Interests != nil {
		d.Interests.Kick()
	}
}

// InterestListResponse is the body of GET /v1/interests: a page of the
// latest rebuild's top-level groups, with what the run found and where the
// next rebuild stands. The run fields are absent before the first rebuild
// is done. NumNew counts the documents placed since the rebuild, and
// NumNewUnsorted those of them placed into Unsorted.
type InterestListResponse struct {
	RunID          string             `json:"run_id,omitempty"`
	ComputedAt     *time.Time         `json:"computed_at,omitempty"`
	Algo           string             `json:"algo,omitempty"`
	Shape          string             `json:"shape,omitempty"`
	NumDocuments   int                `json:"num_documents"`
	NumAreas       int                `json:"num_areas"`
	NumInterests   int                `json:"num_interests"`
	NumLoose       int                `json:"num_loose"`
	NumUnsorted    int                `json:"num_unsorted"`
	NumNew         int                `json:"num_new"`
	NumNewUnsorted int                `json:"num_new_unsorted"`
	Total          int                `json:"total"`
	Rebuild        *InterestRebuild   `json:"rebuild,omitempty"`
	Next           InterestsState     `json:"next"`
	Items          []InterestResponse `json:"items"`
}

// UnsortedMember is a document in no interest, with the interest it is
// nearest to, when there is one.
type UnsortedMember struct {
	DocID         string  `json:"doc_id"`
	Title         string  `json:"title,omitempty"`
	BookmarkTitle string  `json:"bookmark_title,omitempty"`
	URL           string  `json:"url"`
	State         string  `json:"state"`
	MarkdownPath  string  `json:"markdown_path,omitempty"`
	Similarity    float64 `json:"similarity"`
	NearestID     string  `json:"nearest_id,omitempty"`
	NearestLabel  string  `json:"nearest_label,omitempty"`
}

// UnsortedPage is the body of GET /v1/interests/unsorted: a page of the
// latest rebuild's unsorted documents, nearest first, and the documents
// placed in Unsorted since.
type UnsortedPage struct {
	RunID  string           `json:"run_id,omitempty"`
	Total  int              `json:"total"`
	NumNew int              `json:"num_new"`
	Items  []UnsortedMember `json:"items"`
	New    []UnsortedMember `json:"new"`
}

// InterestChanges is the body of GET /v1/interests/changes: what the
// latest rebuild did, without what it kept.
type InterestChanges struct {
	RunID      string           `json:"run_id,omitempty"`
	ComputedAt *time.Time       `json:"computed_at,omitempty"`
	Rebuild    *InterestRebuild `json:"rebuild,omitempty"`
	Events     []InterestEvent  `json:"events"`
}

// RetiredInterest is the 410 problem a retired identity answers with: when
// a rebuild retired it, which one, and what took its documents.
type RetiredInterest struct {
	Problem
	ID         string              `json:"id"`
	Level      string              `json:"level"`
	Label      string              `json:"label,omitempty"`
	RetiredAt  time.Time           `json:"retired_at"`
	RunID      string              `json:"run_id"`
	Successors []InterestSuccessor `json:"successors"`
}

// InterestSuccessor is an identity that took part of a retired one's
// documents.
type InterestSuccessor struct {
	ID      string `json:"id"`
	Level   string `json:"level"`
	Label   string `json:"label,omitempty"`
	Event   string `json:"event"`
	Shared  int    `json:"shared"`
	Retired bool   `json:"retired"`
}

// InterestRetiredProblemType is the problem type of a retired interest's
// 410.
const InterestRetiredProblemType = "urn:curio:problem:interest-retired"

// retiredInterestError is a retired identity asked for by its ID. Its
// message is the 410's detail.
type retiredInterestError struct{ body RetiredInterest }

func (e *retiredInterestError) Error() string { return e.body.Detail }

// Sizes for the interest endpoints. The list previews a few members of each
// interest; one interest shows many more.
const (
	defaultInterestLimit      = 50
	maxInterestLimit          = 500
	defaultInterestChildren   = 5
	maxInterestChildren       = 100
	defaultInterestMembers    = 3
	maxInterestMembers        = 100
	defaultOneInterestMembers = 100
	maxOneInterestMembers     = 1000
	// maxNewMembers is how many of the documents placed into an interest,
	// or into Unsorted, since the rebuild a response lists.
	maxNewMembers = 20
)

// levelInterest is the ?level that lists every interest.
const levelInterest = "interest"

func (d Deps) handleListInterests(w http.ResponseWriter, r *http.Request) {
	offset, err := offsetParam(r)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	level := r.URL.Query().Get("level")
	if level != "" && level != levelInterest {
		d.writeError(w, r, badRequest("level %q must be %s, or absent for the top-level groups", level, levelInterest))
		return
	}
	members := intQuery(r, "members", defaultInterestMembers, 0, maxInterestMembers)
	resp, err := d.interests(r.Context(), interestsOpts{
		Limit:        intQuery(r, "limit", defaultInterestLimit, 1, maxInterestLimit),
		Offset:       offset,
		Children:     intQuery(r, "children", defaultInterestChildren, 0, maxInterestChildren),
		Members:      members,
		ChildMembers: members,
		Flat:         level == levelInterest,
		State:        true,
	})
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// interestsOpts are a page of the latest rebuild's groups.
type interestsOpts struct {
	Limit    int // groups on the page, at least 1
	Offset   int // groups before it, in their order
	Children int // interests listed of each area; 0 for none
	// Members are the members listed of each interest on the page, and
	// ChildMembers of each of an area's interests listed; 0 for none.
	Members, ChildMembers int
	// Flat lists every interest, each with its area, rather than the
	// top-level groups.
	Flat bool
	// State says where the next rebuild stands: the Interests page reads
	// it apart, and the search home does without.
	State bool
	// Bare reads the groups alone, without the documents placed into them
	// or an area's interests, for a list that only names them.
	Bare bool
}

// interests returns a page of the latest done run's top-level groups (its
// areas, or its interests in the flat shape; every interest with Flat),
// largest first, areas with their largest interests, interests with their
// most similar members. Before the first done run it returns none, and no
// error. An offset past the run's groups is an empty page of that run.
func (d Deps) interests(ctx context.Context, opts interestsOpts) (InterestListResponse, error) {
	resp := InterestListResponse{Items: []InterestResponse{}}
	run, groups, err := d.groupPage(ctx, opts)
	if err == nil && len(groups) == 0 && opts.Offset < groupTotal(run, opts.Flat) {
		// The run and its groups are two reads. A rebuild that finished
		// between them has pruned the run, which left the page empty:
		// read it once more, from the newer run. A second miss is answered
		// as read, rather than chasing rebuilds.
		run, groups, err = d.groupPage(ctx, opts)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		run = nil
	case err != nil:
		return InterestListResponse{}, err
	}
	if opts.State {
		resp.Next = d.interestsState()
	}
	if run == nil {
		return resp, nil
	}
	resp.RunID, resp.ComputedAt, resp.Algo, resp.Shape = run.ID, run.FinishedAt, run.Grouper, string(run.Shape)
	resp.NumDocuments, resp.NumAreas, resp.NumInterests = run.NumDocuments, run.NumAreas, run.NumInterests
	resp.NumLoose, resp.NumUnsorted, resp.Total = run.NumLoose, run.NumUnsorted, groupTotal(run, opts.Flat)
	resp.Rebuild = rebuildOf(run)
	for _, g := range groups {
		resp.Items = append(resp.Items, interestResponse(run.ID, g))
	}
	if opts.Bare {
		return resp, nil
	}

	placed, err := d.Insights.PlacementCounts(ctx, run.ID)
	if err != nil {
		return InterestListResponse{}, err
	}
	for _, n := range placed {
		resp.NumNew += n
	}
	resp.NumNewUnsorted = placed[""]
	for i := range resp.Items {
		resp.Items[i].New = placed[resp.Items[i].ID]
	}
	listed, members := pointers(resp.Items), opts.Members // the interests whose members are listed
	if run.Shape == store.InterestShapeAreas && !opts.Flat {
		if listed, err = d.withChildren(ctx, run.ID, resp.Items, opts.Children, placed); err != nil {
			return InterestListResponse{}, err
		}
		members = opts.ChildMembers
	}
	if err := d.withMembers(ctx, run.ID, listed, members); err != nil {
		return InterestListResponse{}, err
	}
	return resp, nil
}

// groupPage reads the latest done run and the page of its groups opts
// names. No run is an error wrapping store.ErrNotFound.
func (d Deps) groupPage(ctx context.Context, opts interestsOpts) (*store.InterestRun, []store.InterestGroup, error) {
	run, err := d.Insights.LatestRun(ctx, d.TenantID, store.InterestRunDone)
	if err != nil {
		return nil, nil, err
	}
	read := d.Insights.TopGroups
	if opts.Flat && run.Shape == store.InterestShapeAreas {
		read = d.Insights.NestedGroups
	}
	groups, err := read(ctx, run.ID, opts.Limit, opts.Offset)
	if err != nil {
		return nil, nil, err
	}
	return run, groups, nil
}

// groupTotal is how many groups the list of run counts: its areas, or its
// interests in the flat shape or with flat.
func groupTotal(run *store.InterestRun, flat bool) int {
	if run.Shape == store.InterestShapeAreas && !flat {
		return run.NumAreas
	}
	return run.NumInterests
}

// withChildren gives each of areas its count of interests, the documents
// placed into them, and up to limit of them, reading the interests of
// every area in one read, and returns the interests it listed.
func (d Deps) withChildren(ctx context.Context, runID string, areas []InterestResponse, limit int, placed map[string]int) ([]*InterestResponse, error) {
	if len(areas) == 0 {
		return nil, nil
	}
	ids := make([]string, len(areas))
	for i, a := range areas {
		ids[i] = a.ID
	}
	children, err := d.Insights.ChildGroups(ctx, runID, ids)
	if err != nil {
		return nil, fmt.Errorf("load the areas' interests: %w", err)
	}
	byArea := make(map[string][]store.InterestGroup, len(areas))
	for _, c := range children {
		byArea[c.ParentID] = append(byArea[c.ParentID], c)
	}
	var listed []*InterestResponse
	for _, area := range pointers(areas) {
		cs := byArea[area.ID]
		area.NumChildren = len(cs)
		for _, c := range cs {
			area.New += placed[c.ID]
		}
		for _, c := range cs[:min(limit, len(cs))] {
			child := interestResponse(runID, c)
			child.New = placed[c.ID]
			area.Children = append(area.Children, child)
		}
		listed = append(listed, pointers(area.Children)...)
	}
	return listed, nil
}

// pointers are pointers to each of items, for the steps that fill them in.
func pointers(items []InterestResponse) []*InterestResponse {
	out := make([]*InterestResponse, len(items))
	for i := range items {
		out[i] = &items[i]
	}
	return out
}

// withMembers gives each of interests up to limit of its members, most
// similar first: a read of each interest's members, then one read of all
// their documents, however many interests and members there are. With
// limit 0, or no members to show, it reads no document.
func (d Deps) withMembers(ctx context.Context, runID string, interests []*InterestResponse, limit int) error {
	if limit <= 0 {
		return nil
	}
	pages := make([]memberPage, 0, len(interests))
	for _, in := range interests {
		rows, err := d.Insights.Members(ctx, runID, in.ID, store.InterestFitMember, limit, 0)
		if err != nil {
			return fmt.Errorf("interest %s: load members: %w", in.ID, err)
		}
		pages = append(pages, memberPage{into: &in.Members, rows: rows, fit: fitMember})
	}
	return d.hydrate(ctx, pages)
}

// memberPage is documents to list as an interest's members: rows, each
// listed with fit, into the list into.
type memberPage struct {
	into *[]InterestMember
	rows []store.InterestAssignment
	fit  string
}

// hydrate lists each page's documents, reading every one in one read.
// Rows cascade with their documents, so a row whose document is missing
// is an inconsistency, not a missing resource.
func (d Deps) hydrate(ctx context.Context, pages []memberPage) error {
	var ids []string
	for _, p := range pages {
		for _, row := range p.rows {
			ids = append(ids, row.DocumentID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	docs, err := d.memberDocuments(ctx, ids)
	if err != nil {
		return err
	}
	for _, p := range pages {
		for _, row := range p.rows {
			doc, ok := docs[row.DocumentID]
			if !ok {
				return fmt.Errorf("member document %s doesn't exist", row.DocumentID)
			}
			*p.into = append(*p.into, InterestMember{DocID: doc.ID, Title: deref(doc.Title),
				BookmarkTitle: doc.BookmarkTitle, URL: doc.URL, State: string(doc.State),
				MarkdownPath: d.contentPath(doc.MarkdownPath), Similarity: row.Similarity, Fit: p.fit})
		}
	}
	return nil
}

// memberDocuments reads the documents of members ids in one read, by ID.
func (d Deps) memberDocuments(ctx context.Context, ids []string) (map[string]store.DocumentWithError, error) {
	docs, err := d.Documents.GetByIDsWithLastError(ctx, d.TenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("load member documents: %w", err)
	}
	byID := make(map[string]store.DocumentWithError, len(docs))
	for _, doc := range docs {
		byID[doc.ID] = doc
	}
	return byID, nil
}

// rebuildOf is what run was and did.
func rebuildOf(run *store.InterestRun) *InterestRebuild {
	return &InterestRebuild{Trigger: string(run.Trigger), Kind: string(run.Kind), SplitCheck: run.SplitCheck,
		ChangedDocuments: run.ChangedDocuments, Kept: run.Kept, Created: run.Created, Split: run.Split,
		Merged: run.Merged, Moved: run.Moved, Dissolved: run.Dissolved}
}

// interestResponse is a group of run in the wire shape, without its
// children or members.
func interestResponse(runID string, g store.InterestGroup) InterestResponse {
	return InterestResponse{ID: g.ID, RunID: runID, Level: string(g.Level), ParentID: g.ParentID,
		ParentLabel: g.ParentLabel, Label: g.Label, Summary: g.Summary, Size: g.Size, Loose: g.Loose,
		Cohesion: g.Cohesion}
}

// handleGetInterest returns one area or interest of the latest rebuild.
func (d Deps) handleGetInterest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	offset, err := offsetParam(r)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	resp, err := d.interest(r.Context(), id, interestOpts{
		Members:     intQuery(r, "members", defaultOneInterestMembers, 0, maxOneInterestMembers),
		AreaMembers: intQuery(r, "members", defaultInterestMembers, 0, maxInterestMembers),
		Offset:      offset,
		NewMembers:  maxNewMembers,
	})
	if err != nil {
		d.writeLookupError(w, r, "interest", id, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// interestOpts are what one area or interest lists.
type interestOpts struct {
	// Members are an interest's members, then loose fits, listed from
	// Offset; AreaMembers each of an area's interests' members.
	Members, AreaMembers int
	Offset               int
	// NewMembers are the documents placed into an interest since the
	// rebuild that it lists, newest first; 0 reads none.
	NewMembers int
	// Window, when set, is the page of an area's interests that is listed
	// with members: Window of them from WindowOffset. Its count and new
	// count are still every interest's. The JSON lists every interest.
	Window, WindowOffset int
}

// interest returns the tenant's area or interest id as the latest rebuild
// found it. An identity that doesn't exist, or another tenant's, is an
// error wrapping store.ErrNotFound; a retired one a *retiredInterestError
// saying what became of it.
func (d Deps) interest(ctx context.Context, id string, opts interestOpts) (InterestResponse, error) {
	for attempt := 0; ; attempt++ {
		run, err := d.Insights.LatestRun(ctx, d.TenantID, store.InterestRunDone)
		var g *store.InterestGroup
		if err == nil {
			g, err = d.Insights.GetGroup(ctx, run.ID, id)
		}
		if err == nil {
			return d.describe(ctx, run, *g, opts)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return InterestResponse{}, err
		}
		// The run doesn't hold it. A rebuild that committed between the two
		// reads may have retired it, or pruned the run that was read: the
		// identity says which, and a live one is read once more from the
		// newest run.
		in, err := d.Insights.GetInterest(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound), err == nil && in.TenantID != d.TenantID:
			return InterestResponse{}, fmt.Errorf("interest %s: %w", id, store.ErrNotFound)
		case err != nil:
			return InterestResponse{}, err
		case in.RetiredAt != nil:
			return InterestResponse{}, d.retired(ctx, in)
		case attempt > 0:
			return InterestResponse{}, fmt.Errorf("interest %s is live, but no done run holds it", id)
		}
	}
}

// describe is group g of run with what its page lists: an area's
// interests, each with a few members; an interest's page of members, then
// loose fits, and the documents placed into it since; and either's events
// in the run.
func (d Deps) describe(ctx context.Context, run *store.InterestRun, g store.InterestGroup, opts interestOpts) (InterestResponse, error) {
	resp := interestResponse(run.ID, g)
	placed, err := d.Insights.PlacementCounts(ctx, run.ID)
	if err != nil {
		return InterestResponse{}, err
	}
	resp.New = placed[g.ID]
	if g.Level == store.InterestLevelArea {
		// Every interest of the area counts, and the window's are listed.
		items := []InterestResponse{resp}
		if _, err := d.withChildren(ctx, run.ID, items, math.MaxInt, placed); err != nil {
			return InterestResponse{}, err
		}
		resp = items[0]
		if opts.Window > 0 {
			resp.Children = window(resp.Children, opts.WindowOffset, opts.Window)
		}
		if err := d.withMembers(ctx, run.ID, pointers(resp.Children), opts.AreaMembers); err != nil {
			return InterestResponse{}, err
		}
	} else if err := d.withPage(ctx, run.ID, &resp, g, opts); err != nil {
		return InterestResponse{}, err
	}
	if resp.Events, err = d.groupEvents(ctx, run, g); err != nil {
		return InterestResponse{}, err
	}
	return resp, nil
}

// window is the size items of items from offset: none past the end.
func window[T any](items []T, offset, size int) []T {
	if offset >= len(items) {
		return nil
	}
	return items[offset:min(len(items), offset+size)]
}

// withPage gives interest in its page of members, then loose fits, from
// opts.Offset, and up to opts.NewMembers of the documents placed into it
// since, newest first, in one read of their documents.
func (d Deps) withPage(ctx context.Context, runID string, in *InterestResponse, g store.InterestGroup, opts interestOpts) error {
	var pages []memberPage
	limit, offset := opts.Members, opts.Offset
	if limit > 0 && offset < g.Size {
		rows, err := d.Insights.Members(ctx, runID, g.ID, store.InterestFitMember, limit, offset)
		if err != nil {
			return fmt.Errorf("interest %s: load members: %w", g.ID, err)
		}
		pages = append(pages, memberPage{into: &in.Members, rows: rows, fit: fitMember})
		limit -= len(rows)
	}
	if offset -= g.Size; limit > 0 && max(offset, 0) < g.Loose {
		rows, err := d.Insights.Members(ctx, runID, g.ID, store.InterestFitLoose, limit, max(offset, 0))
		if err != nil {
			return fmt.Errorf("interest %s: load loose fits: %w", g.ID, err)
		}
		pages = append(pages, memberPage{into: &in.Members, rows: rows, fit: fitLoose})
	}
	if opts.NewMembers > 0 {
		placed, err := d.Insights.Placements(ctx, runID, g.ID, opts.NewMembers)
		if err != nil {
			return fmt.Errorf("interest %s: load new members: %w", g.ID, err)
		}
		pages = append(pages, memberPage{into: &in.NewMembers, rows: placedRows(placed), fit: fitNew})
	}
	return d.hydrate(ctx, pages)
}

// placedRows are placements as the rows a member page lists.
func placedRows(placed []store.Placement) []store.InterestAssignment {
	rows := make([]store.InterestAssignment, len(placed))
	for i, p := range placed {
		rows[i] = store.InterestAssignment{DocumentID: p.DocumentID, InterestID: p.InterestID, Similarity: p.Similarity}
	}
	return rows
}

// groupEvents are g's events in run: the lineage rows naming it, old or
// new, and new when run created it and no row reaches it.
func (d Deps) groupEvents(ctx context.Context, run *store.InterestRun, g store.InterestGroup) ([]InterestEvent, error) {
	lineage, err := d.Insights.RunLineage(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	var rows []store.LineageRow
	reached := false
	for _, l := range lineage {
		if l.OldID == g.ID || l.NewID == g.ID {
			rows = append(rows, l)
		}
		reached = reached || l.NewID == g.ID
	}
	isNew := g.CreatedRunID == run.ID && !reached
	if len(rows) == 0 && !isNew {
		return nil, nil
	}
	refs, err := d.refs(ctx, rows, g.ID, g.ParentID)
	if err != nil {
		return nil, err
	}
	var events []InterestEvent
	for _, l := range rows {
		events = append(events, lineageEvent(l, string(g.Level), refs, g.ParentID))
	}
	if isNew {
		events = append(events, InterestEvent{Event: eventNew, Level: string(g.Level), To: refs.of(g.ID)})
	}
	slices.SortFunc(events, compareEvents)
	return events, nil
}

// lineageEvent is lineage row l as an event at level; area is the area a
// moved interest is in now.
func lineageEvent(l store.LineageRow, level string, refs interestRefs, area string) InterestEvent {
	e := InterestEvent{Event: string(l.Event), Level: level, From: refs.of(l.OldID), To: refs.of(l.NewID), Shared: l.Shared}
	if l.Event == store.LineageMoved && area != "" {
		e.Area = refs.of(area)
	}
	return e
}

// interestRefs are identities by ID, as events name them.
type interestRefs map[string]store.Interest

// refs reads the identities rows name, and extra, in one read.
func (d Deps) refs(ctx context.Context, rows []store.LineageRow, extra ...string) (interestRefs, error) {
	var ids []string
	for _, l := range rows {
		ids = append(ids, l.OldID, l.NewID)
	}
	for _, id := range extra {
		if id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	got, err := d.Insights.GetInterests(ctx, d.TenantID, slices.Compact(ids))
	if err != nil {
		return nil, fmt.Errorf("load interests: %w", err)
	}
	out := make(interestRefs, len(got))
	for _, in := range got {
		out[in.ID] = in
	}
	return out, nil
}

// of is identity id as a reference.
func (r interestRefs) of(id string) *InterestRef {
	in := r[id]
	return &InterestRef{ID: id, Label: in.Label, Retired: in.RetiredAt != nil}
}

// retired is the error a retired identity is answered with: when it was
// retired, by which run, and its successors there.
func (d Deps) retired(ctx context.Context, in *store.Interest) error {
	rows, err := d.Insights.Successors(ctx, in.RetiredRunID, in.ID)
	if err != nil {
		return err
	}
	refs, err := d.refs(ctx, rows)
	if err != nil {
		return err
	}
	// The successor that took the most first, as carry-over ranks them.
	slices.SortStableFunc(rows, func(a, b store.LineageRow) int {
		return cmp.Or(cmp.Compare(b.Shared, a.Shared), strings.Compare(refs[a.NewID].Label, refs[b.NewID].Label))
	})
	body := RetiredInterest{ID: in.ID, Level: string(in.Level), Label: in.Label, RetiredAt: *in.RetiredAt,
		RunID: in.RetiredRunID, Successors: make([]InterestSuccessor, 0, len(rows))}
	for _, l := range rows {
		s := refs[l.NewID]
		body.Successors = append(body.Successors, InterestSuccessor{ID: l.NewID, Level: string(s.Level), Label: s.Label,
			Event: string(l.Event), Shared: l.Shared, Retired: s.RetiredAt != nil})
	}
	body.Detail = retiredDetail(body)
	return &retiredInterestError{body: body}
}

// retiredDetail says what became of a retired identity, in a sentence.
func retiredDetail(b RetiredInterest) string {
	name := func(id, label string) string {
		if label == "" {
			return id
		}
		return fmt.Sprintf("%q", label)
	}
	what := "dissolved: its documents went to other interests or to Unsorted"
	var parts []string
	for _, event := range []store.LineageEvent{store.LineageSplit, store.LineageMerged} {
		var names []string
		for _, s := range b.Successors {
			if s.Event == string(event) {
				names = append(names, name(s.ID, s.Label))
			}
		}
		if len(names) > 0 {
			parts = append(parts, fmt.Sprintf("%s into %s", event, joinAnd(names)))
		}
	}
	if len(parts) > 0 {
		what = strings.Join(parts, ", and ")
	}
	return fmt.Sprintf("%s %s was retired by the rebuild of %s: it %s", b.Level, name(b.ID, b.Label),
		b.RetiredAt.UTC().Format(time.DateOnly), what)
}

// joinAnd joins names as a list: "A", "A and B", "A, B and C".
func joinAnd(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func (d Deps) handleUnsorted(w http.ResponseWriter, r *http.Request) {
	offset, err := offsetParam(r)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	resp, err := d.unsorted(r.Context(), intQuery(r, "limit", defaultInterestLimit, 1, maxInterestLimit), offset)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// unsorted returns a page of the latest rebuild's unsorted documents, most
// similar to their nearest interest first, and the newest documents placed
// in Unsorted since, reading every document in one read.
func (d Deps) unsorted(ctx context.Context, limit, offset int) (UnsortedPage, error) {
	resp := UnsortedPage{Items: []UnsortedMember{}, New: []UnsortedMember{}}
	run, rows, err := d.unsortedPage(ctx, limit, offset)
	if err == nil && len(rows) == 0 && offset < run.NumUnsorted {
		run, rows, err = d.unsortedPage(ctx, limit, offset) // pruned mid-read: see interests
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return resp, nil
	case err != nil:
		return UnsortedPage{}, err
	}
	resp.RunID, resp.Total = run.ID, run.NumUnsorted
	placed, err := d.Insights.Placements(ctx, run.ID, "", maxNewMembers)
	if err != nil {
		return UnsortedPage{}, err
	}
	counts, err := d.Insights.PlacementCounts(ctx, run.ID)
	if err != nil {
		return UnsortedPage{}, err
	}
	resp.NumNew = counts[""]
	var ids []string
	for _, row := range slices.Concat(rows, placedRows(placed)) {
		ids = append(ids, row.DocumentID)
	}
	if len(ids) == 0 {
		return resp, nil
	}
	docs, err := d.memberDocuments(ctx, ids)
	if err != nil {
		return UnsortedPage{}, err
	}
	if resp.Items, err = d.unsortedMembers(rows, docs); err != nil {
		return UnsortedPage{}, err
	}
	if resp.New, err = d.unsortedMembers(placedRows(placed), docs); err != nil {
		return UnsortedPage{}, err
	}
	return resp, nil
}

// unsortedPage reads the latest done run and a page of its unsorted
// documents. No run is an error wrapping store.ErrNotFound.
func (d Deps) unsortedPage(ctx context.Context, limit, offset int) (*store.InterestRun, []store.InterestAssignment, error) {
	run, err := d.Insights.LatestRun(ctx, d.TenantID, store.InterestRunDone)
	if err != nil {
		return nil, nil, err
	}
	rows, err := d.Insights.Unsorted(ctx, run.ID, limit, offset)
	if err != nil {
		return nil, nil, err
	}
	return run, rows, nil
}

// unsortedMembers are rows in the wire shape, hydrated from docs.
func (d Deps) unsortedMembers(rows []store.InterestAssignment, docs map[string]store.DocumentWithError) ([]UnsortedMember, error) {
	out := make([]UnsortedMember, 0, len(rows))
	for _, row := range rows {
		doc, ok := docs[row.DocumentID]
		if !ok {
			return nil, fmt.Errorf("unsorted document %s doesn't exist", row.DocumentID)
		}
		out = append(out, UnsortedMember{DocID: doc.ID, Title: deref(doc.Title), BookmarkTitle: doc.BookmarkTitle,
			URL: doc.URL, State: string(doc.State), MarkdownPath: d.contentPath(doc.MarkdownPath),
			Similarity: row.Similarity, NearestID: row.NearestID, NearestLabel: row.NearestLabel})
	}
	return out, nil
}

// documentPlace is where the latest done rebuild put document id: its
// assignment, or the placement made since. The document page's line is
// its one reader: the document's JSON, which the CLI and MCP share, leaves
// it out. No done run, or a document it has nowhere, is an error wrapping
// store.ErrNotFound.
func (d Deps) documentPlace(ctx context.Context, id string) (*store.DocumentPlace, error) {
	run, err := d.Insights.LatestRun(ctx, d.TenantID, store.InterestRunDone)
	if err != nil {
		return nil, err
	}
	return d.Insights.DocumentPlace(ctx, run.ID, id)
}

func (d Deps) handleInterestChanges(w http.ResponseWriter, r *http.Request) {
	resp, err := d.interestChanges(r.Context())
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// interestChanges is what the latest rebuild did: its lineage rows but the
// kept ones, the identities it retired without a row (dissolved), and
// those it created that no split or merge reaches (new), in eventOrder.
func (d Deps) interestChanges(ctx context.Context) (InterestChanges, error) {
	resp := InterestChanges{Events: []InterestEvent{}}
	run, err := d.Insights.LatestRun(ctx, d.TenantID, store.InterestRunDone)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return resp, nil
	case err != nil:
		return InterestChanges{}, err
	}
	resp.RunID, resp.ComputedAt, resp.Rebuild = run.ID, run.FinishedAt, rebuildOf(run)
	lineage, err := d.Insights.RunLineage(ctx, run.ID)
	if err != nil {
		return InterestChanges{}, err
	}
	created, err := d.Insights.CreatedBy(ctx, run.ID)
	if err != nil {
		return InterestChanges{}, err
	}
	retired, err := d.Insights.RetiredBy(ctx, d.TenantID, run.ID)
	if err != nil {
		return InterestChanges{}, err
	}
	areaOf, err := d.areasOfMoved(ctx, run, lineage)
	if err != nil {
		return InterestChanges{}, err
	}
	var rows []store.LineageRow
	var extra []string
	old, reached := map[string]bool{}, map[string]bool{}
	for _, l := range lineage {
		old[l.OldID] = true
		if l.Event == store.LineageSplit || l.Event == store.LineageMerged {
			reached[l.NewID] = true
		}
		if l.Event != store.LineageKept {
			rows = append(rows, l)
			extra = append(extra, areaOf[l.NewID])
		}
	}
	refs, err := d.refs(ctx, rows, extra...)
	if err != nil {
		return InterestChanges{}, err
	}
	for _, in := range slices.Concat(retired, created) {
		refs[in.ID] = in
	}
	for _, l := range rows {
		resp.Events = append(resp.Events, lineageEvent(l, string(refs[l.OldID].Level), refs, areaOf[l.NewID]))
	}
	for _, in := range retired {
		if !old[in.ID] {
			resp.Events = append(resp.Events, InterestEvent{Event: eventDissolved, Level: string(in.Level), From: refs.of(in.ID)})
		}
	}
	for _, in := range created {
		if !reached[in.ID] {
			resp.Events = append(resp.Events, InterestEvent{Event: eventNew, Level: string(in.Level), To: refs.of(in.ID)})
		}
	}
	slices.SortFunc(resp.Events, compareEvents)
	return resp, nil
}

// areasOfMoved are the areas the interests run moved are in now, by
// interest, read only when it moved one.
func (d Deps) areasOfMoved(ctx context.Context, run *store.InterestRun, lineage []store.LineageRow) (map[string]string, error) {
	out := map[string]string{}
	if !slices.ContainsFunc(lineage, func(l store.LineageRow) bool { return l.Event == store.LineageMoved }) {
		return out, nil
	}
	nested, err := d.Insights.NestedGroups(ctx, run.ID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("load the areas of moved interests: %w", err)
	}
	for _, g := range nested {
		out[g.ID] = g.ParentID
	}
	return out, nil
}

// eventOrder ranks events: kept, which a rebuild's changes leave out,
// first, then the changes in the order they are listed.
var eventOrder = []string{string(store.LineageKept), string(store.LineageSplit), string(store.LineageMerged),
	string(store.LineageMoved), eventDissolved, eventNew}

// compareEvents orders changes by event, then level (areas first), then
// the identity the event is about (its old one, or the new one of a new
// event) by label and ID, then the new one by ID.
func compareEvents(a, b InterestEvent) int {
	subject := func(e InterestEvent) *InterestRef { return cmp.Or(e.From, e.To) }
	sa, sb := subject(a), subject(b)
	return cmp.Or(
		cmp.Compare(slices.Index(eventOrder, a.Event), slices.Index(eventOrder, b.Event)),
		cmp.Compare(levelRank(a.Level), levelRank(b.Level)),
		strings.Compare(sa.Label, sb.Label), strings.Compare(sa.ID, sb.ID),
		strings.Compare(refID(a.To), refID(b.To)),
	)
}

func levelRank(level string) int {
	if level == string(store.InterestLevelArea) {
		return 0
	}
	return 1
}

func refID(r *InterestRef) string {
	if r == nil {
		return ""
	}
	return r.ID
}

// handleRebuildInterests queues a rebuild of the interests now and returns
// 202 + job_id: the new job's, or the pending one's when one is queued
// already. The scheduler's threshold, settle window, drift hold and
// backoff don't apply; the queue's gate does. With ?fresh=1 the rebuild is
// fresh: a fresh rebuild is owed, which the job reads when it runs, so the
// request holds when the job that answers it was pending already.
// Refused with 409 when the insight layer is disabled in config.
func (d Deps) handleRebuildInterests(w http.ResponseWriter, r *http.Request) {
	if !d.InsightEnabled {
		writeProblem(w, r, http.StatusConflict, "insight disabled",
			"the insight layer is disabled; set insight.enabled: true in config.yaml")
		return
	}
	if boolParam(r, "fresh") {
		if err := d.Insights.OweFresh(r.Context(), d.TenantID, store.FreshManual); err != nil {
			d.writeError(w, r, err)
			return
		}
	}
	job, _, err := jobs.EnqueueRebuild(r.Context(), d.Queue, d.TenantID, store.RunTriggerManual)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.kickInterests()
	d.writeJSON(w, r, http.StatusAccepted, map[string]string{"job_id": job.ID})
}
