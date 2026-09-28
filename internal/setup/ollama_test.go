package setup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOlderThan(t *testing.T) {
	cases := []struct {
		v, want string
		older   bool
	}{
		{"0.34.4", "0.30.5", false},
		{"0.30.5", "0.30.5", false},
		{"0.30.4", "0.30.5", true},
		{"0.30.10", "0.30.5", false},
		{"0.30", "0.30.5", true},
		{"0.35.0-rc1", "0.35.0", true},
		{"0.35.0", "0.35.0-rc1", false},
		{"0.35.0-rc1", "0.35.0-rc2", true},
		{"v0.30.6", "0.30.6", false},
		{"1.0.0", "0.99.99", false},
	}
	for _, tc := range cases {
		older, err := olderThan(tc.v, tc.want)
		require.NoError(t, err, "%s vs %s", tc.v, tc.want)
		assert.Equal(t, tc.older, older, "%s older than %s", tc.v, tc.want)
	}
	for _, bad := range []string{"", "dev", "0.x.1", "0..1"} {
		_, err := olderThan(bad, "0.30.5")
		assert.Error(t, err, "%q", bad)
	}
}

// TestOllamaFix: what starts or installs Ollama, by what is installed,
// as exact commands; and when there is nothing curio up can run, what to
// do by hand.
func TestOllamaFix(t *testing.T) {
	const brew = "/opt/homebrew/bin/brew"
	silicon := Machine{OS: "darwin", AppleSilicon: true}
	intel := Machine{OS: "darwin"}
	cases := []struct {
		name        string
		d           Detection
		m           Machine
		interactive bool
		want        [][]string
		download    bool
		blocked     string
	}{
		{"formula installed", Detection{Brew: brew, Formula: true, App: "/Applications/Ollama.app"}, silicon, false,
			[][]string{{brew, "services", "start", "ollama"}}, false, ""},
		{"the app", Detection{Brew: brew, App: "/Applications/Ollama.app", Binary: "/opt/homebrew/bin/ollama"}, silicon, false,
			[][]string{{"/usr/bin/open", "-a", "Ollama"}}, false, ""},
		{"a binary elsewhere", Detection{Brew: brew, Binary: "/usr/local/bin/ollama"}, silicon, true,
			nil, false, "an ollama is installed at /usr/local/bin/ollama"},
		{"nothing, Homebrew on Apple silicon", Detection{Brew: brew}, silicon, false,
			[][]string{{brew, "install", "ollama"}, {brew, "services", "start", "ollama"}}, false, ""},
		{"nothing, no Homebrew, a terminal", Detection{}, silicon, true,
			[][]string{{"/usr/bin/open", "https://ollama.com/download"}}, true, ""},
		{"nothing, no Homebrew, a script", Detection{}, silicon, false,
			nil, false, "install Ollama from https://ollama.com/download"},
		{"nothing, Homebrew on Intel, a terminal", Detection{Brew: "/usr/local/bin/brew"}, intel, true,
			[][]string{{"/usr/bin/open", "https://ollama.com/download"}}, true, ""},
		{"nothing, Homebrew on Intel, a script", Detection{Brew: "/usr/local/bin/brew"}, intel, false,
			nil, false, "install Ollama from https://ollama.com/download"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fix, download, blocked := ollamaFix(tc.d, tc.m, tc.interactive)
			assert.Equal(t, tc.download, download)
			if tc.want == nil {
				assert.Nil(t, fix)
				assert.Contains(t, blocked, tc.blocked)
				return
			}
			require.NotNil(t, fix)
			assert.Equal(t, tc.want, fix.Commands)
			assert.Empty(t, blocked)
		})
	}
}

func TestIsLoopback(t *testing.T) {
	for url, want := range map[string]bool{
		"http://localhost:11434": true, "http://127.0.0.1:11434": true, "http://[::1]:11434": true,
		"http://ollama.lan:11434": false, "http://192.168.1.20:11434": false,
	} {
		assert.Equal(t, want, isLoopback(url), url)
	}
}

func TestHumanDuration(t *testing.T) {
	assert.Equal(t, "10 minutes", humanDuration(10*time.Minute))
	assert.Equal(t, "a minute", humanDuration(time.Minute))
	assert.Equal(t, "1m30s", humanDuration(90*time.Second))
	assert.Equal(t, "5s", humanDuration(5*time.Second))
}

func TestMinOllama(t *testing.T) {
	p := picks{embedding: wanted{Model: EmbeddingModel}}
	v, m := minOllama(p)
	assert.Empty(t, v)
	assert.Empty(t, m)
	p.generation = &wanted{Model: knownModel("gemma4:26b-a4b-it-qat")}
	v, m = minOllama(p)
	assert.Equal(t, "0.30.6", v)
	assert.Equal(t, "gemma4:26b-a4b-it-qat", m)
}
