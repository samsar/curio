package config

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/fetcher"
)

func TestLoad_MissingFile_ReturnsDefaults(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	require.NoError(t, err)
	assert.Equal(t, Default(), got)
}

func TestLoad_EmptyFile_ReturnsDefaults(t *testing.T) {
	path := writeConfig(t, "")
	got, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, Default(), got)
}

func TestLoad_PartialOverlay(t *testing.T) {
	// Only override a couple of fields; the rest should keep defaults.
	path := writeConfig(t, `
daemon:
  listen: "127.0.0.1:9999"
embedding:
  model: "mxbai-embed-large"
`)
	got, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:9999", got.Daemon.Listen)
	assert.Equal(t, "info", got.Daemon.LogLevel, "untouched field keeps default")
	assert.Equal(t, "mxbai-embed-large", got.Embedding.Model)
	assert.Equal(t, Default().Embedding.Dim, got.Embedding.Dim, "untouched field keeps default")
	assert.Equal(t, "ollama", got.Embedding.Provider, "untouched field keeps default")
	assert.Equal(t, Default().Chunking.SizeTokens, got.Chunking.SizeTokens, "untouched section keeps default")
}

func TestLoad_FullConfig(t *testing.T) {
	path := writeConfig(t, `
daemon:
  listen: "127.0.0.1:7000"
  log_level: "debug"
embedding:
  provider: "ollama"
  model: "nomic-embed-text"
  dim: 768
  base_url: "http://192.168.1.20:11434"
fetcher:
  native:
    jina_api_key: "jina_test_key"
  web2md:
    bin: "/usr/local/bin/web2md"
    timeout_seconds: 60
search:
  default_k: 20
  rrf_k: 80
  bm25_weight: 0.5
  vector_weight: 1.5
  fields_weight: 0.75
  cards_weight: 0.25
  title_weight: 1.5
  collapse: "top3_avg"
chunking:
  size_tokens: 1024
  overlap_tokens: 128
`)
	got, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:7000", got.Daemon.Listen)
	assert.Equal(t, "debug", got.Daemon.LogLevel)
	assert.Equal(t, "http://192.168.1.20:11434", got.Embedding.BaseURL)
	assert.Equal(t, "jina_test_key", got.Fetcher.Native.JinaAPIKey)
	assert.True(t, got.Fetcher.Native.JinaFallback, "untouched sibling keeps default")
	assert.Equal(t, "/usr/local/bin/web2md", got.Fetcher.Web2MD.Bin)
	assert.Equal(t, 60, got.Fetcher.Web2MD.TimeoutSeconds)
	assert.Equal(t, 20, got.Search.DefaultK)
	assert.InDelta(t, 0.75, got.Search.FieldsWeight, 0)
	assert.InDelta(t, 0.25, got.Search.CardsWeight, 0)
	assert.InDelta(t, 1.5, got.Search.TitleWeight, 0)
	assert.Equal(t, "top3_avg", got.Search.Collapse)
	assert.Equal(t, 1024, got.Chunking.SizeTokens)
	assert.Equal(t, 128, got.Chunking.OverlapTokens)
}

// TestLoad_JinaSiteRequestsPerMinute: the key sets the pace, and a file
// without it keeps the fetcher's default.
func TestLoad_JinaSiteRequestsPerMinute(t *testing.T) {
	got, err := Load(writeConfig(t, "fetcher:\n  native:\n    jina_site_requests_per_minute: 3\n"))
	require.NoError(t, err)
	assert.Equal(t, 3, got.Fetcher.Native.JinaSiteRequestsPerMinute)
	assert.True(t, got.Fetcher.Native.JinaFallback, "untouched sibling keeps default")

	got, err = Load(writeConfig(t, "fetcher:\n  native:\n    timeout_seconds: 10\n"))
	require.NoError(t, err)
	assert.Equal(t, fetcher.DefaultJinaSiteRequestsPerMinute, got.Fetcher.Native.JinaSiteRequestsPerMinute)
}

