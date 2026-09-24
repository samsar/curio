package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/importer"
)

// unfetchableURLs are URLs curio can't fetch: not http(s), or no host.
var unfetchableURLs = []string{
	"javascript:alert(1)",
	"file:///etc/passwd",
	"mailto:someone@example.com",
	"ftp://example.com/file",
	"https:example.com/x",
	"https:///x",
}

// TestCreateBookmark_RejectsUnfetchableURLs: a URL curio could never fetch
// is a 400, not a document plus a fetch job that fails five times.
func TestCreateBookmark_RejectsUnfetchableURLs(t *testing.T) {
	s := newTestServer(t)
	for _, raw := range unfetchableURLs {
		t.Run(raw, func(t *testing.T) {
			body, err := json.Marshal(CreateBookmarkRequest{URL: raw})
			require.NoError(t, err)
			resp := s.do(t, request{method: http.MethodPost, path: "/v1/bookmarks", contentType: "application/json", body: string(body)})
			assertProblem(t, resp, http.StatusBadRequest)
		})
	}
	assert.Zero(t, s.count(t, "documents"))
	assert.Zero(t, s.count(t, "jobs"))
}

// TestImportBookmarks_FiltersUnfetchableURLs: the import endpoint keeps
// counting unfetchable URLs under their filter reasons.
func TestImportBookmarks_FiltersUnfetchableURLs(t *testing.T) {
	s := newTestServer(t)
	req := ImportRequest{Source: "manual"}
	for _, raw := range unfetchableURLs {
		req.Bookmarks = append(req.Bookmarks, ImportBookmark{URL: raw})
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/bookmarks/import", contentType: "application/json", body: string(body)})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got ImportResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, len(unfetchableURLs), got.Filtered)
	assert.Equal(t, map[importer.FilterReason]int{
		importer.ReasonJavaScript:       1,
		importer.ReasonLocalFile:        1,
		importer.ReasonUnsupportedSchem: 3, // mailto:, ftp://, https:example.com
		importer.ReasonInvalidURL:       1, // https:///x
	}, got.FilteredBy)
	assert.Zero(t, s.count(t, "documents"))
}
