package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// InterestMapResponse is the body of GET /v1/interests/map: the latest
// rebuild's map, one response per run. Areas come in TopGroups' order and
// interests area by area in each area's order (flat: TopGroups' order); an
// index into either is a position in its list, -1 for none. The documents
// are columns, each the same length: those the rebuild assigned, by ID,
// then those placed since, by ID.
type InterestMapResponse struct {
	RunID      string             `json:"run_id"`
	ComputedAt time.Time          `json:"computed_at"`
	Shape      string             `json:"shape"`
	Map        InterestMapView    `json:"map"`
	Areas      []MapArea          `json:"areas"`
	Interests  []MapInterest      `json:"interests"`
	Documents  MapDocumentColumns `json:"documents"`
}

// InterestMapView is how the map was drawn, and the zoom view's dots and
// Unsorted's disc, on a square of side Extent.
type InterestMapView struct {
	Kind      string    `json:"kind"`
	TookMS    int64     `json:"took_ms"`
	Extent    float64   `json:"extent"`
	DotRadius float64   `json:"dot_radius"`
	Unsorted  MapCircle `json:"unsorted"`
}

// MapCircle is a circle on the map.
type MapCircle struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	R float64 `json:"r"`
}

// MapPoint is a point on the map.
type MapPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// MapArea is an area on the map: its circle in the zoom view and its
// label's anchor on the document map.
type MapArea struct {
	ID       string    `json:"id"`
	Label    string    `json:"label,omitempty"`
	Size     int       `json:"size"`
	Cohesion float64   `json:"cohesion"`
	Zoom     MapCircle `json:"zoom"`
	Anchor   MapPoint  `json:"anchor"`
}

// MapInterest is an interest on the map, with its area and its most
// similar interests, as indexes.
type MapInterest struct {
	ID       string       `json:"id"`
	Area     int          `json:"area"`
	Label    string       `json:"label,omitempty"`
	Size     int          `json:"size"`
	Cohesion float64      `json:"cohesion"`
	Zoom     MapCircle    `json:"zoom"`
	Anchor   MapPoint     `json:"anchor"`
	Similar  []MapSimilar `json:"similar"`
}

// MapSimilar is a similar interest, by index, and its centroid's cosine.
type MapSimilar struct {
	Interest int     `json:"interest"`
	Cosine   float64 `json:"cosine"`
}

// MapDocumentColumns are the map's documents as columns, those off the map
// left out (onMap). Interest is the interest a document is in (a member, a
// loose fit, or placed into it), -1 for Unsorted; Nearest an unsorted
// document's nearest interest, -1 otherwise (a document placed into
// Unsorted names none); Area the area of Interest, -1 for none or in the
// flat shape. Fit is member, loose, unsorted, or new (placed since the
// rebuild); Similarity the cosine to Interest's centroid, or for Unsorted
// to the nearest interest's. Title is the document's title, else its
// newest bookmark's, else its host, else its URL, on one line and cut to
// 200 characters. MX and MY are its place on the document map, ZX and ZY
// in the zoom view.
type MapDocumentColumns struct {
	ID         []string  `json:"id"`
	Title      []string  `json:"title"`
	Host       []string  `json:"host"`
	Interest   []int     `json:"interest"`
	Nearest    []int     `json:"nearest"`
	Area       []int     `json:"area"`
	Fit        []string  `json:"fit"`
	Similarity []float64 `json:"similarity"`
	MX         []float64 `json:"mx"`
	MY         []float64 `json:"my"`
	ZX         []float64 `json:"zx"`
	ZY         []float64 `json:"zy"`
}

// InterestMapProblemType is the problem type of a map that isn't there.
const InterestMapProblemType = "urn:curio:problem:interest-map-unavailable"

// Why there is no map: no rebuild is done, the latest drew none, its map
// failed, or the map is off.
const (
	mapNoRun  = "no_run"
	mapNoMap  = "no_map"
	mapFailed = "map_failed"
	mapOff    = "map_off"
)

