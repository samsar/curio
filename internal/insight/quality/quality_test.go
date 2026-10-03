package quality

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/community"
	"gonum.org/v1/gonum/graph/simple"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/louvain"
)

func unit(v ...float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(s))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

// randomCorpus returns n unit vectors around `centers` random directions
// with labels naming their center, every fifth point noise.
func randomCorpus(seed uint64, n, dim, centers int) ([][]float32, []int) {
	r := rand.New(rand.NewPCG(seed, 1))
	cs := make([][]float64, centers)
	for c := range cs {
		cs[c] = make([]float64, dim)
		for d := range cs[c] {
			cs[c][d] = r.NormFloat64()
		}
	}
	vecs := make([][]float32, n)
	labels := make([]int, n)
	for i := range vecs {
		c := r.IntN(centers)
		v := make([]float32, dim)
		for d := range v {
			v[d] = float32(cs[c][d] + 0.8*r.NormFloat64())
		}
		vecs[i] = unit(v...)
		labels[i] = c
		if i%5 == 0 {
			labels[i] = Noise
		}
	}
	return vecs, labels
}

func TestGroupsAndRelabel(t *testing.T) {
	labels := []int{7, Noise, 3, 7, 3, 9, 7}
	assert.Equal(t, [][]int{{0, 3, 6}, {2, 4}, {5}}, Groups(labels))
	assert.Equal(t, []int{0, Noise, 1, 0, 1, 2, 0}, Relabel(labels))
}

func TestCoverageAndSizes(t *testing.T) {
	// 10 interests of sizes 1..10 plus 5 noise points.
	var labels []int
	for c := 1; c <= 10; c++ {
		for range c {
			labels = append(labels, c)
		}
	}
	for range 5 {
		labels = append(labels, Noise)
	}
	n := len(labels) // 55 + 5
	assert.InDelta(t, 55.0/60, Coverage(labels), 1e-12)
	assert.Zero(t, Coverage(nil))

	s := SizesOf(Groups(labels), n)
	assert.Equal(t, 10, s.Interests)
	assert.InDelta(t, 5.5, s.Median, 1e-12)
	assert.InDelta(t, 9.1, s.P90, 1e-12) // rank 0.9*9 = 8.1 between 9 and 10
	assert.Equal(t, 10, s.Max)
	assert.InDelta(t, 0.4, s.SmallShare, 1e-12) // sizes 1-4
	assert.InDelta(t, 55.0/60, s.Top10Share, 1e-12)
	assert.InDelta(t, 5.5, s.Mean, 1e-12)
}

func TestPercentile(t *testing.T) {
	xs := []float64{1, 2, 3, 4}
	assert.InDelta(t, 1, Percentile(xs, 0), 1e-12)
	assert.InDelta(t, 2.5, Percentile(xs, 0.5), 1e-12)
	assert.InDelta(t, 4, Percentile(xs, 1), 1e-12)
	assert.True(t, math.IsNaN(Percentile(nil, 0.5)))
}

// TestCohesion_MatchesTheEngine pins the formula insight's summarize
// stores: the mean of max(0, cos(member, normalized mean)).
func TestCohesion_MatchesTheEngine(t *testing.T) {
	vecs := [][]float32{unit(1, 0), unit(0, 1), unit(1, 1), unit(-1, 0)}
	groups := [][]int{{0, 1, 2}, {3}}
	cents := Centroids(vecs, groups)
	c := CohesionOf(vecs, groups, cents)
	// Centroid of the first group is (1,1)/√2: members score 1/√2, 1/√2, 1.
	want := (2/math.Sqrt2 + 1) / 3
	assert.InDelta(t, want, c.PerInterest[0], 1e-6)
	assert.InDelta(t, 1, c.PerInterest[1], 1e-6)
	assert.InDelta(t, (want+1)/2, c.Mean, 1e-6)
	assert.InDelta(t, (3*want+1)/4, c.DocWeighted, 1e-6)

	// A member opposite its centroid counts 0, not a negative.
	vecs = [][]float32{unit(1, 0), unit(1, 0.1), unit(-1, 0)}
	groups = [][]int{{0, 1, 2}}
	c = CohesionOf(vecs, groups, Centroids(vecs, groups))
	assert.GreaterOrEqual(t, c.PerInterest[0], 0.0)
}

