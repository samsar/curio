package layout

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gonum.org/v1/gonum/mat"
)

// TestSymEigen_MatchesGonum: the small symmetric eigen-solver of the
// Rayleigh-Ritz steps agrees with gonum's on random matrices, eigenvalues
// and eigenvectors (up to sign) to 1e-6.
func TestSymEigen_MatchesGonum(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{2, 3, 5, 12, 40} {
		a := make([][]float64, n)
		flat := make([]float64, n*n)
		for i := range a {
			a[i] = make([]float64, n)
		}
		for i := range n {
			for j := i; j < n; j++ {
				x := r.NormFloat64()
				a[i][j], a[j][i] = x, x
				flat[i*n+j], flat[j*n+i] = x, x
			}
		}
		vals, vecs := symEigen(a)
		var es mat.EigenSym
		require.True(t, es.Factorize(mat.NewSymDense(n, flat), true))
		want := es.Values(nil) // ascending
		var ev mat.Dense
		es.VectorsTo(&ev)
		for k := range n {
			g := n - 1 - k
			assert.InDelta(t, want[g], vals[k], 1e-6, "n %d, eigenvalue %d", n, k)
			sign := math.Copysign(1, dotF(vecs[k], mat.Col(nil, g, &ev)))
			for i := range n {
				assert.InDelta(t, ev.At(i, g), sign*vecs[k][i], 1e-6, "n %d, eigenvector %d", n, k)
			}
		}
	}
}

// TestPrincipalAxes_MatchesGonum: the top two principal axes, found by
// subspace iteration through the rows, agree with gonum's SVD of the
// centred matrix to 1e-6 (up to sign), and so do their variances
// (relative), on random rows of a decaying spectrum, whole or a subset.
func TestPrincipalAxes_MatchesGonum(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for _, size := range []struct{ n, dim int }{{50, 8}, {300, 64}, {700, 200}} {
		rows := make([][]float32, size.n)
		for i := range rows {
			rows[i] = make([]float32, size.dim)
			for d := range rows[i] {
				rows[i][d] = float32(r.NormFloat64()*math.Pow(0.85, float64(d)) + 0.3)
			}
		}
		for _, idx := range [][]int{nil, evens(size.n)} {
			set := newRowSet(rows, idx)
			axes, vals, err := principalAxes(context.Background(), set, 2, 9)
			require.NoError(t, err)
			wantAxes, wantVals := gonumAxes(t, set)
			for k := range 2 {
				sign := math.Copysign(1, dotF(axes[k], wantAxes[k]))
				for d := range size.dim {
					assert.InDelta(t, wantAxes[k][d], sign*axes[k][d], 1e-6, "%v: axis %d", size, k)
				}
				assert.InEpsilon(t, wantVals[k], vals[k], 1e-6, "%v: variance %d", size, k)
			}
		}
	}
}

func evens(n int) []int {
	var out []int
	for i := 0; i < n; i += 2 {
		out = append(out, i)
	}
	return out
}

// gonumAxes are the set's first two principal axes and their variances
// (squared singular values) by gonum's SVD of the centred rows.
func gonumAxes(t *testing.T, s rowSet) ([2][]float64, [2]float64) {
	t.Helper()
	n, dim := s.len(), len(s.mean)
	x := mat.NewDense(n, dim, nil)
	for k := range n {
		for d, v := range s.row(k) {
			x.Set(k, d, float64(v)-s.mean[d])
		}
	}
	var svd mat.SVD
	require.True(t, svd.Factorize(x, mat.SVDThin))
	var v mat.Dense
	svd.VTo(&v)
	sv := svd.Values(nil)
	return [2][]float64{mat.Col(nil, 0, &v), mat.Col(nil, 1, &v)}, [2]float64{sv[0] * sv[0], sv[1] * sv[1]}
}

