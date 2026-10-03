package insight_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
)

// The fixture library: a seeded synthetic library with two levels of
// planted structure, the shape the owner's library has.
//
//   - Areas: random directions in a topic subspace.
//   - Topics: each area holds 3 to 8 topics, centered at
//     normalize(area + topicSpread·u) for a random unit u, so topics of
//     one area are closer to each other than to other areas'. A topic's
//     weight is the area's weight (0.5 to 1.5) times its own (0.3 to 1.3),
//     so sizes are skewed.
//   - A near-duplicate pair: area 0 holds one more topic centered at
//     normalize(t + duplicateSpread·u) of its first topic t, centroid
//     cosine about 0.94, which the merge must join if the grouping cuts
//     them apart.
//   - Documents: normalize(topic + docNoise·u), u a random unit vector of
//     the topic subspace.
//   - Generalists: generalistShare of the documents are random unit
//     vectors of a separate subspace, so their nearest neighbours stay
//     below the area graph's 0.40 and they end up strays, as the owner's
//     library leaves about 8% in Unsorted.
//   - A common offset of norm offsetNorm is added to every document, so
//     the raw cosines sit in a narrow cone, as embedding models' do, and
//     centering matters.
//
// The tests group the default shape, 2,000 documents in 8 areas of 32
// topic and 64 generalist dimensions: it stays at or above the 1,000
// documents of the areas shape at the chain's 60% start, and a pass over
// it stays cheap under -race. Documents go through insight.PreparePoints,
// as a rebuild prepares them.
const (
	topicSpread     = 0.5
	docNoise        = 0.8
	generalistShare = 0.08
	offsetNorm      = 1.0
	duplicateSpread = 0.35
)

// fixtureShape sizes a fixture library.
type fixtureShape struct {
	docs, areas, topicDims, generalistDims int
}

var defaultShape = fixtureShape{docs: 2000, areas: 8, topicDims: 32, generalistDims: 64}

// library is a fixture library's raw vectors, in document ID order, and
// its planted truth: each document's area and topic, -1 for a generalist.
type library struct {
	docs        []store.DocVector
	area, topic []int
}

// fixtureLibrary builds the default-shaped library for a seed.
func fixtureLibrary(seed uint64) library { return defaultShape.build(seed) }

func (fs fixtureShape) build(seed uint64) library {
	r := rand.New(rand.NewPCG(seed, 0xf1c5))
	dim := fs.topicDims + fs.generalistDims
	unit := func(lo, hi int) []float64 {
		v := make([]float64, dim)
		var s float64
		for d := lo; d < hi; d++ {
			v[d] = r.NormFloat64()
			s += v[d] * v[d]
		}
		for d := lo; d < hi; d++ {
			v[d] /= math.Sqrt(s)
		}
		return v
	}
	around := func(c []float64, spread float64) []float64 {
		u := unit(0, fs.topicDims)
		v := make([]float64, dim)
		var s float64
		for d := range v {
			v[d] = c[d] + spread*u[d]
			s += v[d] * v[d]
		}
		for d := range v {
			v[d] /= math.Sqrt(s)
		}
		return v
	}
	type topic struct {
		area   int
		center []float64
		weight float64
	}
	offset := unit(0, dim)
	var topics []topic
	for a := range fs.areas {
		center := unit(0, fs.topicDims)
		aw := 0.5 + r.Float64()
		first := len(topics)
		for range 3 + r.IntN(6) {
			topics = append(topics, topic{area: a, center: around(center, topicSpread), weight: aw * (0.3 + r.Float64())})
		}
		if a == 0 {
			t := topics[first]
			topics = append(topics, topic{area: a, center: around(t.center, duplicateSpread), weight: t.weight})
		}
	}
	var total float64
	for _, t := range topics {
		total += t.weight
	}

	lib := library{docs: make([]store.DocVector, fs.docs), area: make([]int, fs.docs), topic: make([]int, fs.docs)}
	for i := range lib.docs {
		var v []float64
		if r.Float64() < generalistShare {
			v = unit(fs.topicDims, dim)
			lib.area[i], lib.topic[i] = -1, -1
		} else {
			x := r.Float64() * total
			k := 0
			for ; k < len(topics)-1; k++ {
				if x -= topics[k].weight; x < 0 {
					break
				}
			}
			v = around(topics[k].center, docNoise)
			lib.area[i], lib.topic[i] = topics[k].area, k
		}
		vec := make([]float32, dim)
		for d := range vec {
			vec[d] = float32(v[d] + offsetNorm*offset[d])
		}
		lib.docs[i] = store.DocVector{DocumentID: fmt.Sprintf("doc-%05d", i), Vector: vec}
	}
	return lib
}

// subset is the library's documents at idx, ascending.
func (l library) subset(idx []int) []store.DocVector {
	out := make([]store.DocVector, len(idx))
	for k, i := range idx {
		out[k] = l.docs[i]
	}
	return out
}

// lists caches the nearest-neighbour pass by the vectors it is given, so a
// document set is passed over once however many groupings a test makes of
// it. Safe for parallel tests.
var lists = &listCache{m: map[uint64]insight.NeighbourLists{}}

type listCache struct {
	mu sync.Mutex
	m  map[uint64]insight.NeighbourLists
}

