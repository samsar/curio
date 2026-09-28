package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/samsar/curio/internal/store"
)

// QueueSettings implements store.QueueSettingsStore over queue_settings,
// a table of at most one row (id = 1). With no row, the settings are the
// defaults.
type QueueSettings struct {
	db *DB
}

var _ store.QueueSettingsStore = (*QueueSettings)(nil)

func NewQueueSettings(db *DB) *QueueSettings {
	return &QueueSettings{db: db}
}

// Queue settings statements: a primary-key lookup and an upsert of row 1.
const (
	getQueueSettingsSQL = `SELECT paused, throttle, schedule_start, schedule_end, keep_awake FROM queue_settings WHERE id = 1`
	putQueueSettingsSQL = `
	INSERT INTO queue_settings (id, paused, throttle, schedule_start, schedule_end, keep_awake)
	VALUES (1, ?, ?, ?, ?, ?)
	ON CONFLICT (id) DO UPDATE SET
		paused = excluded.paused, throttle = excluded.throttle,
		schedule_start = excluded.schedule_start, schedule_end = excluded.schedule_end,
		keep_awake = excluded.keep_awake,
		updated_at = ` + sqlNow
)

func (s *QueueSettings) Get(ctx context.Context) (store.QueueSettings, error) {
	var (
		settings   store.QueueSettings
		start, end sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, getQueueSettingsSQL).Scan(&settings.Paused, &settings.Throttle, &start, &end,
		&settings.KeepAwake)
	if errors.Is(err, sql.ErrNoRows) {
		return store.DefaultQueueSettings(), nil
	}
	if err != nil {
		return store.QueueSettings{}, fmt.Errorf("get queue settings: %w", err)
	}
	// The table's CHECKs set both ends or neither.
	if start.Valid {
		settings.Schedule = store.DailyWindow{Start: int(start.Int64), End: int(end.Int64)}
	}
	return settings, nil
}

func (s *QueueSettings) Put(ctx context.Context, settings store.QueueSettings) error {
	if !settings.Throttle.Valid() {
		return fmt.Errorf("put queue settings: throttle %q is not one of normal, gentle", settings.Throttle)
	}
	if !settings.Schedule.Valid() {
		return fmt.Errorf("put queue settings: a schedule from minute %d to minute %d is not a window of the day",
			settings.Schedule.Start, settings.Schedule.End)
	}
	var start, end any // NULL for no schedule
	if !settings.Schedule.IsZero() {
		start, end = settings.Schedule.Start, settings.Schedule.End
	}
	if _, err := s.db.ExecContext(ctx, putQueueSettingsSQL, settings.Paused, settings.Throttle, start, end,
		settings.KeepAwake); err != nil {
		return fmt.Errorf("put queue settings: %w", err)
	}
	return nil
}
