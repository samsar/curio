package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// TestConstraintErrors: only uniqueness violations (UNIQUE and TEXT PRIMARY
// KEY, which SQLite reports under different extended codes) become
// ErrConflict, and only foreign-key violations are recognized as those.
func TestConstraintErrors(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms, ins := NewBookmarks(db), NewInsights(db)
	newBookmark := func() *store.Bookmark {
		return &store.Bookmark{TenantID: "local", URL: "https://example.com/a", Source: store.SourceChrome,
			SavedAt: time.Now().UTC()}
	}
	require.NoError(t, bms.Create(ctx, newBookmark()))
	run := &store.ClusterRun{TenantID: "local", Algo: "knn-graph"}
	require.NoError(t, ins.CreateRun(ctx, run))

	t.Run("unique (tenant, url, source)", func(t *testing.T) {
		require.ErrorIs(t, bms.Create(ctx, newBookmark()), store.ErrConflict)
	})
	t.Run("text primary key", func(t *testing.T) {
		err := ins.CreateRun(ctx, &store.ClusterRun{ID: run.ID, TenantID: "local", Algo: "knn-graph"})
		require.ErrorIs(t, err, store.ErrConflict)
		assert.Contains(t, err.Error(), run.ID, "names the run")
	})
	t.Run("check constraint", func(t *testing.T) {
		b := newBookmark()
		b.URL, b.Source = "https://example.com/b", "bogus"
		err := bms.Create(ctx, b)
		require.Error(t, err)
		assert.NotErrorIs(t, err, store.ErrConflict)
		assert.False(t, isUniqueViolation(err))
		assert.False(t, isForeignKeyViolation(err))
	})
	t.Run("foreign key", func(t *testing.T) {
		b := newBookmark()
		missing := uuid.NewString()
		b.URL, b.DocumentID = "https://example.com/c", &missing
		err := bms.Create(ctx, b)
		require.Error(t, err)
		assert.NotErrorIs(t, err, store.ErrConflict)
		assert.False(t, isUniqueViolation(err))
		assert.True(t, isForeignKeyViolation(err))
	})
	t.Run("not a sqlite error", func(t *testing.T) {
		assert.False(t, isUniqueViolation(nil))
		assert.False(t, isUniqueViolation(fmt.Errorf("UNIQUE constraint failed: looks alike")))
		assert.False(t, isForeignKeyViolation(nil))
		assert.False(t, isForeignKeyViolation(fmt.Errorf("FOREIGN KEY constraint failed: looks alike")))
	})
}

// TestInsights_MalformedTimestamps: a timestamp that doesn't parse is an
// error, as in every other scanner, not a silently zero or nil time.
func TestInsights_MalformedTimestamps(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ins := NewInsights(db)
	run := &store.ClusterRun{TenantID: "local", Algo: "knn-graph"}
	require.NoError(t, ins.CreateRun(ctx, run))
	_, err := db.Exec(`UPDATE cluster_runs SET status = 'done', finished_at = 'yesterday-ish' WHERE id = ?`, run.ID)
	require.NoError(t, err)

	_, err = ins.GetRun(ctx, run.ID)
	require.ErrorContains(t, err, "yesterday-ish")
	_, err = ins.LatestRun(ctx, "local", store.ClusterRunDone)
	require.ErrorContains(t, err, "yesterday-ish")

	require.NoError(t, ins.ReplaceClusters(ctx, run.ID, []store.ClusterWithMembers{{Cluster: store.Cluster{TenantID: "local"}}}))
	_, err = db.Exec(`UPDATE clusters SET created_at = 'not a time' WHERE run_id = ?`, run.ID)
	require.NoError(t, err)
	_, err = ins.ListClusters(ctx, run.ID, 0)
	require.ErrorContains(t, err, "not a time")
}

func TestInsights_FinishRun_UnknownRun(t *testing.T) {
	err := NewInsights(newTestDB(t)).FinishRun(context.Background(), "no-such-run",
		store.RunResult{Status: store.ClusterRunDone})
	require.ErrorIs(t, err, store.ErrNotFound)
	assert.Contains(t, err.Error(), "cluster run")
}
