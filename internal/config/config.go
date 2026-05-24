// Package config loads and validates the curio daemon configuration.
//
// The config file lives at $CURIO_HOME/config.yaml. Missing or partial files
// are valid — unspecified fields fall back to documented defaults.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration loaded from config.yaml.
type Config struct {
	Daemon    Daemon    `yaml:"daemon"`
	Embedding Embedding `yaml:"embedding"`
	Fetcher   Fetcher   `yaml:"fetcher"`
	Search    Search    `yaml:"search"`
	Chunking  Chunking  `yaml:"chunking"`
}

type Daemon struct {
	Listen   string `yaml:"listen"`
	LogLevel string `yaml:"log_level"`
	// Workers is the number of background goroutines draining the job
	// queue concurrently. Each worker fetches and indexes one job at a
	// time; the bottleneck is usually Ollama embedding throughput rather
	// than Go concurrency, so don't crank this too high. Default 4.
	Workers int `yaml:"workers"`
}

type Embedding struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Dim      int    `yaml:"dim"`
	BaseURL  string `yaml:"base_url"`
}

type Fetcher struct {
	Default string `yaml:"default"` // "native" | "web2md"
	Native  Native `yaml:"native"`
	Web2MD  Web2MD `yaml:"web2md"`
}

type Native struct {
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	JinaFallback   bool   `yaml:"jina_fallback"`
	JinaBaseURL    string `yaml:"jina_base_url"` // override for offline tests
	UserAgent      string `yaml:"user_agent"`
}

type Web2MD struct {
	Bin            string `yaml:"bin"`
	NodeBin        string `yaml:"node_bin"` // override; defaults to "node" in PATH
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

type Search struct {
	DefaultK     int     `yaml:"default_k"`
	RRFK         int     `yaml:"rrf_k"`
	BM25Weight   float64 `yaml:"bm25_weight"`
	VectorWeight float64 `yaml:"vector_weight"`
	Collapse     string  `yaml:"collapse"` // max | sum | top3_avg
}

type Chunking struct {
	SizeTokens    int `yaml:"size_tokens"`
	OverlapTokens int `yaml:"overlap_tokens"`
}

// Default returns the baseline config. The loader applies these first, then
// overlays whatever the user's config.yaml specifies.
func Default() Config {
	return Config{
		Daemon: Daemon{
			Listen:   "127.0.0.1:8765",
			LogLevel: "info",
			// 16 workers covers the fetch-bound case (most jobs are
			// network-blocked, plenty of headroom). Ollama serializes
			// index work naturally so adding more workers doesn't
			// pile on the embedding queue. Split fetch/index pools
			// would be tidier but the single pool works fine — measured
			// fetch p95 ~5s, index p95 <1s, so the indexer keeps up
			// when fetches finally land. Tune via daemon.workers in
			// ~/.curio/config.yaml.
			Workers: 16,
		},
		Embedding: Embedding{
			Provider: "ollama",
			Model:    "nomic-embed-text",
			Dim:      768,
			BaseURL:  "http://localhost:11434",
		},
		Fetcher: Fetcher{
			Default: "native",
			Native: Native{
				TimeoutSeconds: 30,
				JinaFallback:   true,
			},
			Web2MD: Web2MD{
				Bin:            "web2md",
				TimeoutSeconds: 30,
			},
		},
		Search: Search{
			DefaultK:     10,
			RRFK:         60,
			BM25Weight:   1.0,
			VectorWeight: 1.0,
			Collapse:     "max",
		},
		Chunking: Chunking{
			// 384 words is conservative: nomic-embed-text supports 8192
			// tokens, but dense markdown (URLs, code blocks, tables) can
			// have far more BPE tokens than whitespace-words. 384 words
			// stays comfortably under 8192 tokens even for the worst
			// content. See decisions.md.
			SizeTokens:    384,
			OverlapTokens: 48,
		},
	}
}

// Load reads config.yaml from path. A missing file is not an error; the
// defaults are returned. An empty or partial file overlays onto defaults.
// A malformed or invalid file is an error.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	// Decode on top of defaults: fields the user omits keep their default.
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return cfg, nil
}

// Validate checks invariants that the YAML schema can't enforce. Called by
// Load; can also be called directly when constructing Config in tests.
func (c Config) Validate() error {
	if c.Daemon.Listen == "" {
		return errors.New("daemon.listen must not be empty")
	}
	if !validLogLevel(c.Daemon.LogLevel) {
		return fmt.Errorf("daemon.log_level %q must be one of: debug, info, warn, error", c.Daemon.LogLevel)
	}
	if c.Daemon.Workers <= 0 {
		return fmt.Errorf("daemon.workers must be positive, got %d", c.Daemon.Workers)
	}
	if c.Embedding.Model == "" {
		return errors.New("embedding.model must not be empty")
	}
	if c.Embedding.Dim <= 0 {
		return fmt.Errorf("embedding.dim must be positive, got %d", c.Embedding.Dim)
	}
	if c.Embedding.BaseURL == "" {
		return errors.New("embedding.base_url must not be empty")
	}
	if c.Chunking.SizeTokens <= 0 {
		return fmt.Errorf("chunking.size_tokens must be positive, got %d", c.Chunking.SizeTokens)
	}
	if c.Chunking.OverlapTokens < 0 || c.Chunking.OverlapTokens >= c.Chunking.SizeTokens {
		return fmt.Errorf("chunking.overlap_tokens must be in [0, %d), got %d",
			c.Chunking.SizeTokens, c.Chunking.OverlapTokens)
	}
	if c.Search.DefaultK <= 0 {
		return fmt.Errorf("search.default_k must be positive, got %d", c.Search.DefaultK)
	}
	if c.Search.RRFK <= 0 {
		return fmt.Errorf("search.rrf_k must be positive, got %d", c.Search.RRFK)
	}
	if !validCollapse(c.Search.Collapse) {
		return fmt.Errorf("search.collapse %q must be one of: max, sum, top3_avg", c.Search.Collapse)
	}
	if c.Fetcher.Web2MD.TimeoutSeconds <= 0 {
		return fmt.Errorf("fetcher.web2md.timeout_seconds must be positive, got %d",
			c.Fetcher.Web2MD.TimeoutSeconds)
	}
	if c.Fetcher.Native.TimeoutSeconds <= 0 {
		return fmt.Errorf("fetcher.native.timeout_seconds must be positive, got %d",
			c.Fetcher.Native.TimeoutSeconds)
	}
	switch c.Fetcher.Default {
	case "native", "web2md":
	case "":
		return errors.New("fetcher.default must be set (native or web2md)")
	default:
		return fmt.Errorf("fetcher.default %q must be one of: native, web2md", c.Fetcher.Default)
	}
	return nil
}

func validLogLevel(s string) bool {
	switch s {
	case "debug", "info", "warn", "error":
		return true
	}
	return false
}

func validCollapse(s string) bool {
	switch s {
	case "max", "sum", "top3_avg":
		return true
	}
	return false
}
