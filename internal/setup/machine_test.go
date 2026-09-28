package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appleSilicon is an Apple silicon Mac with memory GiB of memory.
func appleSilicon(memory uint64) Machine {
	free := Volume{Path: "/", Free: 200e9, ID: "disk1"}
	return Machine{OS: "darwin", Chip: "Apple M4", AppleSilicon: true, Memory: memory * GiB, Home: free, Models: free}
}

func TestAssess(t *testing.T) {
	for _, gib := range []uint64{16, 18, 24, 32, 36, 48, 64, 128} {
		v := Assess(appleSilicon(gib))
		assert.Equal(t, Supported, v.Level, "%d GiB", gib)
		assert.Empty(t, v.Reasons, "%d GiB", gib)
		assert.Empty(t, v.Question, "%d GiB", gib)
	}

	eight := Assess(appleSilicon(8))
	assert.Equal(t, Limited, eight.Level)
	require.Len(t, eight.Reasons, 1)
	assert.Contains(t, eight.Reasons[0], "8 GB of memory")
	assert.Contains(t, eight.Reasons[0], "import overnight")
	assert.Empty(t, eight.Question, "8 GB runs; it is only warned about")

	intel := appleSilicon(32)
	intel.AppleSilicon, intel.Chip = false, "Intel(R) Core(TM) i9-9980HK CPU @ 2.40GHz"
	v := Assess(intel)
	assert.Equal(t, Degraded, v.Level)
	assert.Contains(t, v.Question, "Continue?")
	assert.Contains(t, v.Reasons, "an Intel Mac: Ollama runs on the CPU only")
	assert.Contains(t, v.Reasons, "the first import takes 1 to 4 days")

	rosetta := appleSilicon(64)
	rosetta.Translated = true
	v = Assess(rosetta)
	assert.Equal(t, Limited, v.Level)
	assert.Contains(t, v.Reasons[0], "install the arm64 build")

	v = Assess(Machine{OS: "linux"})
	assert.Equal(t, Unsupported, v.Level)
	assert.Contains(t, v.Reasons[0], "automatic setup is macOS-only")
	assert.Empty(t, v.Question)
}

func TestDiskShortage(t *testing.T) {
	gemma := Model{Name: "gemma4:26b", Size: 19e9}
	qwen := Model{Name: "qwen3-embedding:0.6b", Size: 639e6}
	unknown := Model{Name: "mistral-small:24b"}
	withFree := func(models, home uint64, sameVolume bool) Machine {
		m := appleSilicon(64)
		m.Models = Volume{Path: "/Users/x/.ollama", Free: models, ID: "disk1"}
		m.Home = Volume{Path: "/Users/x", Free: home, ID: "disk1"}
		if !sameVolume {
			m.Home.ID = "disk2"
		}
		return m
	}

	assert.Empty(t, diskShortage(withFree(100e9, 100e9, true), []Model{gemma, qwen}, true))
	assert.Empty(t, diskShortage(withFree(1e9, 1e9, true), nil, false), "nothing to pull")

	short := diskShortage(withFree(20e9, 20e9, true), []Model{gemma, qwen}, true)
	for _, want := range []string{"/Users/x/.ollama", "has 20 GB free", "gemma4:26b (19 GB)",
		"qwen3-embedding:0.6b (639 MB)", "need 21.8 GB", "2 GiB margin"} {
		assert.Contains(t, short, want)
	}

	short = diskShortage(withFree(1e9, 100e9, true), []Model{unknown}, true)
	assert.Contains(t, short, "mistral-small:24b (size unknown)", "an unknown size counts the margin")
	assert.Empty(t, diskShortage(withFree(3e9, 100e9, true), []Model{unknown}, true))

	short = diskShortage(withFree(100e9, 1e9, false), []Model{gemma}, true)
	assert.Contains(t, short, "the volume of /Users/x has 1 GB free, and a new curio home needs 2 GiB")
	assert.Empty(t, diskShortage(withFree(100e9, 1e9, false), []Model{gemma}, false), "no new home")
	assert.Empty(t, diskShortage(withFree(100e9, 1e9, true), []Model{gemma}, true),
		"one volume: the models' margin covers the home")

	unmeasured := withFree(1, 1, false)
	unmeasured.Models, unmeasured.Home = Volume{}, Volume{}
	assert.Empty(t, diskShortage(unmeasured, []Model{gemma}, true), "a volume the probe couldn't measure isn't judged")
}

func TestParseGPUCores(t *testing.T) {
	fixture := `+-o AGXAcceleratorG16X  <class AGXAcceleratorG16X, id 0x100000436, registered, matched, active, busy 0 (47 ms), retain 1178>
    {
      "IOClass" = "AGXAcceleratorG16X"
      "gpu-core-count" = 40
      "model" = "Apple M4 Max"
      "AGXParameterBufferMaxSize" = 1006632960
    }
`
	assert.Equal(t, 40, parseGPUCores(fixture))
	assert.Zero(t, parseGPUCores(""))
	assert.Zero(t, parseGPUCores("ioreg: no such class\n"))
	assert.Zero(t, parseGPUCores(`"gpu-core-count" = lots`))
	assert.Zero(t, parseGPUCores(`"gpu-core-count" = 999999999999999999999999`))
}

func TestMachineString(t *testing.T) {
	m := appleSilicon(64)
	m.Chip, m.PerfCores, m.EffCores, m.GPUCores = "Apple M4 Max", 12, 4, 40
	assert.Equal(t, "Apple M4 Max, 64 GB of memory, 12 performance and 4 efficiency cores, 40 GPU cores", m.String())
	assert.Equal(t, "linux, 0 GB of memory", Machine{OS: "linux"}.String())
}

func TestModelsDir(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", "/Volumes/Models")
	assert.Equal(t, "/Volumes/Models", ModelsDir())
	t.Setenv("OLLAMA_MODELS", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	assert.Equal(t, filepath.Join(home, ".ollama", "models"), ModelsDir())
}

func TestResolveHome(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "work", "curio")
	link := filepath.Join(dir, ".curio")
	require.NoError(t, os.Symlink(filepath.Join("work", "curio"), link))

	got, err := resolveHome(link)
	require.NoError(t, err)
	assert.Equal(t, target, got, "a dangling relative link resolves to where it points")
	got, err = resolveHome(target)
	require.NoError(t, err)
	assert.Equal(t, target, got)

	loop := filepath.Join(dir, "loop")
	require.NoError(t, os.Symlink(loop, loop))
	_, err = resolveHome(loop)
	require.Error(t, err)
}