// TestLoad_TitleWeight: the key weighs the title list, and a search
// section without it, as every config.yaml written before the list has,
// keeps the default 2.
func TestLoad_TitleWeight(t *testing.T) {
	got, err := Load(writeConfig(t, "search:\n  title_weight: 0\n"))
	require.NoError(t, err)
	assert.Zero(t, got.Search.TitleWeight)

	got, err = Load(writeConfig(t, "search:\n  fields_weight: 0.25\n  cards_weight: 0.5\n"))
	require.NoError(t, err)
	assert.InDelta(t, 2.0, got.Search.TitleWeight, 0)
}

// TestDefault_JinaSiteRequestsPerMinute: config's default is the fetcher's,
// which config can't import without linking the fetcher into every binary
// that reads config.yaml.
func TestDefault_JinaSiteRequestsPerMinute(t *testing.T) {
	assert.Equal(t, fetcher.DefaultJinaSiteRequestsPerMinute, Default().Fetcher.Native.JinaSiteRequestsPerMinute)
}

func TestLoad_MalformedYAML(t *testing.T) {
	path := writeConfig(t, "daemon: {not valid yaml")
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"empty listen", func(c *Config) { c.Daemon.Listen = "" }, "daemon.listen"},
		{"bad log level", func(c *Config) { c.Daemon.LogLevel = "verbose" }, "daemon.log_level"},
		{"zero fetch workers", func(c *Config) { c.Daemon.FetchWorkers = 0 }, "daemon.fetch_workers"},
		{"zero index workers", func(c *Config) { c.Daemon.IndexWorkers = 0 }, "daemon.index_workers"},
		{"unknown embedding provider", func(c *Config) { c.Embedding.Provider = "voyage" }, "embedding.provider"},
		{"unknown generation provider", func(c *Config) { c.Generation.Provider = "openai" }, "generation.provider"},
		{"empty model", func(c *Config) { c.Embedding.Model = "" }, "embedding.model"},
		{"zero dim", func(c *Config) { c.Embedding.Dim = 0 }, "embedding.dim must be in [1, 8192], got 0"},
		{"negative dim", func(c *Config) { c.Embedding.Dim = -1 }, "embedding.dim must be in [1, 8192], got -1"},
		{"dim past the vector index's limit", func(c *Config) { c.Embedding.Dim = 8193 }, "embedding.dim must be in [1, 8192], got 8193"},
		{"empty base_url", func(c *Config) { c.Embedding.BaseURL = "" }, "embedding.base_url"},
		{"zero embedding timeout", func(c *Config) { c.Embedding.TimeoutSeconds = 0 }, "embedding.timeout_seconds"},
		{"negative embedding timeout", func(c *Config) { c.Embedding.TimeoutSeconds = -5 }, "embedding.timeout_seconds"},
		{"zero chunk size", func(c *Config) { c.Chunking.SizeTokens = 0 }, "chunking.size_tokens"},
		{"overlap >= size", func(c *Config) { c.Chunking.OverlapTokens = 512 }, "chunking.overlap_tokens"},
		{"negative overlap", func(c *Config) { c.Chunking.OverlapTokens = -1 }, "chunking.overlap_tokens"},
		{"zero default_k", func(c *Config) { c.Search.DefaultK = 0 }, "search.default_k"},
		{"default_k above the max", func(c *Config) { c.Search.DefaultK = 101 }, "search.default_k"},
		{"NaN bm25 weight", func(c *Config) { c.Search.BM25Weight = math.NaN() }, "search.bm25_weight"},
		{"negative bm25 weight", func(c *Config) { c.Search.BM25Weight = -1 }, "search.bm25_weight"},
		{"infinite bm25 weight", func(c *Config) { c.Search.BM25Weight = math.Inf(1) }, "search.bm25_weight"},
		{"NaN vector weight", func(c *Config) { c.Search.VectorWeight = math.NaN() }, "search.vector_weight"},
		{"negative vector weight", func(c *Config) { c.Search.VectorWeight = -0.5 }, "search.vector_weight"},
		{"infinite vector weight", func(c *Config) { c.Search.VectorWeight = math.Inf(-1) }, "search.vector_weight"},
		{"NaN fields weight", func(c *Config) { c.Search.FieldsWeight = math.NaN() }, "search.fields_weight"},
		{"negative fields weight", func(c *Config) { c.Search.FieldsWeight = -0.25 }, "search.fields_weight"},
		{"infinite cards weight", func(c *Config) { c.Search.CardsWeight = math.Inf(1) }, "search.cards_weight"},
		{"negative cards weight", func(c *Config) { c.Search.CardsWeight = -1 }, "search.cards_weight"},
		{"NaN title weight", func(c *Config) { c.Search.TitleWeight = math.NaN() }, "search.title_weight"},
		{"negative title weight", func(c *Config) { c.Search.TitleWeight = -2 }, "search.title_weight"},
		{"infinite title weight", func(c *Config) { c.Search.TitleWeight = math.Inf(1) }, "search.title_weight"},
		{"every weight zero", func(c *Config) {
			c.Search.BM25Weight, c.Search.VectorWeight, c.Search.FieldsWeight, c.Search.CardsWeight = 0, 0, 0, 0
			c.Search.TitleWeight = 0
		}, "search.bm25_weight, vector_weight, fields_weight, cards_weight and title_weight must not all be 0"},
		{"zero embed timeout", func(c *Config) { c.Search.EmbedTimeoutSeconds = 0 }, "search.embed_timeout_seconds"},
		{"zero understand timeout", func(c *Config) { c.Search.UnderstandTimeoutSeconds = 0 },
			"search.understand_timeout_seconds must be in [1, 23], got 0"},
		{"negative understand timeout", func(c *Config) { c.Search.UnderstandTimeoutSeconds = -1 },
			"search.understand_timeout_seconds must be in [1, 23], got -1"},
		{"an understand timeout past the CLI's budget alone", func(c *Config) { c.Search.UnderstandTimeoutSeconds = 24 },
			"search.understand_timeout_seconds must be in [1, 23], got 24"},
		{"zero keep-alive", func(c *Config) { c.Search.UnderstandKeepAliveMinutes = 0 },
			"search.understand_keep_alive_minutes must be in [1, 1440], got 0"},
		{"a keep-alive past a day", func(c *Config) { c.Search.UnderstandKeepAliveMinutes = 1441 },
			"search.understand_keep_alive_minutes must be in [1, 1440], got 1441"},
		{"NaN rewrite weight", func(c *Config) { c.Search.RewriteWeight = math.NaN() },
			"search.rewrite_weight must be a finite number >= 0, got NaN"},
		{"negative rewrite weight", func(c *Config) { c.Search.RewriteWeight = -0.5 }, "search.rewrite_weight"},
		{"infinite rewrite weight", func(c *Config) { c.Search.RewriteWeight = math.Inf(1) }, "search.rewrite_weight"},
		{"NaN name weight", func(c *Config) { c.Search.NameWeight = math.NaN() }, "search.name_weight"},
		{"negative name weight", func(c *Config) { c.Search.NameWeight = -1 }, "search.name_weight"},
		{"infinite name weight", func(c *Config) { c.Search.NameWeight = math.Inf(1) },
			"search.name_weight must be a finite number >= 0, got +Inf"},
		{"zero rrf_k", func(c *Config) { c.Search.RRFK = 0 }, "search.rrf_k"},
		{"bad collapse", func(c *Config) { c.Search.Collapse = "average" }, "search.collapse"},
		{"zero web2md timeout", func(c *Config) { c.Fetcher.Web2MD.TimeoutSeconds = 0 }, "web2md.timeout_seconds"},
		{"zero Jina requests a site", func(c *Config) { c.Fetcher.Native.JinaSiteRequestsPerMinute = 0 },
			"fetcher.native.jina_site_requests_per_minute must be at least 1, got 0"},
		{"negative Jina requests a site", func(c *Config) { c.Fetcher.Native.JinaSiteRequestsPerMinute = -3 },
			"fetcher.native.jina_site_requests_per_minute"},
		{"zero labeling timeout", func(c *Config) { c.Insight.LabelingTimeoutSeconds = 0 }, "insight.labeling_timeout_seconds"},
		{"empty generation base_url", func(c *Config) { c.Generation.BaseURL = "" }, "generation.base_url"},
		{"web2md without a bin", func(c *Config) { c.Fetcher.Default, c.Fetcher.Web2MD.Bin = "web2md", "" },
			"fetcher.web2md.bin must be set when fetcher.default is web2md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestValidate_BaseURLs: an Ollama address the daemon's client would
// refuse fails the config, for both keys, so a daemon never gets as far as
// binding its port and migrating with one.
func TestValidate_BaseURLs(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"http://localhost:11434", true},
		{"https://ollama.example", true},
		{"http://127.0.0.1:11434/", true},
		{"localhost:11434", false},
		{"127.0.0.1:11434", false},
		{"ftp://x", false},
		{"http://", false},
		{"", false},
	}
	for _, tc := range cases {
		for _, key := range []string{"embedding.base_url", "generation.base_url"} {
			t.Run(key+"="+tc.url, func(t *testing.T) {
				cfg := Default()
				if key == "embedding.base_url" {
					cfg.Embedding.BaseURL = tc.url
				} else {
					cfg.Generation.BaseURL = tc.url
				}
				err := cfg.Validate()
				if tc.ok {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				assert.Contains(t, err.Error(), key)
			})
		}
	}
}

