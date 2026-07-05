package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func TestBookmarks_TagsForDocument(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	docs := NewDocuments(db)
	bms := NewBookmarks(db)

	d1 := &store.Document{TenantID: "local", URL: "https://x/a", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Upsert(ctx, d1))
	d2 := &store.Document{TenantID: "local", URL: "https://x/b", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Upsert(ctx, d2))

	// Two bookmarks (distinct sources) for d1 with overlapping tags.
	require.NoError(t, bms.Create(ctx, &store.Bookmark{
		TenantID: "local", DocumentID: &d1.ID, URL: d1.URL, Source: store.SourceChrome,
		SavedAt: time.Now().UTC(), Tags: []string{"go", "db"},
	}))
	require.NoError(t, bms.Create(ctx, &store.Bookmark{
		TenantID: "local", DocumentID: &d1.ID, URL: d1.URL, Source: store.SourceManual,
		SavedAt: time.Now().UTC(), Tags: []string{"db", "sql"},
	}))
	// A different doc's tags must not leak in.
	require.NoError(t, bms.Create(ctx, &store.Bookmark{
		TenantID: "local", DocumentID: &d2.ID, URL: d2.URL, Source: store.SourceChrome,
		SavedAt: time.Now().UTC(), Tags: []string{"other"},
	}))

	tags, err := bms.TagsForDocument(ctx, "local", d1.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"go", "db", "sql"}, tags, "union, deduplicated, scoped to the doc")

	// Different tenant sees nothing.
	none, err := bms.TagsForDocument(ctx, "other-tenant", d1.ID)
	require.NoError(t, err)
	assert.Empty(t, none)
}
