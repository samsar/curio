package quality

import (
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/samsar/curio/internal/insight"
)

// KeepFraction is the share of an interest's members its best match in
// another run must hold for the interest to count as kept: carry-over's.
const KeepFraction = insight.KeepFraction

// Agreement compares two partitions of the same points.
type Agreement struct {
	// Shared is how many points both partitions label.
	Shared int `json:"shared"`
	// ARI is the adjusted Rand index and NMI the normalized mutual
	// information (arithmetic-mean normalization, scikit-learn's default),
	// each with every noise point as a community of its own: two points
	// agree only by sharing an interest in both runs, or by being apart in
	// both. 1 is identical; ARI is about 0 for unrelated partitions.
	ARI float64 `json:"ari"`
	NMI float64 `json:"nmi"`
	// ARIClustered is the ARI over the points clustered in both runs only:
	// whether the documents both runs keep are grouped alike.
	ARIClustered float64 `json:"ari_clustered"`
	// Kept is the share of the reference's interests whose best match in
	// the other run (the interest holding most of its members) holds more
	// than KeepFraction of them; KeptDocs weights that by interest size.
	Kept     float64 `json:"kept"`
	KeptDocs float64 `json:"kept_docs"`
	// NoiseJaccard is the Jaccard index of the two runs' noise sets: 1
	// when neither leaves any point out.
	NoiseJaccard float64 `json:"noise_jaccard"`
}

// Compare measures how far other agrees with ref; both label the same
// points, in the same order.
func Compare(ref, other []int) Agreement {
	a := Agreement{Shared: len(ref)}
	if len(ref) == 0 || len(ref) != len(other) {
		return a
	}
	a.ARI = ARI(singletons(ref), singletons(other))
	a.NMI = NMI(singletons(ref), singletons(other))

	var rc, oc []int
	noiseBoth, noiseEither := 0, 0
	for i := range ref {
		rn, on := ref[i] == Noise, other[i] == Noise
		if rn && on {
			noiseBoth++
		}
		if rn || on {
			noiseEither++
		}
		if !rn && !on {
			rc = append(rc, ref[i])
			oc = append(oc, other[i])
		}
	}
	a.ARIClustered = ARI(rc, oc)
	a.NoiseJaccard = 1
	if noiseEither > 0 {
		a.NoiseJaccard = float64(noiseBoth) / float64(noiseEither)
	}

	// Best match per reference interest, by overlap with the other run's
	// interests (noise is no match).
	overlap := make(map[[2]int]int)
	size := make(map[int]int)
	for i, l := range ref {
		if l == Noise {
			continue
		}
		size[l]++
		if other[i] != Noise {
			overlap[[2]int{l, other[i]}]++
		}
	}
	best := make(map[int]int)
	for k, v := range overlap {
		best[k[0]] = max(best[k[0]], v)
	}
	kept, keptDocs, docs := 0, 0, 0
	for l, n := range size {
		docs += n
		if float64(best[l]) > KeepFraction*float64(n) {
			kept++
			keptDocs += n
		}
	}
	if len(size) > 0 {
		a.Kept = float64(kept) / float64(len(size))
		a.KeptDocs = float64(keptDocs) / float64(docs)
	}
	return a
}

// singletons gives every noise point a community of its own, numbered below
// every interest so none collides.
func singletons(labels []int) []int {
	out := make([]int, len(labels))
	for i, l := range labels {
		if l == Noise {
			out[i] = -(i + 2)
		} else {
			out[i] = l
		}
	}
	return out
}

// contingency counts co-membership of the two labelings.
func contingency(a, b []int) (cells map[[2]int]int, rows, cols map[int]int) {
	cells = make(map[[2]int]int)
	rows = make(map[int]int)
	cols = make(map[int]int)
	for i := range a {
		cells[[2]int{a[i], b[i]}]++
		rows[a[i]]++
		cols[b[i]]++
	}
	return cells, rows, cols
}

func choose2(n int) float64 { return float64(n) * float64(n-1) / 2 }

// ARI is the adjusted Rand index of two labelings of the same points, every
// label value a cluster (noise included: see singletons). Two labelings
// that both put every point alone, or both put every point together, score 1.
func ARI(a, b []int) float64 {
	n := len(a)
	if n < 2 {
		return 1
	}
	cells, rows, cols := contingency(a, b)
	var index, sumRows, sumCols float64
	for _, v := range cells {
		index += choose2(v)
	}
	for _, v := range rows {
		sumRows += choose2(v)
	}
	for _, v := range cols {
		sumCols += choose2(v)
	}
	expected := sumRows * sumCols / choose2(n)
	maxIndex := (sumRows + sumCols) / 2
	if maxIndex == expected {
		return 1
	}
	return (index - expected) / (maxIndex - expected)
}