func TestValidate_DefaultPasses(t *testing.T) {
	assert.NoError(t, Default().Validate())
}

// TestValidate_AnyIndexableWidth: embedding.dim is the home's width, not a
// constant, so any width the vector index takes is valid.
func TestValidate_AnyIndexableWidth(t *testing.T) {
	for _, dim := range []int{1, 384, 768, 1024, 4096, 8192} {
		t.Run(strconv.Itoa(dim), func(t *testing.T) {
			cfg := Default()
			cfg.Embedding.Dim = dim
			assert.NoError(t, cfg.Validate())
		})
	}
}

// TestDefault_Models pins the models a new home gets and Qwen3-Embedding's
// prompts: no document prefix, and the query instruction exactly as the
// model card's get_detailed_instruct writes it, a real newline and nothing
// after "Query:".
func TestDefault_Models(t *testing.T) {
	emb := Default().Embedding
	assert.Equal(t, "qwen3-embedding:0.6b", emb.Model)
	assert.Equal(t, 1024, emb.Dim)
	assert.Empty(t, emb.DocumentPrefix)
	assert.Equal(t, []byte("Instruct: Given a web search query, retrieve relevant passages that answer the query"+
		"\nQuery:"), []byte(emb.QueryPrefix))
	assert.Equal(t, "qwen3:4b-instruct", Default().Generation.Model)
}

