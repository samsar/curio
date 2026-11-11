package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/client"
)

func TestLabelEscapesTerminalControls(t *testing.T) {
	got := label(client.Interest{Label: "a\u009bb\u202ec"})
	assert.False(t, strings.ContainsRune(got, '\u009b'))
	assert.False(t, strings.ContainsRune(got, '\u202e'))
	assert.Contains(t, got, `\u009b`)
	assert.Contains(t, got, `\u202e`)
}