// InterestMapUnavailable is the 404 problem of a map that isn't there:
// why, the run it would be the map of, and a failed map's error.
type InterestMapUnavailable struct {
	Problem
	Reason   string `json:"reason"`
	RunID    string `json:"run_id,omitempty"`
	MapError string `json:"map_error,omitempty"`
}

// mapUnavailableError is the latest rebuild having no map. Its message is
// the 404's detail.
type mapUnavailableError struct{ body InterestMapUnavailable }

func (e *mapUnavailableError) Error() string { return e.body.Detail }

func (*mapUnavailableError) problem() (int, string, string) {
	return http.StatusNotFound, "interest map unavailable", InterestMapProblemType
}

func (e *mapUnavailableError) withProblem(p Problem) any {
	b := e.body
	b.Problem = p
	return b
}

// unavailable is the error of run's missing map (run nil for no run).
func unavailable(run *store.InterestRun) error {
	b := InterestMapUnavailable{Reason: mapNoRun, Problem: Problem{
		Detail: "no rebuild of the interests is done yet: the first one draws the map"}}
	switch {
	case run == nil:
	case run.Map == nil:
		b.Reason, b.RunID = mapNoMap, run.ID
		b.Detail = "the latest rebuild drew no map: a rebuild to draw it is due, " +
			"and `curio interests rebuild` draws it now"
	default:
		b.Reason, b.RunID, b.MapError = mapFailed, run.ID, run.Map.Error
		b.Detail = fmt.Sprintf("the latest rebuild's map failed (%s): the next rebuild, once enough of the library "+
			"changes, or `curio interests rebuild`, draws it again", run.Map.Error)
	}
	return &mapUnavailableError{body: b}
}

// mapOffError is the map switched off in config.yaml.
func mapOffError() error {
	return &mapUnavailableError{body: InterestMapUnavailable{Reason: mapOff, Problem: Problem{
		Detail: "the interest map is off (insight.map: false in config.yaml): remove the setting, " +
			"or set it to true, and restart the daemon"}}}
}

func (d Deps) handleInterestMap(w http.ResponseWriter, r *http.Request) {
	resp, err := d.interestMap(r.Context())
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// mapRun is the latest done rebuild, whose map the endpoint serves and the
// map page draws, so that the two never disagree about whether there is
// one. A run with a built map comes with a nil error; a missing map is a
// *mapUnavailableError, with the run when there is one, for the page's
// counts. With the map off it reads nothing.
func (d Deps) mapRun(ctx context.Context) (*store.InterestRun, error) {
	if d.MapOff {
		return nil, mapOffError()
	}
	run, err := d.Insights.LatestRun(ctx, d.TenantID, store.InterestRunDone)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, unavailable(nil)
	case err != nil:
		return nil, err
	case run.Map == nil || run.Map.Status != store.MapBuilt:
		return run, unavailable(run)
	}
	return run, nil
}

// interestMap is the latest done rebuild's map. A missing map is a
// *mapUnavailableError, and so is every map with the map off, which reads
// nothing. The run, its groups and its documents are three reads, outside
// a transaction (every one of this store's takes the write lock): a
// rebuild that commits between them prunes the run read, so the map is
// read once more from the newer run, and a second change is answered as
// read.
func (d Deps) interestMap(ctx context.Context) (InterestMapResponse, error) {
	for attempt := 0; ; attempt++ {
		run, err := d.mapRun(ctx)
		if err != nil {
			return InterestMapResponse{}, err
		}
		groups, err := d.Insights.RunGroups(ctx, run.ID)
		if err != nil {
			return InterestMapResponse{}, err
		}
		docs, err := d.Insights.MapDocuments(ctx, run.ID)
		if err != nil {
			return InterestMapResponse{}, err
		}
		latest, err := d.Insights.LatestRun(ctx, d.TenantID, store.InterestRunDone)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return InterestMapResponse{}, err
		}
		if attempt > 0 || err == nil && latest.ID == run.ID {
			return mapResponse(run, groups, docs)
		}
	}
}