// TestLoad_QueryPrefixFromYAML: the prefix written as a double-quoted YAML
// string, with its \n escape, loads as the same bytes as the default.
func TestLoad_QueryPrefixFromYAML(t *testing.T) {
	got, err := Load(writeConfig(t, `embedding:
  query_prefix: "Instruct: Given a web search query, retrieve relevant passages that answer the query\nQuery:"
`))
	require.NoError(t, err)
	assert.Equal(t, []byte(Default().Embedding.QueryPrefix), []byte(got.Embedding.QueryPrefix))
}

func TestValidate_DoesNotMutate(t *testing.T) {
	cfg := Default()
	cfg.Daemon.Workers = 8
	before := cfg
	require.NoError(t, cfg.Validate())
	assert.Equal(t, before, cfg)
}

func TestValidate_Listen(t *testing.T) {
	cases := []struct {
		listen  string
		wantErr string // empty = valid
	}{
		{"127.0.0.1:8765", ""},
		{"127.0.0.2:8765", ""},
		{"localhost:8765", ""},
		{"LOCALHOST:8765", ""},
		{"[::1]:8765", ""},
		{":8765", "must stay on loopback"},
		{"0.0.0.0:8765", "must stay on loopback"},
		{"[::]:8765", "must stay on loopback"},
		{"192.168.1.10:8765", "must stay on loopback"},
		{"example.com:8765", "must stay on loopback"},
		{"127.0.0.1:0", "port must be a fixed number"},
		{"127.0.0.1:65536", "port must be a fixed number"},
		{"127.0.0.1:http", "port must be a fixed number"},
		{"127.0.0.1", "must be host:port"},
	}
	for _, tc := range cases {
		t.Run(tc.listen, func(t *testing.T) {
			cfg := Default()
			cfg.Daemon.Listen = tc.listen
			err := cfg.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "daemon.listen")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoad_RejectsNonLoopbackListen(t *testing.T) {
	for _, listen := range []string{":8765", "0.0.0.0:8765", "192.168.1.10:8765", "example.com:8765", "127.0.0.1:0"} {
		t.Run(listen, func(t *testing.T) {
			_, err := Load(writeConfig(t, "daemon:\n  listen: \""+listen+"\"\n"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "daemon.listen")
		})
	}
}

func TestLoad_LegacyWorkers(t *testing.T) {
	cases := []struct {
		name      string
		yaml      string
		wantFetch int
		wantIndex int
		wantErr   string
	}{
		{name: "workers alone is split 75/25", yaml: "daemon:\n  workers: 8\n", wantFetch: 6, wantIndex: 2},
		{name: "small workers keeps both pools non-empty", yaml: "daemon:\n  workers: 1\n", wantFetch: 1, wantIndex: 1},
		{name: "new fields alone", yaml: "daemon:\n  fetch_workers: 3\n  index_workers: 2\n", wantFetch: 3, wantIndex: 2},
		{
			name:    "workers combined with the new fields",
			yaml:    "daemon:\n  workers: 8\n  fetch_workers: 0\n  index_workers: 0\n",
			wantErr: "daemon.workers is deprecated",
		},
		{
			name:    "workers combined with one new field",
			yaml:    "daemon:\n  workers: 8\n  index_workers: 2\n",
			wantErr: "daemon.workers is deprecated",
		},
		{name: "negative workers", yaml: "daemon:\n  workers: -2\n", wantErr: "daemon.workers must be positive"},
		{name: "zero workers", yaml: "daemon:\n  workers: 0\n", wantErr: "daemon.workers must be positive"},
		{name: "explicit zero fetch workers", yaml: "daemon:\n  fetch_workers: 0\n", wantErr: "daemon.fetch_workers must be positive"},
		{name: "explicit zero index workers", yaml: "daemon:\n  index_workers: 0\n", wantErr: "daemon.index_workers must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Load(writeConfig(t, tc.yaml))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantFetch, got.Daemon.FetchWorkers)
			assert.Equal(t, tc.wantIndex, got.Daemon.IndexWorkers)
			assert.Zero(t, got.Daemon.Workers, "the legacy field is folded into the split pools")
		})
	}
}

func TestLoad_StrictKeys(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantKey string
	}{
		{name: "top-level typo", yaml: "embeding:\n  model: x\n", wantKey: "embeding"},
		{name: "nested typo", yaml: "fetcher:\n  native:\n    timeout_secs: 5\n", wantKey: "timeout_secs"},
		{name: "jina key typo", yaml: "fetcher:\n  native:\n    jina_key: x\n", wantKey: "jina_key"},
		{name: "jina site pace typo", yaml: "fetcher:\n  native:\n    jina_requests_per_site: 3\n",
			wantKey: "jina_requests_per_site"},
		{name: "ui key typo", yaml: "ui:\n  load_images: true\n", wantKey: "load_images"},
		{name: "insight key typo", yaml: "insight:\n  maps: false\n", wantKey: "maps"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.yaml)
			_, err := Load(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantKey)
			assert.Contains(t, err.Error(), path)
		})
	}
}

