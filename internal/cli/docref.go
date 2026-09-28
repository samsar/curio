package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/samsar/curio/internal/client"
)

// resolveDocumentID turns a command's document argument into a document
// ID. An http(s) URL is looked up, normalized the way curio stored it, so
// `curio refetch https://…` works with the URL a bookmark was saved
// under; anything else is taken as an ID as it is.
func resolveDocumentID(ctx context.Context, c *client.Client, arg string) (string, error) {
	if !isWebURL(arg) {
		return arg, nil
	}
	doc, err := c.LookupDocument(ctx, arg)
	var apiErr *client.APIError
	switch {
	case err == nil:
		return doc.ID, nil
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		// The lookup's own 404 names the URL it looked for. A daemon from
		// before the lookup routes the request to GET /v1/documents/{id},
		// whose 404 is about a document ID "lookup".
		if strings.HasPrefix(apiErr.Problem.Detail, lookupMissPrefix) {
			return "", fmt.Errorf("no document for %s in the library (curio add %s saves it)", arg, arg)
		}
		return "", fmt.Errorf("the running daemon is older than this curio and can't look documents up by URL: " +
			"upgrade it (brew upgrade curio, then curio up), or pass the document ID")
	default:
		return "", err
	}
}

// lookupMissPrefix starts the detail of GET /v1/documents/lookup's 404
// (api.handleLookupDocument).
const lookupMissPrefix = "document for url "

// isWebURL reports whether s is an absolute http or https URL.
func isWebURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
