package layout

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
)

// symEigen returns the eigenvalues of the symmetric matrix a, largest first
// (ties in their original order), and the eigenvectors as rows, by cyclic
// Jacobi rotations; a is not modified. It is for the small matrices of
// classical MDS and the Rayleigh-Ritz steps: O(n³) a sweep.
func symEigen(a [][]float64) ([]float64, [][]float64) {
	n := len(a)
	m := make([][]float64, n)
	v := make([][]float64, n)
	var frob float64
	for i := range a {
		m[i] = slices.Clone(a[i])
		v[i] = make([]float64, n)
		v[i][i] = 1
		for _, x := range a[i] {
			frob += x * x
		}
	}
	// Converged once what is left off the diagonal is rounding error.
	tol := frob * 1e-30
	for range jacobiSweeps {
		var off float64
		for i := range n {
			for j := i + 1; j < n; j++ {
				off += m[i][j] * m[i][j]
			}
		}
		if off <= tol {
			break
		}
		for p := range n {
			for q := p + 1; q < n; q++ {
				rotate(m, v, p, q)
			}
		}
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(x, y int) int { return -compareFloat(m[x][x], m[y][y]) })
	vals := make([]float64, n)
	vecs := make([][]float64, n)
	for r, k := range order {
		vals[r] = m[k][k]
		vecs[r] = make([]float64, n)
		for i := range n {
			vecs[r][i] = v[i][k]
		}
	}
	return vals, vecs
}

// jacobiSweeps caps symEigen's sweeps; a symmetric matrix converges
// quadratically, in well under ten for the sizes here.
const jacobiSweeps = 100

// rotate zeroes m[p][q] with one Jacobi rotation, accumulating it in v.
func rotate(m, v [][]float64, p, q int) {
	if m[p][q] == 0 {
		return
	}
	theta := (m[q][q] - m[p][p]) / (2 * m[p][q])
	t := 1 / (math.Abs(theta) + math.Sqrt(theta*theta+1))
	if theta < 0 {
		t = -t
	}
	c := 1 / math.Sqrt(t*t+1)
	s := t * c
	for k := range m {
		mkp, mkq := m[k][p], m[k][q]
		m[k][p], m[k][q] = c*mkp-s*mkq, s*mkp+c*mkq
	}
	for k := range m {
		mpk, mqk := m[p][k], m[q][k]
		m[p][k], m[q][k] = c*mpk-s*mqk, s*mpk+c*mqk
	}
	for k := range v {
		vkp, vkq := v[k][p], v[k][q]
		v[k][p], v[k][q] = c*vkp-s*vkq, s*vkp+c*vkq
	}
}

func compareFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// The principal axes' subspace iteration: a block of pcaBlock vectors, two
// more than the axes wanted, so the second converges at the rate of the
// fifth eigenvalue over the second rather than the third over the second;
// it stops once the axes turn by less than pcaTolerance (1 − |cos|) in an
// iteration, or after pcaMaxIterations.
const (
	pcaBlock         = 4
	pcaMaxIterations = 300
	pcaTolerance     = 1e-14
	// pcaChunk is the rows one part of an iteration sums: the parts are
	// summed in order, so the result is the same however many goroutines
	// sum them.
	pcaChunk = 256
)

// rowSet is a set of rows centred on their mean: the rows at idx of rows
// (all of them when idx is nil).
type rowSet struct {
	rows [][]float32
	idx  []int
	mean []float64
}

func newRowSet(rows [][]float32, idx []int) rowSet {
	s := rowSet{rows: rows, idx: idx}
	n := s.len()
	if n == 0 {
		return s
	}
	s.mean = make([]float64, len(s.row(0)))
	for k := range n {
		for j, x := range s.row(k) {
			s.mean[j] += float64(x)
		}
	}
	for j := range s.mean {
		s.mean[j] /= float64(n)
	}
	return s
}

func (s rowSet) len() int {
	if s.idx != nil {
		return len(s.idx)
	}
	return len(s.rows)
}

func (s rowSet) row(k int) []float32 {
	if s.idx != nil {
		return s.rows[s.idx[k]]
	}
	return s.rows[k]
}

// project is the centred row k's coordinate along each of axes.
func (s rowSet) project(k int, axes [][]float64) []float64 {
	r := s.row(k)
	out := make([]float64, len(axes))
	for a, ax := range axes {
		var sum float64
		for j, x := range r {
			sum += (float64(x) - s.mean[j]) * ax[j]
		}
		out[a] = sum
	}
	return out
}