// TestLoad_UI: the dashboard is on and shows no remote images unless
// config.yaml says otherwise.
func TestLoad_UI(t *testing.T) {
	d := Default()
	assert.True(t, d.Daemon.UI, "the pages are on by default")
	assert.False(t, d.UI.LoadRemoteImages, "remote images are off by default")

	got, err := Load(writeConfig(t, "daemon:\n  ui: false\nui:\n  load_remote_images: true\n"))
	require.NoError(t, err)
	assert.False(t, got.Daemon.UI)
	assert.True(t, got.UI.LoadRemoteImages)
}

// TestLoad_InsightMap: the interest map is on unless config.yaml turns it
// off; a value that isn't a bool is a parse error.
func TestLoad_InsightMap(t *testing.T) {
	assert.True(t, Default().Insight.Map, "on by default")
	got, err := Load(writeConfig(t, "insight:\n  labeling: terms\n"))
	require.NoError(t, err)
	assert.True(t, got.Insight.Map, "a config.yaml that doesn't name it keeps it on")
	got, err = Load(writeConfig(t, "insight:\n  map: false\n"))
	require.NoError(t, err)
	assert.False(t, got.Insight.Map)
	assert.Empty(t, got.DeprecatedKeys())
	require.NoError(t, got.Validate())

	_, err = Load(writeConfig(t, "insight:\n  map: sometimes\n"))
	require.ErrorContains(t, err, "cannot unmarshal !!str `sometimes` into bool")
}

