package fetcher

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// This file implements the fetcher_rules.yaml routing engine designed in
// docs/architecture.md ("Fetcher strategy selection"): rules are listed
// top-to-bottom, first match wins, and the file is hot-reloadable — the
// daemon picks up edits without a restart.
//
//	rules:
//	  - match: { host: "github.com" }
//	    fetcher: github
//	  - match: { host_suffix: ".youtube.com" }
//	    fetcher: youtube
//	  - match: { host_in: ["news.ycombinator.com", "lobste.rs"] }
//	    fetcher: native
//	  - match: {}          # catch-all
//	    fetcher: native
//
// Matchers are URL-based only: host (exact), host_suffix (label-boundary
// suffix), host_in (list of exact hosts), or empty (catch-all). The
// content_type matcher sketched in the original design is rejected with a
// clear error — dispatch happens before the response exists, and PDFs are
// already handled inside the Native fetcher.

// MatchSpec is the YAML `match:` block of one rule.
type MatchSpec struct {
	Host       string   `yaml:"host"`
	HostSuffix string   `yaml:"host_suffix"`
	HostIn     []string `yaml:"host_in"`
	// ContentType is parsed only to give a helpful error: it appeared in
	// the design sketch but content-type routing can't work pre-fetch.
	ContentType string `yaml:"content_type"`
}

// RuleSpec is one YAML rule: a matcher plus the name of the fetcher that
// handles matching URLs.
type RuleSpec struct {
	Match   MatchSpec `yaml:"match"`
	Fetcher string    `yaml:"fetcher"`
}

// RulesFile is the parsed shape of fetcher_rules.yaml.
type RulesFile struct {
	Rules []RuleSpec `yaml:"rules"`
}

// ParseRules parses and validates fetcher_rules.yaml content. Returns an
// error describing the first invalid rule — callers keep the last good
// rule set when this fails.
//
// Decoding is STRICT (KnownFields): an unknown key is an error, not a
// silent no-op. Without this, a typo'd matcher key (`host_sufix:`) would
// decode to an all-empty MatchSpec — which is the catch-all — and one
// misspelled rule would silently hijack every URL on the next reload.
func ParseRules(data []byte) (*RulesFile, error) {
	var rf RulesFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&rf); err != nil {
		if errors.Is(err, io.EOF) {
			return &rf, nil // empty file: no rules, everything falls to the fallback
		}
		return nil, fmt.Errorf("fetcher rules: parse yaml: %w", err)
	}
	for i, r := range rf.Rules {
		if r.Fetcher == "" {
			return nil, fmt.Errorf("fetcher rules: rule %d: missing fetcher name", i+1)
		}
		if r.Match.ContentType != "" {
			return nil, fmt.Errorf("fetcher rules: rule %d: content_type matching is not supported (dispatch happens before the response exists; PDFs are handled inside the native fetcher)", i+1)
		}
		set := 0
		if r.Match.Host != "" {
			set++
		}
		if r.Match.HostSuffix != "" {
			set++
		}
		if len(r.Match.HostIn) > 0 {
			set++
		}
		if set > 1 {
			return nil, fmt.Errorf("fetcher rules: rule %d: use only one of host, host_suffix, host_in", i+1)
		}
	}
	return &rf, nil
}

// matchesHost reports whether the rule's matcher covers host (already
// lowercased). host_suffix respects label boundaries: "youtube.com" (or
// ".youtube.com") matches "youtube.com" and "www.youtube.com" but not
// "evilyoutube.com".
func (r RuleSpec) matchesHost(host string) bool {
	switch {
	case r.Match.Host != "":
		return host == strings.ToLower(r.Match.Host)
	case r.Match.HostSuffix != "":
		suffix := strings.ToLower(strings.TrimPrefix(r.Match.HostSuffix, "."))
		return host == suffix || strings.HasSuffix(host, "."+suffix)
	case len(r.Match.HostIn) > 0:
		for _, h := range r.Match.HostIn {
			if host == strings.ToLower(h) {
				return true
			}
		}
		return false
	default:
		return true // catch-all
	}
}

// boundRule is a RuleSpec resolved against the fetcher registry.
type boundRule struct {
	spec    RuleSpec
	fetcher Fetcher
}

// ruleSnapshot is an immutable compiled rule set; RulesDispatcher swaps
// whole snapshots on reload so concurrent For() calls never see a
// half-updated state.
type ruleSnapshot struct {
	rules    []boundRule
	fromFile bool // false = built-in defaults (file absent)
}

