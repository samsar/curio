// Package fetcher abstracts content extraction from URLs.
//
// The Fetcher interface returns extracted markdown for a given URL. The
// implementations are Native (Go HTTP + Readability, with Jina Reader as
// fallback), Web2MD (the Node tool as a subprocess), GitHub (REST API) and
// YouTube (yt-dlp). A Dispatcher picks the fetcher for a URL: the daemon
// uses RulesDispatcher, driven by fetcher_rules.yaml.
package fetcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/time/rate"

	"github.com/samsar/curio/internal/store"
)

// Result is the per-fetch output the indexer needs.
type Result struct {
	// Markdown is the extracted content body, ready for chunking.
	Markdown string
	// FinalURL is the URL after following redirects. May equal the input.
	FinalURL string
	// ContentType is one of the store.ContentType* constants. Anything else
	// fails the documents CHECK constraint after a successful fetch.
	ContentType store.ContentType
	// Title is the extracted document title; empty if the fetcher could
	// not determine one.
	Title string
	// Author / Language / PublishedAt are best-effort. Empty/nil if absent.
	Author      string
	Language    string
	PublishedAt *time.Time
	// Meta is fetcher-specific metadata; persisted as extraction_meta.
	// JSON-serializable.
	Meta map[string]any
	// Partial marks a fetch that succeeded but is missing its primary
	// content (a video without a transcript). The extraction is stored
	// with status "partial" instead of "ok".
	Partial bool
}

// Fetcher pulls content from a URL.
type Fetcher interface {
	// Name uniquely identifies this fetcher; written to
	// document_extractions.fetcher. Used by the dispatcher and the
	// metrics layer.
	Name() string

	// Fetch extracts the resource at rawURL. Honors ctx for cancellation
	// and deadlines.
	Fetch(ctx context.Context, rawURL string) (*Result, error)
}

// Dispatcher chooses which Fetcher to use for a given URL. RulesDispatcher
// is the implementation the daemon uses.
type Dispatcher interface {
	For(rawURL string) (Fetcher, error)
}

// ErrFetcherNotFound is returned by Dispatcher.For when no rule matches.
var ErrFetcherNotFound = errors.New("fetcher: no fetcher matches url")

// Single is a Dispatcher that always returns the same fetcher, for tests
// that exercise the job handlers with one fake fetcher.
type Single struct{ F Fetcher }

func (s *Single) For(_ string) (Fetcher, error) {
	if s.F == nil {
		return nil, ErrFetcherNotFound
	}
	return s.F, nil
}

// Rule maps a set of hostnames to a fetcher: the built-in routing
// RulesDispatcher uses while fetcher_rules.yaml is absent.
type Rule struct {
	Hosts   []string
	Fetcher Fetcher
}

// RateLimited wraps a Fetcher with a token-bucket rate limiter.
// Each call to Fetch blocks until a token is available.
type RateLimited struct {
	Inner   Fetcher
	limiter *rate.Limiter
}

// NewRateLimited returns a Fetcher that starts at most rps fetches per
// second on average from a token bucket holding burst tokens: after a quiet
// spell, up to burst fetches may start back to back. It limits how often
// fetches start, not how many run at once.
func NewRateLimited(f Fetcher, rps float64, burst int) *RateLimited {
	return &RateLimited{
		Inner:   f,
		limiter: rate.NewLimiter(rate.Limit(rps), burst),
	}
}

func (r *RateLimited) Name() string { return r.Inner.Name() }

func (r *RateLimited) Fetch(ctx context.Context, rawURL string) (*Result, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("%s: rate limiter: %w", r.Inner.Name(), err)
	}
	return r.Inner.Fetch(ctx, rawURL)
}
