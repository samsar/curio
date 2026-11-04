package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/summarize"
)

// writeSummariesConfig writes a config.yaml whose summaries role writes
// with planModel at the fake, its search the chunk lists and the summary
// list at weight 1.
func writeSummariesConfig(t *testing.T, dir, baseURL string) string {
	t.Helper()
	path := filepath.Join(dir, "summaries.yaml")
	cfg := fmt.Sprintf("embedding:\n  model: %s\n  dim: %d\n  base_url: %s\n  query_prefix: \"\"\n"+
		"generation:\n  model: %s\n  base_url: %s\n"+
		"summaries:\n  enabled: false\n"+
		"search:\n  fields_weight: 0\n  cards_weight: 0\n  title_weight: 0\n  summary_weight: 1\n",
		testModel, testDim, baseURL, planModel, baseURL)
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o600))
	return path
}

// madeUpSummary answers every page with one summary its text grounds, but
// note 01's, which names a kind there isn't. It answers the fitness
// check's probes wrong too: the check is reported, never a gate.
func madeUpSummary(prompt string) string {
	if strings.Contains(prompt, "note 01 about") {
		return `{"kind":"essay","site":"","author":"","about":"x","entities":[],"claims":[],"descriptions":[]}`
	}
	return `{"kind":"article","site":"","author":"","about":"Notes about things.","entities":["things","Atlantis"],` +
		`"claims":[],"descriptions":["a made-up note"]}`
}

// copyFile copies src to dst.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	require.NoError(t, err)
	defer in.Close()
	out, err := os.Create(dst)
	require.NoError(t, err)
	_, err = io.Copy(out, in)
	require.NoError(t, err)
	require.NoError(t, out.Close())
}

// readSummaryRecords reads a -summaries-out file.
func readSummaryRecords(t *testing.T, path string) []summaryRecord {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var out []summaryRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r summaryRecord
		require.NoError(t, json.Unmarshal(sc.Bytes(), &r))
		out = append(out, r)
	}
	require.NoError(t, sc.Err())
	return out
}

