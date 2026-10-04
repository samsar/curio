package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"math/rand/v2"
	"time"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/quality"
	"github.com/samsar/curio/internal/store"
)

// The draws: the research's (research/cluster-quality's drawSeed and
// chain), which docs/decisions.md's measurements use. -seed adds to every
// seed.
const (
	// changeShare is the share of the library a change touches: the
	// scheduler's threshold.
	changeShare = 0.05
	// A 5% change's permutation is seeded addedSeed+d or mixedSeed+d for
	// draw d.
	addedSeed = 5001
	mixedSeed = 5008
	// The chain's permutation is seeded chainSeed+chainSeedStride·d. Its
	// steps add changeShare of the library each, from 60% to 100%, the
	// split check at every splitEvery-th: the engine's cadence when each
	// rebuild absorbs a threshold's worth of changes.
	chainSeed       = 8080
	chainSeedStride = 1010
	chainSteps      = 8
	splitEvery      = 4
)

// lenientDuplicate is the research's lenient near-duplicate threshold:
// the 10th percentile of the cosines between random halves of one interest
// on the owner's library, whose median is insight.MergeThreshold. A pair
// of interests at or above it is closer than nine in ten of those halves.
const lenientDuplicate = 0.74

// changeKind is how a draw changes the library.
type changeKind string

const (
	// changeAdded: the new library is the previous one and 5% more.
	changeAdded changeKind = "added"
	// changeMixed: 2.5% added and 2.5% of the previous library removed.
	changeMixed changeKind = "mixed"
)

// shuffled is a seeded permutation of 0..n-1.
func shuffled(n int, seed uint64) []int {
	return rand.New(rand.NewPCG(seed, 0xc4a9e)).Perm(n) //nolint:gosec // G404: a seeded, reproducible draw, not a secret
}

// changeSeed is the permutation seed of draw d of a change of kind.
func changeSeed(kind changeKind, d uint, seed uint64) uint64 {
	base := uint64(addedSeed)
	if kind == changeMixed {
		base = mixedSeed
	}
	return base + uint64(d) + seed
}

// changeDraw returns the previous and the new library of draw d of a 5%
// change of n documents, as ascending indexes. Added: the permutation's
// first round(5% of n) are held out of the previous library. Mixed: of
// half = round(2.5% of n), the first half are held out of the previous
// library and the next half removed from the new one.
func changeDraw(n int, kind changeKind, d uint, seed uint64) (prevIdx, newIdx []int) {
	perm := shuffled(n, changeSeed(kind, d, seed))
	var added, removed []int
	switch kind {
	case changeAdded:
		added = perm[:share(n, changeShare)]
	case changeMixed:
		half := share(n, changeShare/2)
		added, removed = perm[:half], perm[half:2*half]
	}
	return without(n, added), without(n, removed)
}

// chainDraw returns the library at each step of draw d of the chain, as
// ascending indexes: step k holds everything but the permutation's first
// (chainSteps-k)·step documents, step being round(5% of n).
func chainDraw(n int, d uint, seed uint64) [][]int {
	perm := shuffled(n, chainSeed+chainSeedStride*uint64(d)+seed)
	step := share(n, changeShare)
	out := make([][]int, chainSteps+1)
	for k := range out {
		out[k] = without(n, perm[:(chainSteps-k)*step])
	}
	return out
}

func share(n int, s float64) int { return int(math.Round(s * float64(n))) }

// libraryPercent is the share of the library chain step k holds.
func libraryPercent(k int) int {
	return int(math.Round(100 * (1 - changeShare*float64(chainSteps-k))))
}

// without returns 0..n-1 minus drop, ascending.
func without(n int, drop []int) []int {
	gone := make(map[int]bool, len(drop))
	for _, i := range drop {
		gone[i] = true
	}
	out := make([]int, 0, n-len(gone))
	for i := range n {
		if !gone[i] {
			out = append(out, i)
		}
	}
	return out
}

// measurer runs the report's measurements on one library.
type measurer struct {
	ctx     context.Context
	log     *slog.Logger
	grouper insight.Grouper
	docs    []store.DocVector // by document ID
	draws   uint
	seed    uint64
	// whole holds the fresh groupings of the whole library, by the shape
	// they started from: a first grouping starts from ShapeFlat, a fresh
	// rebuild from its prior's shape.
	whole map[insight.Shape]*grouping
}

func newMeasurer(ctx context.Context, log *slog.Logger, docs []store.DocVector, draws uint, seed uint64) *measurer {
	return &measurer{ctx: ctx, log: log, grouper: insight.NewLouvainGrouper(log), docs: docs, draws: draws,
		seed: seed, whole: map[insight.Shape]*grouping{}}
}

