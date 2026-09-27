package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func TestQueueSettings_DefaultsWhenNoneStored(t *testing.T) {
	got, err := NewQueueSettings(newTestDB(t)).Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, store.DefaultQueueSettings(), got)
}

func TestQueueSettings_PutThenGet(t *testing.T) {
	ctx := context.Background()
	qs := NewQueueSettings(newTestDB(t))
	for _, want := range []store.QueueSettings{
		{Paused: true, Throttle: store.ThrottleGentle, Schedule: store.DailyWindow{Start: 22 * 60, End: 7 * 60}},
		{Paused: false, Throttle: store.ThrottleNormal, Schedule: store.DailyWindow{Start: 0, End: 30}},
		{Paused: true, Throttle: store.ThrottleGentle}, // the schedule cleared
	} {
		require.NoError(t, qs.Put(ctx, want))
		got, err := qs.Get(ctx)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

// TestQueueSettings_SurviveReopening: what a daemon stored is what the next
// one reads.
func TestQueueSettings_SurviveReopening(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "curio.db")
	open := func() *DB {
		db, err := Open(ctx, path)
		require.NoError(t, err)
		_, err = Migrate(ctx, db)
		require.NoError(t, err)
		return db
	}
	want := store.QueueSettings{Paused: true, Throttle: store.ThrottleGentle, Schedule: store.DailyWindow{Start: 60, End: 120}}

	db := open()
	require.NoError(t, NewQueueSettings(db).Put(ctx, want))
	require.NoError(t, db.Close())

	db = open()
	defer db.Close()
	got, err := NewQueueSettings(db).Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestQueueSettings_PutRefusesInvalidSettings(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	qs := NewQueueSettings(db)
	stored := store.QueueSettings{Paused: true, Throttle: store.ThrottleGentle}
	require.NoError(t, qs.Put(ctx, stored))

	for name, s := range map[string]store.QueueSettings{
		"unknown throttle": {Throttle: "fast"},
		"no throttle":      {},
		"equal ends":       {Throttle: store.ThrottleNormal, Schedule: store.DailyWindow{Start: 60, End: 60}},
		"end past the day": {Throttle: store.ThrottleNormal, Schedule: store.DailyWindow{Start: 60, End: 1440}},
	} {
		assert.Error(t, qs.Put(ctx, s), name)
	}
	got, err := qs.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, stored, got, "nothing was written")
}