func TestSeparate(t *testing.T) {
	cents := [][]float64{{1, 0, 0}, {0.9, math.Sqrt(1 - 0.81), 0}, {0, 0, 1}}
	s := Separate(cents, []float64{0.5, 0.95})
	assert.InDeltaSlice(t, []float64{0.9, 0.9, 0}, s.NearestPer, 1e-9)
	require.Len(t, s.Duplicates, 2)
	assert.Equal(t, DuplicateCount{Threshold: 0.5, Pairs: 1, Interests: 2}, s.Duplicates[0])
	assert.Equal(t, DuplicateCount{Threshold: 0.95, Pairs: 0, Interests: 0}, s.Duplicates[1])
	require.Len(t, s.Pairs, 1)
	assert.Equal(t, 0, s.Pairs[0].A)
	assert.Equal(t, 1, s.Pairs[0].B)
}

// bruteSilhouette is the textbook definition over pairwise cosine
// distances.
func bruteSilhouette(vecs [][]float32, labels []int) float64 {
	groups := Groups(labels)
	var total float64
	n := 0
	for gi, g := range groups {
		for _, i := range g {
			n++
			if len(g) < 2 {
				continue
			}
			var a float64
			for _, j := range g {
				if j != i {
					a += 1 - dot32(vecs[i], f32to64(vecs[j]))
				}
			}
			a /= float64(len(g) - 1)
			b := math.Inf(1)
			for gj, h := range groups {
				if gj == gi {
					continue
				}
				var d float64
				for _, j := range h {
					d += 1 - dot32(vecs[i], f32to64(vecs[j]))
				}
				b = min(b, d/float64(len(h)))
			}
			total += (b - a) / max(a, b)
		}
	}
	return total / float64(n)
}

func TestSilhouette_MatchesPairwise(t *testing.T) {
	vecs, labels := randomCorpus(3, 300, 16, 6)
	labels[1] = 99 // a singleton interest scores 0
	assert.InDelta(t, bruteSilhouette(vecs, labels), Silhouette(vecs, labels), 1e-9)
	assert.Zero(t, Silhouette(vecs, make([]int, len(vecs)))) // one interest
}

// toGonum builds the same weighted graph for gonum's modularity.
func toGonum(adj [][]louvain.Edge) *simple.WeightedUndirectedGraph {
	g := simple.NewWeightedUndirectedGraph(0, 0)
	for i := range adj {
		g.AddNode(simple.Node(i))
	}
	for i, es := range adj {
		for _, e := range es {
			if e.To > i {
				g.SetWeightedEdge(simple.WeightedEdge{F: simple.Node(i), T: simple.Node(e.To), W: e.Weight})
			}
		}
	}
	return g
}

func TestModularity_MatchesGonum(t *testing.T) {
	vecs, labels := randomCorpus(5, 200, 16, 4)
	points := make([]insight.Point, len(vecs))
	for i, v := range vecs {
		points[i] = insight.Point{ID: string(rune('a' + i%26)), Vector: v}
	}
	adj, err := insight.KNNGraph(t.Context(), points, 8, 0.2)
	require.NoError(t, err)

	// gonum takes the communities explicitly; noise points go alone.
	comms := map[int][]graph.Node{}
	for i, l := range labels {
		key := l
		if l == Noise {
			key = -(i + 2)
		}
		comms[key] = append(comms[key], simple.Node(i))
	}
	cs := make([][]graph.Node, 0, len(comms))
	for _, c := range comms {
		cs = append(cs, c)
	}
	for _, res := range []float64{0.5, 1, 2} {
		q, err := Modularity(adj, labels, res)
		require.NoError(t, err)
		assert.InDelta(t, community.Q(toGonum(adj), cs, res), q, 1e-9, "resolution %g", res)
	}
	q, err := Modularity(make([][]louvain.Edge, 3), []int{0, 0, 1}, 1)
	require.NoError(t, err)
	assert.Zero(t, q)
	_, err = Modularity(adj, labels[1:], 1)
	require.Error(t, err, "a label per node")
	_, err = Modularity([][]louvain.Edge{{{To: 0, Weight: 1}}}, []int{0}, 1)
	require.ErrorIs(t, err, louvain.ErrInvalidGraph)
}

