package ui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnderstood_Note: a search of the words says why from its status
// alone, never the daemon's reason; a planned or supplied one, or one with
// understanding off, says nothing.
func TestUnderstood_Note(t *testing.T) {
	for status, want := range map[string]string{
		"cold": "Searched your words: the model that understands questions (gemma4:26b) is loading; " +
			"press Enter again in a few seconds.",
		"checking": "Searched your words: curio is checking gemma4:26b; press Enter again in a few seconds.",
		"unfit": "Searched your words: gemma4:26b didn't pass curio's check for understanding questions; set " +
			"generation.roles.planner.model to a model that passes, or search.understand: false.",
		"timeout": "Searched your words: understanding took longer than 5 s; curio doctor says what helps when it " +
			"keeps doing so.",
		"unavailable": "Searched your words: gemma4:26b isn't available; curio doctor says why.",
		"invalid":     "Searched your words: the model's answer couldn't be used.",
		"too_long":    "Searched your words: the question is too long to understand; a shorter one can be.",
		"off":         "",
		"planned":     "",
		"supplied":    "",
	} {
		assert.Equal(t, want, Understood{Status: status, Model: "gemma4:26b", Timeout: 5 * time.Second}.Note(), status)
	}
	assert.Equal(t, "Searched your words: understanding took longer than 1.5 s; curio doctor says what helps when "+
		"it keeps doing so.", Understood{Status: "timeout", Timeout: 1500 * time.Millisecond}.Note())
}

// TestSearch_Understood: a plan that adds something is a line of chips,
// each the first page of the search without its filter; the undated
// documents' link switches whether they are kept; the words' link drops
// the plan; a plan that adds nothing shows no line.
func TestSearch_Understood(t *testing.T) {
	undated := 7
	s := Search{Query: "kafka talks", Type: "video", Page: 2, Plan: "P",
		Understood: &Understood{Status: "planned", Filters: []PlanFilter{
			{Field: "content_type", Values: []string{"video"}, Token: "no-type"},
			{Field: "saved", After: "2018-06-01", Before: "2018-09-23", Token: "no-saved"},
		}, Rewrites: []string{"kafka streams", "event logs"}, Names: []string{"Kafka Summit"}, Undated: &undated,
			UndatedToken: "with-undated"}}
	require.True(t, s.UnderstoodLine())
	assert.Equal(t, []Chip{
		{Label: "Videos", Href: "/ui/?content_type=video&plan=no-type&q=kafka+talks"},
		{Label: "Saved Jun 1 – Sep 22, 2018", Href: "/ui/?content_type=video&plan=no-saved&q=kafka+talks"},
	}, s.Chips())
	assert.Equal(t, &UndatedLine{Text: "7 undated left out", Action: "include them",
		Href: "/ui/?content_type=video&plan=with-undated&q=kafka+talks"}, s.UndatedLine())
	s.Understood.UndatedKept = true
	assert.Equal(t, "7 undated kept", s.UndatedLine().Text)
	assert.Equal(t, "leave them out", s.UndatedLine().Action)
	assert.Equal(t, "/ui/?content_type=video&q=kafka+talks", s.WordsHref())
	assert.Equal(t, "Also searched: kafka streams; event logs", s.Understood.AlsoSearched())
	assert.Equal(t, "Looked for: Kafka Summit", s.Understood.LookedFor())
	assert.Equal(t, "/ui/?content_type=video&page=2&plan=P&q=kafka+talks", s.Address())
	for _, tab := range s.TypeTabs() {
		assert.Contains(t, tab.Href, "plan=P", "a type keeps the plan")
	}

	s.Understood.Filters = s.Understood.Filters[:1]
	assert.Nil(t, s.UndatedLine(), "no date filter, nothing undated to say")
	for _, empty := range []*Understood{nil, {Status: "planned"}, {Status: "cold", Rewrites: []string{"x"}}} {
		s.Understood = empty
		assert.False(t, s.UnderstoodLine(), "%+v", empty)
		assert.Nil(t, s.Chips())
	}
}
