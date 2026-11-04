package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/store"
)

// TestCauses: every failure cause has a label and a sentence, and no two
// causes share a label; its card's icon is one icons.html defines, and its
// tone one of the three a cause icon is drawn in.
func TestCauses(t *testing.T) {
	labels := map[string]store.FailureCause{}
	icons := iconNames(t)
	for _, cause := range store.FailureCauses() {
		label := causeLabel(string(cause))
		assert.NotEmpty(t, label, cause)
		assert.NotEqual(t, string(cause), label, "%s has words of its own", cause)
		assert.NotEmpty(t, causeWhy(string(cause)), cause)
		if other, dup := labels[label]; dup {
			t.Errorf("%s and %s are both labeled %q", other, cause, label)
		}
		labels[label] = cause
		assert.Contains(t, icons, causeIcon(string(cause)), cause)
		assert.Contains(t, []string{"danger", "warn", "neutral"}, causeTone(string(cause)), cause)
	}
	assert.Len(t, causes, len(store.FailureCauses()), "words only for causes there are")

	assert.Equal(t, "Blocked by bot protection", causeLabel("anti_bot"))
	assert.Equal(t, "Dead link", causeLabel("dead_link"))
	assert.Empty(t, causeLabel(""))
	assert.Empty(t, causeWhy(""))
	assert.Equal(t, "new_cause", causeLabel("new_cause"), "a cause this build doesn't know is named by its code")
	assert.Empty(t, causeWhy("new_cause"))
}

// TestCauses_IconAndTone pins each cause's icon and tone: warn where a
// refetch later usually works or the owner can act (a block, a host down,
// an index that gave up), neutral for a verdict a refetch won't change;
// never danger, which is kept for outages. A cause this build doesn't
// know, or none, is a neutral alert.
func TestCauses_IconAndTone(t *testing.T) {
	for cause, want := range map[store.FailureCause][2]string{
		store.FailureCauseDeadLink:    {"unlink", "neutral"},
		store.FailureCauseAntiBot:     {"shield-alert", "warn"},
		store.FailureCauseLoginWall:   {"lock", "neutral"},
		store.FailureCauseJinaRefused: {"ban", "neutral"},
		store.FailureCauseTLS:         {"shield-x", "neutral"},
		store.FailureCauseUnreachable: {"wifi-off", "warn"},
		store.FailureCauseTimeout:     {"hourglass", "warn"},
		store.FailureCauseNetwork:     {"zap-off", "warn"},
		store.FailureCauseRateLimited: {"gauge", "warn"},
		store.FailureCauseHTTPError:   {"server", "neutral"},
		store.FailureCauseUnsupported: {"file-x", "neutral"},
		store.FailureCauseTooLarge:    {"weight", "neutral"},
		store.FailureCauseIndex:       {"database", "warn"},
		store.FailureCauseOther:       {"alert-circle", "neutral"},
		"":                            {"alert-circle", "neutral"},
		"new_cause":                   {"alert-circle", "neutral"},
		evilScript:                    {"alert-circle", "neutral"},
	} {
		assert.Equal(t, want, [2]string{causeIcon(string(cause)), causeTone(string(cause))}, cause)
	}
}
