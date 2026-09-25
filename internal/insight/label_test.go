package insight

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/generator"
)

func TestTermLabeler_NamesByFrequentTerms(t *testing.T) {
	l := NewTermLabeler()
	lab, err := l.Label(context.Background(), ClusterInfo{
		Titles: []string{
			"Machine Learning Basics",
			"Deep Learning and Neural Networks",
			"Learning Rate Schedules",
		},
		Size: 3,
	})
	require.NoError(t, err)
	// "learning" appears in all three titles → should lead the label.
	assert.Contains(t, strings.ToLower(lab.Name), "learning")
	// Stopwords ("and") must not appear.
	assert.NotContains(t, strings.ToLower(lab.Name), "and")
}

func TestTermLabeler_EmptyOnNoSalientTerms(t *testing.T) {
	l := NewTermLabeler()
	lab, err := l.Label(context.Background(), ClusterInfo{Titles: []string{"a", "of", "the"}})
	require.NoError(t, err)
	assert.Empty(t, lab.Name)
}

func TestParseLabel(t *testing.T) {
	cases := []struct {
		name, in, wantName, wantSummary string
	}{
		{
			name:        "well formed",
			in:          "NAME: Distributed Systems\nSUMMARY: Articles about consensus and replication.",
			wantName:    "Distributed Systems",
			wantSummary: "Articles about consensus and replication.",
		},
		{name: "wrapping quotes stripped", in: `NAME: "Web Security"`, wantName: "Web Security"},
		{name: "dash separator", in: "NAME - DNS Fundamentals", wantName: "DNS Fundamentals"},
		{
			name:        "markdown bold around the field",
			in:          "**NAME:** DNS Fundamentals\n**SUMMARY:** about DNS",
			wantName:    "DNS Fundamentals",
			wantSummary: "about DNS",
		},
		{name: "lowercase field after a preamble", in: "Sure!\nname: Rust Programming", wantName: "Rust Programming"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLabel(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.wantName, got.Name)
			assert.Equal(t, tc.wantSummary, got.Summary)
		})
	}
}

func TestParseLabel_RejectsRepliesWithoutAName(t *testing.T) {
	for _, reply := range []string{
		"SUMMARY: Kubernetes operations articles.",
		"Sure! Here you go:\nKubernetes Ops",
		"Here is the label:\n\nNAME Kubernetes", // no separator: not a field
		"  Rust Programming\n",                  // a bare name is indistinguishable from a preamble
		"NAME: Sure, here is the topic name you asked me for",
		"",
	} {
		t.Run(reply, func(t *testing.T) {
			_, err := parseLabel(reply)
			require.ErrorIs(t, err, ErrUnparseableLabel)
		})
	}
}

// fakeGenerator returns a canned reply (or error) and counts calls.
type fakeGenerator struct {
	reply string
	err   error
	calls int
}

func (f *fakeGenerator) Generate(context.Context, string, generator.Options) (string, error) {
	f.calls++
	return f.reply, f.err
}
func (*fakeGenerator) Model() string              { return "fake" }
func (*fakeGenerator) Ping(context.Context) error { return nil }

func TestLLMLabeler_UnparseableReply(t *testing.T) {
	reply := "Sure! Here you go:\n" + strings.Repeat("機", 300)
	_, err := NewLLMLabeler(&fakeGenerator{reply: reply}).Label(context.Background(), ClusterInfo{Titles: []string{"a"}})
	require.ErrorIs(t, err, ErrUnparseableLabel)
	assert.True(t, utf8.ValidString(err.Error()), "the reply excerpt is cut on a rune boundary")
	assert.Less(t, len(err.Error()), len(reply), "the error quotes only an excerpt of the reply")
}

func TestTermLabeler_UnicodeWords(t *testing.T) {
	l := NewTermLabeler()
	lab, err := l.Label(context.Background(), ClusterInfo{Titles: []string{
		"Montréal café guide",
		"Montréal bakery café",
	}})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(lab.Name, "Montréal Café"), "got %q", lab.Name)

	lab, err = l.Label(context.Background(), ClusterInfo{Titles: []string{"機械学習 入門", "深層学習 入門"}})
	require.NoError(t, err)
	require.NotEmpty(t, lab.Name)
	for word := range strings.FieldsSeq(lab.Name) {
		assert.Contains(t, []string{"機械学習", "深層学習"}, word, "labels are built from whole tokens")
	}
}

func TestCleanLabel_CapsInRunes(t *testing.T) {
	got := cleanLabel(strings.Repeat("機", 150)) // 450 bytes
	assert.True(t, utf8.ValidString(got))
	assert.Equal(t, 150, utf8.RuneCountInString(got), "under the rune cap, nothing is cut")

	got = cleanLabel(strings.Repeat("機", 250))
	assert.True(t, utf8.ValidString(got))
	assert.Equal(t, maxLabelRunes, utf8.RuneCountInString(got))
}