func TestLoad_CommentOnlyFile_ReturnsDefaults(t *testing.T) {
	got, err := Load(writeConfig(t, "# all defaults\n# daemon:\n#   listen: 127.0.0.1:9000\n"))
	require.NoError(t, err)
	assert.Equal(t, Default(), got)
}

func TestDaemon_SlogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, want, Daemon{LogLevel: name}.SlogLevel())
		})
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

// TestLoad_DeprecatedInsightKeys: the insight keys nothing reads any more
// still load, whatever their value, including those validation used to
// refuse, and the loaded config names the ones the file sets; a value of
// the wrong type is still a parse error.
func TestLoad_DeprecatedInsightKeys(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want []string
	}{
		{"insight:\n  knn: 0\n  min_similarity: .nan\n  min_cluster_size: -1\n",
			[]string{"insight.knn", "insight.min_similarity", "insight.min_cluster_size"}},
		{"insight:\n  min_cluster_size: 3\n  min_similarity: 1.5\n",
			[]string{"insight.min_similarity", "insight.min_cluster_size"}},
		{"insight:\n  knn:\n", []string{"insight.knn"}},
		{"insight:\n  labeling: terms\n", nil},
		{"", nil},
	} {
		got, err := Load(writeConfig(t, tc.yaml))
		require.NoError(t, err, tc.yaml)
		assert.Equal(t, tc.want, got.DeprecatedKeys(), tc.yaml)
	}
	assert.Empty(t, Default().DeprecatedKeys())
	d := Default().Insight
	assert.Zero(t, d.KNN+d.MinClusterSize, "the defaults set none of them")
	assert.Zero(t, d.MinSimilarity)

	_, err := Load(writeConfig(t, "insight:\n  knn: abc\n"))
	require.ErrorContains(t, err, "cannot unmarshal !!str `abc` into int")
}

func TestValidate_OneZeroWeightIsAllowed(t *testing.T) {
	cfg := Default()
	cfg.Search.BM25Weight = 0 // vector-only ranking
	assert.NoError(t, cfg.Validate())
	for _, only := range []func(*Search){
		func(s *Search) { s.BM25Weight = 1 },
		func(s *Search) { s.VectorWeight = 1 },
		func(s *Search) { s.FieldsWeight = 1 },
		func(s *Search) { s.CardsWeight = 1 },
		func(s *Search) { s.TitleWeight = 1 },
	} {
		cfg := Default()
		cfg.Search.BM25Weight, cfg.Search.VectorWeight, cfg.Search.FieldsWeight, cfg.Search.CardsWeight = 0, 0, 0, 0
		cfg.Search.TitleWeight = 0
		only(&cfg.Search)
		assert.NoError(t, cfg.Validate(), "any one list alone ranks: %+v", cfg.Search)
	}
}

// TestDefault_Roles: without generation.roles, every role writes with
// generation's own settings, and both are in use.
func TestDefault_Roles(t *testing.T) {
	d := Default()
	assert.Nil(t, d.Generation.Roles)
	want := ModelSpec{Provider: "ollama", Model: "qwen3:4b-instruct", BaseURL: "http://localhost:11434",
		TimeoutSeconds: 120}
	assert.Equal(t, want, d.Generation.Role(RoleLabels))
	assert.Equal(t, want, d.Generation.Role(RolePlanner))
	assert.Equal(t, []Role{RoleLabels, RolePlanner}, d.RolesInUse())
	assert.Equal(t, []Role{RoleLabels, RolePlanner}, Roles())
	assert.Equal(t, "interest labels", RoleLabels.UsedFor())
	assert.Equal(t, "search understanding", RolePlanner.UsedFor())
	assert.Empty(t, Role("lables").UsedFor())
}

// TestDefault_Understanding: search understands questions by default,
// within 5 s a plan, its rewrites at half the question's weight and its
// names at the question's.
func TestDefault_Understanding(t *testing.T) {
	s := Default().Search
	assert.True(t, s.Understand)
	assert.Equal(t, 5, s.UnderstandTimeoutSeconds)
	assert.Equal(t, 30, s.UnderstandKeepAliveMinutes)
	assert.Equal(t, 0.5, s.RewriteWeight)
	assert.Equal(t, 1.0, s.NameWeight)
}

