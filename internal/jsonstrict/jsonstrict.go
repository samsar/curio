// Package jsonstrict decodes a writing model's structured answer as its
// schema spells it, and nothing looser.
//
// encoding/json matches a key case-insensitively ("KIND" fills kind),
// takes a repeated key's last value and, with DisallowUnknownFields, still
// accepts both. A model's answer is held to its schema exactly instead, so
// a provider or a model that strays from the schema is refused rather than
// read one way here and another way by whoever reads its record.
package jsonstrict

import (
	"encoding/json/v2"
	"errors"
)

// ErrNotStrict is the error Decode returns for anything but one JSON value
// of v's shape: its names spelled exactly as v's tags spell them, none
// repeated in an object and none unknown, at any depth. It wraps no detail
// of the input: a decoder's message quotes the key it refused, and a
// model's key can hold its user's words.
var ErrNotStrict = errors.New("not one JSON value of the expected shape")

// Decode reads raw, one JSON value, into v under the strict rules above.
func Decode(raw []byte, v any) error {
	if err := json.Unmarshal(raw, v, json.RejectUnknownMembers(true)); err != nil {
		return ErrNotStrict
	}
	return nil
}