func (s *ruleSnapshot) match(rawURL string, fallback Fetcher) (Fetcher, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		if fallback != nil {
			return fallback, nil
		}
		return nil, ErrFetcherNotFound
	}
	host := strings.ToLower(u.Hostname())
	for _, r := range s.rules {
		if r.spec.matchesHost(host) {
			return r.fetcher, nil
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, ErrFetcherNotFound
}

// RulesDispatcherOptions configures NewRulesDispatcher.
type RulesDispatcherOptions struct {
	// Path of fetcher_rules.yaml. A missing file is not an error — the
	// dispatcher uses DefaultRules until the file appears.
	Path string
	// Registry maps fetcher names (Fetcher.Name()) to instances. Rules
	// naming a fetcher absent from the registry are skipped with a logged
	// warning (e.g. "youtube" when yt-dlp isn't installed).
	Registry map[string]Fetcher
	// DefaultRules are the built-in host rules used when the file is
	// absent — the same wiring the daemon used before rules existed.
	DefaultRules []Rule
	// Fallback handles URLs no rule matches (and everything when the
	// file is absent and DefaultRules don't match).
	Fallback Fetcher
	// CheckInterval is how often the file's mtime is re-checked on
	// dispatch. Zero means the 2s default; tests can set it negative to
	// check on every call.
	CheckInterval time.Duration
	Log           *slog.Logger
}

// RulesDispatcher routes URLs to fetchers per fetcher_rules.yaml,
// hot-reloading the file when it changes. Reload semantics:
//
//   - file absent        → built-in DefaultRules (+ Fallback)
//   - file appears/edits → recompiled on the next dispatch (mtime+size
//     stat, throttled to CheckInterval)
//   - file invalid       → keep the last good rule set, log a warning
//   - file deleted       → back to built-in DefaultRules
type RulesDispatcher struct {
	path          string
	registry      map[string]Fetcher
	fallback      Fetcher
	builtin       *ruleSnapshot
	checkInterval time.Duration
	log           *slog.Logger

	mu        sync.Mutex
	current   *ruleSnapshot
	lastCheck time.Time
	lastMod   time.Time
	lastSize  int64
	haveFile  bool
}

// NewRulesDispatcher builds the dispatcher and loads the rules file once,
// so config problems surface in the daemon log at startup rather than on
// the first fetch.
func NewRulesDispatcher(opts RulesDispatcherOptions) *RulesDispatcher {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.CheckInterval == 0 {
		opts.CheckInterval = 2 * time.Second
	}
	builtin := make([]boundRule, 0, len(opts.DefaultRules))
	for _, r := range opts.DefaultRules {
		builtin = append(builtin, boundRule{
			spec:    RuleSpec{Match: MatchSpec{HostIn: r.Hosts}, Fetcher: r.Fetcher.Name()},
			fetcher: r.Fetcher,
		})
	}
	d := &RulesDispatcher{
		path:          opts.Path,
		registry:      opts.Registry,
		fallback:      opts.Fallback,
		builtin:       &ruleSnapshot{rules: builtin},
		checkInterval: opts.CheckInterval,
		log:           opts.Log,
	}
	d.current = d.builtin
	d.mu.Lock()
	d.reloadLocked()
	d.mu.Unlock()
	return d
}

// For implements Dispatcher.
func (d *RulesDispatcher) For(rawURL string) (Fetcher, error) {
	d.mu.Lock()
	if time.Since(d.lastCheck) >= d.checkInterval {
		d.reloadLocked()
	}
	snap := d.current
	d.mu.Unlock()
	return snap.match(rawURL, d.fallback)
}

// reloadLocked stats the rules file and recompiles it if it changed.
// Callers hold d.mu.
func (d *RulesDispatcher) reloadLocked() {
	d.lastCheck = time.Now()

	info, err := os.Stat(d.path)
	if err != nil {
		if d.haveFile {
			d.log.Info("fetcher rules file removed, reverting to built-in rules", "path", d.path)
		}
		d.haveFile = false
		d.current = d.builtin
		return
	}

	// Unchanged since the last look — nothing to do. This also covers a
	// file that failed to parse: its stat was recorded too, so it isn't
	// re-parsed (and re-warned about) every interval; the current rules
	// stay whatever was last good (possibly the built-ins).
	if d.haveFile && info.ModTime().Equal(d.lastMod) && info.Size() == d.lastSize {
		return
	}

	data, err := os.ReadFile(d.path)
	if err != nil {
		d.log.Warn("fetcher rules unreadable, keeping current rules", "path", d.path, "err", err)
		return
	}
	rf, err := ParseRules(data)
	if err != nil {
		d.log.Warn("fetcher rules invalid, keeping current rules", "path", d.path, "err", err)
		// Remember the stat so we don't re-parse the same broken file on
		// every dispatch; the next edit changes mtime and retriggers.
		d.haveFile = true
		d.lastMod = info.ModTime()
		d.lastSize = info.Size()
		return
	}

	rules := make([]boundRule, 0, len(rf.Rules))
	for i, spec := range rf.Rules {
		f, ok := d.registry[spec.Fetcher]
		if !ok {
			d.log.Warn("fetcher rule names an unavailable fetcher, skipping rule",
				"rule", i+1, "fetcher", spec.Fetcher, "available", registryNames(d.registry))
			continue
		}
		rules = append(rules, boundRule{spec: spec, fetcher: f})
	}

	d.haveFile = true
	d.lastMod = info.ModTime()
	d.lastSize = info.Size()
	d.current = &ruleSnapshot{rules: rules, fromFile: true}
	d.log.Info("fetcher rules loaded", "path", d.path, "rules", len(rules), "skipped", len(rf.Rules)-len(rules))
}

func registryNames(reg map[string]Fetcher) []string {
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	return names
}
