package layout

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
)

// The dots' lattice: hexagonal, of spacing 2·(1 + latticeGap) dot radii, so
// two dots on it never touch; a circle holds (1 + spareSlots) times as many
// slots as documents, so it fills evenly and a document placed later finds
// room.
const (
	latticeGap = 0.1
	spareSlots = 0.15
)

// latticeSpacing is the distance between neighbouring slots, in dot radii.
const latticeSpacing = 2 * (1 + latticeGap)

// lattice is a hexagonal lattice's points around the origin, nearest first:
// point (i, j) is at spacing·(i + j/2, j·√3/2), at squared distance
// spacing²·(i² + ij + j²), so the order is exact, ties broken by angle.
type lattice struct {
	points []XY
	norms  []int // i² + ij + j² of each point
}

// newLattice returns at least n of the lattice's points nearest the origin,
// and every point as near as the n-th.
func newLattice(n int) *lattice {
	n = max(n, 1)
	// A disc of radius ρ spacings holds about 3.6·ρ² points.
	for rho := math.Sqrt(float64(n))/1.8 + 2; ; rho *= 1.5 {
		l := latticeWithin(rho)
		if len(l.points) >= n {
			return l
		}
	}
}

// latticeWithin is the lattice's points within rho spacings of the origin.
func latticeWithin(rho float64) *lattice {
	type pt struct {
		norm  int
		angle float64
		at    XY
	}
	limit := int(math.Floor(rho * rho))
	span := int(math.Ceil(2*rho/math.Sqrt(3))) + 1
	var pts []pt
	for j := -span; j <= span; j++ {
		for i := -span - span; i <= span+span; i++ {
			norm := i*i + i*j + j*j
			if norm > limit {
				continue
			}
			at := XY{latticeSpacing * (float64(i) + float64(j)/2), latticeSpacing * float64(j) * math.Sqrt(3) / 2}
			angle := math.Atan2(at.Y, at.X)
			if angle < 0 {
				angle += 2 * math.Pi
			}
			pts = append(pts, pt{norm, angle, at})
		}
	}
	slices.SortFunc(pts, func(a, b pt) int { return cmp.Or(cmp.Compare(a.norm, b.norm), cmp.Compare(a.angle, b.angle)) })
	l := &lattice{points: make([]XY, len(pts)), norms: make([]int, len(pts))}
	for k, p := range pts {
		l.points[k], l.norms[k] = p.at, p.norm
	}
	return l
}

// slotsFor is how many slots a circle of m documents holds.
func slotsFor(m int) int { return max(1, int(math.Ceil((1+spareSlots)*float64(m)))) }

// circleFor is the radius of the smallest circle whose lattice holds
// slotsFor(m) slots, each a whole dot inside it, and how many slots it
// holds: every point as near as the slotsFor(m)-th.
func (l *lattice) circleFor(m int) (radius float64, slots int) {
	need := slotsFor(m)
	norm := l.norms[need-1]
	slots = need
	for slots < len(l.norms) && l.norms[slots] == norm {
		slots++
	}
	return latticeSpacing*math.Sqrt(float64(norm)) + 1, slots
}

// assignSlots gives each target a slot among the first slots points of the
// lattice around c: in order, each takes the free slot nearest its target,
// ties to the lower slot. Two documents never share a slot, so no two dots
// overlap, at O(len(targets)·slots).
func (l *lattice) assignSlots(ctx context.Context, c XY, slots int, targets []XY) ([]XY, error) {
	taken := make([]bool, slots)
	out := make([]XY, len(targets))
	for t, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("layout: placing dots: %w", err)
		}
		best, bestD := -1, math.Inf(1)
		for s := range slots {
			if taken[s] {
				continue
			}
			p := XY{c.X + l.points[s].X, c.Y + l.points[s].Y}
			if d := (p.X-target.X)*(p.X-target.X) + (p.Y-target.Y)*(p.Y-target.Y); d < bestD {
				best, bestD = s, d
			}
		}
		taken[best] = true
		out[t] = XY{c.X + l.points[best].X, c.Y + l.points[best].Y}
	}
	return out, nil
}