func (c *listCache) get(ctx context.Context, vecs [][]float32) (insight.NeighbourLists, error) {
	h := fnv.New64a()
	var b [4]byte
	for _, v := range vecs {
		for _, x := range v {
			binary.LittleEndian.PutUint32(b[:], math.Float32bits(x))
			h.Write(b[:])
		}
	}
	key := h.Sum64()
	c.mu.Lock()
	l, ok := c.m[key]
	c.mu.Unlock()
	if ok {
		return l, nil
	}
	l, err := insight.NearestNeighbours(ctx, vecs)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.m[key] = l
	c.mu.Unlock()
	return l, nil
}

// grouper is the shipped grouper, with the shared pass cache.
func grouper() *insight.LouvainGrouper {
	return insight.NewLouvainGrouper(nil).WithNeighbours(lists.get)
}

// run is the whole grouping a rebuild makes of a document set: Group, the
// merge, centroids and the strays.
type run struct {
	ids       []string
	points    []insight.Point
	mean      []float64
	raw       insight.Grouping // before the merge
	g         insight.Grouping // after
	merged    int
	centroids [][]float32
	fits      []insight.Fit
}

// seeds is the Prior the next grouping of this run reads.
func (r run) seeds() map[string]insight.Seed {
	m := make(map[string]insight.Seed, len(r.ids))
	for i, id := range r.ids {
		m[id] = r.g.Seeds[i]
	}
	return m
}

// rebuild groups docs: fresh when prev is nil, else warm from it.
func rebuild(t testing.TB, docs []store.DocVector, prev *run, split bool) run {
	t.Helper()
	in := insight.GroupInput{Shape: insight.ShapeFlat, Split: split}
	if prev != nil {
		in.Shape, in.Prior = prev.g.Shape, prev.seeds()
	}
	return group(t, grouper(), docs, in)
}

func group(t testing.TB, g insight.Grouper, docs []store.DocVector, in insight.GroupInput) run {
	t.Helper()
	points, mean, err := insight.PreparePoints(docs, true)
	require.NoError(t, err)
	in.Points = points
	raw, err := g.Group(context.Background(), in)
	require.NoError(t, err)
	require.NoError(t, raw.Validate(len(points)))
	merged, n, err := insight.MergeNearDuplicates(points, raw, insight.MergeThreshold)
	require.NoError(t, err)
	cents, err := insight.Centroids(points, merged.Interest)
	require.NoError(t, err)
	fits, err := insight.AssignStrays(points, merged, cents, insight.LooseFitThreshold)
	require.NoError(t, err)
	ids := make([]string, len(points))
	for i, p := range points {
		ids[i] = p.ID
	}
	return run{ids: ids, points: points, mean: mean, raw: raw, g: merged, merged: n, centroids: cents, fits: fits}
}

// numGroups is how many groups labels has (0..k-1, NoiseLabel for none).
func numGroups(labels []int) int {
	k := 0
	for _, l := range labels {
		k = max(k, l+1)
	}
	return k
}

// namesKept is the share of prev's interests and areas whose identity
// carries into next, by insight.Carry over next's library.
func namesKept(t testing.TB, prev, next run) (interests, areas float64) {
	t.Helper()
	ac := carry(t, prev, next, true, nil)
	ic := carry(t, prev, next, false, &ac)
	share := func(c insight.Carried) float64 {
		if len(c.Fates) == 0 {
			return 1
		}
		return float64(c.Counts.Kept) / float64(len(c.Fates))
	}
	return share(ic), share(ac)
}

// carry runs one level's carry-over from prev to next, old groups named by
// their zero-padded labels.
func carry(t testing.TB, prev, next run, areaLevel bool, areas *insight.Carried) insight.Carried {
	t.Helper()
	labels := func(r run) []int {
		if areaLevel {
			return r.g.Area
		}
		return r.g.Interest
	}
	pl, nl := labels(prev), labels(next)
	in := insight.CarryInput{
		Old: make([]insight.OldGroup, numGroups(pl)), New: make([]insight.NewGroup, numGroups(nl)),
		Library: next.ids, Areas: areas,
	}
	for o := range in.Old {
		in.Old[o].ID = fmt.Sprintf("%06d", o)
	}
	for j := range in.New {
		in.New[j].Parent = -1
	}
	for i, l := range pl {
		if l == insight.NoiseLabel {
			continue
		}
		in.Old[l].Members = append(in.Old[l].Members, prev.ids[i])
		if !areaLevel && prev.g.Shape == insight.ShapeAreas {
			in.Old[l].Parent = fmt.Sprintf("%06d", prev.g.Area[i])
		}
	}
	for i, l := range nl {
		if l == insight.NoiseLabel {
			continue
		}
		in.New[l].Members = append(in.New[l].Members, next.ids[i])
		if !areaLevel && next.g.Shape == insight.ShapeAreas && areas != nil {
			in.New[l].Parent = next.g.Area[i]
		}
	}
	c, err := insight.Carry(in)
	require.NoError(t, err)
	return c
}

// without is 0..n-1 minus drop, ascending.
func without(n int, drop []int) []int {
	gone := make(map[int]bool, len(drop))
	for _, i := range drop {
		gone[i] = true
	}
	out := make([]int, 0, n-len(drop))
	for i := range n {
		if !gone[i] {
			out = append(out, i)
		}
	}
	return out
}

// shuffled is a seeded permutation of 0..n-1.
func shuffled(n int, seed uint64) []int {
	return rand.New(rand.NewPCG(seed, 0xc4a9e)).Perm(n)
}
