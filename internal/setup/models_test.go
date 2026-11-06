package setup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
)

// TestUp_KeepsTheUsersWritingModel: config.yaml's generation.model is
// pulled and kept, with no question about models; a --generation-model
// that contradicts it is refused, naming the file and the key.
func TestUp_KeepsTheUsersWritingModel(t *testing.T) {
	h := newHarness(t)
	h.writeHome("qwen3:4b-instruct")
	r := h.runner(setuptest.NewUI(t), setup.Options{})
	res := checkStep(t, r, "models")
	assert.Contains(t, res.Notes, "keeping generation.model: qwen3:4b-instruct from config.yaml")
	require.NotNil(t, res.Fix)
	assert.Equal(t, "pull qwen3-embedding:0.6b (639 MB) and qwen3:4b-instruct (2.5 GB)", res.Fix.Summary)
	assert.Empty(t, res.Hint, "no pick of curio's to override")

	r = h.runner(setuptest.NewUI(t), setup.Options{GenerationModel: "gemma4:12b"})
	res = checkStep(t, r, "models")
	assert.True(t, res.Blocked())
	assert.Contains(t, res.Detail, "--generation-model gemma4:12b: "+filepath.Join(h.home, "config.yaml")+
		" sets generation.model: qwen3:4b-instruct, and curio up never rewrites a value you set")

	ui := setuptest.NewUI(t, setuptest.Yes().About("Pull"), setuptest.Yes().About("launchd"))
	_, _, err := h.up(ui, setup.Options{})
	require.NoError(t, err)
	assert.Equal(t, []string{embedModel, "qwen3:4b-instruct"}, h.ollama.Pulls())
}

// TestUp_ConfigWithoutAWritingModel: a config.yaml that sets no
// generation.model is left as it is: the daemon's default is pulled, and
// the plan says what curio would pick and where to set it.
func TestUp_ConfigWithoutAWritingModel(t *testing.T) {
	h := newHarness(t)
	home := h.writeHome(genModel)
	cfg := strings.Replace(mustRead(t, home.ConfigPath()), "  model: "+genModel+"\n", "", 1)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))

	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Contains(t, res.Fix.Summary, config.Default().Generation.Model)
	assert.Contains(t, strings.Join(res.Notes, "\n"), "curio would pick gemma4:26b for this Mac: set generation.model: gemma4:26b in "+
		home.ConfigPath())
	res = checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{GenerationModel: "gemma4:12b"}), "models")
	assert.True(t, res.Blocked(), "config.yaml is never edited, so a flag can't take effect")
	assert.Equal(t, cfg, mustRead(t, home.ConfigPath()))
}

// TestUp_NoWritingModel: with interest labels, search understanding and
// summaries off, no writing model is pulled, and the plan says so once,
// naming the keys that turn each role off.
func TestUp_NoWritingModel(t *testing.T) {
	h := newHarness(t)
	home := h.writeHome(genModel)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(mustRead(t, home.ConfigPath())+
		"insight:\n  labeling: terms\nsearch:\n  understand: false\nsummaries:\n  enabled: false\n"), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, "pull qwen3-embedding:0.6b (639 MB)", res.Fix.Summary)
	require.Len(t, res.Notes, 1)
	assert.Equal(t, "no writing model is pulled: no role writes with one (interest labels are off with "+
		"insight.enabled false or insight.labeling not llm, search understanding with search.understand false, "+
		"document summaries unless summaries.enabled is true, search reranking unless search.rerank is true, or "+
		"left out for a model measured as the reranker)", res.Notes[0])
	assert.Empty(t, res.Hint, "no writing model to pick another of")

	ui := setuptest.NewUI(t, setuptest.Yes().About("Pull qwen3-embedding:0.6b"), setuptest.Yes().About("launchd"))
	_, _, err := h.up(ui, setup.Options{})
	require.NoError(t, err, "%v", ui.Events())
	assert.Equal(t, []string{embedModel}, h.ollama.Pulls())
}