// subset is the library's documents at idx.
func (m *measurer) subset(idx []int) []store.DocVector {
	out := make([]store.DocVector, len(idx))
	for k, i := range idx {
		out[k] = m.docs[i]
	}
	return out
}

// first is a first grouping of dvs: no prior, from ShapeFlat.
func (m *measurer) first(dvs []store.DocVector) (*grouping, error) {
	return regroup(m.ctx, m.grouper, dvs, insight.ShapeFlat, nil, false)
}

// warm is a warm rebuild of dvs from prev's shape and seeds.
func (m *measurer) warm(dvs []store.DocVector, prev *grouping, split bool) (*grouping, error) {
	return regroup(m.ctx, m.grouper, dvs, prev.g.Shape, prev.seeds(), split)
}

// next is a chain's grouping of dvs: a first grouping without prev, else
// a warm rebuild from it.
func (m *measurer) next(dvs []store.DocVector, prev *grouping, split bool) (*grouping, error) {
	if prev == nil {
		return m.first(dvs)
	}
	return m.warm(dvs, prev, split)
}

// wholeFrom is the fresh grouping of the whole library from shape: a
// first grouping from ShapeFlat, a fresh rebuild after a grouping of that
// shape (none of its seeds).
func (m *measurer) wholeFrom(shape insight.Shape) (*grouping, error) {
	if gp, ok := m.whole[shape]; ok {
		return gp, nil
	}
	gp, err := regroup(m.ctx, m.grouper, m.docs, shape, nil, false)
	if err != nil {
		return nil, err
	}
	m.whole[shape] = gp
	return gp, nil
}

// measure runs every measurement and returns the report.
func (m *measurer) measure(lib *library) (*report, error) {
	start := time.Now()
	rep := &report{Documents: len(m.docs), DroppedNonFinite: lib.dropped, Grouper: m.grouper.Name(),
		Params: runParams(m.grouper), Draws: m.draws, Seed: m.seed}
	rep.TimingsMS.Read = lib.read.Milliseconds()

	t := time.Now()
	m.log.Info("measuring a fresh grouping of the whole library")
	full, err := m.wholeFrom(insight.ShapeFlat)
	if err != nil {
		return nil, fmt.Errorf("fresh grouping: %w", err)
	}
	rep.Fresh = freshOf(full)
	rep.TimingsMS.Fresh = time.Since(t).Milliseconds()

	t = time.Now()
	m.log.Info("measuring the baseline clusterer")
	if rep.Baseline, err = m.baseline(full); err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	rep.TimingsMS.Baseline = time.Since(t).Milliseconds()

	if err := m.changes(rep); err != nil {
		return nil, err
	}

	t = time.Now()
	m.log.Info("measuring a cold map of the whole library")
	if _, err := m.coldMap(full, &rep.Map); err != nil {
		return nil, fmt.Errorf("map: %w", err)
	}
	rep.TimingsMS.Map += time.Since(t).Milliseconds()

	t = time.Now()
	if rep.Chain, err = m.chain(full); err != nil {
		return nil, fmt.Errorf("chain: %w", err)
	}
	rep.TimingsMS.Chain = time.Since(t).Milliseconds()

	if lib.stored != nil {
		t = time.Now()
		s, err := storedOf(lib.stored, full, rep.Grouper, rep.Params)
		if err != nil {
			return nil, fmt.Errorf("stored run: %w", err)
		}
		rep.StoredRun = &s
		rep.TimingsMS.StoredRun = time.Since(t).Milliseconds()
	}
	rep.TimingsMS.Total = time.Since(start).Milliseconds() + rep.TimingsMS.Read
	if rep.Map.PeakRSSBytes, err = peakRSS(); err != nil {
		return nil, err
	}
	return rep, nil
}

// runParams are the params a rebuild records on its run: the grouper's and
// center, as Engine.runParams writes them.
func runParams(gr insight.Grouper) map[string]any {
	params := maps.Clone(gr.Params())
	if params == nil {
		params = map[string]any{}
	}
	params["center"] = true
	return params
}

// freshOf describes a fresh grouping.
func freshOf(gp *grouping) freshReport {
	vecs := gp.vectors()
	_, _, loose, unsorted := gp.counts()
	f := freshReport{
		Shape:                gp.g.Shape,
		Interests:            levelOf(vecs, gp.g.Interest),
		InterestsBeforeMerge: numGroups(gp.raw.Interest),
		Merged:               gp.merged,
		Loose:                loose,
		Unsorted:             unsorted,
		NearDuplicates:       nearDuplicatesOf(vecs, gp.g),
	}
	if gp.g.Shape == insight.ShapeAreas {
		areas := levelOf(vecs, gp.g.Area)
		f.Areas = &areas
	}
	return f
}