// principalAxes returns the set's k leading principal axes, unit vectors of
// the rows' width, and the variance along each (the scatter matrix's
// eigenvalues), largest first. It never forms the scatter matrix or a
// float64 copy of the rows: each iteration applies it to the block through
// the rows, subtracting the mean on the fly. Axes beyond the set's rank are
// zero vectors with variance zero. The start is seeded.
func principalAxes(ctx context.Context, s rowSet, k int, seed uint64) ([][]float64, []float64, error) {
	n := s.len()
	if n == 0 || k <= 0 {
		return nil, nil, nil
	}
	dim := len(s.mean)
	b := min(pcaBlock, dim)
	k = min(k, b)
	rng := rand.New(rand.NewPCG(seed, pcaStream)) //nolint:gosec // G404: a seeded start, not a secret
	q := make([][]float64, b)
	for i := range q {
		q[i] = make([]float64, dim)
		for j := range q[i] {
			q[i][j] = rng.NormFloat64()
		}
	}
	orthonormalize(q)
	var axes, prev [][]float64
	var vals []float64
	for it := 0; ; it++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("layout: principal axes: %w", err)
		}
		z := s.scatter(q)
		// Rayleigh-Ritz on the block: the Ritz vectors are the current
		// axes, and A·(their combination) the next block.
		t := make([][]float64, b)
		for i := range t {
			t[i] = make([]float64, b)
			for j := range t[i] {
				t[i][j] = dotF(q[i], z[j])
			}
		}
		symmetrize(t)
		var w [][]float64
		vals, w = symEigen(t)
		axes = combine(q, w)
		if it > 0 && turned(prev, axes, k) < pcaTolerance || it == pcaMaxIterations {
			break
		}
		prev = axes
		q = combine(z, w)
		orthonormalize(q)
	}
	for i := range vals {
		vals[i] = math.Max(vals[i], 0)
	}
	return axes[:k], vals[:k], nil
}

// pcaStream is the PCG stream of principalAxes' start.
const pcaStream = 0x9ca

// scatter returns A·q for each vector of q, A the set's scatter matrix
// Xcᵀ·Xc, summed in parts of pcaChunk rows that goroutines take in turn and
// that are then added up in order.
func (s rowSet) scatter(q [][]float64) [][]float64 {
	n, dim, b := s.len(), len(s.mean), len(q)
	parts := (n + pcaChunk - 1) / pcaChunk
	type part struct {
		sum   [][]float64 // Σ (r·q_i − μ·q_i) r
		coeff []float64   // Σ (r·q_i − μ·q_i)
	}
	sums := make([]part, parts)
	mq := make([]float64, b)
	for i := range q {
		mq[i] = dotF(s.mean, q[i])
	}
	work := func(c int) {
		p := part{sum: make([][]float64, b), coeff: make([]float64, b)}
		for i := range p.sum {
			p.sum[i] = make([]float64, dim)
		}
		proj := make([]float64, b)
		for k := c * pcaChunk; k < min(n, (c+1)*pcaChunk); k++ {
			r := s.row(k)
			for i := range q {
				var sum float64
				for j, x := range r {
					sum += float64(x) * q[i][j]
				}
				proj[i] = sum - mq[i]
			}
			for i, w := range proj {
				p.coeff[i] += w
				row := p.sum[i]
				for j, x := range r {
					row[j] += w * float64(x)
				}
			}
		}
		sums[c] = p
	}
	if parts == 1 {
		work(0)
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		for range min(runtime.GOMAXPROCS(0), parts) {
			wg.Go(func() {
				for c := int(next.Add(1)) - 1; c < parts; c = int(next.Add(1)) - 1 {
					work(c)
				}
			})
		}
		wg.Wait()
	}
	out := make([][]float64, b)
	for i := range out {
		out[i] = make([]float64, dim)
		var coeff float64
		for _, p := range sums {
			coeff += p.coeff[i]
			for j, x := range p.sum[i] {
				out[i][j] += x
			}
		}
		for j := range out[i] {
			out[i][j] -= coeff * s.mean[j]
		}
	}
	return out
}

