package insight

import (
	"context"
	"fmt"
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

// fakeGenerator returns a canned reply (or error), counts calls and keeps
// the last prompt.
type fakeGenerator struct {
	reply  string
	err    error
	calls  int
	prompt string
}

func (f *fakeGenerator) Generate(_ context.Context, prompt string, _ generator.Options) (string, error) {
	f.calls++
	f.prompt = prompt
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

// prompt is the prompt LLMLabeler sends for info.
func prompt(t *testing.T, info ClusterInfo) string {
	t.Helper()
	gen := &fakeGenerator{reply: "NAME: Topic\nSUMMARY: About it."}
	lab, err := NewLLMLabeler(gen).Label(context.Background(), info)
	require.NoError(t, err)
	assert.Equal(t, Label{Name: "Topic", Summary: "About it."}, lab)
	return gen.prompt
}

// TestLLMLabeler_InterestPrompt: an interest's prompt lists its titles,
// names its area when the area has a name, and its siblings' names, which
// its name must differ from; a second try names the name it took.
func TestLLMLabeler_InterestPrompt(t *testing.T) {
	titles := []string{"Building agents", "Agent harnesses"}
	alone := prompt(t, ClusterInfo{Titles: titles, Size: 2})
	assert.Equal(t, `These document titles were grouped together because they are about a similar topic:

- Building agents
- Agent harnesses
Give this group a short topic name (2 to 4 words), and a single-sentence summary of what it's about.
Respond in exactly this format and nothing else:
NAME: <topic name>
SUMMARY: <one sentence>`, alone)

	inArea := prompt(t, ClusterInfo{Titles: titles, Size: 2, Area: "Tech Skills and Strategy",
		Siblings: []string{"AI-Assisted Software Engineering", "AI Agent Engineering"}})
	assert.Contains(t, inArea, "- Agent harnesses\nThey belong to the broader area \"Tech Skills and Strategy\". "+
		"Its other topics are already named:\n- AI-Assisted Software Engineering\n- AI Agent Engineering\n"+
		"Give this group a short topic name (2 to 4 words) that is different from those names, and a single-sentence")

	unnamedArea := prompt(t, ClusterInfo{Titles: titles, Size: 2, Siblings: []string{"AI Agent Engineering"}})
	assert.NotContains(t, unnamedArea, "broader area", "no area line before the area has a name")
	assert.Contains(t, unnamedArea, "Other topics are already named:\n- AI Agent Engineering\n")
	assert.Contains(t, unnamedArea, "that is different from those names")

	retry := prompt(t, ClusterInfo{Titles: titles, Size: 2, Siblings: []string{"AI Agent Engineering"},
		Taken: "AI Agent Engineering"})
	assert.Contains(t, retry, "summary of what it's about.\nThe name \"AI Agent Engineering\" is already taken; "+
		"choose another.\nRespond in exactly this format")
	assert.NotContains(t, unnamedArea, "already taken")

	many := make([]string, 20)
	for i := range many {
		many[i] = fmt.Sprintf("Title %02d", i)
	}
	capped := prompt(t, ClusterInfo{Titles: many, Size: 20})
	assert.Contains(t, capped, "- Title 11\n")
	assert.NotContains(t, capped, "Title 12", "12 titles at most")
}

// TestLLMLabeler_AreaPrompt: an area is named from its interests' names
// and sizes and their titles, apart from the other areas.
func TestLLMLabeler_AreaPrompt(t *testing.T) {
	got := prompt(t, ClusterInfo{
		Titles: []string{"a1", "b1", "a2"}, Size: 131,
		Children: []ChildInfo{{Name: "AI Agent Engineering", Size: 86}, {Name: "", Size: 45}},
		Siblings: []string{"Behavioral Finance and Investing", "Personal Development"},
	})
	assert.Equal(t, `These topics form one broad area of a personal reading library:
- AI Agent Engineering (86 documents)
- an unnamed topic (45 documents)
Their most representative titles:
- a1
- b1
- a2
Other areas are already named: Behavioral Finance and Investing; Personal Development.
Give this area a short name (1 to 4 words) that covers all its topics and differs from the other areas' names, and a single-sentence summary.
Respond in exactly this format and nothing else:
NAME: <topic name>
SUMMARY: <one sentence>`, got)

	first := prompt(t, ClusterInfo{Titles: []string{"a1"}, Size: 10, Children: []ChildInfo{{Name: "Agents", Size: 10}},
		Taken: "Agents"})
	assert.NotContains(t, first, "Other areas")
	assert.Contains(t, first, "covers all its topics, and a single-sentence summary.\n"+
		"The name \"Agents\" is already taken; choose another.\n")
}

// TestTitleCache_RoundRobin: an area's titles are each interest's most
// central, then each one's second, up to the cap.
func TestTitleCache_RoundRobin(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5)
	gr := &grouped{ids: make([]string, len(f.vectors.dvs)), groups: make([]newGroup, 3)}
	for i, dv := range f.vectors.dvs {
		gr.ids[i] = dv.DocumentID
		g := &gr.groups[axisOf(Point{Vector: dv.Vector})]
		g.members = append(g.members, i)
	}
	c := &titleCache{docs: f.docs, gr: gr, titles: map[int][]string{}, next: map[int]int{}}
	got, err := c.roundRobin(context.Background(), []int{2, 1, 0}, 7)
	require.NoError(t, err)
	assert.Equal(t, []string{"group2 topic2 item7", "group1 topic1 item3", "group0 topic0 item0",
		"group2 topic2 item8", "group1 topic1 item4", "group0 topic0 item1", "group2 topic2 item9"}, got)
	got, err = c.roundRobin(context.Background(), []int{0}, 12)
	require.NoError(t, err)
	assert.Len(t, got, 3, "as many as there are")
}

// TestTermLabeler_SkipsSiblingWords: a term label reads apart from its
// siblings' names, and is empty when their words are all it has.
func TestTermLabeler_SkipsSiblingWords(t *testing.T) {
	l := NewTermLabeler()
	titles := []string{"Rust async runtimes", "Rust async traits", "Tokio runtimes"}
	lab, err := l.Label(context.Background(), ClusterInfo{Titles: titles, Siblings: []string{"Rust Programming"}})
	require.NoError(t, err)
	assert.Equal(t, "Async Runtimes Traits Tokio", lab.Name)
	lab, err = l.Label(context.Background(), ClusterInfo{Titles: []string{"Rust"}, Siblings: []string{"rust!"}})
	require.NoError(t, err)
	assert.Empty(t, lab.Name)
}

func TestLabelKey(t *testing.T) {
	for in, want := range map[string]string{
		"AI Agent Engineering":     "ai agent engineering",
		"  AI-Agent  engineering!": "ai agent engineering",
		"Café, Société":            "café société",
		"!!!":                      "",
		"":                         "",
		"C++ & Go 1.26":            "c go 1 26",
	} {
		assert.Equal(t, want, LabelKey(in), "%q", in)
	}
}
