package cli

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/summarize"
)

// TestSummariesWarning: status warns while summaries are on and no page
// gets one, the first cause that holds, and says nothing otherwise.
func TestSummariesWarning(t *testing.T) {
	until := time.Now().Add(15 * time.Minute)
	for name, tc := range map[string]struct {
		h    *client.SummariesHealth
		want string
	}{
		"an older daemon": {nil, ""},
		"off":             {&client.SummariesHealth{Reason: summarize.OffReason}, ""},
		"writing":         {&client.SummariesHealth{On: true, Model: "gemma4:26b", Check: "fit"}, ""},
		"withheld": {&client.SummariesHealth{On: true, Reason: "pages would leave this machine"},
			"warning: no page is summarized: pages would leave this machine; run `curio doctor`\n"},
		"unfit": {&client.SummariesHealth{On: true, Model: "m", Check: "unfit"},
			"warning: no page is summarized: m failed its check; run `curio doctor`\n"},
		"an outage": {&client.SummariesHealth{On: true, Outage: &client.SummaryOutage{Since: until,
			Error: "connection refused"}}, "warning: the summaries' model hasn't answered since " + localTime(until) +
			" (connection refused); pages wait for it; run `curio doctor`\n"},
		"the brake": {&client.SummariesHealth{On: true, Brake: &client.ReembedBrake{Until: until, Failures: 3,
			Error: "HTTP 500"}}, "warning: the summary pass waits until " + localTime(until) +
			": the summaries of 3 pages failed in a row (HTTP 500); run `curio doctor`\n"},
		"a check that reached no verdict": {&client.SummariesHealth{On: true, Model: "m", Check: "unchecked",
			CheckRetryAt: until, LastFailure: &client.SummaryWriterFailure{Cause: "check", At: until,
				Error: "probe \"an article\": HTTP 500"}},
			"warning: no page is summarized: m couldn't be checked (probe \"an article\": HTTP 500); checked again " +
				"at " + localTime(until) + "; run `curio doctor`\n"},
		"unchecked, its last failure another's": {&client.SummariesHealth{On: true, Model: "m", Check: "unchecked",
			LastFailure: &client.SummaryWriterFailure{Cause: "timeout", At: until}}, ""},
		"an eviction hold": {&client.SummariesHealth{On: true, Model: "m", Check: "fit", EvictionHoldUntil: until},
			"warning: the summary pass waits until " + localTime(until) + ": Ollama keeps unloading m for other " +
				"models; run `curio doctor`\n"},
	} {
		assert.Equal(t, tc.want, summariesWarning(tc.h), name)
	}
}

// TestSummariesLines: status's lines: written of the total, with the model,
// what is left and when it is done, then what is outdated, failed or not
// the page.
func TestSummariesLines(t *testing.T) {
	assert.Empty(t, summariesLines(nil, nil), "summaries off")
	assert.Empty(t, summariesLines(nil, &client.SummariesHealth{Model: "m", Reason: summarize.OffReason}),
		"off by config.yaml: nothing to say")
	assert.Equal(t, "summaries: off by default; summaries.enabled: true in config.yaml, then a restart (curio daemon "+
		"stop; any curio command starts it), turns them on\n",
		summariesLines(nil, &client.SummariesHealth{Model: "qwen3:4b-instruct", OffByDefault: true,
			Reason: summarize.OffByDefaultReason}))
	h := &client.SummariesHealth{On: true, Model: "gemma4:26b"}
	assert.Equal(t, "summaries: 1230 of 5309 written (gemma4:26b) · 4000 to write, about 6 h 20 min left at 10.5 a minute\n"+
		"           50 by another model or prompt: `curio summarize --outdated` writes them again\n"+
		"           29 failed for good (tried again in a week, when the page, model or prompt changes, or at once "+
		"with `curio summarize --all`)\n"+
		"           20 not the page saved (a sign-in wall, an error page)\n",
		summariesLines(&client.SummaryStats{Written: 1210, ToWrite: 4000, Outdated: 50, Failed: 29, NotThePage: 20,
			PerMinute: 10.5, ETASeconds: 22800}, h))
	assert.Equal(t, "summaries: 10 of 10 written (gemma4:26b)\n", summariesLines(&client.SummaryStats{Written: 10}, h))
}