// levelOf measures one level of a grouping, labels being its groups
// (NoiseLabel for none).
func levelOf(vecs [][]float32, labels []int) level {
	groups := quality.Groups(labels)
	l := level{Groups: len(groups), Coverage: quality.Coverage(labels), Sizes: quality.SizesOf(groups, len(labels)),
		Cohesion: meanCohesion(vecs, groups)}
	if len(groups) > 1 {
		l.Silhouette = new(quality.Silhouette(vecs, labels))
	}
	return l
}

// meanCohesion is the mean cohesion of groups, nil without any.
func meanCohesion(vecs [][]float32, groups [][]int) *float64 {
	if len(groups) == 0 {
		return nil
	}
	return new(quality.CohesionOf(vecs, groups, quality.Centroids(vecs, groups)).Mean)
}

// cohesionOf is a grouping's mean interest cohesion, nil without interests.
func cohesionOf(gp *grouping) *float64 {
	return meanCohesion(gp.vectors(), quality.Groups(gp.g.Interest))
}

// nearDuplicatesOf counts the pairs of g's interests whose centroids are
// near-duplicates: at the merge's threshold within one area (none should
// be left) and anywhere, and at the lenient threshold anywhere.
func nearDuplicatesOf(vecs [][]float32, g insight.Grouping) nearDuplicates {
	groups := quality.Groups(g.Interest)
	sep := quality.Separate(quality.Centroids(vecs, groups), []float64{lenientDuplicate, insight.MergeThreshold})
	d := nearDuplicates{Threshold: insight.MergeThreshold, LenientThreshold: lenientDuplicate,
		AnywhereLenient: sep.Duplicates[0].Pairs, Anywhere: sep.Duplicates[1].Pairs}
	if g.Shape != insight.ShapeAreas {
		return d
	}
	within := 0
	for _, p := range sep.Pairs {
		if p.Cosine >= insight.MergeThreshold && g.Area[groups[p.A][0]] == g.Area[groups[p.B][0]] {
			within++
		}
	}
	d.WithinAnArea = &within
	return d
}

// baselineClusterer is the clusterer the engine ran before the two-level
// grouping, at its last defaults: kept as the report's baseline.
func baselineClusterer() insight.Clusterer {
	return insight.NewKNNGraphClusterer(insight.KNNGraphOptions{K: 10, MinSimilarity: 0.5, MinClusterSize: 3})
}

// baseline measures the baseline clusterer on full's points, its clusters
// as the old engine stored them: no merge, no strays.
func (m *measurer) baseline(full *grouping) (baselineReport, error) {
	c := baselineClusterer()
	g, err := insight.FlatGrouper(c).Group(m.ctx, insight.GroupInput{Points: full.points, Shape: insight.ShapeFlat})
	if err != nil {
		return baselineReport{}, err
	}
	return baselineReport{Clusterer: c.Name(), Params: c.Params(), Interests: levelOf(full.vectors(), g.Interest)}, nil
}

// changes measures the warm rebuilds after a 5% change, and their warm
// maps, and the fresh rebuilds after 5% added on the same draws.
func (m *measurer) changes(rep *report) error {
	var freshTime, mapTime time.Duration
	t := time.Now()
	for _, kind := range []changeKind{changeAdded, changeMixed} {
		var warm, fresh []keptDraw
		var drawnMaps []warmMapDraw
		for d := range m.draws {
			prev, next, k, err := m.warmDraw(kind, d)
			if err != nil {
				return fmt.Errorf("warm rebuild, %s draw %d: %w", kind, d, err)
			}
			warm = append(warm, k)
			mt := time.Now()
			w, err := m.warmMap(kind, d, prev, next)
			if err != nil {
				return fmt.Errorf("warm map, %s draw %d: %w", kind, d, err)
			}
			drawnMaps = append(drawnMaps, w)
			mapTime += time.Since(mt)
			if kind != changeAdded {
				continue
			}
			ft := time.Now()
			f, err := m.freshRebuildDraw(prev, d)
			if err != nil {
				return fmt.Errorf("fresh rebuild, draw %d: %w", d, err)
			}
			fresh = append(fresh, f)
			freshTime += time.Since(ft)
		}
		if kind == changeAdded {
			rep.Warm.Added = summarize(warm)
			rep.FreshRebuild = summarize(fresh)
			rep.Map.Warm.Added = summarizeMaps(drawnMaps)
		} else {
			rep.Warm.Mixed = summarize(warm)
			rep.Map.Warm.Mixed = summarizeMaps(drawnMaps)
		}
	}
	rep.TimingsMS.Warm = (time.Since(t) - freshTime - mapTime).Milliseconds()
	rep.TimingsMS.FreshRebuild = freshTime.Milliseconds()
	rep.TimingsMS.Map = mapTime.Milliseconds()
	return nil
}

