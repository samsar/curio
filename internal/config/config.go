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
	Daemon     Daemon     `yaml:"daemon"`
	Embedding  Embedding  `yaml:"embedding"`
	Fetcher    Fetcher    `yaml:"fetcher"`
	Search     Search     `yaml:"search"`
	Chunking   Chunking   `yaml:"chunking"`
	Insight    Insight    `yaml:"insight"`
	Generation Generation `yaml:"generation"`
}

type Daemon struct {
	Listen   string `yaml:"listen"`
	LogLevel string `yaml:"log_level"`
	// FetchWorkers handles fetch jobs — mostly network-bound. Can run
	// high (16+) without breaking a sweat since each worker is mostly
	// blocked on remote HTTP. Default 16.
	FetchWorkers int `yaml:"fetch_workers"`
	// IndexWorkers handles index jobs — Ollama embedding throughput is
	// the bottleneck. nomic-embed-text on Metal saturates around 4
	// concurrent embed requests; more workers just queue up inside
	// Ollama. Default 4.
	IndexWorkers int `yaml:"index_workers"`
	// Workers is the legacy single-pool count. Kept for migration:
	// when set and the new fields are zero, we split it 75/25
	// fetch/index. New configs should use FetchWorkers + IndexWorkers
	// directly.
	Workers int `yaml:"workers,omitempty"`
}

type Embedding struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Dim      int    `yaml:"dim"`
	BaseURL  string `yaml:"base_url"`
	// AutoPull downloads the embedding model via Ollama at startup if it isn't
	// present locally. Default true. Set false on metered/offline setups.
	AutoPull bool `yaml:"auto_pull"`
}

type Fetcher struct {
	Default string  `yaml:"default"` // "native" | "web2md"
	Native  Native  `yaml:"native"`
	Web2MD  Web2MD  `yaml:"web2md"`
	YouTube YouTube `yaml:"youtube"`
	GitHub  GitHub  `yaml:"github"`
}

type YouTube struct {
	Bin            string `yaml:"bin"`             // default "yt-dlp"
	TimeoutSeconds int    `yaml:"timeout_seconds"` // default 60
	SubLangs       string `yaml:"sub_langs"`       // default "en.*,en"
}

type GitHub struct {
	Token          string `yaml:"token"`           // optional; also reads CURIO_GITHUB_TOKEN env
	TimeoutSeconds int    `yaml:"timeout_seconds"` // default 30
}

