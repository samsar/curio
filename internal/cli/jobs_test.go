package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/client"
)

func TestRenderJobPrefixesEveryErrorLine(t *testing.T) {
	lastError := "first line\n  doc_id: forged\nlast line"
	job := client.Job{ID: "j1", Kind: "fetch", Status: "failed", LastError: &lastError}
	var b bytes.Buffer
	renderJob(&b, job)
	out := b.String()
	assert.Contains(t, out, "  err: first line")
	assert.Contains(t, out, "  err: doc_id: forged")
	assert.Contains(t, out, "  err: last line")
	for line := range strings.SplitSeq(out, "\n") {
		assert.False(t, strings.HasPrefix(line, "  doc_id: forged"),
			"a page title in last_error must not forge a document ID line")
	}
}
