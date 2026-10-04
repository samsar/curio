package main

import (
	"fmt"
	"runtime"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/quality"
	"github.com/samsar/curio/internal/store"
)

// The map's measures: the document map's neighbourhoods (NP5 and NP15) and
// areas (purity@5) of a cold map of the whole library; how far a warm map
// moves the documents and the interests' centres after a 5% change; how far
// another seed moves a cold map; and what drawing one costs.
const (
	npNear = 5
	npFar  = 15
	purity = 5
)

// mapKeys name a grouping's groups for BuildMap, as the engine's identity
// IDs would: each area and interest, and a new group's start.
type mapKeys struct {
	areas, interests           []string
	areaStarts, interestStarts []string
}

// labelKeys name a grouping's groups by their zero-padded labels, prefixed
// by level so an area never shares an interest's key.
func labelKeys(gp *grouping) mapKeys {
	var k mapKeys
	for a := range numGroups(gp.g.Area) {
		k.areas = append(k.areas, areaKey(groupID(a)))
	}
	for l := range numGroups(gp.g.Interest) {
		k.interests = append(k.interests, interestKey(groupID(l)))
	}
	return k
}

func areaKey(id string) string     { return "a" + id }
func interestKey(id string) string { return "i" + id }

// carriedKeys name next's groups as the engine's carry-over names them
// from prev's (labelKeys): a group that inherits an old one's identity
// gets its key, a new one a key of its own and, as its start, the old
// group it shares the most members with.
func carriedKeys(prev, next *grouping) (mapKeys, error) {
	areas, interests, err := carryOver(prev, next)
	if err != nil {
		return mapKeys{}, err
	}
	var k mapKeys
	k.areas, k.areaStarts = keysOf(areas, areaKey, "a-new-%d")
	k.interests, k.interestStarts = keysOf(interests, interestKey, "i-new-%d")
	return k, nil
}

// keysOf are a level's keys and starts from its carry-over, the starts the
// engine's (Carried.Starts) under the report's keys.
func keysOf(c insight.Carried, key func(string) string, fresh string) (keys, starts []string) {
	keys = make([]string, len(c.Predecessor))
	for j, p := range c.Predecessor {
		keys[j] = key(p)
		if p == "" {
			keys[j] = fmt.Sprintf(fresh, j)
		}
	}
	starts = c.Starts()
	for j, s := range starts {
		if s != "" {
			starts[j] = key(s)
		}
	}
	return keys, starts
}

// mapInput is what BuildMap draws gp from, its groups named by keys,
// started from prior when it is set.
func mapInput(gp *grouping, keys mapKeys, prior *insight.PriorMap, seed uint64) insight.MapInput {
	return insight.MapInput{Points: gp.points, Grouping: gp.g, Centroids: gp.centroids, Fits: gp.fits,
		AreaKeys: keys.areas, InterestKeys: keys.interests, AreaStarts: keys.areaStarts,
		InterestStarts: keys.interestStarts, Prior: prior, WarmDocs: prior != nil, Center: true, Seed: seed}
}

// drawn is a map and what drawing it cost.
type drawn struct {
	m     *insight.Map
	took  time.Duration
	alloc uint64 // bytes allocated
}

// drawMap draws gp's map, cold without prior, timing it and counting what
// it allocates.
func (m *measurer) drawMap(gp *grouping, keys mapKeys, prior *insight.PriorMap, seed uint64) (drawn, error) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	built, err := insight.BuildMap(m.ctx, mapInput(gp, keys, prior, seed))
	took := time.Since(start)
	if err != nil {
		return drawn{}, fmt.Errorf("draw the map: %w", err)
	}
	runtime.ReadMemStats(&after)
	return drawn{m: built, took: took, alloc: after.TotalAlloc - before.TotalAlloc}, nil
}

// priorOf is a map of gp, its groups named by keys, as the next map
// starts from it: as the engine reads a run's.
func priorOf(gp *grouping, keys mapKeys, m *insight.Map, params []byte) *insight.PriorMap {
	p := &insight.PriorMap{Params: params, Shape: gp.g.Shape, DotRadius: m.DotRadius, Unsorted: m.Unsorted,
		Docs: make(map[string]insight.PriorDoc, len(gp.ids)), Groups: map[string]insight.PriorGroup{}}
	for i, id := range gp.ids {
		p.Docs[id] = insight.PriorDoc{Map: m.Docs[i]}
	}
	parents := make([]string, len(keys.interests))
	for i, f := range gp.fits {
		if a := gp.g.Area[i]; f.Kind == insight.FitMember && a != insight.NoiseLabel {
			parents[f.Interest] = keys.areas[a]
		}
	}
	for a, key := range keys.areas {
		p.Groups[key] = insight.PriorGroup{Level: store.InterestLevelArea, Map: m.Areas[a]}
	}
	for l, key := range keys.interests {
		p.Groups[key] = insight.PriorGroup{Level: store.InterestLevelInterest, ParentID: parents[l], Map: m.Interests[l]}
	}
	return p
}

