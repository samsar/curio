package sqlite

import (
	"database/sql"
	"fmt"
	"time"
)

// timeFormat matches the migration's strftime('%Y-%m-%dT%H:%M:%fZ','now')
// output. Used to parse and emit timestamps everywhere.
const timeFormat = "2006-01-02T15:04:05.000Z"

// formatTime turns a Go time.Time into the canonical TEXT format.
func formatTime(t time.Time) string {
	return t.UTC().Format(timeFormat)
}

// parseTime parses the canonical TEXT format. Accepts a few RFC 3339
// variants for resilience (different precisions).
func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{
		timeFormat,
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q", s)
}

// nullableString turns sql.NullString into *string.
func nullableString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

// nullableInt turns sql.NullInt64 into *int.
func nullableInt(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// strPtr returns the value pointed to or "" if nil. For binding *string
// columns into a non-nullable context.
func strPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// intPtr is the int variant of strPtr.
func intPtr(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}

// timePtr is the *time.Time variant.
func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}