// orthonormalize makes q's vectors orthonormal in order by modified
// Gram-Schmidt; a vector left with (almost) nothing of its own becomes
// zero.
func orthonormalize(q [][]float64) {
	for i := range q {
		before := norm(q[i])
		for j := range i {
			p := dotF(q[i], q[j])
			for d := range q[i] {
				q[i][d] -= p * q[j][d]
			}
		}
		n := norm(q[i])
		if n <= 1e-10*before || n == 0 {
			clear(q[i])
			continue
		}
		for d := range q[i] {
			q[i][d] /= n
		}
	}
}

// combine returns the vectors Σ_j w[i][j]·q[j], one per row of w.
func combine(q, w [][]float64) [][]float64 {
	out := make([][]float64, len(w))
	for i, wi := range w {
		out[i] = make([]float64, len(q[0]))
		for j, c := range wi {
			for d, x := range q[j] {
				out[i][d] += c * x
			}
		}
	}
	return out
}

// turned is the most any of the first k axes turned from prev to cur, as
// 1 − |cos|.
func turned(prev, cur [][]float64, k int) float64 {
	var worst float64
	for i := range k {
		if dotF(prev[i], prev[i]) == 0 && dotF(cur[i], cur[i]) == 0 {
			continue // an axis beyond the rank
		}
		worst = math.Max(worst, 1-math.Abs(dotF(prev[i], cur[i])))
	}
	return worst
}

func symmetrize(t [][]float64) {
	for i := range t {
		for j := i + 1; j < len(t); j++ {
			avg := (t[i][j] + t[j][i]) / 2
			t[i][j], t[j][i] = avg, avg
		}
	}
}

func dotF(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func norm(a []float64) float64 { return math.Sqrt(dotF(a, a)) }

// cosine is the cosine of two float32 vectors, 0 when either is zero.
func cosine(a, b []float32) float64 {
	var ab, aa, bb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		ab += x * y
		aa += x * x
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return ab / math.Sqrt(aa*bb)
}

// classicalMDS embeds the distance matrix dm in the plane by Torgerson's
// method: the top two eigenpairs of the double-centred squared distances.
func classicalMDS(dm [][]float64) []XY {
	n := len(dm)
	switch n {
	case 0:
		return nil
	case 1:
		return []XY{{}}
	}
	b := make([][]float64, n)
	rowMean := make([]float64, n)
	var all float64
	for i := range dm {
		b[i] = make([]float64, n)
		for j := range dm {
			d2 := dm[i][j] * dm[i][j]
			b[i][j] = d2
			rowMean[i] += d2 / float64(n)
		}
		all += rowMean[i] / float64(n)
	}
	for i := range b {
		for j := range b {
			b[i][j] = -0.5 * (b[i][j] - rowMean[i] - rowMean[j] + all)
		}
	}
	vals, vecs := symEigen(b)
	s0, s1 := math.Sqrt(math.Max(vals[0], 0)), math.Sqrt(math.Max(vals[1], 0))
	out := make([]XY, n)
	for i := range out {
		out[i] = XY{vecs[0][i] * s0, vecs[1][i] * s1}
	}
	return out
}

// stressIterations caps stress majorization; it stops sooner once an
// iteration lowers the stress by less than stressTolerance of it.
const (
	stressIterations = 500
	stressTolerance  = 1e-6
)

// stressMajorize lowers Σ (|x_i − x_j| − d_ij)² from start by localized
// (Gauss-Seidel) majorization (Gansner, Koren and North, 2004), moving only
// the points mobile marks (all of them when mobile is nil), for at most
// iterations. start is scaled first to fit the distances' scale when every
// point may move.
func stressMajorize(ctx context.Context, dm [][]float64, start []XY, mobile []bool, iterations int) ([]XY, error) {
	n := len(dm)
	x := slices.Clone(start)
	if n < 2 {
		return x, nil
	}
	if mobile == nil {
		scaleToDistances(dm, x)
	}
	prev := stress(dm, x)
	for range iterations {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("layout: stress majorization: %w", err)
		}
		for i := range n {
			if mobile != nil && !mobile[i] {
				continue
			}
			var sx, sy float64
			for j := range n {
				if i == j {
					continue
				}
				e := math.Max(dist(x[i], x[j]), 1e-9)
				sx += x[j].X + dm[i][j]*(x[i].X-x[j].X)/e
				sy += x[j].Y + dm[i][j]*(x[i].Y-x[j].Y)/e
			}
			x[i] = XY{sx / float64(n-1), sy / float64(n-1)}
		}
		s := stress(dm, x)
		if prev-s < stressTolerance*prev {
			break
		}
		prev = s
	}
	return x, nil
}

