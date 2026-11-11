package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/client"
)

func TestRenderDocShowEscapesControls(t *testing.T) {
	title := "A title\x1b[2J"
	author := "An author\x1b]8;;x\x07"
	doc := &client.Document{
		ID: "d1", URL: "https://example.com/\x1b[2J", Title: &title, Author: &author,
		ContentType: "article", State: "fetched",
	}
	var b bytes.Buffer
	renderDocShow(&b, doc)
	assert.NotContains(t, b.String(), "\x1b")
	assert.NotContains(t, b.String(), "\x07")
	assert.Contains(t, b.String(), `\x1b`)
}