// TestPrincipalPlane_IsClassicalMDS: the plane of unit vectors is classical
// MDS of their chord distances, as gonum's eigendecomposition of the
// double-centred squared distances gives it, to 1e-6 (each axis up to
// sign).
func TestPrincipalPlane_IsClassicalMDS(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for _, size := range []struct{ n, dim int }{{12, 6}, {60, 24}, {200, 64}} {
		vecs := make([][]float32, size.n)
		for i := range vecs {
			v := make([]float64, size.dim)
			var sum float64
			for d := range v {
				v[d] = r.NormFloat64()*math.Pow(0.7, float64(d)) + 0.2
				sum += v[d] * v[d]
			}
			vecs[i] = make([]float32, size.dim)
			for d, x := range v {
				vecs[i][d] = float32(x / math.Sqrt(sum))
			}
		}
		got, err := principalPlane(context.Background(), vecs, 11)
		require.NoError(t, err)
		want := gonumMDS(t, vecs)
		for k := range 2 {
			var agree float64
			for i := range got {
				agree += coord(got[i], k) * want[k][i]
			}
			sign := math.Copysign(1, agree)
			for i := range got {
				assert.InDelta(t, want[k][i], sign*coord(got[i], k), 1e-6, "%v: point %d, axis %d", size, i, k)
			}
		}
	}
}

// TestPrincipalPlane_Cancelled: placing the centroids checks its context,
// so a deadline stops a cold layout of many groups where it is.
func TestPrincipalPlane_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := principalPlane(ctx, [][]float32{{1, 0}, {0, 1}, {1, 1}}, 1)
	require.ErrorIs(t, err, context.Canceled)
}

func coord(p XY, k int) float64 {
	if k == 0 {
		return p.X
	}
	return p.Y
}

// gonumMDS is classical MDS of the vectors' distances by gonum: the top two
// eigenvectors of −½·J·D²·J, each scaled by its eigenvalue's root.
func gonumMDS(t *testing.T, vecs [][]float32) [2][]float64 {
	t.Helper()
	n := len(vecs)
	d2 := make([][]float64, n)
	rowMean := make([]float64, n)
	var all float64
	for i := range vecs {
		d2[i] = make([]float64, n)
		for j := range vecs {
			for d := range vecs[i] {
				x := float64(vecs[i][d]) - float64(vecs[j][d])
				d2[i][j] += x * x
			}
			rowMean[i] += d2[i][j] / float64(n)
		}
		all += rowMean[i] / float64(n)
	}
	b := mat.NewSymDense(n, nil)
	for i := range n {
		for j := i; j < n; j++ {
			b.SetSym(i, j, -0.5*(d2[i][j]-rowMean[i]-rowMean[j]+all))
		}
	}
	var es mat.EigenSym
	require.True(t, es.Factorize(b, true))
	vals := es.Values(nil) // ascending
	var ev mat.Dense
	es.VectorsTo(&ev)
	var out [2][]float64
	for k := range 2 {
		g := n - 1 - k
		out[k] = mat.Col(nil, g, &ev)
		for i := range out[k] {
			out[k][i] *= math.Sqrt(vals[g])
		}
	}
	return out
}

// TestPrincipalAxes_RankDeficient: axes beyond a set's rank are zero, and
// a single row has none.
func TestPrincipalAxes_RankDeficient(t *testing.T) {
	rows := [][]float32{{1, 0, 0}, {3, 0, 0}, {2, 0, 0}}
	axes, vals, err := principalAxes(context.Background(), newRowSet(rows, nil), 2, 1)
	require.NoError(t, err)
	assert.InDelta(t, 1, math.Abs(axes[0][0]), 1e-12)
	assert.InDelta(t, 2, vals[0], 1e-12)
	assert.Zero(t, vals[1])
	assert.Equal(t, []float64{0, 0, 0}, axes[1])
}

// TestProcrustes: the transform recovers a rotation, a reflection, a scale
// and a translation; without scaling it keeps the scale.
func TestProcrustes(t *testing.T) {
	src := []XY{{0, 0}, {3, 1}, {1, 4}, {-2, 2}}
	for _, mirror := range []bool{false, true} {
		const angle, scale = 0.7, 2.5
		dst := make([]XY, len(src))
		for i, p := range src {
			if mirror {
				p.Y = -p.Y
			}
			dst[i] = XY{10 + scale*(p.X*math.Cos(angle)-p.Y*math.Sin(angle)), -4 + scale*(p.X*math.Sin(angle)+p.Y*math.Cos(angle))}
		}
		tr := procrustes(src, dst, true)
		assert.InDelta(t, scale, tr.scale, 1e-9)
		for i, p := range src {
			got := tr.apply(p)
			assert.InDelta(t, dst[i].X, got.X, 1e-9)
			assert.InDelta(t, dst[i].Y, got.Y, 1e-9)
		}
		assert.InDelta(t, 1, procrustes(src, dst, false).scale, 0)
	}
}