// mapResponse is a run's built map in the wire shape. A group without a
// place, or a group or document naming a group the run lacks, is an
// inconsistency, not a missing resource.
func mapResponse(run *store.InterestRun, groups []store.InterestGroup, docs []store.MapDocument) (InterestMapResponse, error) {
	if run.FinishedAt == nil {
		return InterestMapResponse{}, fmt.Errorf("done run %s has no finish time", run.ID)
	}
	m := run.Map
	resp := InterestMapResponse{RunID: run.ID, ComputedAt: run.FinishedAt.UTC(), Shape: string(run.Shape),
		Map: InterestMapView{Kind: string(m.Kind), TookMS: m.Took.Milliseconds(), Extent: store.MapExtent,
			DotRadius: m.DotRadius, Unsorted: MapCircle{X: m.Unsorted.X, Y: m.Unsorted.Y, R: m.Unsorted.R}},
		Areas: []MapArea{}, Interests: []MapInterest{}}
	areas, interests := orderGroups(groups)
	areaAt := make(map[string]int, len(areas))
	for i, g := range areas {
		if g.Map == nil {
			return InterestMapResponse{}, fmt.Errorf("area %s of run %s has no place on its map", g.ID, run.ID)
		}
		areaAt[g.ID] = i
		resp.Areas = append(resp.Areas, MapArea{ID: g.ID, Label: g.Label, Size: g.Size, Cohesion: g.Cohesion,
			Zoom: zoomOf(g.Map), Anchor: anchorOf(g.Map)})
	}
	interestAt := make(map[string]int, len(interests))
	for i, g := range interests {
		interestAt[g.ID] = i
	}
	for _, g := range interests {
		if g.Map == nil {
			return InterestMapResponse{}, fmt.Errorf("interest %s of run %s has no place on its map", g.ID, run.ID)
		}
		in := MapInterest{ID: g.ID, Area: -1, Label: g.Label, Size: g.Size, Cohesion: g.Cohesion, Zoom: zoomOf(g.Map),
			Anchor: anchorOf(g.Map), Similar: make([]MapSimilar, 0, len(g.Similar))}
		if a, ok := areaAt[g.ParentID]; ok {
			in.Area = a
		}
		for _, s := range g.Similar {
			j, ok := interestAt[s.ID]
			if !ok {
				return InterestMapResponse{}, fmt.Errorf("interest %s of run %s is similar to %s, which the run lacks", g.ID, run.ID, s.ID)
			}
			in.Similar = append(in.Similar, MapSimilar{Interest: j, Cosine: s.Cosine})
		}
		resp.Interests = append(resp.Interests, in)
	}
	var err error
	resp.Documents, err = documentColumns(docs, interestAt, resp.Interests, resp.Map.Unsorted)
	return resp, err
}

// orderGroups splits a run's groups into its areas, in TopGroups' order
// (size, then cohesion, both descending, then ID), and its interests, area
// by area in that order (in the flat shape, the interests in it).
func orderGroups(groups []store.InterestGroup) (areas, interests []store.InterestGroup) {
	byOrder := func(a, b store.InterestGroup) int {
		return cmp.Or(cmp.Compare(b.Size, a.Size), cmp.Compare(b.Cohesion, a.Cohesion), strings.Compare(a.ID, b.ID))
	}
	for _, g := range groups {
		if g.Level == store.InterestLevelArea {
			areas = append(areas, g)
		} else {
			interests = append(interests, g)
		}
	}
	slices.SortFunc(areas, byOrder)
	areaAt := make(map[string]int, len(areas))
	for i, a := range areas {
		areaAt[a.ID] = i
	}
	slices.SortFunc(interests, func(a, b store.InterestGroup) int {
		return cmp.Or(cmp.Compare(areaRank(areaAt, a.ParentID), areaRank(areaAt, b.ParentID)), byOrder(a, b))
	})
	return areas, interests
}

// areaRank is an area's place in the order, -1 for none.
func areaRank(areaAt map[string]int, id string) int {
	if i, ok := areaAt[id]; ok {
		return i
	}
	return -1
}

