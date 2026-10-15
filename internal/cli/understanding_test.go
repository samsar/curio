package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/understand"
)

var update = flag.Bool("update", false, "record testdata/search-understanding.golden")

// TestRenderUnderstanding_Golden: the lines before the results, for a
// plan with and without rewrites and names, every status that searched the
// words, and understanding off, are byte for byte the golden: a plan's
// windows as days in the local zone (New York here), the end inclusive.
func TestRenderUnderstanding_Golden(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	summer := client.SearchFilters{ContentType: []string{"video"}, SavedAfter: "2018-06-01T00:00:00-04:00",
		SavedBefore: "2018-09-23T00:00:00-04:00"}
	cases := []struct {
		name string
		u    *client.Understanding
	}{
		{"planned", &client.Understanding{Status: "planned", Model: "gemma4:26b", TookMS: 1340,
			Plan: &client.Plan{Kind: "list", Filters: summer, Rewrites: []string{"raft consensus", "leader election"},
				Names: []string{"In Search of an Understandable Consensus Algorithm", "etcd"}}}},
		{"planned, filters alone", &client.Understanding{Status: "planned", Model: "gemma4:26b", TookMS: 980,
			Plan: &client.Plan{Kind: "list", Filters: client.SearchFilters{Source: []string{"safari"},
				SavedBefore: "2022-04-18T00:00:00-04:00"}}}},
		{"planned, names alone", &client.Understanding{Status: "planned", Model: "gemma4:26b", TookMS: 2050,
			Plan: &client.Plan{Kind: "one", Names: []string{"Attention Is All You Need"}}}},
		{"supplied", &client.Understanding{Status: "supplied", Plan: &client.Plan{Kind: "keywords",
			Filters: client.SearchFilters{PublishedAfter: "2019-01-01", PublishedBefore: "2020-01-01"}}}},
		{"cold", &client.Understanding{Status: "cold", Reason: "gemma4:26b is loading", Model: "gemma4:26b"}},
		{"checking", &client.Understanding{Status: "checking", Reason: "curio is checking gemma4:26b"}},
		{"unfit", &client.Understanding{Status: "unfit",
			Reason: `gemma4:26b didn't pass the check "a month and a type"; ` + unfitFix}},
		{"timeout", &client.Understanding{Status: "timeout", Reason: "understanding took longer than 5s",
			TookMS: 5004}},
		{"timeout, held", &client.Understanding{Status: "timeout",
			Reason: "3 plans in a row took longer than 5s; understanding resumes at 15:02"}},
		{"unavailable", &client.Understanding{Status: "unavailable", Reason: "ollama generate: ollama unreachable"}},
		{"unavailable, not pulled", &client.Understanding{Status: "unavailable",
			Reason: "gemma4:26b isn't pulled — run curio up"}},
		{"invalid", &client.Understanding{Status: "invalid", Reason: "the answer isn't JSON"}},
		{"too long", &client.Understanding{Status: "too_long",
			Reason: "the question is 600 bytes; curio understands questions of at most 512"}},
		{"off", &client.Understanding{Status: "off", Reason: "search understanding is off (search.understand: false)"}},
		{"not asked", nil},
	}
	var out bytes.Buffer
	for _, tc := range cases {
		fmt.Fprintf(&out, "== %s\n", tc.name)
		renderUnderstanding(&out, tc.u, ny)
	}
	path := filepath.Join("testdata", "search-understanding.golden")
	if *update {
		require.NoError(t, os.WriteFile(path, out.Bytes(), 0o600))
	}
	golden, err := os.ReadFile(path)
	require.NoError(t, err, "record %s with -update", path)
	assert.Equal(t, string(golden), out.String())
}

// TestUnfitFix: doctor's fix for an unfit model is the one the planner's
// reason ends with, which doctor's line leaves to the hint.
func TestUnfitFix(t *testing.T) {
	assert.Equal(t, understand.UnfitFix, unfitFix)
}

