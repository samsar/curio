package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_Usage(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
		says string
	}{
		{"no -db", []string{"-queries", "q.yaml"}, exitUsage, "-db is required"},
		{"no -queries", []string{"-db", "x.db"}, exitUsage, "-queries is required"},
		{"an unknown flag", []string{"-db", "x.db", "-queries", "q.yaml", "-k", "5"}, exitUsage,
			"flag provided but not defined"},
		{"an unknown variant", []string{"-db", "x.db", "-queries", "q.yaml", "-variants", "hybrid,rerank"}, exitUsage,
			`unknown variant "rerank" in -variants (the variants are: hybrid, bm25, vector)`},
		{"a variant twice", []string{"-db", "x.db", "-queries", "q.yaml", "-variants", "bm25,hybrid,bm25"}, exitUsage,
			`variant "bm25" named twice`},
		{"no variant", []string{"-db", "x.db", "-queries", "q.yaml", "-variants", ""}, exitUsage, `unknown variant ""`},
		{"an argument", []string{"-db", "x.db", "-queries", "q.yaml", "extra"}, exitUsage, `unexpected argument "extra"`},
		{"help", []string{"-h"}, exitOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runEval(t, tc.args...)
			assert.Equal(t, tc.code, res.code)
			assert.Empty(t, res.stdout)
			assert.Contains(t, res.stderr, tc.says)
			assert.Contains(t, res.stderr, "usage: searcheval -db <copy of curio.db> -queries <file> "+
				"[-config <config.yaml>] [-json <file>] [-baseline <previous -json>] [-variants hybrid,bm25,vector]")
		})
	}
}

// closedURL is the address of a port nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return "http://" + addr
}

// TestRun_Refusals: each cause stops the run with a message naming it,
// exit 1, and no JSON report.
func TestRun_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, fx *fixture) []string
		says  []string
	}{
		{"a home's own database", func(t *testing.T, fx *fixture) []string {
			require.NoError(t, os.WriteFile(filepath.Join(fx.dir, "daemon.pid"), []byte("4242\n"), 0o600))
			return fx.args(queries)
		}, []string{"daemon.pid", `sqlite3 -readonly`, ".backup <copy>"}},
		{"a -config that isn't there", func(_ *testing.T, fx *fixture) []string {
			return []string{"-db", fx.db, "-queries", queries, "-config", filepath.Join(fx.dir, "nope.yaml")}
		}, []string{"the -config file", "no such file"}},
		{"a config with an unknown key", func(t *testing.T, fx *fixture) []string {
			fx.config = writeConfig(t, fx.dir, fx.ollama.url, "search:\n  rrf: 60\n")
			return fx.args(queries)
		}, []string{"field rrf not found"}},
		{"a config of another width", func(t *testing.T, fx *fixture) []string {
			path := filepath.Join(fx.dir, "wide.yaml")
			require.NoError(t, os.WriteFile(path, []byte("embedding:\n  model: "+testModel+"\n  dim: 16\n  base_url: "+
				fx.ollama.url+"\n"), 0o600))
			return []string{"-db", fx.db, "-queries", queries, "-config", path}
		}, []string{"the copy's vectors are 8 wide", "embedding.dim 16", "wide.yaml"}},
		{"an Ollama without the model", func(_ *testing.T, fx *fixture) []string {
			fx.ollama.withModels("other-model:1")
			return fx.args(queries)
		}, []string{"doesn't have " + testModel, "never pulls a model"}},
		{"no Ollama", func(t *testing.T, fx *fixture) []string {
			fx.config = writeConfig(t, fx.dir, closedURL(t), "")
			return fx.args(queries)
		}, []string{"no Ollama answers at http://127.0.0.1:", testModel}},
		{"an embed failing mid-run", func(_ *testing.T, fx *fixture) []string {
			fx.ollama.failOn("gamma")
			return fx.args(queries)
		}, []string{"variant hybrid, query q-gamma", "keyword-only", "semantic search unavailable",
			"model runner has unexpectedly stopped"}},
		{"a query file with a mistake", func(t *testing.T, fx *fixture) []string {
			path := filepath.Join(fx.dir, "typo.yaml")
			require.NoError(t, os.WriteFile(path, []byte("queries:\n  - id: a\n    tag: [x]\n    query: q\n"+
				"    relevant: [https://example.com/doc/01]\n"), 0o600))
			return fx.args(path)
		}, []string{"typo.yaml", "query 0", `unknown key "tag"`}},
		{"a query file that isn't there", func(_ *testing.T, fx *fixture) []string {
			return fx.args(filepath.Join(fx.dir, "none.yaml"))
		}, []string{"the query file", "no such file"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			out := filepath.Join(fx.dir, "report.json")
			res := runEval(t, append(tc.setup(t, fx), "-json", out)...)
			assert.Equal(t, exitFailure, res.code)
			assert.Empty(t, res.stdout)
			for _, s := range tc.says {
				assert.Contains(t, res.stderr, s)
			}
			assert.NoFileExists(t, out)
		})
	}
}

// TestRun_RefusesAnUnusableJSONPath: a -json path the report couldn't be
// written to is refused before the copy is opened.
func TestRun_RefusesAnUnusableJSONPath(t *testing.T) {
	fx := newFixture(t)
	res := runEval(t, fx.args(queries, "-json", filepath.Join(fx.dir, "missing", "r.json"))...)
	assert.Equal(t, exitFailure, res.code)
	assert.Contains(t, res.stderr, "the -json file's directory")
	assert.Empty(t, fx.ollama.embeds, "nothing searched")
}