// TestLoad_Understanding: the keys load, and zero weights leave a plan's
// rewrites and names out, which Validate takes.
func TestLoad_Understanding(t *testing.T) {
	got, err := Load(writeConfig(t, "search:\n  understand: false\n  understand_timeout_seconds: 8\n"+
		"  understand_keep_alive_minutes: 5\n  rewrite_weight: 0\n  name_weight: 0\n"))
	require.NoError(t, err)
	assert.False(t, got.Search.Understand)
	assert.Equal(t, 8, got.Search.UnderstandTimeoutSeconds)
	assert.Equal(t, 5, got.Search.UnderstandKeepAliveMinutes)
	assert.Zero(t, got.Search.RewriteWeight)
	assert.Zero(t, got.Search.NameWeight)
}

// TestLoad_A26EmbedTimeout: a config.yaml 2.6 took is taken, understanding
// on by default: an embed timeout that leaves a plan little or no room
// within the CLI's 30 s is the planner's to deal with (it shortens its
// timeout, or understands nothing), never a refused daemon start.
func TestLoad_A26EmbedTimeout(t *testing.T) {
	for _, embed := range []int{19, 20, 29} {
		got, err := Load(writeConfig(t, fmt.Sprintf("search:\n  embed_timeout_seconds: %d\n", embed)))
		require.NoError(t, err, "embed_timeout_seconds: %d", embed)
		assert.True(t, got.Search.Understand)
		assert.Equal(t, embed, got.Search.EmbedTimeoutSeconds)
	}
}

// TestPullsAlone: the daemon pulls the labels' model on its own, and the
// planner's only with another role's pull of the same model.
func TestPullsAlone(t *testing.T) {
	assert.True(t, RoleLabels.PullsAlone())
	assert.False(t, RolePlanner.PullsAlone())
	assert.False(t, Role("lables").PullsAlone())
}

// TestSharesModel: a role shares its model when another role in use
// writes with the same provider, model and server, a trailing slash
// aside.
func TestSharesModel(t *testing.T) {
	cfg := Default()
	assert.True(t, cfg.SharesModel(RolePlanner), "both write with generation.model")
	assert.True(t, cfg.SharesModel(RoleLabels))

	cfg.Generation.Roles = map[Role]ModelSpec{RoleLabels: {BaseURL: cfg.Generation.BaseURL + "/"}}
	assert.True(t, cfg.SharesModel(RolePlanner), "the same server")

	cfg.Generation.Roles = map[Role]ModelSpec{RolePlanner: {Model: "gemma4:26b"}}
	assert.False(t, cfg.SharesModel(RolePlanner), "a model of its own")

	cfg.Generation.Roles = map[Role]ModelSpec{RolePlanner: {BaseURL: "http://127.0.0.1:11500"}}
	assert.False(t, cfg.SharesModel(RolePlanner), "another server")

	cfg = Default()
	cfg.Insight.Labeling = "terms"
	assert.False(t, cfg.SharesModel(RolePlanner), "labels not in use")
}

// TestOnThisMachine: a model server on loopback, at the unspecified
// address (which connects to this machine), or the provider's default, is
// on this machine; any other host isn't.
func TestOnThisMachine(t *testing.T) {
	for url, want := range map[string]bool{
		"":                          true,
		"http://localhost:11434":    true,
		"http://LOCALHOST:11434":    true,
		"http://127.0.0.1:11434":    true,
		"http://127.0.0.2:11434/":   true,
		"http://[::1]:11434":        true,
		"http://0.0.0.0:11434":      true,
		"http://[::]:11434":         true,
		"http://gpu-box.lan:11434":  false,
		"https://api.example.com":   false,
		"http://192.168.1.20:11434": false,
		"http://localhost.example":  false,
		"://bad":                    false,
	} {
		assert.Equal(t, want, OnThisMachine(url), url)
	}
}