type Native struct {
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	JinaFallback   bool   `yaml:"jina_fallback"`
	JinaBaseURL    string `yaml:"jina_base_url"` // override for offline tests
	UserAgent      string `yaml:"user_agent"`
	// DeadLinkDetection classifies hard 404/410s and detected soft 404s
	// as permanently dead (doc state `dead`, no retries, no Jina).
	// Default true; the kill switch exists because the soft-404 title
	// heuristics can false-positive on unusual corpora.
	DeadLinkDetection bool `yaml:"dead_link_detection"`
	// Backend selects the HTTP transport: "chrome" (default) parrots a
	// Chrome TLS+HTTP/2 fingerprint via uTLS to clear JA3/Akamai bot
	// checks; "stock" uses Go's net/http. "chrome_120|124|131|133" pin a
	// profile. See internal/fetcher/transport.go.
	Backend string `yaml:"backend"`
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

// Insight configures the M4 insight layer (document clustering → interests).
type Insight struct {
	// Enabled gates clustering. When false, POST /v1/interests/rebuild is
	// refused; reading existing interests still works.
	Enabled bool `yaml:"enabled"`
	// KNN is the neighbors-per-node in the clustering graph.
	KNN int `yaml:"knn"`
	// MinSimilarity is the cosine threshold to keep a graph edge (0..1). This
	// is the main knob for cluster granularity — higher = tighter, more
	// specific clusters and more noise; lower = broader clusters. The right
	// value is corpus-dependent; tune it with the eval harness.
	MinSimilarity float64 `yaml:"min_similarity"`
	// MinClusterSize drops communities smaller than this to noise.
	MinClusterSize int `yaml:"min_cluster_size"`
	// Labeling selects cluster naming: "llm" (default; needs a generation
	// model, else falls back to deterministic term labels), "terms", or "off".
	Labeling string `yaml:"labeling"`
}

// Generation configures the LLM text-generation client used to label clusters
// (M4) and, later, synthesize RAG answers (M6). Separate from Embedding: a
// different model and endpoint. Only used when a feature asks for it (e.g.
// insight.labeling = "llm").
type Generation struct {
	Provider       string `yaml:"provider"`        // "ollama" (only provider in v1)
	Model          string `yaml:"model"`           // a chat/instruct model, e.g. "llama3.2"
	BaseURL        string `yaml:"base_url"`        // Ollama server; can share the embedder's
	TimeoutSeconds int    `yaml:"timeout_seconds"` // per-request; generation is slow
	// AutoPull downloads the generation model via Ollama at startup if it isn't
	// present locally. Default true. Set false on metered/offline setups.
	AutoPull bool `yaml:"auto_pull"`
}

// Default returns the baseline config. The loader applies these first, then
// overlays whatever the user's config.yaml specifies.
func Default() Config {
	return Config{
		Daemon: Daemon{
			Listen:       "127.0.0.1:8765",
			LogLevel:     "info",
			FetchWorkers: 16,
			IndexWorkers: 4,
		},
		Embedding: Embedding{
			Provider: "ollama",
			Model:    "nomic-embed-text",
			Dim:      768,
			BaseURL:  "http://localhost:11434",
			AutoPull: true,
		},
		Fetcher: Fetcher{
			Default: "native",
			Native: Native{
				TimeoutSeconds:    30,
				JinaFallback:      true,
				DeadLinkDetection: true,
				Backend:           "chrome",
			},
			Web2MD: Web2MD{
				Bin:            "web2md",
				TimeoutSeconds: 30,
			},
			YouTube: YouTube{
				Bin:            "yt-dlp",
				TimeoutSeconds: 60,
				SubLangs:       "en.*,en",
			},
			GitHub: GitHub{
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
		Insight: Insight{
			Enabled:        true,
			KNN:            10,
			MinSimilarity:  0.5,
			MinClusterSize: 3,
			// LLM labels by default (richer topic names + summaries). This
			// needs a generation model, but with auto-pull the daemon fetches
			// it on first start, and if it's ever unavailable the engine falls
			// back to deterministic term labels — so it's still safe with zero
			// setup. Set "terms" to force the deterministic labeler, "off" to
			// skip labeling.
			Labeling: "llm",
		},
		Generation: Generation{
			Provider:       "ollama",
			Model:          "llama3.2",
			BaseURL:        "http://localhost:11434",
			TimeoutSeconds: 120,
			AutoPull:       true,
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
	// Resolve legacy single-pool field. When daemon.workers is set and
	// the new fields are zero, split 75/25 fetch/index. Done as a
	// best-effort migration; the user should switch to the explicit
	// fields.
	if c.Daemon.Workers > 0 && c.Daemon.FetchWorkers == 0 && c.Daemon.IndexWorkers == 0 {
		c.Daemon.FetchWorkers = (c.Daemon.Workers * 3) / 4
		if c.Daemon.FetchWorkers < 1 {
			c.Daemon.FetchWorkers = 1
		}
		c.Daemon.IndexWorkers = c.Daemon.Workers - c.Daemon.FetchWorkers
		if c.Daemon.IndexWorkers < 1 {
			c.Daemon.IndexWorkers = 1
		}
	}
	if c.Daemon.FetchWorkers <= 0 {
		return fmt.Errorf("daemon.fetch_workers must be positive, got %d", c.Daemon.FetchWorkers)
	}
	if c.Daemon.IndexWorkers <= 0 {
		return fmt.Errorf("daemon.index_workers must be positive, got %d", c.Daemon.IndexWorkers)
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
	if c.Insight.KNN <= 0 {
		return fmt.Errorf("insight.knn must be positive, got %d", c.Insight.KNN)
	}
	if c.Insight.MinClusterSize <= 0 {
		return fmt.Errorf("insight.min_cluster_size must be positive, got %d", c.Insight.MinClusterSize)
	}
	// Strictly positive: the clusterer treats a non-positive threshold as
	// "unset" and substitutes its default, so 0 here would be silently ignored.
	if c.Insight.MinSimilarity <= 0 || c.Insight.MinSimilarity > 1 {
		return fmt.Errorf("insight.min_similarity must be in (0, 1], got %g", c.Insight.MinSimilarity)
	}
	switch c.Insight.Labeling {
	case "llm", "terms", "off":
	default:
		return fmt.Errorf("insight.labeling %q must be one of: llm, terms, off", c.Insight.Labeling)
	}
	if c.Generation.Model == "" {
		return errors.New("generation.model must not be empty")
	}
	if c.Generation.BaseURL == "" {
		return errors.New("generation.base_url must not be empty")
	}
	if c.Generation.TimeoutSeconds <= 0 {
		return fmt.Errorf("generation.timeout_seconds must be positive, got %d", c.Generation.TimeoutSeconds)
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