// TestPolar_IsOrthogonal: polar's factor is a rotation or a reflection and
// reaches the sum of m's singular values (gonum's SVD), whatever m's rank:
// full, one (m = a·bᵀ, as two points or collinear ones give), or zero. A
// rank-one m, which a turn fits as well as a mirror, gets the turn.
func TestPolar_IsOrthogonal(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for c := range 3000 {
		var m [2][2]float64
		rank := c % 3
		a, b := [2]float64{r.NormFloat64(), r.NormFloat64()}, [2]float64{r.NormFloat64(), r.NormFloat64()}
		for i := range 2 {
			for j := range 2 {
				switch rank {
				case 1:
					m[i][j] = 2 * a[i] * b[j]
				case 2:
					m[i][j] = r.NormFloat64()
				}
			}
		}
		rot, sum := polar(m)
		for i := range 2 {
			for j := range 2 {
				var dot float64
				for k := range 2 {
					dot += rot[k][i] * rot[k][j]
				}
				want := 0.0
				if i == j {
					want = 1
				}
				require.InDelta(t, want, dot, 1e-12, "case %d (rank %d): RᵀR[%d][%d]", c, rank, i, j)
			}
		}
		var svd mat.SVD
		require.True(t, svd.Factorize(mat.NewDense(2, 2, []float64{m[0][0], m[0][1], m[1][0], m[1][1]}), mat.SVDNone))
		values := svd.Values(nil)
		var trace float64
		for i := range 2 {
			for j := range 2 {
				trace += rot[i][j] * m[i][j]
			}
		}
		require.InDelta(t, values[0]+values[1], sum, 1e-9*(1+sum), "case %d (rank %d): the sum of singular values", c, rank)
		require.InDelta(t, sum, trace, 1e-9*(1+sum), "case %d (rank %d): R reaches it", c, rank)
		if rank < 2 {
			require.InDelta(t, 1, rot[0][0]*rot[1][1]-rot[0][1]*rot[1][0], 1e-12, "case %d (rank %d): a turn", c, rank)
		}
	}
}

// TestProcrustes_Degenerate: an unscaled alignment of two points, or of
// collinear ones, is rigid, keeping the distance between any two points of
// the plane, and maps the points onto a moved copy of themselves.
func TestProcrustes_Degenerate(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for c := range 500 {
		n := 2 + c%4
		dir := XY{r.NormFloat64(), r.NormFloat64()}
		src := make([]XY, n)
		for i := range src {
			s := r.NormFloat64() * 50
			src[i] = XY{3 + s*dir.X, -7 + s*dir.Y}
		}
		angle, mirror := r.Float64()*2*math.Pi, c%2 == 1
		to := XY{r.NormFloat64() * 100, r.NormFloat64() * 100}
		dst := make([]XY, n)
		for i, p := range src {
			if mirror {
				p.Y = -p.Y
			}
			dst[i] = XY{to.X + p.X*math.Cos(angle) - p.Y*math.Sin(angle), to.Y + p.X*math.Sin(angle) + p.Y*math.Cos(angle)}
		}
		tr := procrustes(src, dst, false)
		for i, p := range src {
			got := tr.apply(p)
			require.InDelta(t, dst[i].X, got.X, 1e-8, "case %d: point %d", c, i)
			require.InDelta(t, dst[i].Y, got.Y, 1e-8, "case %d: point %d", c, i)
		}
		for range 5 {
			p, q := XY{r.NormFloat64() * 100, r.NormFloat64() * 100}, XY{r.NormFloat64() * 100, r.NormFloat64() * 100}
			require.InDelta(t, dist(p, q), dist(tr.apply(p), tr.apply(q)), 1e-9*dist(p, q), "case %d: rigid", c)
		}
	}
}