func zoomOf(g *store.GroupMap) MapCircle  { return MapCircle{X: g.ZoomX, Y: g.ZoomY, R: g.ZoomR} }
func anchorOf(g *store.GroupMap) MapPoint { return MapPoint{X: g.AnchorX, Y: g.AnchorY} }

// mapTitleRunes is the longest a map document's title is.
const mapTitleRunes = 200

// documentColumns are the map's documents as columns, leaving out those
// off the map (onMap).
func documentColumns(docs []store.MapDocument, interestAt map[string]int, interests []MapInterest,
	unsorted MapCircle) (MapDocumentColumns, error) {
	n := len(docs)
	c := MapDocumentColumns{ID: make([]string, 0, n), Title: make([]string, 0, n), Host: make([]string, 0, n),
		Interest: make([]int, 0, n), Nearest: make([]int, 0, n), Area: make([]int, 0, n), Fit: make([]string, 0, n),
		Similarity: make([]float64, 0, n), MX: make([]float64, 0, n), MY: make([]float64, 0, n), ZX: make([]float64, 0, n),
		ZY: make([]float64, 0, n)}
	index := func(id string) (int, error) {
		if id == "" {
			return -1, nil
		}
		i, ok := interestAt[id]
		if !ok {
			return 0, fmt.Errorf("interest %s isn't one of the run's", id)
		}
		return i, nil
	}
	for _, d := range docs {
		interest, err := index(d.InterestID)
		if err != nil {
			return MapDocumentColumns{}, fmt.Errorf("document %s: %w", d.DocumentID, err)
		}
		circle := unsorted
		if interest >= 0 {
			circle = interests[interest].Zoom
		}
		if !onMap(d, circle) {
			continue
		}
		nearest := -1
		if !d.Placed && d.Fit == store.InterestFitUnsorted {
			if nearest, err = index(d.NearestID); err != nil {
				return MapDocumentColumns{}, fmt.Errorf("document %s: %w", d.DocumentID, err)
			}
		}
		area, fit := -1, string(d.Fit)
		if interest >= 0 {
			area = interests[interest].Area
		}
		if d.Placed {
			fit = fitNew
		}
		h := ui.Host(d.URL)
		c.ID, c.Title, c.Host = append(c.ID, d.DocumentID), append(c.Title, mapTitle(d, h)), append(c.Host, h)
		c.Interest, c.Nearest, c.Area = append(c.Interest, interest), append(c.Nearest, nearest), append(c.Area, area)
		c.Fit, c.Similarity = append(c.Fit, fit), append(c.Similarity, d.Similarity)
		c.MX, c.MY = append(c.MX, d.Map.MapX), append(c.MY, d.Map.MapY)
		c.ZX, c.ZY = append(c.ZX, d.Map.ZoomX), append(c.ZY, d.Map.ZoomY)
	}
	return c, nil
}

// onMap reports whether d has a place on the map: any place, for a
// document the run assigned; for one placed since, a dot whose centre lies
// inside circle, its interest's or Unsorted's disc. Placing with the map
// off gives a document no place, and so does 2.5.x, where moving one into
// another interest keeps the place it had. Such a document is left out
// until the placement sweep repairs it, rather than drawn in a circle it
// isn't in or given a place no row holds.
func onMap(d store.MapDocument, circle MapCircle) bool {
	if d.Map == nil {
		return false
	}
	if !d.Placed {
		return true
	}
	dx, dy := d.Map.ZoomX-circle.X, d.Map.ZoomY-circle.Y
	return dx*dx+dy*dy <= circle.R*circle.R
}

// mapTitle names a document on the map: its title, else its newest
// bookmark's, else its host, else its URL, on one line, cut to
// mapTitleRunes.
func mapTitle(d store.MapDocument, host string) string {
	for _, s := range []string{d.Title, d.BookmarkTitle, host, d.URL} {
		if t := strings.Join(strings.Fields(s), " "); t != "" {
			if r := []rune(t); len(r) > mapTitleRunes {
				t = strings.TrimSpace(string(r[:mapTitleRunes]))
			}
			return t
		}
	}
	return ""
}
