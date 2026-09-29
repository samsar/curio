package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/samsar/curio/internal/store"
)

// A cursor is opaque to clients: base64url of the JSON form of the store
// position the next page starts after. A daemon upgrade may change it; an
// old cursor is then refused with a 400 and the client starts its walk
// again (docs/decisions.md "List pagination: keyset on (timestamp, id)").
type cursor struct {
	At time.Time `json:"t"`
	ID string    `json:"id"`
	// Order names the order of the list that issued the cursor when it
	// isn't that list's default, so a cursor pages only the order it came
	// from. A default order records none: the cursors issued before lists
	// had orders keep working.
	Order string `json:"o,omitempty"`
}

// errOtherOrder is a cursor that another order of a list, or another list,
// issued: its position means nothing in the walk it was sent to.
var errOtherOrder = errors.New("another list or order issued it")

// encodeCursor is the cursor after key in a list walked in order, "" for a
// list's default order.
func encodeCursor(key store.PageKey, order string) (string, error) {
	b, err := json.Marshal(cursor{At: key.At.UTC(), ID: key.ID, Order: order})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// decodeCursor reads a cursor of a list walked in order, "" for a list's
// default order; one issued for another order is errOtherOrder.
func decodeCursor(s, order string) (store.PageKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return store.PageKey{}, err
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return store.PageKey{}, err
	}
	if c.ID == "" || c.At.IsZero() {
		return store.PageKey{}, errors.New("it names no position")
	}
	if c.Order != order {
		return store.PageKey{}, errOtherOrder
	}
	return store.PageKey{At: c.At, ID: c.ID}, nil
}

// cursorParam reads a list endpoint's ?cursor, for a walk in order, ""
// for the list's default order: the start of the list when it is absent,
// and a requestError when it isn't a cursor this daemon issued for that
// order.
func cursorParam(r *http.Request, order string) (store.PageKey, error) {
	s := r.URL.Query().Get("cursor")
	if s == "" {
		return store.PageKey{}, nil
	}
	key, err := decodeCursor(s, order)
	if err != nil {
		return store.PageKey{}, badRequest("invalid cursor: %w; list again from the first page", err)
	}
	return key, nil
}

// onePage trims rows, which the store was asked for with a limit of
// limit+1, to limit, and returns the cursor of the page that follows in
// order ("" for the list's default): "" when these rows are the last. key
// is a row's position in the list. A page holds at least one row: a limit
// below 1 is an error, since the next page's cursor is the last row's.
func onePage[T any](rows []T, limit int, order string, key func(T) store.PageKey) ([]T, string, error) {
	if limit < 1 {
		return nil, "", fmt.Errorf("page size %d: a page holds at least one row", limit)
	}
	if len(rows) <= limit {
		return rows, "", nil
	}
	rows = rows[:limit]
	next, err := encodeCursor(key(rows[limit-1]), order)
	return rows, next, err
}