// NMI is the normalized mutual information of two labelings of the same
// points, I(A;B) / ((H(A)+H(B))/2); 1 when both have zero entropy.
func NMI(a, b []int) float64 {
	n := float64(len(a))
	if n == 0 {
		return 1
	}
	cells, rows, cols := contingency(a, b)
	entropy := func(m map[int]int) float64 {
		var h float64
		for _, v := range m {
			p := float64(v) / n
			h -= p * math.Log(p)
		}
		return h
	}
	ha, hb := entropy(rows), entropy(cols)
	var mi float64
	for k, v := range cells {
		p := float64(v) / n
		mi += p * math.Log(p*n*n/(float64(rows[k[0]])*float64(cols[k[1]])))
	}
	if ha+hb == 0 {
		return 1
	}
	return max(0, 2*mi/(ha+hb))
}

// Inheritance is how many groups keep their identity across two runs when
// a new group inherits an old group's ID and label: insight.Carry's rule,
// one to one, a new group inheriting from the old group it holds more than
// KeepFraction of, pairs taken largest overlap first so a merge passes the
// name to one heir.
type Inheritance struct {
	// Named is the share of the old run's groups whose name lives on in
	// the new run; NamedDocs weights that by old group size.
	Named     float64 `json:"named"`
	NamedDocs float64 `json:"named_docs"`
	// Carried is the share of the new run's groups that carry an old name
	// (the rest are new to the reader).
	Carried float64 `json:"carried"`
	// Heirs maps each new group label to the old label it inherits.
	Heirs map[int]int `json:"-"`
}

// Inherit decides which groups of next inherit a group of prev with
// insight.Carry; both label the same points in the same order, Noise for
// none, and a group's size is counted over these points. Labels become
// IDs that sort as the labels do, so ties go to the lower label as in
// Carry. Labelings of different lengths share nothing.
func Inherit(prev, next []int) Inheritance {
	inh := Inheritance{Heirs: map[int]int{}}
	if len(prev) != len(next) {
		return inh
	}
	oldLabels, newLabels := labelsOf(prev), labelsOf(next)
	in := insight.CarryInput{
		Old:     make([]insight.OldGroup, len(oldLabels)),
		New:     make([]insight.NewGroup, len(newLabels)),
		Library: make([]string, len(prev)),
	}
	oldIndex, newIndex := indexOf(oldLabels), indexOf(newLabels)
	oldByID := make(map[string]int, len(oldLabels))
	for o := range in.Old {
		in.Old[o].ID = fmt.Sprintf("%010d", o)
		oldByID[in.Old[o].ID] = o
	}
	for j := range in.New {
		in.New[j].Parent = -1
	}
	sizes := make([]int, len(oldLabels))
	for i := range prev {
		doc := strconv.Itoa(i)
		in.Library[i] = doc
		if prev[i] != Noise {
			o := oldIndex[prev[i]]
			in.Old[o].Members = append(in.Old[o].Members, doc)
			sizes[o]++
		}
		if next[i] != Noise {
			j := newIndex[next[i]]
			in.New[j].Members = append(in.New[j].Members, doc)
		}
	}
	c, err := insight.Carry(in)
	if err != nil {
		// One label per point makes groups that never overlap, all of
		// them in the library.
		panic(fmt.Sprintf("quality: carry-over of a labeling: %v", err))
	}
	named, namedDocs, docs := 0, 0, 0
	for o, f := range c.Fates {
		docs += sizes[o]
		if f.Kept {
			named++
			namedDocs += sizes[o]
		}
	}
	for j, pred := range c.Predecessor {
		if pred != "" {
			inh.Heirs[newLabels[j]] = oldLabels[oldByID[pred]]
		}
	}
	if len(oldLabels) > 0 {
		inh.Named = float64(named) / float64(len(oldLabels))
		inh.NamedDocs = float64(namedDocs) / float64(docs)
	}
	if len(newLabels) > 0 {
		inh.Carried = float64(named) / float64(len(newLabels))
	}
	return inh
}

// labelsOf returns the distinct labels other than Noise, ascending.
func labelsOf(labels []int) []int {
	out := slices.Clone(labels)
	slices.Sort(out)
	out = slices.Compact(out)
	return slices.DeleteFunc(out, func(l int) bool { return l == Noise })
}

func indexOf(labels []int) map[int]int {
	idx := make(map[int]int, len(labels))
	for i, l := range labels {
		idx[l] = i
	}
	return idx
}
