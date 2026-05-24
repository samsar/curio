package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
  listen: "0.0.0.0:9999"
embedding:
  model: "voyage-3"
  dim: 1024
`)
	got, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0:9999", got.Daemon.Listen)
	assert.Equal(t, "info", got.Daemon.LogLevel, "untouched field keeps default")
	assert.Equal(t, "voyage-3", got.Embedding.Model)
	assert.Equal(t, 1024, got.Embedding.Dim)
	assert.Equal(t, "ollama", got.Embedding.Provider, "untouched field keeps default")
	assert.Equal(t, Default().Chunking.SizeTokens, got.Chunking.SizeTokens, "untouched section keeps default")
}

func TestLoad_FullConfig(t *testing.T) {
	path := writeConfig(t, `
daemon:
  listen: "127.0.0.1:7000"
  log_level: "debug"
embedding:
  provider: "voyage"
  model: "voyage-3"
  dim: 1024
  base_url: "https://api.voyageai.com"
fetcher:
  web2md:
    bin: "/usr/local/bin/web2md"
    timeout_seconds: 60
search:
  default_k: 20
  rrf_k: 80
  bm25_weight: 0.5
  vector_weight: 1.5
  collapse: "top3_avg"
chunking:
  size_tokens: 1024
  overlap_tokens: 128
`)
	got, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:7000", got.Daemon.Listen)
	assert.Equal(t, "debug", got.Daemon.LogLevel)
	assert.Equal(t, "voyage", got.Embedding.Provider)
	assert.Equal(t, 1024, got.Embedding.Dim)
	assert.Equal(t, "/usr/local/bin/web2md", got.Fetcher.Web2MD.Bin)
	assert.Equal(t, 60, got.Fetcher.Web2MD.TimeoutSeconds)
	assert.Equal(t, 20, got.Search.DefaultK)
	assert.Equal(t, "top3_avg", got.Search.Collapse)
	assert.Equal(t, 1024, got.Chunking.SizeTokens)
	assert.Equal(t, 128, got.Chunking.OverlapTokens)
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
		{"empty model", func(c *Config) { c.Embedding.Model = "" }, "embedding.model"},
		{"zero dim", func(c *Config) { c.Embedding.Dim = 0 }, "embedding.dim"},
		{"negative dim", func(c *Config) { c.Embedding.Dim = -1 }, "embedding.dim"},
		{"empty base_url", func(c *Config) { c.Embedding.BaseURL = "" }, "embedding.base_url"},
		{"zero chunk size", func(c *Config) { c.Chunking.SizeTokens = 0 }, "chunking.size_tokens"},
		{"overlap >= size", func(c *Config) { c.Chunking.OverlapTokens = 512 }, "chunking.overlap_tokens"},
		{"negative overlap", func(c *Config) { c.Chunking.OverlapTokens = -1 }, "chunking.overlap_tokens"},
		{"zero default_k", func(c *Config) { c.Search.DefaultK = 0 }, "search.default_k"},
		{"zero rrf_k", func(c *Config) { c.Search.RRFK = 0 }, "search.rrf_k"},
		{"bad collapse", func(c *Config) { c.Search.Collapse = "average" }, "search.collapse"},
		{"zero web2md timeout", func(c *Config) { c.Fetcher.Web2MD.TimeoutSeconds = 0 }, "web2md.timeout_seconds"},
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

func TestValidate_DefaultPasses(t *testing.T) {
	assert.NoError(t, Default().Validate())
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}