func TestStraysAndAttach(t *testing.T) {
	vecs := [][]float32{
		unit(1, 0, 0), unit(1, 0.1, 0), // interest 0 around x
		unit(0, 1, 0), unit(0.1, 1, 0), // interest 1 around y
		unit(1, 0.2, 0.1), // a stray close to x
		unit(0, 0, 1),     // a stray close to nothing
	}
	labels := []int{0, 0, 1, 1, Noise, Noise}
	groups := Groups(labels)
	s := StraysOf(vecs, labels, groups, Centroids(vecs, groups), []float64{0.5, 0.99})
	assert.Equal(t, 2, s.Count)
	assert.Equal(t, 0, s.NearestGroup[4])
	assert.Greater(t, s.NearestPer[4], 0.9)
	assert.Less(t, s.NearestPer[5], 0.1)
	assert.Equal(t, -1, s.NearestGroup[0])
	assert.True(t, math.IsNaN(s.NearestPer[0]))
	require.Len(t, s.Attach, 2)
	assert.Equal(t, 1, s.Attach[0].Count)
	assert.InDelta(t, 0.5, s.Attach[0].ShareOfStrays, 1e-12)
	assert.InDelta(t, 5.0/6, s.Attach[0].CoverageAfter, 1e-12)
	assert.Equal(t, 0, s.Attach[1].Count)
	assert.Equal(t, 4, s.MemberLOO.N)

	assert.Equal(t, []int{0, 0, 1, 1, 0, Noise}, AttachStrays(labels, groups, s, 0.5))
	assert.Equal(t, labels, AttachStrays(labels, groups, s, 0.999))
}

func TestSplitHalfCosines(t *testing.T) {
	vecs, labels := randomCorpus(9, 400, 16, 4)
	groups := Groups(labels)
	a := SplitHalfCosines(vecs, groups, 6, 1)
	assert.Len(t, a, 4)
	assert.Equal(t, a, SplitHalfCosines(vecs, groups, 6, 1), "seeded")
	for _, c := range a {
		assert.Greater(t, c, 0.5, "halves of one interest point the same way")
	}
	assert.Empty(t, SplitHalfCosines(vecs, groups, 1000, 1))
}

func TestARIAndNMI_KnownValues(t *testing.T) {
	// scikit-learn: adjusted_rand_score([0,0,1,1],[0,0,1,2]) = 4/7 and
	// normalized_mutual_info_score(...) = 0.8.
	assert.InDelta(t, 4.0/7, ARI([]int{0, 0, 1, 1}, []int{0, 0, 1, 2}), 1e-12)
	assert.InDelta(t, 0.8, NMI([]int{0, 0, 1, 1}, []int{0, 0, 1, 2}), 1e-12)
	assert.InDelta(t, 1, ARI([]int{0, 0, 1, 1}, []int{5, 5, 2, 2}), 1e-12)
	assert.InDelta(t, 1, NMI([]int{0, 0, 1, 1}, []int{5, 5, 2, 2}), 1e-12)
	// Maximally discordant: ARI below zero.
	assert.Less(t, ARI([]int{0, 0, 1, 1}, []int{0, 1, 0, 1}), 0.0)
}

func TestCompare(t *testing.T) {
	ref := []int{0, 0, 0, 0, 1, 1, 1, Noise, Noise}
	same := []int{4, 4, 4, 4, 2, 2, 2, Noise, Noise}
	a := Compare(ref, same)
	assert.InDelta(t, 1, a.ARI, 1e-12)
	assert.InDelta(t, 1, a.NMI, 1e-12)
	assert.InDelta(t, 1, a.Kept, 1e-12)
	assert.InDelta(t, 1, a.NoiseJaccard, 1e-12)
	assert.Equal(t, 9, a.Shared)

	// Interest 0 keeps 3 of 4 (> 70%), interest 1 loses two members to
	// noise and keeps 1 of 3.
	moved := []int{4, 4, 4, Noise, 2, Noise, Noise, Noise, Noise}
	a = Compare(ref, moved)
	assert.InDelta(t, 0.5, a.Kept, 1e-12)
	assert.InDelta(t, 4.0/7, a.KeptDocs, 1e-12)
	assert.InDelta(t, 2.0/5, a.NoiseJaccard, 1e-12)
	assert.Less(t, a.ARI, 1.0)
	// Among the points clustered in both runs, the grouping is identical.
	assert.InDelta(t, 1, a.ARIClustered, 1e-12)

	assert.Equal(t, Agreement{}, Compare(nil, nil))
	assert.InDelta(t, 1, Compare([]int{0, 0, 1}, []int{1, 1, 0}).NoiseJaccard, 1e-12, "no noise in either run")
}