// TestRun_Summarize: -summarize writes the summaries the pass would of
// the listed documents, one call each, whatever summaries.enabled and the
// check say; a page's failure is counted by its rule, recorded as the
// pass's, and the run goes on; each summary is appended to -summaries-out
// with its cost; a rerun writes only what is left; and a replay into a
// copy taken before stores them asking no model.
func TestRun_Summarize(t *testing.T) {
	fx := newFixture(t)
	fx.ollama.withModels(testModel, planModel)
	fx.ollama.summary = madeUpSummary
	fx.config = writeSummariesConfig(t, fx.dir, fx.ollama.url)
	pristine := filepath.Join(fx.dir, "pristine.db")
	copyFile(t, fx.db, pristine)
	ids := filepath.Join(fx.dir, "sample.txt")
	// doc-22 is failed: the pass writes no summary of it.
	require.NoError(t, os.WriteFile(ids, []byte("# a sample\ndoc-00\n\ndoc-01\ndoc-02\ndoc-22\n"), 0o600))
	out, jsonPath := filepath.Join(fx.dir, "summaries.jsonl"), filepath.Join(fx.dir, "report.json")

	res := runEval(t, fx.args(queries, "-variants", "hybrid,summaries", "-summarize", "-content", fx.content,
		"-documents", ids, "-summaries-out", out, "-json", jsonPath)...)
	require.Equal(t, exitOK, res.code, res.stderr)
	rep := readReport(t, jsonPath)
	s := rep.Summaries
	require.NotNil(t, s)
	assert.Equal(t, "write", s.Mode)
	assert.Equal(t, planModel, s.Model)
	assert.Equal(t, summarize.PromptVersion, s.PromptVersion)
	assert.True(t, strings.HasPrefix(s.Check, "failed:"), "the check is reported: %s", s.Check)
	assert.Equal(t, 3, s.ToWrite, "the listed documents the pass would write")
	assert.Equal(t, 2, s.Written)
	assert.Equal(t, map[string]int{"invalid:kind": 1}, s.Failed)
	assert.Equal(t, map[string]int{"article": 2}, s.Kinds)
	assert.Equal(t, 2, s.EntitiesDropped, "Atlantis isn't on the pages")
	assert.Equal(t, intPercent{P50: 900, P95: 900}, s.PromptTokens)
	assert.Contains(t, res.stdout, "Summaries: 2 of 3 written with "+planModel)
	assert.Contains(t, res.stdout, "failed: invalid:kind 1")
	assert.NotContains(t, res.stdout, "Notes about things", "the report names no summary's text")

	records := readSummaryRecords(t, out)
	require.Len(t, records, 3)
	written := 0
	for _, r := range records {
		if r.Status == recordWritten {
			written++
			assert.Equal(t, []string{"things"}, r.Entities)
			assert.Equal(t, 900, r.PromptTokens)
		}
	}
	assert.Equal(t, 2, written)
	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "it holds what pages say")

	asked := fx.ollama.summarized
	res = runEval(t, fx.args(queries, "-variants", "hybrid", "-summarize", "-content", fx.content,
		"-documents", ids, "-json", jsonPath)...)
	require.Equal(t, exitOK, res.code, res.stderr)
	again := readReport(t, jsonPath).Summaries
	assert.Zero(t, again.ToWrite, "a rerun writes only what is left: none, the page that failed being failed "+
		"for good within its wait, as the pass leaves it")
	assert.Equal(t, asked, fx.ollama.summarized, "nothing to write asks no model")

	// The replay, into a copy taken before: the same documents and
	// extractions, asking no model.
	fx.ollama.summary = nil
	asked = fx.ollama.summarized
	res = runEval(t, "-db", pristine, "-queries", queries, "-config", fx.config, "-variants", "hybrid,summaries",
		"-summaries", out, "-json", jsonPath)
	require.Equal(t, exitOK, res.code, res.stderr)
	replayed := readReport(t, jsonPath).Summaries
	assert.Equal(t, "replay", replayed.Mode)
	assert.Equal(t, 2, replayed.Replayed)
	assert.Zero(t, replayed.Mismatched)
	assert.Equal(t, planModel, replayed.Model)
	assert.Equal(t, asked, fx.ollama.summarized, "a replay asks no model")
}

// TestRun_SummariesReplayMismatch: a replayed summary whose document
// isn't at the extraction it was written from is counted, not stored.
func TestRun_SummariesReplayMismatch(t *testing.T) {
	fx := newFixture(t)
	fx.config = writeSummariesConfig(t, fx.dir, fx.ollama.url)
	out := filepath.Join(fx.dir, "summaries.jsonl")
	lines := `{"document_id":"doc-00","extraction_id":"another","status":"written","kind":"article",` +
		`"model":"m","prompt_version":"p","written_at":"2026-11-03T00:00:00Z","took_ms":1,"prompt_tokens":1,` +
		`"output_tokens":1,"entities_dropped":0}` + "\n" +
		`{"document_id":"nowhere","extraction_id":"x","status":"written","kind":"article","model":"m",` +
		`"prompt_version":"p","written_at":"2026-11-03T00:00:00Z","took_ms":1,"prompt_tokens":1,"output_tokens":1,` +
		`"entities_dropped":0}` + "\n"
	require.NoError(t, os.WriteFile(out, []byte(lines), 0o600))
	jsonPath := filepath.Join(fx.dir, "report.json")
	res := runEval(t, fx.args(queries, "-summaries", out, "-json", jsonPath)...)
	require.Equal(t, exitOK, res.code, res.stderr)
	s := readReport(t, jsonPath).Summaries
	assert.Equal(t, 2, s.Mismatched)
	assert.Zero(t, s.Replayed)

	require.NoError(t, os.WriteFile(out, []byte("{\"document_id\":\"doc-00\",\"surprise\":1}\n"), 0o600))
	res = runEval(t, fx.args(queries, "-summaries", out)...)
	assert.Equal(t, exitFailure, res.code)
	assert.Contains(t, res.stderr, "record 1")
}
