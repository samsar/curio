package layout

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
)

// Packing circles so none overlaps: separate pushes overlapping pairs apart
// for up to separationSweeps sweeps; when overlaps remain, it spreads the
// starting positions by spreadFactor about their centroid and tries again.
// Spreading multiplies every distance, and the starting centres are distinct
// (coincident ones are spread apart first), so a start with no overlap at all
// comes within a bounded number of tries, and separate always ends with none.
// compact then pulls the circles toward their centroid, one at a time, a move
// taken only when it overlaps nothing, for up to compactionRounds rounds.
const (
	separationSweeps = 100
	spreadFactor     = 1.25
	compactionRounds = 40
	// coincident is the distance, in dot radii, under which two centres are
	// spread apart before separating.
	coincident = 0.01
	// goldenAngle spreads coincident centres, and orders dots without a
	// direction.
	goldenAngle = 2.399963229728653
)

// overlapTolerance is the relative shortfall two circles may have and still
// count as apart: what rounding leaves. A push moves a pair separationSlack
// (in dot radii) past apart, so sweeps end rather than creep toward it.
const (
	overlapTolerance = 1e-9
	separationSlack  = 1e-3
)

// separate returns pos moved so that no two circles of radii r are closer
// than gap edge to edge.
func separate(ctx context.Context, pos []XY, r []float64, gap float64) ([]XY, error) {
	base := spreadCoincident(pos)
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("layout: packing: %w", err)
		}
		cand := slices.Clone(base)
		for range separationSweeps {
			if !pushApart(cand, r, gap) {
				break
			}
		}
		if !overlapping(cand, r, gap) {
			return cand, nil
		}
		c := centroid(base)
		for i, p := range base {
			base[i] = XY{c.X + spreadFactor*(p.X-c.X), c.Y + spreadFactor*(p.Y-c.Y)}
		}
	}
}

// pushApart moves each overlapping pair apart along the line between them,
// each by half the overlap, and reports whether it moved any.
func pushApart(pos []XY, r []float64, gap float64) bool {
	moved := false
	for i := range pos {
		for j := i + 1; j < len(pos); j++ {
			need := r[i] + r[j] + gap
			dx, dy := pos[j].X-pos[i].X, pos[j].Y-pos[i].Y
			d := math.Hypot(dx, dy)
			if d >= need*(1-overlapTolerance) {
				continue
			}
			ux, uy := math.Cos(goldenAngle*float64(j)), math.Sin(goldenAngle*float64(j))
			if d > 0 {
				ux, uy = dx/d, dy/d
			}
			push := (need + separationSlack - d) / 2
			pos[i] = XY{pos[i].X - ux*push, pos[i].Y - uy*push}
			pos[j] = XY{pos[j].X + ux*push, pos[j].Y + uy*push}
			moved = true
		}
	}
	return moved
}

// overlapping reports whether two circles are closer than gap.
func overlapping(pos []XY, r []float64, gap float64) bool {
	for i := range pos {
		for j := i + 1; j < len(pos); j++ {
			if dist(pos[i], pos[j]) < (r[i]+r[j]+gap)*(1-overlapTolerance) {
				return true
			}
		}
	}
	return false
}

// spreadCoincident returns pos, or, when two centres are closer than
// coincident, pos with each centre moved coincident along its own golden
// angle, so no two start at one place.
func spreadCoincident(pos []XY) []XY {
	out := slices.Clone(pos)
	crowded := false
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			crowded = crowded || dist(out[i], out[j]) < coincident
		}
	}
	if !crowded {
		return out
	}
	for k := range out {
		a := goldenAngle * float64(k)
		out[k] = XY{out[k].X + coincident*math.Cos(a), out[k].Y + coincident*math.Sin(a)}
	}
	return out
}

// compact pulls the circles toward their area-weighted centroid in place,
// farthest first: each moves half the way, or a quarter, an eighth or a
// sixteenth, the first that overlaps no other circle, and the rounds stop
// once one moves none.
func compact(ctx context.Context, pos []XY, r []float64, gap float64) error {
	for range compactionRounds {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("layout: compaction: %w", err)
		}
		var c XY
		var weight float64
		for i, p := range pos {
			w := r[i] * r[i]
			c.X += w * p.X
			c.Y += w * p.Y
			weight += w
		}
		c = XY{c.X / weight, c.Y / weight}
		order := make([]int, len(pos))
		for i := range order {
			order[i] = i
		}
		slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(dist(pos[b], c), dist(pos[a], c)) })
		moved := false
		for _, i := range order {
			for _, t := range []float64{0.5, 0.25, 0.125, 0.0625} {
				cand := XY{pos[i].X + t*(c.X-pos[i].X), pos[i].Y + t*(c.Y-pos[i].Y)}
				if dist(cand, pos[i]) < coincident || !fits(pos, r, gap, i, cand) {
					continue
				}
				pos[i], moved = cand, true
				break
			}
		}
		if !moved {
			return nil
		}
	}
	return nil
}

// fits reports whether circle i at at overlaps none of the others.
func fits(pos []XY, r []float64, gap float64, i int, at XY) bool {
	for j, p := range pos {
		if j != i && dist(at, p) < r[i]+r[j]+gap {
			return false
		}
	}
	return true
}