// TestUp_PlannerModelWithConsent: with interest labels off, search
// understanding on, as a 2.7 home with term labels is after the upgrade,
// and summaries turned on, so reranking on by default for gemma4:26b, the
// daemon pulls no writing model (the planner's, the summaries' and the
// reranker's ride only on another role's pull), so curio up offers
// generation.model for the three: a warning, never a failure, which
// doctor shares. A no leaves it out, says what that leaves and how
// to get it, and the run goes on; a yes pulls it. config.yaml is never
// edited.
func TestUp_PlannerModelWithConsent(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel)
	home := h.writeHome(genModel)
	cfg := mustRead(t, home.ConfigPath()) + "insight:\n  labeling: terms\nsummaries:\n  enabled: true\n"
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.Warn, res.Status, "doctor warns, never fails, for a model only optional roles write with")
	assert.Equal(t, genModel+" isn't pulled, and only search understanding and document summaries and search "+
		"reranking would write with it: searches use their words and pages get no summary and searches keep their "+
		"first ranking until it is", res.Detail)
	require.NotNil(t, res.Fix)
	assert.Equal(t, setup.Offer, res.Fix.Consent)
	assert.Equal(t, "pull "+genModel+" (19 GB) for search understanding and document summaries and search reranking",
		res.Fix.Summary)
	assert.Contains(t, res.Notes, "search reranking is on by default for "+genModel+" with the summaries, as it "+
		"was measured; search.rerank: false turns it off")

	declined := setuptest.NewUI(t, setuptest.No().About("Pull "+genModel), setuptest.Yes().About("launchd"))
	_, out, err := h.up(declined, setup.Options{})
	require.NoError(t, err, "a no to the optional model doesn't fail curio up: %v", declined.Events())
	assert.Empty(t, h.ollama.Pulls(), "nothing is pulled without a yes")
	assert.Contains(t, declined.Lines("info"), "models: not pulling "+genModel+": searches use their words and "+
		"pages get no summary and searches keep their first ranking; run curio up again to pull it, or set "+
		"search.understand: false to stop understanding questions and summaries.enabled: false to write none and "+
		"search.rerank: false to stop reranking")
	var warned bool
	for _, w := range out.Status.Warnings {
		warned = warned || strings.HasPrefix(w.Text, "models: "+genModel+" isn't pulled")
	}
	assert.True(t, warned, "the run ends saying so: %v", out.Status.Warnings)
	assert.Equal(t, cfg, mustRead(t, home.ConfigPath()), "config.yaml is never edited")

	ui := setuptest.NewUI(t, setuptest.Yes().About("Pull"), setuptest.Yes().About("launchd"))
	_, _, err = h.up(ui, setup.Options{})
	require.NoError(t, err, "%v", ui.Events())
	assert.Equal(t, []string{genModel}, h.ollama.Pulls())
}

// TestUp_SummariesModelWithConsent: a model only the summaries write
// with is optional, offered for them alone; a no leaves pages without a
// summary and the run goes on.
func TestUp_SummariesModelWithConsent(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel, genModel)
	home := h.writeHome(genModel)
	// Turned on: summaries are off by default.
	cfg := strings.Replace(mustRead(t, home.ConfigPath()), "generation:\n",
		"generation:\n  roles:\n    summaries:\n      model: gemma4:12b\n", 1) + "summaries:\n  enabled: true\n"
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.Warn, res.Status)
	assert.Equal(t, "gemma4:12b isn't pulled, and only document summaries would write with it: pages get no "+
		"summary until it is", res.Detail)
	require.NotNil(t, res.Fix)
	assert.Equal(t, setup.Offer, res.Fix.Consent)
	assert.Equal(t, "pull gemma4:12b (7.6 GB) for document summaries", res.Fix.Summary)

	declined := setuptest.NewUI(t, setuptest.No().About("Pull gemma4:12b"), setuptest.Yes().About("launchd"))
	_, _, err := h.up(declined, setup.Options{})
	require.NoError(t, err, "%v", declined.Events())
	assert.Empty(t, h.ollama.Pulls())
	assert.Contains(t, declined.Lines("info"), "models: not pulling gemma4:12b: pages get no summary; run curio up "+
		"again to pull it, or set summaries.enabled: false to write none")
}

