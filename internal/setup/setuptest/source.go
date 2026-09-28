package setuptest

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/samsar/curio/internal/importer"
)

// browserDirs are the variables that point the importer at each browser's
// files.
var browserDirs = []string{"CURIO_CHROME_DIR", "CURIO_SAFARI_DIR", "CURIO_FIREFOX_DIR"}

// WithoutBrowsers runs run, a TestMain's m.Run, with every browser's
// directory pointed at an empty one, so no test reads the bookmarks of the
// machine it runs on, and returns its exit code.
func WithoutBrowsers(run func() int) int {
	empty, err := os.MkdirTemp("", "curio-no-browsers-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "setuptest:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(empty) }() // an empty directory left in the temp dir is harmless
	for _, v := range browserDirs {
		if err := os.Setenv(v, empty); err != nil {
			fmt.Fprintln(os.Stderr, "setuptest:", err)
			return 1
		}
	}
	return run()
}

// NoSources is a Deps.Sources that finds nothing.
func NoSources() []importer.Source { return nil }

// Source is an importer.Source a test scripts: its bookmarks, whether it
// can be read, and why not, which it can change while a run goes on (Full
// Disk Access granted, say).
type Source struct {
	// SourceName, SourceLabel and SourceSpec are its Name, Label and Spec.
	SourceName, SourceLabel, SourceSpec string

	mu        sync.Mutex
	av        importer.Availability
	bookmarks []importer.ParsedBookmark
	parseErr  error
	parses    int
}

var _ importer.Source = (*Source)(nil)

// NewSource is an available source named name, with label and spec, that
// holds a bookmark of each of urls.
func NewSource(name, label, spec string, urls ...string) *Source {
	s := &Source{SourceName: name, SourceLabel: label, SourceSpec: spec,
		av: importer.Availability{State: importer.Available}}
	for _, u := range urls {
		s.bookmarks = append(s.bookmarks, importer.ParsedBookmark{URL: u, Title: u})
	}
	return s
}

func (s *Source) Name() string  { return s.SourceName }
func (s *Source) Label() string { return s.SourceLabel }
func (s *Source) Spec() string  { return s.SourceSpec }

func (s *Source) Check(context.Context) importer.Availability {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.av
}

func (s *Source) Parse(context.Context) ([]importer.ParsedBookmark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parses++
	if s.av.State != importer.Available {
		return nil, fmt.Errorf("%s: %s", s.SourceName, s.av.Reason)
	}
	if s.parseErr != nil {
		return nil, s.parseErr
	}
	if len(s.bookmarks) == 0 {
		return nil, importer.ErrEmpty
	}
	return slices.Clone(s.bookmarks), nil
}

// Set makes the source's availability av from now on.
func (s *Source) Set(av importer.Availability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.av = av
}

// FailParse makes parsing fail with err.
func (s *Source) FailParse(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parseErr = err
}

// Parses is how many times it was parsed.
func (s *Source) Parses() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parses
}
