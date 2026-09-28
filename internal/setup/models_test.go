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

// TestUp_NoWritingModel: with interest labels off, no writing model is
// pulled, and the plan says so once.
func TestUp_NoWritingModel(t *testing.T) {
	h := newHarness(t)
	home := h.writeHome(genModel)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(mustRead(t, home.ConfigPath())+"insight:\n  labeling: terms\n"), 0o600))
	res := checkStep(t, h.runner(setuptest.NewUI(t), setup.Options{}), "models")
	assert.Equal(t, "pull qwen3-embedding:0.6b (639 MB)", res.Fix.Summary)
	require.Len(t, res.Notes, 1)
	assert.Contains(t, res.Notes[0], "no writing model is pulled")
	assert.Empty(t, res.Hint, "no writing model to pick another of")

	ui := setuptest.NewUI(t, setuptest.Yes().About("Pull qwen3-embedding:0.6b"), setuptest.Yes().About("launchd"))
	_, _, err := h.up(ui, setup.Options{})
	require.NoError(t, err, "%v", ui.Events())
	assert.Equal(t, []string{embedModel}, h.ollama.Pulls())
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