// scaleToDistances scales x about the origin by the factor that best fits
// its distances to dm.
func scaleToDistances(dm [][]float64, x []XY) {
	var num, den float64
	for i := range x {
		for j := i + 1; j < len(x); j++ {
			e := dist(x[i], x[j])
			num += e * dm[i][j]
			den += e * e
		}
	}
	if den == 0 {
		return
	}
	s := num / den
	for i := range x {
		x[i] = XY{x[i].X * s, x[i].Y * s}
	}
}

func stress(dm [][]float64, x []XY) float64 {
	var s float64
	for i := range x {
		for j := i + 1; j < len(x); j++ {
			e := dist(x[i], x[j]) - dm[i][j]
			s += e * e
		}
	}
	return s
}

// transform is a similarity transform of the plane: y = scale·R·(x − from)
// + to, R a rotation or a reflection.
type transform struct {
	r        [2][2]float64
	scale    float64
	from, to XY
}

func (t transform) apply(p XY) XY {
	dx, dy := p.X-t.from.X, p.Y-t.from.Y
	return XY{t.to.X + t.scale*(dx*t.r[0][0]+dy*t.r[1][0]), t.to.Y + t.scale*(dx*t.r[0][1]+dy*t.r[1][1])}
}

// procrustes is the transform that best maps src onto dst in least squares:
// a rotation or reflection and a translation, and a uniform scale when
// scaled. Points src that coincide give the identity rotation.
func procrustes(src, dst []XY, scaled bool) transform {
	cs, cd := centroid(src), centroid(dst)
	var m [2][2]float64
	var ss float64
	for i := range src {
		ax, ay := src[i].X-cs.X, src[i].Y-cs.Y
		bx, by := dst[i].X-cd.X, dst[i].Y-cd.Y
		m[0][0] += ax * bx
		m[0][1] += ax * by
		m[1][0] += ay * bx
		m[1][1] += ay * by
		ss += ax*ax + ay*ay
	}
	r, sigma := polar(m)
	t := transform{r: r, scale: 1, from: cs, to: cd}
	if scaled && ss > 0 && sigma > 0 {
		t.scale = sigma / ss
	}
	return t
}

// polar returns the orthogonal factor R = U·Vᵀ of m's SVD U·S·Vᵀ, which
// maximises trace(Rᵀm) over rotations and reflections, and the sum of m's
// singular values. A zero m gives the identity.
func polar(m [2][2]float64) ([2][2]float64, float64) {
	// mᵀm = V·S²·Vᵀ.
	a := m[0][0]*m[0][0] + m[1][0]*m[1][0]
	b := m[0][0]*m[0][1] + m[1][0]*m[1][1]
	c := m[0][1]*m[0][1] + m[1][1]*m[1][1]
	vals, vecs := symEigen([][]float64{{a, b}, {b, c}})
	s0, s1 := math.Sqrt(math.Max(vals[0], 0)), math.Sqrt(math.Max(vals[1], 0))
	if s0 == 0 {
		return [2][2]float64{{1, 0}, {0, 1}}, 0
	}
	v0 := [2]float64{vecs[0][0], vecs[0][1]}
	v1 := [2]float64{-v0[1], v0[0]}
	mv := func(v [2]float64) [2]float64 {
		return [2]float64{m[0][0]*v[0] + m[0][1]*v[1], m[1][0]*v[0] + m[1][1]*v[1]}
	}
	u0 := mv(v0)
	u0 = [2]float64{u0[0] / s0, u0[1] / s0}
	u1 := [2]float64{-u0[1], u0[0]}
	if s1 > 1e-12*s0 {
		u1 = mv(v1)
		u1 = [2]float64{u1[0] / s1, u1[1] / s1}
	}
	var r [2][2]float64
	for i := range 2 {
		for j := range 2 {
			r[i][j] = u0[i]*v0[j] + u1[i]*v1[j]
		}
	}
	return r, s0 + s1
}

func centroid(p []XY) XY {
	var c XY
	for _, q := range p {
		c.X += q.X
		c.Y += q.Y
	}
	if len(p) > 0 {
		c.X /= float64(len(p))
		c.Y /= float64(len(p))
	}
	return c
}