// summarizeMaps takes the mean and the largest of the draws' mean
// displacements, and their mean NP5.
func summarizeMaps(draws []warmMapDraw) warmMapsReport {
	w := warmMapsReport{Draws: draws}
	for _, d := range draws {
		for _, p := range []struct{ sum, of *displacement }{{&w.Documents, &d.Documents}, {&w.Interests, &d.Interests}} {
			p.sum.Mean += p.of.Mean / float64(len(draws))
			p.sum.AlignedMean += p.of.AlignedMean / float64(len(draws))
			p.sum.Max = max(p.sum.Max, p.of.Mean)
			p.sum.AlignedMax = max(p.sum.AlignedMax, p.of.AlignedMean)
		}
		w.MeanNP5 += d.NP5 / float64(len(draws))
	}
	return w
}

// warmDraw measures draw d of a change of kind: a first grouping of the
// previous library, then a warm rebuild of the new one from it without the
// split check. It returns both groupings too.
func (m *measurer) warmDraw(kind changeKind, d uint) (prev, next *grouping, k keptDraw, err error) {
	prevIdx, newIdx := changeDraw(len(m.docs), kind, d, m.seed)
	if prev, err = m.first(m.subset(prevIdx)); err != nil {
		return nil, nil, keptDraw{}, err
	}
	if next, err = m.warm(m.subset(newIdx), prev, false); err != nil {
		return nil, nil, keptDraw{}, err
	}
	names, err := namesKept(prev, next)
	if err != nil {
		return nil, nil, keptDraw{}, err
	}
	m.log.Info("warm rebuild", "change", kind, "draw", d, "interests_kept", fmtShare(names.Interests),
		"areas_kept", fmtShare(names.Areas))
	return prev, next, keptDraw{Draw: d, Seed: changeSeed(kind, d, m.seed), kept: names}, nil
}

// freshRebuildDraw measures the names a fresh rebuild of the whole library
// keeps from prev, the previous grouping of added draw d.
func (m *measurer) freshRebuildDraw(prev *grouping, d uint) (keptDraw, error) {
	next, err := m.wholeFrom(prev.g.Shape)
	if err != nil {
		return keptDraw{}, err
	}
	k, err := namesKept(prev, next)
	if err != nil {
		return keptDraw{}, err
	}
	m.log.Info("fresh rebuild", "draw", d, "interests_kept", fmtShare(k.Interests), "areas_kept", fmtShare(k.Areas))
	return keptDraw{Draw: d, Seed: changeSeed(changeAdded, d, m.seed), kept: k}, nil
}

// summarize takes the mean and the minimum of each level's shares over
// the draws that have one.
func summarize(draws []keptDraw) keptReport {
	ks := make([]kept, len(draws))
	for i, d := range draws {
		ks[i] = d.kept
	}
	mean, low := meanMin(ks)
	return keptReport{Draws: draws, Mean: mean, Min: low}
}

// meanMin is the mean and the minimum of each level's shares, nil for a
// level none of ks has.
func meanMin(ks []kept) (mean, low kept) {
	of := func(level func(kept) *float64) (*float64, *float64) {
		var sum, lo float64
		n := 0
		for _, k := range ks {
			v := level(k)
			if v == nil {
				continue
			}
			if n == 0 || *v < lo {
				lo = *v
			}
			sum += *v
			n++
		}
		if n == 0 {
			return nil, nil
		}
		return new(sum / float64(n)), new(lo)
	}
	mean.Interests, low.Interests = of(func(k kept) *float64 { return k.Interests })
	mean.Areas, low.Areas = of(func(k kept) *float64 { return k.Areas })
	return mean, low
}