// TestSummariesCheck: doctor's verdicts, each warning with its fix, and
// outdated summaries a note.
func TestSummariesCheck(t *testing.T) {
	on := client.SummariesHealth{On: true, Model: "gemma4:26b", Check: "fit"}
	stats := &client.SummaryStats{Written: 10, ToWrite: 5}
	status, detail, _ := summariesCheck(nil, client.SummariesHealth{Reason: summarize.OffReason}, false)
	assert.Equal(t, statusOK, status)
	assert.Equal(t, "off (summaries.enabled: false)", detail)

	status, detail, hint := summariesCheck(nil, client.SummariesHealth{Model: "gemma4:12b", OffByDefault: true},
		false)
	assert.Equal(t, statusOK, status)
	assert.Equal(t, "off by default", detail)
	assert.Equal(t, summarize.OnFix, hint)

	status, detail, hint = summariesCheck(stats, client.SummariesHealth{On: true, Model: "gemma4:12b",
		Check: "fit"}, true)
	assert.Equal(t, statusWarn, status, "true set with a model below the bars")
	assert.Equal(t, "on with gemma4:12b, which misses the summaries' measured bars: 6% of gemma4:12b's summaries "+
		"stated what their page doesn't", detail)
	assert.Equal(t, summariesBarsFix, hint)
	status, detail, hint = summariesCheck(stats, client.SummariesHealth{On: true, Model: "gemma4:12b",
		Check: "fit"}, false)
	assert.Equal(t, statusWarn, status, "a pass stopped below the bars")
	assert.Equal(t, "10 of 15 written with gemma4:12b; 5 to write, and no summarize job is queued to write them",
		detail, "the restart it needs comes before the bars' advisory")
	assert.Equal(t, summariesRestart, hint)
	status, _, _ = summariesCheck(stats, client.SummariesHealth{On: true, Model: "elsewhere:7b", Check: "fit"}, true)
	assert.Equal(t, statusOK, status, "an unmeasured model: nothing to say")

	status, detail, _ = summariesCheck(stats, client.SummariesHealth{On: true, Check: "unfit",
		Unfit: "m didn't pass the check; " + summarize.UnfitFix}, true)
	assert.Equal(t, statusWarn, status)
	assert.Contains(t, detail, summarize.UnfitFix)

	outage := on
	outage.Outage = &client.SummaryOutage{Since: time.Now(), Error: "gemma4:26b isn't pulled — run curio up"}
	status, _, hint = summariesCheck(stats, outage, true)
	assert.Equal(t, statusWarn, status)
	assert.Equal(t, summariesOutageFix, hint)

	braked := on
	braked.Brake = &client.ReembedBrake{Until: time.Now(), Failures: 3, Documents: []string{"a", "b", "c"}}
	status, detail, hint = summariesCheck(stats, braked, true)
	assert.Equal(t, statusWarn, status)
	assert.Contains(t, detail, "(a, b and c)")
	assert.Equal(t, summariesBrakeFix, hint)

	unchecked := client.SummariesHealth{On: true, Model: "gemma4:26b", Check: "unchecked",
		CheckRetryAt: time.Now(), LastFailure: &client.SummaryWriterFailure{Cause: "check", At: time.Now(),
			Error: "probe \"an article\": HTTP 500"}}
	status, detail, hint = summariesCheck(stats, unchecked, true)
	assert.Equal(t, statusWarn, status)
	assert.Equal(t, "no page is summarized: gemma4:26b couldn't be checked: probe \"an article\": HTTP 500; "+
		"checked again at "+localTime(unchecked.CheckRetryAt), detail)
	assert.Equal(t, summariesCheckFix, hint)
	unchecked.CheckRetryAt = time.Time{}
	_, detail, _ = summariesCheck(stats, unchecked, true)
	assert.Equal(t, "no page is summarized: gemma4:26b couldn't be checked: probe \"an article\": HTTP 500", detail,
		"the time past, the daemon says none (a pause keeps the job that runs the check): none said")

	evicted := on
	evicted.EvictionHoldUntil = time.Now().Add(15 * time.Minute)
	status, detail, hint = summariesCheck(stats, evicted, false)
	assert.Equal(t, statusWarn, status)
	assert.Equal(t, "the summary pass waits until "+localTime(evicted.EvictionHoldUntil)+": Ollama unloaded "+
		"gemma4:26b within its keep-alive twice within an hour, for other models", detail)
	assert.Equal(t, summariesEvictionFix, hint)

	status, detail, hint = summariesCheck(stats, on, false)
	assert.Equal(t, statusWarn, status)
	assert.Contains(t, detail, "5 to write, and no summarize job is queued")
	assert.Equal(t, summariesRestart, hint)

	status, detail, hint = summariesCheck(&client.SummaryStats{Written: 10, Outdated: 4}, on, false)
	assert.Equal(t, statusOK, status)
	assert.Equal(t, "10 of 14 written with gemma4:26b", detail)
	assert.Contains(t, hint, "`curio summarize --outdated`", "outdated summaries are a note")
}

