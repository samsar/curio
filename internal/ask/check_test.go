package ask

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/modelgate"
	"github.com/samsar/curio/internal/ollama"
)

// TestCheck: a fit model passes; one that obeys a source, cites the wrong
// source or never abstains fails the probe that says so; a provider's 500
// leaves the model unchecked, and a 400 makes it unfit.
func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(question string) string
		failed string
	}{
		{"fit", nil, ""},
		{"obeys a source", func(q string) string {
			if q == probes[2].question {
				return "AARDVARK [1]"
			}
			return fitProbeAnswer(q)
		}, "a source instructs"},
		{"obeys a source, citing the right one too", func(q string) string {
			if q == probes[2].question {
				return "Aardvark. Spring tides come at the new moon [3]."
			}
			return fitProbeAnswer(q)
		}, "a source instructs"},
		{"cites the wrong source", func(q string) string {
			if q == probes[0].question {
				return "Let it cool for an hour [1]."
			}
			return fitProbeAnswer(q)
		}, "one source answers"},
		{"never abstains", func(q string) string {
			if q == probes[1].question {
				return "Mercury boils at 357 degrees [2]."
			}
			return fitProbeAnswer(q)
		}, "none answers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.writer.probe = tc.answer
			failed, err := r.a.Check(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.failed, failed)
			want := modelgate.CheckFit
			if tc.failed != "" {
				want = modelgate.CheckUnfit
			}
			assert.Equal(t, want, r.a.State().Check)
		})
	}
	for _, tc := range []struct {
		name  string
		code  int
		check string
	}{
		{"a 500", http.StatusInternalServerError, modelgate.CheckUnchecked},
		{"a 400", http.StatusBadRequest, modelgate.CheckUnfit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.a.gen = failingStream{err: &ollama.StatusError{Code: tc.code, Body: "no"}, kind: statusKind(tc.code)}
			_, _ = r.a.Check(context.Background())
			assert.Equal(t, tc.check, r.a.State().Check)
		})
	}
}

// statusKind is the sentinel a status matches, as the generator's.
func statusKind(code int) error {
	if code >= http.StatusInternalServerError {
		return generator.ErrUnavailable
	}
	return generator.ErrBadRequest
}

// failingStream fails every stream with err, matching kind.
type failingStream struct {
	err  error
	kind error
}

func (failingStream) Generate(context.Context, string, generator.Options) (string, error) {
	return "", nil
}
func (failingStream) Model() string              { return "m" }
func (failingStream) Ping(context.Context) error { return nil }
func (f failingStream) GenerateStream(context.Context, string, generator.Options, func(string) error) (
	generator.StreamEnd, error) {
	return generator.StreamEnd{}, fmt.Errorf("%w: %w", f.kind, f.err)
}

// TestCheck_PromptTooLong: a probe whose prompt the provider refuses as
// past the context leaves the model unchecked, never unfit: the prompt
// is the fault, not the model.
func TestCheck_PromptTooLong(t *testing.T) {
	r := newRig(t, nil)
	r.a.gen = failingStream{err: errors.New("request (9000 tokens) exceeds the available context size (8192 tokens)"),
		kind: generator.ErrPromptTooLong}
	failed, err := r.a.Check(context.Background())
	require.Error(t, err, "unreached")
	assert.Empty(t, failed)
	assert.Equal(t, modelgate.CheckUnchecked, r.a.State().Check)
}

// TestCheck_Truncated: a probe cut at its cap is unfit.
func TestCheck_Truncated(t *testing.T) {
	r := newRig(t, nil)
	r.a.gen = truncatingStream{}
	failed, err := r.a.Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "one source answers", failed)
}

type truncatingStream struct{ failingStream }

func (truncatingStream) GenerateStream(_ context.Context, _ string, _ generator.Options, emit func(string) error) (
	generator.StreamEnd, error) {
	return generator.StreamEnd{Truncated: true}, emit("Let it cool [2] and")
}
