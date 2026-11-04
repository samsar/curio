package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/config"
)

// TestWithoutModel_EveryRoleThatDoesWithout: every role that doesn't pull
// its model alone says what goes without it while that model isn't
// pulled, and a role that pulls alone, whose model is never optional, has
// no row.
func TestWithoutModel_EveryRoleThatDoesWithout(t *testing.T) {
	for _, r := range config.Roles() {
		_, ok := withoutModel[r]
		assert.Equal(t, !r.PullsAlone(), ok, "%s", r)
	}
}