// TestRenderDocumentSummary: docs show prints the summary, and says when
// it is stale or outdated, and the last failure.
func TestRenderDocumentSummary(t *testing.T) {
	var b bytes.Buffer
	renderDocumentSummary(&b, &client.Document{Summary: &client.DocumentSummary{Kind: "article",
		About: "Why queues back up.", Site: "Example Times", Entities: []string{"Kafka", "Zephyrine"},
		Model: "gemma4:26b", WrittenAt: time.Date(2026, 11, 3, 1, 2, 0, 0, time.Local), Outdated: true},
		SummaryFailure: &client.SummaryFailure{At: time.Date(2026, 11, 3, 2, 0, 0, 0, time.Local),
			Error: "made-up error"}})
	assert.Equal(t, "\nsummary:\n  about:        Why queues back up.\n  kind:         article\n"+
		"  site:         Example Times\n  names:        Kafka, Zephyrine\n"+
		"  written:      2026-11-03 01:02 by gemma4:26b\n"+
		"  stale:        it read an earlier extraction; the summary pass writes it again\n"+
		"  outdated:     another model or prompt wrote it; `curio summarize --outdated` writes it again\n"+
		"\nsummary failed 2026-11-03 02:00: made-up error\n", b.String())
	b.Reset()
	renderDocumentSummary(&b, &client.Document{})
	assert.Empty(t, b.String())
}

// stubModel answers nothing: the writer in these tests is never asked.
type stubModel struct{}

func (stubModel) Generate(context.Context, string, generator.Options) (string, error) { return "", nil }
func (stubModel) Model() string                                                       { return "gemma4:26b" }
func (stubModel) Ping(context.Context) error                                          { return nil }

// TestSummarize: `curio summarize` owes the outdated summaries, or all, a
// rewrite and says how many; it needs one of the two, and is refused while
// summaries are off.
func TestSummarize(t *testing.T) {
	writer := summarize.New(stubModel{}, summarize.Options{On: true, Log: slog.New(slog.DiscardHandler),
		Model: config.ModelSpec{Provider: "acme", Model: "gemma4:26b"}})
	srv := apitest.Start(t, func(d *api.Deps) { d.SummaryWriter = writer })
	doc, ext := seedFetched(t, srv, "https://example.com/a")
	sums := srv.Deps.Summaries
	require.NoError(t, sums.PutSummary(context.Background(), "local", store.Summary{DocumentID: doc, ExtractionID: ext,
		Kind: store.SummaryKindArticle, SummaryWriter: store.SummaryWriter{Model: "old:1b", PromptVersion: "p0"}}))

	_, err := runCLI(t, srv, "summarize")
	require.ErrorContains(t, err, "pass --outdated or --all")
	assert.Equal(t, "1 page to summarize again\n1 page to write in all, under a minute at gemma4:26b's 3.6 s a "+
		"page (normal throttle, as measured on a 64 GB M4 Max; searches pause it)\n",
		mustRun(t, srv, "summarize", "--outdated"),
		"no rate measured: the model's measured time a page")
	assert.Contains(t, mustRun(t, srv, "summarize", "--all"), "0 pages to summarize again", "already owed")
	failed, failedExt := seedFetched(t, srv, "https://example.com/failed")
	require.NoError(t, sums.RecordSummaryFailure(context.Background(), "local", failed, store.SummaryFailure{
		ExtractionID: failedExt, SummaryWriter: writer.Identity(), At: time.Now(), Error: "invalid"}))
	assert.Contains(t, mustRun(t, srv, "summarize", "--all"), "1 page to summarize again",
		"a page whose summary failed for good, which has none to write again, is tried again")

	unmeasured := summarize.New(stubModel{}, summarize.Options{On: true, Log: slog.New(slog.DiscardHandler),
		Model: config.ModelSpec{Provider: "acme", Model: "elsewhere:7b"}})
	other := apitest.Start(t, func(d *api.Deps) { d.SummaryWriter = unmeasured })
	doc, ext = seedFetched(t, other, "https://example.com/b")
	require.NoError(t, other.Deps.Summaries.PutSummary(context.Background(), "local", store.Summary{DocumentID: doc,
		ExtractionID: ext, Kind: store.SummaryKindArticle, SummaryWriter: store.SummaryWriter{Model: "old:1b",
			PromptVersion: "p0"}}))
	assert.Equal(t, "1 page to summarize again\n1 page to write in all, one at a time; the time a page isn't "+
		"measured yet: `curio status` shows the rate once the pass runs\n", mustRun(t, other, "summarize", "--outdated"),
		"a model curio up never measured: no time")

	off := apitest.Start(t)
	_, err = runCLI(t, off, "summarize", "--all")
	require.ErrorContains(t, err, "summaries are off")
}

// seedFetched saves url as a fetched document with an extraction, and
// returns the document's and the extraction's IDs.
func seedFetched(t *testing.T, srv *apitest.Server, url string) (string, string) {
	t.Helper()
	ctx := context.Background()
	doc := &store.Document{TenantID: "local", URL: url, ContentType: store.ContentTypeArticle,
		State: store.DocStateFetched}
	require.NoError(t, srv.Deps.Documents.Create(ctx, doc))
	ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK}
	require.NoError(t, srv.Deps.Extractions.Create(ctx, ext))
	require.NoError(t, srv.Deps.Documents.SetCurrentExtraction(ctx, doc.ID, ext.ID))
	return doc.ID, ext.ID
}