// chain measures the warm rebuilds of each chain draw against fresh
// groupings of the same libraries, full being the whole library's.
func (m *measurer) chain(full *grouping) (chainReport, error) {
	var c chainReport
	c.Fresh.Areas, c.Fresh.Interests, _, _ = full.counts()
	c.Fresh.Cohesion = cohesionOf(full)
	for d := range m.draws {
		steps, err := m.chainOnce(d, full)
		if err != nil {
			return chainReport{}, fmt.Errorf("draw %d: %w", d, err)
		}
		c.Steps = append(c.Steps, steps...)
	}
	c.summarize()
	return c, nil
}

// chainOnce runs draw d of the chain: a first grouping at 60%, then a warm
// rebuild at each step from the step before, with the split check at
// every splitEvery-th, each against a fresh grouping of the same library
// (full at the last step).
func (m *measurer) chainOnce(d uint, full *grouping) ([]chainStep, error) {
	libs := chainDraw(len(m.docs), d, m.seed)
	steps := make([]chainStep, 0, len(libs))
	var prev *grouping
	for k, idx := range libs {
		dvs := m.subset(idx)
		split := k > 0 && k%splitEvery == 0
		cur, err := m.next(dvs, prev, split)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", k, err)
		}
		fresh := full
		if k < chainSteps {
			if fresh, err = m.first(dvs); err != nil {
				return nil, fmt.Errorf("step %d, fresh: %w", k, err)
			}
		}
		s := chainStep{Draw: d, Step: k, LibraryPercent: libraryPercent(k), Documents: len(idx), SplitCheck: split}
		if err := s.measure(cur, fresh, prev); err != nil {
			return nil, fmt.Errorf("step %d: %w", k, err)
		}
		m.log.Info("chain step", "draw", d, "step", k, "documents", s.Documents, "split_check", split,
			"areas", s.Areas, "interests", s.Interests, "gap", fmtFloat(s.Gap, "%+.4f"),
			"interests_kept", fmtShare(s.Kept.Interests), "areas_kept", fmtShare(s.Kept.Areas))
		steps = append(steps, s)
		prev = cur
	}
	return steps, nil
}

// measure fills in the step's grouping cur against fresh, a fresh grouping
// of the same library, and the names it kept from prev, the step before
// (nil at the first).
func (s *chainStep) measure(cur, fresh, prev *grouping) error {
	s.Areas, s.Interests, _, _ = cur.counts()
	s.FreshAreas, s.FreshInterests, _, _ = fresh.counts()
	s.Cohesion, s.FreshCohesion = cohesionOf(cur), cohesionOf(fresh)
	if s.Cohesion != nil && s.FreshCohesion != nil {
		s.Gap = new(*s.Cohesion - *s.FreshCohesion)
	}
	if prev == nil {
		return nil
	}
	var err error
	s.Kept, err = namesKept(prev, cur)
	return err
}

// summarize sums the steps up: each draw's end against fresh, the worst
// gap, and the names kept at the split-check steps and the others.
func (c *chainReport) summarize() {
	var endInterests, endAreas, endCohesion float64
	cohesions := 0
	var split, other []kept
	for _, s := range c.Steps {
		if s.Gap != nil && (c.WorstGap == nil || math.Abs(*s.Gap) > math.Abs(c.WorstGap.Gap)) {
			c.WorstGap = &worstGap{Draw: s.Draw, Step: s.Step, Gap: *s.Gap}
		}
		switch {
		case s.Step == 0:
		case s.SplitCheck:
			split = append(split, s.Kept)
		default:
			other = append(other, s.Kept)
		}
		if s.Step != chainSteps {
			continue
		}
		e := chainEnd{Draw: s.Draw, Areas: s.Areas, Interests: s.Interests, Cohesion: s.Cohesion, Gap: s.Gap}
		c.End = append(c.End, e)
		endInterests += float64(s.Interests)
		endAreas += float64(s.Areas)
		if s.Cohesion != nil {
			endCohesion += *s.Cohesion
			cohesions++
		}
	}
	if n := float64(len(c.End)); n > 0 {
		c.MeanEndInterests, c.MeanEndAreas = endInterests/n, endAreas/n
	}
	if c.Fresh.Interests > 0 {
		c.EndInterestsDiffPercent = new(100 * (c.MeanEndInterests - float64(c.Fresh.Interests)) / float64(c.Fresh.Interests))
	}
	if cohesions > 0 {
		c.MeanEndCohesion = new(endCohesion / float64(cohesions))
		if c.Fresh.Cohesion != nil {
			c.MeanEndGap = new(*c.MeanEndCohesion - *c.Fresh.Cohesion)
		}
	}
	c.SplitSteps.Mean, c.SplitSteps.Min = meanMin(split)
	c.OtherSteps.Mean, c.OtherSteps.Min = meanMin(other)
}