// TestUp_NoSummariesModelWhileOff: summaries off by default write nothing,
// so a model only they would write with is neither pulled nor offered,
// whatever config.yaml names for their role.
func TestUp_NoSummariesModelWhileOff(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel, genModel)
	home := h.writeHome(genModel)
	cfg := strings.Replace(mustRead(t, home.ConfigPath()), "generation:\n",
		"generation:\n  roles:\n    summaries:\n      model: gemma4:12b\n", 1)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.OK, res.Status, res.Detail)
	assert.Nil(t, res.Fix, "nothing offered for summaries that are off")
	_, _, err := h.up(setuptest.NewUI(t, setuptest.Yes().About("launchd")), setup.Options{})
	require.NoError(t, err)
	assert.Empty(t, h.ollama.Pulls())
}

// TestUp_OptionalModelsOfTwoRoles: two models only roles that don't pull
// alone write with, the planner's and the summaries', are offered in one
// line that names both roles.
func TestUp_OptionalModelsOfTwoRoles(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel, genModel)
	home := h.writeHome(genModel)
	cfg := strings.Replace(mustRead(t, home.ConfigPath()), "generation:\n", "generation:\n  roles:\n"+
		"    planner:\n      model: gemma4:12b\n    summaries:\n      model: qwen3:4b-instruct\n", 1) +
		"summaries:\n  enabled: true\n"
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.Warn, res.Status)
	assert.Equal(t, "gemma4:12b and qwen3:4b-instruct aren't pulled, and only search understanding and document "+
		"summaries would write with them: searches use their words and pages get no summary until they are",
		res.Detail)
	require.NotNil(t, res.Fix)
	assert.Equal(t, setup.Offer, res.Fix.Consent)
}

// TestUp_RerankerOnGenerationModel: a reranker turned on that writes with
// generation.model, the labels' and the planner's, offers nothing new, and
// curio up writes no search.rerank key.
func TestUp_RerankerOnGenerationModel(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel, genModel)
	home := h.writeHome(genModel)
	cfg := mustRead(t, home.ConfigPath()) + "search:\n  rerank: true\n"
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.OK, res.Status, res.Detail)
	assert.Nil(t, res.Fix, "nothing to pull")
	_, _, err := h.up(setuptest.NewUI(t, setuptest.Yes().About("launchd")), setup.Options{})
	require.NoError(t, err)
	assert.Empty(t, h.ollama.Pulls())
	assert.Equal(t, cfg, mustRead(t, home.ConfigPath()), "config.yaml is never edited")

	fresh := newHarness(t)
	fresh.firstRun()
	assert.NotContains(t, mustRead(t, fresh.openHome().ConfigPath()), "rerank",
		"a new home's config.yaml leaves reranking to its model")
}

// TestUp_RerankerModelWithConsent: a model only the reranker writes with
// is optional, offered for it alone, with consent; a no leaves searches
// with their first ranking and the run goes on.
func TestUp_RerankerModelWithConsent(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel, genModel)
	home := h.writeHome(genModel)
	cfg := strings.Replace(mustRead(t, home.ConfigPath()), "generation:\n",
		"generation:\n  roles:\n    reranker:\n      model: gemma4:12b\n", 1) + "search:\n  rerank: true\n"
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.Warn, res.Status)
	assert.Equal(t, "gemma4:12b isn't pulled, and only search reranking would write with it: searches keep their "+
		"first ranking until it is", res.Detail)
	require.NotNil(t, res.Fix)
	assert.Equal(t, setup.Offer, res.Fix.Consent)
	assert.Equal(t, "pull gemma4:12b (7.6 GB) for search reranking", res.Fix.Summary)

	declined := setuptest.NewUI(t, setuptest.No().About("Pull gemma4:12b"), setuptest.Yes().About("launchd"))
	_, _, err := h.up(declined, setup.Options{})
	require.NoError(t, err, "%v", declined.Events())
	assert.Empty(t, h.ollama.Pulls())
	assert.Contains(t, declined.Lines("info"), "models: not pulling gemma4:12b: searches keep their first ranking; "+
		"run curio up again to pull it, or set search.rerank: false to stop reranking")
	assert.Equal(t, cfg, mustRead(t, home.ConfigPath()), "config.yaml is never edited")
}

