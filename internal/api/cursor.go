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
}

func encodeCursor(key store.PageKey) (string, error) {
	b, err := json.Marshal(cursor{At: key.At.UTC(), ID: key.ID})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor(s string) (store.PageKey, error) {
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
	return store.PageKey{At: c.At, ID: c.ID}, nil
}

// cursorParam reads a list endpoint's ?cursor: the start of the list when
// it is absent, and a requestError when it isn't a cursor this daemon
// issued.
func cursorParam(r *http.Request) (store.PageKey, error) {
	s := r.URL.Query().Get("cursor")
	if s == "" {
		return store.PageKey{}, nil
	}
	key, err := decodeCursor(s)
	if err != nil {
		return store.PageKey{}, badRequest("invalid cursor: %w; list again from the first page", err)
	}
	return key, nil
}

// onePage trims rows, which the store was asked for with a limit of
// limit+1, to limit, and returns the cursor of the page that follows: ""
// when these rows are the last. key is a row's position in the list.
func onePage[T any](rows []T, limit int, key func(T) store.PageKey) ([]T, string, error) {
	if len(rows) <= limit {
		return rows, "", nil
	}
	rows = rows[:limit]
	next, err := encodeCursor(key(rows[limit-1]))
	return rows, next, err
}
