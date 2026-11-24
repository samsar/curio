package jsonstrict

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type inner struct {
	X string `json:"x"`
}

type shape struct {
	Kind  string   `json:"kind"`
	List  []string `json:"list"`
	Inner inner    `json:"inner"`
	Items []inner  `json:"items"`
}

func TestDecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"exact names", `{"kind":"a","list":["b"],"inner":{"x":"c"},"items":[{"x":"d"}]}`, true},
		{"a member left out", `{"kind":"a"}`, true},
		{"surrounding whitespace", " \n{\"kind\":\"a\"}\n ", true},
		{"case variant beside the name", `{"kind":"a","KIND":"b"}`, false},
		{"case variant alone", `{"Kind":"a"}`, false},
		{"repeated name", `{"kind":"a","kind":"b"}`, false},
		{"unknown name", `{"kind":"a","extra":1}`, false},
		{"case variant nested", `{"inner":{"X":"c"}}`, false},
		{"repeated nested", `{"inner":{"x":"c","x":"d"}}`, false},
		{"unknown nested in an array", `{"items":[{"x":"d","y":1}]}`, false},
		{"two values", `{"kind":"a"}{"kind":"b"}`, false},
		{"trailing text", `{"kind":"a"} and more`, false},
		{"wrong type", `{"kind":1}`, false},
		{"not JSON", `kind: a`, false},
		{"empty", ``, false},
		{"invalid UTF-8", "{\"kind\":\"\xff\"}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v shape
			err := Decode([]byte(tc.raw), &v)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrNotStrict)
		})
	}
}

// TestDecode_NeverQuotesTheInput: the error carries nothing of the input,
// a key the model wrote included.
func TestDecode_NeverQuotesTheInput(t *testing.T) {
	var v shape
	err := Decode([]byte(`{"kind":"a","zebrafinch":1}`), &v)
	require.Error(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "zebrafinch")
}

func TestDecode_Fills(t *testing.T) {
	var v shape
	require.NoError(t, Decode([]byte(`{"kind":"a","list":["b","c"],"inner":{"x":"d"}}`), &v))
	assert.Equal(t, shape{Kind: "a", List: []string{"b", "c"}, Inner: inner{X: "d"}}, v)
}