// TestUp_RerankerWithoutUnderstanding: a reranker turned on with search
// understanding off would rerank no search, since every route needs a
// plan, so its own model is neither pulled nor offered, and the plan says
// why.
func TestUp_RerankerWithoutUnderstanding(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel, genModel)
	home := h.writeHome(genModel)
	cfg := strings.Replace(mustRead(t, home.ConfigPath()), "generation:\n",
		"generation:\n  roles:\n    reranker:\n      model: gemma4:12b\n", 1) +
		"search:\n  understand: false\n  rerank: true\n"
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.OK, res.Status, res.Detail)
	assert.Nil(t, res.Fix, "no offer for a reranker no search reaches")
	assert.Contains(t, res.Notes, "search reranking's model isn't pulled: curio reranks only understood searches, "+
		"and search.understand is off; set search.understand: true, or search.rerank: false")
	_, _, err := h.up(setuptest.NewUI(t, setuptest.Yes().About("launchd")), setup.Options{})
	require.NoError(t, err)
	assert.Empty(t, h.ollama.Pulls())
}

// TestUp_RerankerWithheld: a reranker whose questions and pages may not go
// where its model runs writes nothing, so its model is neither pulled nor
// offered, and the plan says why.
func TestUp_RerankerWithheld(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel)
	home := h.writeHome(genModel)
	cfg := mustRead(t, home.ConfigPath())
	before, _, _ := strings.CutLast(cfg, "  base_url: ")
	remote := before + "  base_url: http://gpu-box.lan:11434\n"
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(remote+"insight:\n  labeling: terms\nsearch:\n"+
		"  understand: false\n  rerank: true\n"), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.OK, res.Status, res.Detail)
	assert.Nil(t, res.Fix, "no offer for a withheld reranker")
	require.Len(t, res.Notes, 1)
	assert.Contains(t, res.Notes[0], "search reranking's model isn't pulled: questions and pages would leave this "+
		"machine for http://gpu-box.lan:11434")
}

// TestUp_SharedPlannerModelIsRequired: on a default home the labels and
// the planner write with generation.model, so the labels need it: missing,
// it fails the models check and is asked for, never offered as optional.
func TestUp_SharedPlannerModelIsRequired(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel)
	h.writeHome(genModel)
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.Fail, res.Status, res.Detail)
	require.NotNil(t, res.Fix)
	assert.NotEqual(t, setup.Offer, res.Fix.Consent, "the labels' model is never optional")
}

// TestUp_PlannerWithheld: a planner whose questions, and summaries, turned
// on, whose pages may not go where their model runs (generation.base_url
// off this machine, the roles naming no server) write nothing, so their
// model is neither pulled nor offered, and the plan says why for each; the
// labels' model on that server still is.
func TestUp_PlannerWithheld(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel)
	home := h.writeHome(genModel)
	cfg := mustRead(t, home.ConfigPath())
	before, _, _ := strings.CutLast(cfg, "  base_url: ")
	remote := before + "  base_url: http://gpu-box.lan:11434\n"
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(remote+"insight:\n  labeling: terms\nsummaries:\n"+
		"  enabled: true\n"), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.OK, res.Status, res.Detail)
	assert.Nil(t, res.Fix)
	require.Len(t, res.Notes, 3)
	assert.Contains(t, res.Notes[0], "search understanding's model isn't pulled: questions would leave this machine "+
		"for http://gpu-box.lan:11434")
	assert.Contains(t, res.Notes[1], "document summaries' model isn't pulled: pages would leave this machine "+
		"for http://gpu-box.lan:11434")
	assert.Contains(t, res.Notes[2], "search reranking's model isn't pulled: questions and pages would leave "+
		"this machine for http://gpu-box.lan:11434", "on by default for gemma4:26b with the summaries")
}