// TestLoad_Roles: a role inherits every field it leaves out, or sets to 0
// or "", from generation, whose own model stays as set.
func TestLoad_Roles(t *testing.T) {
	got, err := Load(writeConfig(t, `generation:
  model: gemma4:26b
  base_url: http://127.0.0.1:11500
  timeout_seconds: 300
  roles:
    labels:
      model: qwen3:4b-instruct
      timeout_seconds: 0
`))
	require.NoError(t, err)
	assert.Equal(t, "gemma4:26b", got.Generation.Model)
	assert.Equal(t, ModelSpec{Provider: "ollama", Model: "qwen3:4b-instruct", BaseURL: "http://127.0.0.1:11500",
		TimeoutSeconds: 300}, got.Generation.Role(RoleLabels))

	got, err = Load(writeConfig(t, "generation:\n  roles:\n    labels:\n      base_url: http://127.0.0.1:11600\n"+
		"      timeout_seconds: 30\n"))
	require.NoError(t, err)
	assert.Equal(t, ModelSpec{Provider: "ollama", Model: "qwen3:4b-instruct", BaseURL: "http://127.0.0.1:11600",
		TimeoutSeconds: 30}, got.Generation.Role(RoleLabels))

	got, err = Load(writeConfig(t, "generation:\n  model: gemma4:12b\n  roles:\n    planner:\n      model: gemma4:26b\n"))
	require.NoError(t, err)
	assert.Equal(t, "gemma4:26b", got.Generation.Role(RolePlanner).Model)
	assert.Equal(t, "gemma4:12b", got.Generation.Role(RoleLabels).Model, "one role's model is its own")
}

// TestRolesInUse: labels is in use only while insight is on and labels
// with an LLM, and the planner only while search understands questions,
// whatever insight says.
func TestRolesInUse(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		label      string
		understand bool
		want       []Role
	}{
		{"llm", true, "llm", true, []Role{RoleLabels, RolePlanner}},
		{"llm, no understanding", true, "llm", false, []Role{RoleLabels}},
		{"insight off", false, "llm", true, []Role{RolePlanner}},
		{"term labels", true, "terms", true, []Role{RolePlanner}},
		{"no labels, no understanding", true, "off", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Insight.Enabled, cfg.Insight.Labeling = tc.enabled, tc.label
			cfg.Search.Understand = tc.understand
			assert.Equal(t, tc.want, cfg.RolesInUse())
		})
	}
}

// TestLoad_RoleErrors: a role Roles doesn't list, a setting of a role's
// the daemon would refuse, and a key a role doesn't have each fail the
// load, naming the key.
func TestLoad_RoleErrors(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"an unknown role", "generation:\n  roles:\n    lables:\n      model: m:1\n",
			`generation.roles: unknown role "lables"; the roles are: labels, planner`},
		{"unknown roles, the first in order", "generation:\n  roles:\n    zeta: {}\n    alpha: {}\n",
			`unknown role "alpha"`},
		{"another provider", "generation:\n  roles:\n    labels:\n      provider: openai\n",
			`generation.roles.labels.provider "openai" is not supported; the only provider is "ollama"`},
		{"a base_url the client refuses", "generation:\n  roles:\n    labels:\n      base_url: ftp://x\n",
			"generation.roles.labels.base_url: "},
		{"a negative timeout", "generation:\n  roles:\n    labels:\n      timeout_seconds: -1\n",
			"generation.roles.labels.timeout_seconds must be positive, got -1"},
		{"an unknown key", "generation:\n  roles:\n    labels:\n      models: m:1\n", "field models not found"},
		{"the planner's checked too", "generation:\n  roles:\n    planner:\n      timeout_seconds: -2\n",
			"generation.roles.planner.timeout_seconds must be positive, got -2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestValidate_GenerationMessages: generation's own errors are exactly
// these messages, whole, though its roles go through the same checks.
func TestValidate_GenerationMessages(t *testing.T) {
	for _, tc := range []struct {
		mutate func(*Generation)
		want   string
	}{
		{func(g *Generation) { g.Provider = "openai" }, `generation.provider "openai" is not supported; the only provider is "ollama"`},
		{func(g *Generation) { g.Model = "" }, "generation.model must not be empty"},
		{func(g *Generation) { g.BaseURL = "" }, "generation.base_url must not be empty"},
		{func(g *Generation) { g.TimeoutSeconds = 0 }, "generation.timeout_seconds must be positive, got 0"},
	} {
		cfg := Default()
		tc.mutate(&cfg.Generation)
		assert.EqualError(t, cfg.Validate(), tc.want)
	}
}
