package generator

import (
	"fmt"
	"time"

	"github.com/samsar/curio/internal/config"
)

// New builds the Generator spec describes, the one place a provider is
// picked: a provider added later is a case here and its implementation,
// with nothing to change where a model is used. config.Validate refuses a
// provider New doesn't know before any caller gets here.
//
// What a provider can do beyond prose is the interfaces its Generator
// implements: a caller that needs JSON under a schema type-asserts
// JSONGenerator, one that warms the model Warmer. A provider without
// structured output must not implement JSONGenerator, so that such a
// caller refuses it rather than parse prose.
//
// generator reads config, never the reverse: config is read by every
// binary, and must not link the clients into them.
func New(spec config.ModelSpec) (Generator, error) {
	switch spec.Provider {
	case config.ProviderOllama:
		g, err := NewOllama(OllamaOptions{
			BaseURL: spec.BaseURL,
			Model:   spec.Model,
			Timeout: time.Duration(spec.TimeoutSeconds) * time.Second,
		})
		if err != nil {
			return nil, err
		}
		return g, nil
	default:
		return nil, fmt.Errorf("generation provider %q is not supported", spec.Provider)
	}
}