// TestUp_RerankerByDefault: with search.rerank left out, gemma4:26b
// reranks by default while summaries are written, which the plan says,
// pulling nothing more than the labels' and the planner's model; without
// the summaries the plan says what turns it on; another model says
// nothing of it.
func TestUp_RerankerByDefault(t *testing.T) {
	h := newHarness(t)
	h.ollama = setuptest.NewOllama(t, "0.34.4", embedModel, genModel)
	home := h.writeHome(genModel)
	base := mustRead(t, home.ConfigPath())
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(base+"summaries:\n  enabled: true\n"), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, setup.OK, res.Status, res.Detail)
	assert.Nil(t, res.Fix, "nothing more to pull")
	assert.Contains(t, res.Notes, "search reranking is on by default for "+genModel+" with the summaries, as it "+
		"was measured; search.rerank: false turns it off")

	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(base), 0o600))
	res = checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Contains(t, res.Notes, "search reranking is off by default for "+genModel+" without the summaries, "+
		"which it was measured with; summaries.enabled: true turns it on")

	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(base+"search:\n  rerank: false\n"), 0o600))
	res = checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	require.Contains(t, res.Notes, "keeping generation.model: "+genModel+" from config.yaml", "config.yaml loaded")
	for _, n := range res.Notes {
		assert.NotContains(t, n, "search reranking is", "the key says")
	}
}

// TestUp_RoleModel: a role config.yaml gives its own model writes with
// it, whatever generation.model says, and the plan says it is kept, while
// the other role writes with generation.model; with every role on its own
// model, generation.model's notes are gone, and a --generation-model has
// nothing to change.
func TestUp_RoleModel(t *testing.T) {
	h := newHarness(t)
	home := h.writeHome("qwen3:4b-instruct")
	labelsOwn := strings.Replace(mustRead(t, home.ConfigPath()), "generation:\n",
		"generation:\n  roles:\n    labels:\n      model: gemma4:12b\n", 1)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(labelsOwn), 0o600))

	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	require.NotNil(t, res.Fix)
	assert.Equal(t, "pull qwen3-embedding:0.6b (639 MB) and gemma4:12b (7.6 GB) and qwen3:4b-instruct (2.5 GB)",
		res.Fix.Summary, "the planner writes with generation.model")
	assert.Equal(t, []string{"keeping generation.roles.labels.model: gemma4:12b from config.yaml",
		"keeping generation.model: qwen3:4b-instruct from config.yaml"}, res.Notes)

	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(strings.Replace(labelsOwn, "  roles:\n",
		"  roles:\n    planner:\n      model: gemma4:12b\n    summaries:\n      model: gemma4:12b\n", 1)+
		"summaries:\n  enabled: true\n"), 0o600))
	res = checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	require.NotNil(t, res.Fix)
	assert.Equal(t, "pull qwen3-embedding:0.6b (639 MB) and gemma4:12b (7.6 GB)", res.Fix.Summary)
	assert.Equal(t, []string{"keeping generation.roles.labels.model: gemma4:12b from config.yaml",
		"keeping generation.roles.planner.model: gemma4:12b from config.yaml",
		"keeping generation.roles.summaries.model: gemma4:12b from config.yaml"}, res.Notes)

	res = checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{GenerationModel: "gemma4:26b"}), "models")
	assert.True(t, res.Blocked())
	assert.Contains(t, res.Detail, "gives every writing role its own model in generation.roles")

	ui := setuptest.NewUI(t, setuptest.Yes().About("Pull"), setuptest.Yes().About("launchd"))
	_, _, err := h.up(ui, setup.Options{})
	require.NoError(t, err, "%v", ui.Events())
	assert.Equal(t, []string{embedModel, "gemma4:12b"}, h.ollama.Pulls())
	res = checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, "qwen3-embedding:0.6b for search and gemma4:12b for writing: present", res.Detail)
}