// TestDoctorAndStatus_Understanding: while search understanding is on and
// plans no question, status says why in one warning line, and doctor
// warns with the fix: questions withheld from the model, a model that
// failed its check, one Ollama keeps unloading, plans that keep timing
// out. A fit or unchecked model, or understanding off, warns of nothing.
func TestDoctorAndStatus_Understanding(t *testing.T) {
	at := time.Date(2026, 10, 14, 14, 2, 0, 0, time.UTC)
	until := time.Now().Add(10 * time.Minute)
	for _, tc := range []struct {
		name         string
		state        understand.State
		status       string // the status line, "" for none
		mark, doctor string
		hint         string
	}{
		{"unfit", understand.State{On: true, Model: genModel, Check: understand.CheckUnfit, CheckedAt: at,
			Unfit: genModel + ` didn't pass the check "no filter"; ` + unfitFix},
			"warning: search understanding searches every question's words: " + genModel + " failed its check; " +
				"run `curio doctor`\n",
			"!", genModel + ` didn't pass the check "no filter" (checked ` + localTime(at) + "); every search uses " +
				"its words", unfitFix},
		{"withheld", understand.State{On: true, Model: genModel, Check: understand.CheckUnchecked,
			Reason: "questions would leave this machine for http://gpu-box.lan:11434"},
			"warning: search understanding searches every question's words: questions would leave this machine " +
				"for http://gpu-box.lan:11434; run `curio doctor`\n",
			"!", "every search uses its words: questions would leave this machine for http://gpu-box.lan:11434", ""},
		{"evicted", understand.State{On: true, Model: genModel, Check: understand.CheckFit, CheckedAt: at,
			EvictionHoldUntil: until},
			"warning: search understanding searches words until " + localTime(until) + ": Ollama keeps unloading " +
				genModel + " for other models; run `curio doctor`\n",
			"!", "Ollama unloaded " + genModel + " within its keep-alive twice, for other models; curio doesn't " +
				"load it again until " + localTime(until) + ", and searches use their words meanwhile", evictFix},
		{"timing out", understand.State{On: true, Model: genModel, Check: understand.CheckFit, CheckedAt: at,
			TimeoutHoldUntil: until},
			"warning: search understanding searches words until " + localTime(until) + ": plans with " + genModel +
				" keep timing out; run `curio doctor`\n",
			"!", "plans with " + genModel + " kept taking longer than the timeout; searches use their words until " +
				localTime(until), slowFix},
		{"fit", understand.State{On: true, Model: genModel, Check: understand.CheckFit, CheckedAt: at}, "",
			"✓", genModel + " passed its check (" + localTime(at) + ")", ""},
		{"unchecked", understand.State{On: true, Model: genModel, Check: understand.CheckUnchecked}, "",
			"✓", genModel + " not checked yet: the first understood search loads and checks it", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner := apitest.NewPlanner(understand.Outcome{})
			planner.SetState(tc.state)
			w := upWorld(t, apitest.WithPlanner(planner))
			out, err := w.run(t, "status")
			require.NoError(t, err)
			if tc.status == "" {
				assert.NotContains(t, out, "search understanding")
			} else {
				assert.Contains(t, out, tc.status)
			}
			out, err = w.run(t, "doctor")
			require.NoError(t, err, out)
			line, hint := doctorLine(t, out, "understanding")
			assert.Equal(t, fmt.Sprintf("%s %-22s %s", tc.mark, "understanding", tc.doctor), line)
			assert.Equal(t, tc.hint, hint)
			assert.Empty(t, planner.Questions(), "status and doctor ask the planner nothing")
		})
	}

	off := upWorld(t)
	out, err := off.run(t, "doctor")
	require.NoError(t, err, out)
	line, _ := doctorLine(t, out, "understanding")
	assert.Equal(t, fmt.Sprintf("✓ %-22s off (search.understand: false)", "understanding"), line)
}
