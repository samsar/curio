package cli

import (
	"context"
	"fmt"
	"net/url"

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
	if client.IsNotFound(err) {
		return "", fmt.Errorf("no document for %s in the library (curio add %s saves it)", arg, arg)
	}
	if err != nil {
		return "", err
	}
	return doc.ID, nil
}

// isWebURL reports whether s is an absolute http or https URL.
func isWebURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