// TestUp_ModelsDeclined: n to `Use these?` ends the run, naming the flag
// that picks another model.
func TestUp_ModelsDeclined(t *testing.T) {
	h := newHarness(t)
	ui := setuptest.NewUI(t, setuptest.Pick(1).About("Use these?"))
	_, _, err := h.up(ui, setup.Options{})
	var declined *setup.DeclinedError
	require.ErrorAs(t, err, &declined)
	assert.Equal(t, "models", declined.Step)
	assert.Contains(t, err.Error(), "--generation-model")
	assert.Empty(t, h.ollama.Pulls())
	assert.NoDirExists(t, h.home)
	assert.True(t, ui.Said("smaller:     gemma4:26b-a4b-it-qat (16 GB), the tier below"))
}

// TestUp_ModelsChoose: choose lists every tier, and the one picked is
// pulled and written to config.yaml.
func TestUp_ModelsChoose(t *testing.T) {
	h := newHarness(t)
	ui := setuptest.NewUI(t, setuptest.Pick(2).About("Use these?"), setuptest.Pick(1).About("gemma4:12b (7.6 GB)"),
		setuptest.Yes().About("launchd"))
	_, _, err := h.up(ui, setup.Options{})
	require.NoError(t, err, "%v", ui.Events())
	assert.Equal(t, []string{embedModel, "gemma4:12b"}, h.ollama.Pulls())
	cfg, err := config.Load(filepath.Join(h.home, "config.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "gemma4:12b", cfg.Generation.Model)
}

// TestUp_NotEnoughDisk: the disk is checked once the missing models are
// known, and short of room blocks before anything is pulled, naming the
// models, the volume and the free space.
func TestUp_NotEnoughDisk(t *testing.T) {
	h := newHarness(t)
	h.probe.Facts.Models.Free, h.probe.Facts.Home.Free = 10e9, 10e9
	plan, _, err := h.up(setuptest.NewUI(t), setup.Options{})
	var blocked *setup.BlockedError
	require.ErrorAs(t, err, &blocked)
	models := item(t, plan, "models")
	for _, want := range []string{"not enough disk space", "has 10 GB free", "qwen3-embedding:0.6b (639 MB)", "gemma4:26b (19 GB)"} {
		assert.Contains(t, models.Detail, want)
	}
	assert.Empty(t, h.ollama.Pulls())

	h.ollama.Stop()
	models = checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.NotContains(t, models.Detail, "disk", "not known before Ollama answers")
}

// TestUp_PullFailsThenResumes: a failed pull names `ollama pull`, and the
// next run pulls only what is still missing.
func TestUp_PullFailsThenResumes(t *testing.T) {
	h := newHarness(t)
	h.ollama.FailPull(genModel, "pull model manifest: file does not exist")
	ui := setuptest.NewUI(t, setuptest.Pick(0).About("Use these?"))
	_, _, err := h.up(ui, setup.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file does not exist")
	assert.Contains(t, err.Error(), "`ollama pull gemma4:26b` pulls it by hand")
	reports := ui.Reports()
	require.Len(t, reports, 2)
	_, stopped := reports[1].Finished()
	assert.True(t, stopped, "the failed pull's progress ends where it stood")
	assert.NoDirExists(t, h.home, "nothing after the failed step ran")

	h.ollama.FailPull(genModel, "")
	h.firstRun()
	assert.Equal(t, []string{embedModel, genModel, genModel}, h.ollama.Pulls(), "the embedder only once")
}

// TestUp_MeasuresTheWidth: a new home records the width its model's
// vectors have, measured after the pull, and that model's prompts.
func TestUp_MeasuresTheWidth(t *testing.T) {
	h := newHarness(t)
	const nomic = "nomic-embed-text:v1.5"
	h.ollama.SetWidth(nomic, 768)
	plan, _, err := h.up(setuptest.NewUI(t), setup.Options{EmbeddingModel: nomic, DryRun: true})
	require.NoError(t, err)
	assert.Contains(t, item(t, plan, "home").Fix.Summary, nomic+", its width measured after the pull")

	ui := setuptest.NewUI(t, setuptest.Pick(0).About("Use these?"), setuptest.Yes().About("launchd"))
	_, _, err = h.up(ui, setup.Options{EmbeddingModel: nomic})
	require.NoError(t, err, "%v", ui.Events())
	meta, err := h.openHome().Meta()
	require.NoError(t, err)
	assert.Equal(t, nomic, meta.EmbeddingModel)
	assert.Equal(t, 768, meta.EmbeddingDim)
	cfg, err := config.Load(filepath.Join(h.home, "config.yaml"))
	require.NoError(t, err)
	assert.Equal(t, nomic, cfg.Embedding.Model)
	assert.Equal(t, 768, cfg.Embedding.Dim)
	assert.Equal(t, "search_document: ", cfg.Embedding.DocumentPrefix)
	assert.Equal(t, "search_query: ", cfg.Embedding.QueryPrefix)
	assert.Contains(t, h.ollama.Embeds(), nomic, "measured with an embed")
}

// TestUp_UnknownEmbeddingModel: a model whose prompts curio doesn't know
// gets none, and a warning to set them before importing.
func TestUp_UnknownEmbeddingModel(t *testing.T) {
	h := newHarness(t)
	const mxbai = "mxbai-embed-large:335m"
	ui := setuptest.NewUI(t, setuptest.Pick(0), setuptest.Yes())
	_, _, err := h.up(ui, setup.Options{EmbeddingModel: mxbai})
	require.NoError(t, err)
	assert.True(t, ui.Said("curio doesn't know the prompts "+mxbai+" expects"))
	cfg, err := config.Load(filepath.Join(h.home, "config.yaml"))
	require.NoError(t, err)
	assert.Empty(t, cfg.Embedding.QueryPrefix)
	assert.Empty(t, cfg.Embedding.DocumentPrefix)
}

// TestUp_DefaultModelOfAnotherWidth: the default model measuring another
// width than curio expects is recorded as measured, with a warning naming
// both.
func TestUp_DefaultModelOfAnotherWidth(t *testing.T) {
	h := newHarness(t)
	h.ollama.SetWidth(embedModel, 896)
	ui := setuptest.NewUI(t, setuptest.Pick(0), setuptest.Yes())
	_, _, err := h.up(ui, setup.Options{})
	require.NoError(t, err)
	assert.True(t, ui.Said("returned 896-dimensional vectors, not the 1024 curio expects"))
	meta, err := h.openHome().Meta()
	require.NoError(t, err)
	assert.Equal(t, 896, meta.EmbeddingDim)
}

// TestUp_UntaggedWritingModel: a --generation-model without a tag is
// warned about, and used.
func TestUp_UntaggedWritingModel(t *testing.T) {
	h := newHarness(t)
	ui := setuptest.NewUI(t, setuptest.Yes().About("Pull"), setuptest.Yes())
	_, _, err := h.up(ui, setup.Options{GenerationModel: "llama3.2"})
	require.NoError(t, err)
	assert.True(t, ui.Said("--generation-model llama3.2 has no tag, so it means llama3.2:latest"))
	assert.Contains(t, h.ollama.Pulls(), "llama3.2")
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
