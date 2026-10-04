package layout

import (
	"math"
	"testing"
)

// TestPower: the descent's d^(2b) is math.Pow's within 1e-12 relative,
// over the distances a layout meets and past them.
func TestPower(t *testing.T) {
	worst := 0.0
	for e := -60.0; e <= 60; e += 0.0137 {
		d2 := math.Pow(2, e)
		want := math.Pow(d2, curveB)
		worst = math.Max(worst, math.Abs(power(d2)-want)/want)
	}
	if worst > 1e-12 {
		t.Fatalf("relative error %g", worst)
	}
}
