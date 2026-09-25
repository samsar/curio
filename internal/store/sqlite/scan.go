package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// listLimitDefault is the page size of a list whose options leave it unset.
const listLimitDefault = 50

// listLimit is limit, or listLimitDefault when limit <= 0.
func listLimit(limit int) int {
	if limit <= 0 {
		return listLimitDefault
	}
	return limit
}

// timeFormat matches the migration's strftime('%Y-%m-%dT%H:%M:%fZ','now')
// output. Used to parse and emit timestamps everywhere.
const timeFormat = "2006-01-02T15:04:05.000Z"

// sqlNow is the current time in timeFormat, the expression the schema's
// DEFAULTs use. No trigger maintains updated_at, so every UPDATE sets it in
// the same statement: with this, or by binding formatTime of the time the
// statement already binds for another column.
const sqlNow = `strftime('%Y-%m-%dT%H:%M:%fZ','now')`

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

// qualify prefixes every column in a comma-separated list with a table
// alias, for queries that join tables sharing column names.
func qualify(alias, columns string) string {
	cols := strings.Split(columns, ",")
	for i, c := range cols {
		cols[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

// likeEscaper backslash-escapes LIKE's wildcards and the escape character.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLike makes s match only itself in a LIKE pattern that declares
// ESCAPE '\'.
func escapeLike(s string) string {
	return likeEscaper.Replace(s)
}