func TestDuplicateLabels(t *testing.T) {
	d := DuplicateLabels([]string{
		"AWS Lambda Functions", "AWS Lambda", "aws lambda functions!", "Stock Valuation", "", "",
	})
	// Exact: 0 and 2. Near: 0-1 and 1-2 (2 of 3 words).
	assert.Equal(t, 1, d.Exact)
	assert.Equal(t, 2, d.Near)
	assert.Equal(t, 3, d.Interests)
	assert.Len(t, d.Pairs, 3)

	d = DuplicateLabels([]string{"Investment Psychology", "Investment Strategy and Philosophy"})
	assert.Zero(t, d.Exact+d.Near, "1 of 4 words")
}

func TestEvaluate(t *testing.T) {
	vecs, labels := randomCorpus(11, 500, 32, 5)
	points := make([]insight.Point, len(vecs))
	for i, v := range vecs {
		points[i] = insight.Point{ID: string(rune(i)), Vector: v}
	}
	adj, err := insight.KNNGraph(t.Context(), points, 10, 0.3)
	require.NoError(t, err)
	r, err := Evaluate(vecs, labels, Options{Graph: adj, DuplicateThresholds: []float64{0.9}, StrayThresholds: []float64{0.4}})
	require.NoError(t, err)
	assert.Equal(t, 500, r.Documents)
	assert.InDelta(t, 0.8, r.Coverage, 1e-12)
	assert.Equal(t, 5, r.Sizes.Interests)
	require.NotNil(t, r.Modularity)
	assert.Greater(t, *r.Modularity, 0.3, "planted clusters are modular")
	assert.Greater(t, r.Silhouette, 0.0)
	assert.Equal(t, 100, r.Strays.Count)
	assert.Zero(t, r.Separation.Duplicates[0].Pairs, "random centers are far apart")

	r, err = Evaluate(vecs, labels, Options{})
	require.NoError(t, err)
	assert.Nil(t, r.Modularity)
	_, err = Evaluate(vecs, labels[1:], Options{})
	require.Error(t, err)
}

func TestInherit(t *testing.T) {
	n := Noise
	// Old 0 (4 members) survives as new 7; old 1 and old 2 merge into new
	// 8, which only one of them can name; old 3 scatters.
	prev := []int{0, 0, 0, 0, 1, 1, 1, 2, 2, 3, 3, 3, n}
	next := []int{7, 7, 7, 7, 8, 8, 8, 8, 8, 5, 6, n, 9}
	inh := Inherit(prev, next)
	assert.Equal(t, map[int]int{7: 0, 8: 1}, inh.Heirs, "the larger overlap names the merge")
	assert.InDelta(t, 2.0/4, inh.Named, 1e-12)
	assert.InDelta(t, 7.0/12, inh.NamedDocs, 1e-12)
	assert.InDelta(t, 2.0/5, inh.Carried, 1e-12)

	same := Inherit([]int{0, 0, 1, 1}, []int{3, 3, 4, 4})
	assert.InDelta(t, 1, same.Named, 1e-12)
	assert.InDelta(t, 1, same.Carried, 1e-12)
	assert.Equal(t, Inheritance{Heirs: map[int]int{}}, Inherit([]int{n}, []int{n}))
}

// TestInherit_DifferentLengths: labelings of different points share
// nothing, however alike their labels.
func TestInherit_DifferentLengths(t *testing.T) {
	assert.Equal(t, Inheritance{Heirs: map[int]int{}}, Inherit([]int{0, 0, 0}, []int{0, 0}))
	assert.Equal(t, Inheritance{Heirs: map[int]int{}}, Inherit(nil, []int{0, 0, 0}))
}