// docPlaces are a map's documents' places on the document map, by ID.
func docPlaces(gp *grouping, m *insight.Map) map[string][2]float64 {
	out := make(map[string][2]float64, len(gp.ids))
	for i, id := range gp.ids {
		out[id] = [2]float64{m.Docs[i].MapX, m.Docs[i].MapY}
	}
	return out
}

// interestCentres are a map's interests' centres in the zoom view, by key.
func interestCentres(keys mapKeys, m *insight.Map) map[string][2]float64 {
	out := make(map[string][2]float64, len(keys.interests))
	for l, key := range keys.interests {
		out[key] = [2]float64{m.Interests[l].ZoomX, m.Interests[l].ZoomY}
	}
	return out
}

// positions are a map's documents' places on the document map, by index.
func positions(m *insight.Map) [][2]float64 {
	out := make([][2]float64, len(m.Docs))
	for i, d := range m.Docs {
		out[i] = [2]float64{d.MapX, d.MapY}
	}
	return out
}

// near are each point's neighbours in the space, best first: the
// grouping's own pass.
func near(gp *grouping) [][]int {
	out := make([][]int, len(gp.g.Neighbours))
	for i, es := range gp.g.Neighbours {
		for _, e := range es {
			out[i] = append(out[i], e.To)
		}
	}
	return out
}

// areaLabels are each point's area, Noise for an unsorted one and in the
// flat shape: what purity is measured against.
func areaLabels(gp *grouping) []int {
	out := make([]int, len(gp.fits))
	for i, f := range gp.fits {
		out[i] = quality.Noise
		if f.Kind != insight.FitUnsorted {
			out[i] = gp.g.Area[i]
		}
	}
	return out
}

// coldMap measures a cold map of full, the whole library, and how far a
// second seed moves it.
func (m *measurer) coldMap(full *grouping, rep *mapReport) (*insight.Map, error) {
	keys := labelKeys(full)
	cold, err := m.drawMap(full, keys, nil, insight.MapSeed)
	if err != nil {
		return nil, err
	}
	other, err := m.drawMap(full, keys, nil, insight.MapSeed+1)
	if err != nil {
		return nil, err
	}
	nb, pos, areas := near(full), positions(cold.m), areaLabels(full)
	rep.Cold = coldMapReport{TookMS: cold.took.Milliseconds(), AllocBytes: cold.alloc,
		NP5: quality.NeighbourPreservation(nb, pos, npNear), NP15: quality.NeighbourPreservation(nb, pos, npFar)}
	if full.g.Shape == insight.ShapeAreas {
		rep.Cold.AreaPurity = new(quality.MapPurity(pos, areas, purity))
		rep.Cold.SpaceAreaPurity = new(quality.SpacePurity(nb, areas, purity))
	}
	seed := quality.Displace(docPlaces(full, cold.m), docPlaces(full, other.m))
	rep.SeedToSeed = seed.AlignedMean
	return cold.m, nil
}

// warmMap measures draw d of a change: a cold map of prev, then a warm map
// of next from it.
func (m *measurer) warmMap(kind changeKind, d uint, prev, next *grouping) (warmMapDraw, error) {
	prevKeys := labelKeys(prev)
	before, err := m.drawMap(prev, prevKeys, nil, insight.MapSeed)
	if err != nil {
		return warmMapDraw{}, err
	}
	nextKeys, err := carriedKeys(prev, next)
	if err != nil {
		return warmMapDraw{}, err
	}
	after, err := m.drawMap(next, nextKeys, priorOf(prev, prevKeys, before.m, before.m.Params), insight.MapSeed)
	if err != nil {
		return warmMapDraw{}, err
	}
	docs := quality.Displace(docPlaces(prev, before.m), docPlaces(next, after.m))
	interests := quality.Displace(interestCentres(prevKeys, before.m), interestCentres(nextKeys, after.m))
	w := warmMapDraw{Draw: d, Seed: changeSeed(kind, d, m.seed), Kind: string(after.m.Kind), ColdMS: before.took.Milliseconds(),
		WarmMS: after.took.Milliseconds(), Documents: displacementOf(docs), Interests: displacementOf(interests),
		NP5: quality.NeighbourPreservation(near(next), positions(after.m), npNear)}
	m.log.Info("warm map", "change", kind, "draw", d, "documents_moved", fmtFloat(&w.Documents.Mean, "%.4f"),
		"interests_moved", fmtFloat(&w.Interests.Mean, "%.4f"), "np5", fmtFloat(&w.NP5, "%.3f"))
	return w, nil
}

func displacementOf(d quality.Displacement) displacement {
	return displacement{Mean: d.Mean, Max: d.Max, AlignedMean: d.AlignedMean, AlignedMax: d.AlignedMax}
}

// peakRSS is the process's peak resident set, in bytes: getrusage reports
// it in bytes on darwin and in kilobytes elsewhere.
func peakRSS() (int64, error) {
	var u syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &u); err != nil {
		return 0, fmt.Errorf("read the peak resident set: %w", err)
	}
	if runtime.GOOS == "darwin" {
		return u.Maxrss, nil
	}
	return u.Maxrss * 1024, nil
}
