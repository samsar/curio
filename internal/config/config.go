// Package config loads and validates the curio daemon configuration.
//
// The config file lives at $CURIO_HOME/config.yaml. Missing or partial files
// are valid — unspecified fields fall back to documented defaults.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
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
	// Workers is the deprecated single-pool count. Load translates it
	// into FetchWorkers/IndexWorkers (75/25) and zeroes it, so code reading
	// a loaded Config only ever sees the split pools.
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
	// TimeoutSeconds bounds one embed request. The indexer sends at most 32
	// chunks per request, so the default of 60 leaves room for CPU-only
	// Ollama and for requests queued behind the other index workers'.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// DocumentPrefix / QueryPrefix are task-instruction prefixes prepended
	// before embedding. nomic-embed-text is a prefixed model and REQUIRES
	// these ("search_document: " for indexed text, "search_query: " for
	// queries); without them its embedding space collapses. They must stay
	// consistent — changing either requires reindexing the whole corpus (the
	// stored doc vectors and query vectors must share the same scheme). Set
	// both to "" for a model that takes no prefix.
	DocumentPrefix string `yaml:"document_prefix"`
	QueryPrefix    string `yaml:"query_prefix"`
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
	// JinaAPIKey raises Jina Reader's rate limit (20 → 500 requests/min
	// upstream; curio paces keyed calls at 200/min). Optional; also read
	// from CURIO_JINA_API_KEY.
	JinaAPIKey string `yaml:"jina_api_key"`
	UserAgent  string `yaml:"user_agent"`
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
	// DefaultK is the number of results when a request doesn't set k.
	DefaultK int `yaml:"default_k"`
	RRFK     int `yaml:"rrf_k"`
	// BM25Weight and VectorWeight weigh each retriever in RRF: finite, not
	// negative, not both zero. A zero switches that retriever's
	// contribution off.
	BM25Weight   float64 `yaml:"bm25_weight"`
	VectorWeight float64 `yaml:"vector_weight"`
	Collapse     string  `yaml:"collapse"` // max | sum | top3_avg
	// EmbedTimeoutSeconds bounds embedding a search query. When Ollama is
	// down or slower than this, search returns keyword-only results marked
	// degraded instead of failing. Default 10. Keep it well under the CLI and
	// MCP client's 30 s request timeout, or a hung Ollama surfaces there as a
	// client timeout instead of a degraded result.
	EmbedTimeoutSeconds int `yaml:"embed_timeout_seconds"`
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
	// CenterVectors subtracts the corpus mean vector before clustering. Default
	// true: embedding models like nomic-embed-text are anisotropic (vectors in
	// a narrow cone), so without it raw cosines are uniformly high and the
	// corpus collapses into one giant cluster. Turn off only if your embeddings
	// are already isotropic.
	CenterVectors bool `yaml:"center_vectors"`
	// Labeling selects cluster naming: "llm" (default; needs a generation
	// model, else falls back to deterministic term labels), "terms", or "off".
	Labeling string `yaml:"labeling"`
	// LabelingTimeoutSeconds bounds the total time one clustering run waits
	// on the LLM labeler; clusters left when it runs out get term labels.
	// Default 900. Keeps a hung Ollama from holding the single cluster worker.
	LabelingTimeoutSeconds int `yaml:"labeling_timeout_seconds"`
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
			Provider:       providerOllama,
			Model:          "nomic-embed-text",
			Dim:            store.EmbeddingDim,
			BaseURL:        "http://localhost:11434",
			AutoPull:       true,
			TimeoutSeconds: 60,
			DocumentPrefix: "search_document: ",
			QueryPrefix:    "search_query: ",
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
			DefaultK:            10,
			RRFK:                60,
			BM25Weight:          1.0,
			VectorWeight:        1.0,
			Collapse:            "max",
			EmbedTimeoutSeconds: 10,
		},
		Chunking: Chunking{
			// 384 words is conservative: nomic-embed-text's context is
			// 2048 tokens (its GGUF context_length; the num_ctx we send is
			// advisory), and dense markdown (URLs, code blocks, tables)
			// has far more BPE tokens than whitespace-words. The chunker's
			// 3500-byte cap backs this up for the worst content. See
			// decisions.md.
			SizeTokens:    384,
			OverlapTokens: 48,
		},
		Insight: Insight{
			Enabled:        true,
			KNN:            10,
			MinSimilarity:  0.5,
			MinClusterSize: 3,
			CenterVectors:  true,
			// LLM labels by default (richer topic names + summaries). This
			// needs a generation model, but with auto-pull the daemon fetches
			// it on first start, and if it's ever unavailable the engine falls
			// back to deterministic term labels — so it's still safe with zero
			// setup. Set "terms" to force the deterministic labeler, "off" to
			// skip labeling.
			Labeling:               "llm",
			LabelingTimeoutSeconds: 900,
		},
		Generation: Generation{
			Provider:       providerOllama,
			Model:          "llama3.2",
			BaseURL:        "http://localhost:11434",
			TimeoutSeconds: 120,
			AutoPull:       true,
		},
	}
}

