package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/store"
)

// TestCauses: every failure cause has a label and a sentence, and no two
// causes share a label.
func TestCauses(t *testing.T) {
	labels := map[string]store.FailureCause{}
	for _, cause := range store.FailureCauses() {
		label := causeLabel(string(cause))
		assert.NotEmpty(t, label, cause)
		assert.NotEqual(t, string(cause), label, "%s has words of its own", cause)
		assert.NotEmpty(t, causeWhy(string(cause)), cause)
		if other, dup := labels[label]; dup {
			t.Errorf("%s and %s are both labeled %q", other, cause, label)
		}
		labels[label] = cause
	}
	assert.Len(t, causes, len(store.FailureCauses()), "words only for causes there are")

	assert.Equal(t, "Blocked by bot protection", causeLabel("anti_bot"))
	assert.Equal(t, "Dead link", causeLabel("dead_link"))
	assert.Empty(t, causeLabel(""))
	assert.Empty(t, causeWhy(""))
	assert.Equal(t, "new_cause", causeLabel("new_cause"), "a cause this build doesn't know is named by its code")
	assert.Empty(t, causeWhy("new_cause"))
}
