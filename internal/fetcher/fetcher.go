// Package fetcher abstracts content extraction from URLs.
//
// The Fetcher interface returns extracted markdown for a given URL; concrete
// implementations might shell out to web2md, hit a self-hosted Jina Reader,
// call the GitHub API, or use yt-dlp. The Dispatcher selects which Fetcher
// to use based on URL — for M0 it always returns the only registered
// fetcher; M2 introduces a rules engine.
package fetcher

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Result is the per-fetch output the indexer needs.
type Result struct {
	// Markdown is the extracted content body, ready for chunking.
	Markdown string
	// FinalURL is the URL after following redirects. May equal the input.
	FinalURL string
	// ContentType is one of the store.ContentType* values, or "unknown".
	ContentType string
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
}

// Fetcher pulls content from a URL.
type Fetcher interface {
	// Name uniquely identifies this fetcher; written to
	// document_extractions.fetcher. Used by the dispatcher and the
	// metrics layer.
	Name() string

	// Fetch extracts the resource at url. Honors ctx for cancellation
	// and deadlines.
	Fetch(ctx context.Context, url string) (*Result, error)
}

// Dispatcher chooses which Fetcher to use for a given URL. M0 has only one
// fetcher and trivially returns it; M2 will replace this with a
// rules-engine-backed impl that picks by host/content_type.
type Dispatcher interface {
	For(url string) (Fetcher, error)
}

// ErrFetcherNotFound is returned by Dispatcher.For when no rule matches.
var ErrFetcherNotFound = errors.New("fetcher: no fetcher matches url")

// Single is a Dispatcher that always returns the same fetcher. Use for M0.
type Single struct{ F Fetcher }

func (s *Single) For(_ string) (Fetcher, error) {
	if s.F == nil {
		return nil, ErrFetcherNotFound
	}
	return s.F, nil
}

// Rule maps a set of hostnames to a fetcher.
type Rule struct {
	Hosts   []string
	Fetcher Fetcher
}

// PatternDispatcher selects a fetcher by matching the URL's hostname
// against registered rules. First match wins; unmatched URLs go to
// Fallback.
type PatternDispatcher struct {
	Rules    []Rule
	Fallback Fetcher
}

func (d *PatternDispatcher) For(rawURL string) (Fetcher, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		if d.Fallback != nil {
			return d.Fallback, nil
		}
		return nil, ErrFetcherNotFound
	}
	host := strings.ToLower(u.Hostname())
	for _, r := range d.Rules {
		for _, h := range r.Hosts {
			if host == h {
				return r.Fetcher, nil
			}
		}
	}
	if d.Fallback != nil {
		return d.Fallback, nil
	}
	return nil, ErrFetcherNotFound
}