// providerOllama is the only embedding and generation provider implemented.
const providerOllama = "ollama"

// Load reads config.yaml from path. A missing file is not an error; the
// defaults are returned. An empty or partial file overlays onto defaults.
// A malformed or invalid file is an error, and so is an unknown key: a
// typo'd section would otherwise be silently ignored and its defaults used.
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
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := cfg.applyLegacyWorkers(data); err != nil {
		return Config{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return cfg, nil
}

// applyLegacyWorkers folds the deprecated daemon.workers count into the split
// pools. It keys off which keys the file sets, not their values: decoding on
// top of Default() leaves fetch_workers at 16 whether or not the user wrote
// it, so comparing against defaults can't tell a legacy config from a new one.
func (c *Config) applyLegacyWorkers(data []byte) error {
	var set struct {
		Daemon struct {
			Workers      *int `yaml:"workers"`
			FetchWorkers *int `yaml:"fetch_workers"`
			IndexWorkers *int `yaml:"index_workers"`
		} `yaml:"daemon"`
	}
	// The strict decode in Load already accepted this document.
	if err := yaml.Unmarshal(data, &set); err != nil {
		return fmt.Errorf("re-read daemon worker keys: %w", err)
	}
	d := set.Daemon
	if d.Workers == nil {
		return nil
	}
	if d.FetchWorkers != nil || d.IndexWorkers != nil {
		return errors.New("daemon.workers is deprecated and cannot be combined with " +
			"daemon.fetch_workers / daemon.index_workers; remove daemon.workers")
	}
	if *d.Workers <= 0 {
		return fmt.Errorf("daemon.workers must be positive, got %d "+
			"(deprecated: prefer daemon.fetch_workers and daemon.index_workers)", *d.Workers)
	}
	c.Daemon.FetchWorkers = max(1, *d.Workers*3/4)
	c.Daemon.IndexWorkers = max(1, *d.Workers-c.Daemon.FetchWorkers)
	c.Daemon.Workers = 0
	return nil
}

// Validate checks invariants that the YAML schema can't enforce. Called by
// Load; can also be called directly when constructing Config in tests. It
// never modifies c.
func (c Config) Validate() error {
	if err := validateListen(c.Daemon.Listen); err != nil {
		return err
	}
	if _, ok := logLevels[c.Daemon.LogLevel]; !ok {
		return fmt.Errorf("daemon.log_level %q must be one of: debug, info, warn, error", c.Daemon.LogLevel)
	}
	if c.Daemon.FetchWorkers <= 0 {
		return fmt.Errorf("daemon.fetch_workers must be positive, got %d", c.Daemon.FetchWorkers)
	}
	if c.Daemon.IndexWorkers <= 0 {
		return fmt.Errorf("daemon.index_workers must be positive, got %d", c.Daemon.IndexWorkers)
	}
	if c.Embedding.Provider != providerOllama {
		return fmt.Errorf("embedding.provider %q is not supported; the only provider is %q",
			c.Embedding.Provider, providerOllama)
	}
	if c.Embedding.Model == "" {
		return errors.New("embedding.model must not be empty")
	}
	if c.Embedding.Dim != store.EmbeddingDim {
		return fmt.Errorf("embedding.dim must be %d, got %d: the vector index is created with a fixed "+
			"dimension and a different-dimension model swap isn't implemented yet "+
			"(see docs/decisions.md \"Embedding model swap\")", store.EmbeddingDim, c.Embedding.Dim)
	}
	if c.Embedding.BaseURL == "" {
		return errors.New("embedding.base_url must not be empty")
	}
	if c.Embedding.TimeoutSeconds <= 0 {
		return fmt.Errorf("embedding.timeout_seconds must be positive, got %d", c.Embedding.TimeoutSeconds)
	}
	if c.Chunking.SizeTokens <= 0 {
		return fmt.Errorf("chunking.size_tokens must be positive, got %d", c.Chunking.SizeTokens)
	}
	if c.Chunking.OverlapTokens < 0 || c.Chunking.OverlapTokens >= c.Chunking.SizeTokens {
		return fmt.Errorf("chunking.overlap_tokens must be in [0, %d), got %d",
			c.Chunking.SizeTokens, c.Chunking.OverlapTokens)
	}
	if c.Search.DefaultK <= 0 || c.Search.DefaultK > search.MaxK {
		return fmt.Errorf("search.default_k must be in [1, %d], got %d", search.MaxK, c.Search.DefaultK)
	}
	if c.Search.RRFK <= 0 {
		return fmt.Errorf("search.rrf_k must be positive, got %d", c.Search.RRFK)
	}
	if !validCollapse(c.Search.Collapse) {
		return fmt.Errorf("search.collapse %q must be one of: max, sum, top3_avg", c.Search.Collapse)
	}
	if err := validateWeights(c.Search); err != nil {
		return err
	}
	if c.Search.EmbedTimeoutSeconds <= 0 {
		return fmt.Errorf("search.embed_timeout_seconds must be positive, got %d", c.Search.EmbedTimeoutSeconds)
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
	// Written so NaN (YAML .nan), which fails every comparison, is rejected.
	if !(c.Insight.MinSimilarity > 0 && c.Insight.MinSimilarity <= 1) {
		return fmt.Errorf("insight.min_similarity must be in (0, 1], got %g", c.Insight.MinSimilarity)
	}
	switch c.Insight.Labeling {
	case "llm", "terms", "off":
	default:
		return fmt.Errorf("insight.labeling %q must be one of: llm, terms, off", c.Insight.Labeling)
	}
	if c.Insight.LabelingTimeoutSeconds <= 0 {
		return fmt.Errorf("insight.labeling_timeout_seconds must be positive, got %d",
			c.Insight.LabelingTimeoutSeconds)
	}
	if c.Generation.Provider != providerOllama {
		return fmt.Errorf("generation.provider %q is not supported; the only provider is %q",
			c.Generation.Provider, providerOllama)
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

// validateListen requires a loopback host and a fixed port. The API has no
// authentication — anything that can reach the socket can read the corpus
// and enqueue fetches — so it must never bind a routable interface. Port 0
// is rejected too: clients derive the daemon's URL from this setting, so an
// ephemeral port would be unreachable.
func validateListen(addr string) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("daemon.listen %q must be host:port on loopback, e.g. 127.0.0.1:8765: %w", addr, err)
	}
	if port, err := strconv.Atoi(portStr); err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("daemon.listen %q: port must be a fixed number in 1-65535", addr)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("daemon.listen %q: the API is unauthenticated and must stay on loopback "+
			"(use 127.0.0.1, ::1 or localhost)", addr)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// logLevels maps daemon.log_level values to slog levels; its keys are the
// accepted values.
var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

// SlogLevel returns the slog level for LogLevel. Validate guarantees a known
// value; an unknown one maps to info.
func (d Daemon) SlogLevel() slog.Level {
	if lvl, ok := logLevels[d.LogLevel]; ok {
		return lvl
	}
	return slog.LevelInfo
}

// validateWeights checks the RRF weights. NaN or a negative weight corrupts
// the fused ordering, and with both at zero every document scores zero.
func validateWeights(s Search) error {
	for _, w := range []struct {
		key string
		v   float64
	}{{"search.bm25_weight", s.BM25Weight}, {"search.vector_weight", s.VectorWeight}} {
		// Written so NaN, which fails every comparison, is rejected.
		if !(w.v >= 0) || math.IsInf(w.v, 1) {
			return fmt.Errorf("%s must be a finite number >= 0, got %g", w.key, w.v)
		}
	}
	if s.BM25Weight == 0 && s.VectorWeight == 0 {
		return errors.New("search.bm25_weight and search.vector_weight must not both be 0")
	}
	return nil
}

func validCollapse(s string) bool {
	switch s {
	case "max", "sum", "top3_avg":
		return true
	}
	return false
}
