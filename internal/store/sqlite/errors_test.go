package sqlite

import (
	"context"
	"errors"
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
	newRun := func(id string) *store.InterestRun {
		return &store.InterestRun{ID: id, TenantID: "local", Trigger: store.RunTriggerManual, Grouper: "louvain",
			RunOutcome: store.RunOutcome{Kind: store.RunKindFresh, Shape: store.InterestShapeFlat}}
	}
	run := newRun("")
	require.NoError(t, ins.CreateRun(ctx, run))

	t.Run("unique (tenant, url, source)", func(t *testing.T) {
		require.ErrorIs(t, bms.Create(ctx, newBookmark()), store.ErrConflict)
	})
	t.Run("text primary key", func(t *testing.T) {
		err := ins.CreateRun(ctx, newRun(run.ID))
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
		assert.False(t, isUniqueViolation(errors.New("UNIQUE constraint failed: looks alike")))
		assert.False(t, isForeignKeyViolation(nil))
		assert.False(t, isForeignKeyViolation(errors.New("FOREIGN KEY constraint failed: looks alike")))
	})
}

// TestInsights_MalformedTimestamps: a timestamp that doesn't parse is an
// error, as in every other scanner, not a silently zero or nil time.
func TestInsights_MalformedTimestamps(t *testing.T) {
	f := newInsightFixture(t, 7)
	run := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(run)))
	_, err := f.db.Exec(`UPDATE interest_runs SET finished_at = 'yesterday-ish' WHERE id = ?`, run.ID)
	require.NoError(t, err)
	_, err = f.ins.GetRun(f.ctx, run.ID)
	require.ErrorContains(t, err, "yesterday-ish")
	_, err = f.ins.LatestRun(f.ctx, "local", store.InterestRunDone)
	require.ErrorContains(t, err, "yesterday-ish")

	_, err = f.db.Exec(`UPDATE interests SET labeled_at = 'not a time' WHERE id = 'area-1'`)
	require.NoError(t, err)
	_, err = f.ins.TopGroups(f.ctx, run.ID, 0, 0)
	require.ErrorContains(t, err, "not a time")
	_, err = f.ins.GetInterest(f.ctx, "area-1")
	require.ErrorContains(t, err, "not a time")
}

// TestInsights_NotFound: a run, an identity, or a group the run doesn't
// hold, is ErrNotFound naming it.
func TestInsights_NotFound(t *testing.T) {
	f := newInsightFixture(t, 7)
	run := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(run)))

	_, err := f.ins.GetRun(f.ctx, "no-such-run")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.ins.GetInterest(f.ctx, "no-such-interest")
	require.ErrorIs(t, err, store.ErrNotFound)
	assert.Contains(t, err.Error(), "no-such-interest")
	_, err = f.ins.GetGroup(f.ctx, run.ID, "no-such-interest")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = f.ins.GetGroup(f.ctx, "no-such-run", "interest-1")
	require.ErrorIs(t, err, store.ErrNotFound)
	assert.Contains(t, err.Error(), "no-such-run")
}
