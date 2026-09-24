package config

import (
	"log/slog"
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
  listen: "127.0.0.1:9999"
embedding:
  model: "mxbai-embed-large"
`)
	got, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:9999", got.Daemon.Listen)
	assert.Equal(t, "info", got.Daemon.LogLevel, "untouched field keeps default")
	assert.Equal(t, "mxbai-embed-large", got.Embedding.Model)
	assert.Equal(t, 768, got.Embedding.Dim, "untouched field keeps default")
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
	assert.Equal(t, "http://192.168.1.20:11434", got.Embedding.BaseURL)
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
		{"zero fetch workers", func(c *Config) { c.Daemon.FetchWorkers = 0 }, "daemon.fetch_workers"},
		{"zero index workers", func(c *Config) { c.Daemon.IndexWorkers = 0 }, "daemon.index_workers"},
		{"unknown embedding provider", func(c *Config) { c.Embedding.Provider = "voyage" }, "embedding.provider"},
		{"unknown generation provider", func(c *Config) { c.Generation.Provider = "openai" }, "generation.provider"},
		{"empty model", func(c *Config) { c.Embedding.Model = "" }, "embedding.model"},
		{"zero dim", func(c *Config) { c.Embedding.Dim = 0 }, "embedding.dim"},
		{"negative dim", func(c *Config) { c.Embedding.Dim = -1 }, "embedding.dim"},
		{"dim other than the schema's", func(c *Config) { c.Embedding.Dim = 1024 }, "Embedding model swap"},
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
