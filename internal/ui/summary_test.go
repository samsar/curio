package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSummaryText: a description loses the stock opening a writing model
// gives it, what is left starting with a capital unless its first word is
// a name; anything else is shown as stored.
func TestSummaryText(t *testing.T) {
	for in, want := range map[string]string{
		"A collection of resources covering sourdough baking, starters and ovens.":      "Sourdough baking, starters and ovens.",
		"A collection of resources focused on trail running and race nutrition.":        "Trail running and race nutrition.",
		"A collection of tools, maps, and guides focused on long-distance cycling.":     "Tools, maps, and guides focused on long-distance cycling.",
		"This group focuses on houseplant care, from watering to repotting.":            "Houseplant care, from watering to repotting.",
		"This collection explores the history of mechanical watches.":                   "History of mechanical watches.",
		"This group covers various techniques for fermenting vegetables at home.":       "Techniques for fermenting vegetables at home.",
		"This collection examines tidal energy projects in northern Europe.":            "Tidal energy projects in northern Europe.",
		"This group consists of recipes for one-pot weeknight dinners.":                 "Recipes for one-pot weeknight dinners.",
		"These documents explore the economics of small bookshops.":                     "Economics of small bookshops.",
		"This collection provides strategies for learning a second language.":           "Strategies for learning a second language.",
		"Technical documentation covering low-level firmware for model trains.":         "Low-level firmware for model trains.",
		"Technical documentation for a fictional query language, Quux.":                 "A fictional query language, Quux.",
		"Resources covering beekeeping equipment and hive inspections.":                 "Beekeeping equipment and hive inspections.",
		"An analysis of board game design, balance and playtesting.":                    "Board game design, balance and playtesting.",
		"An examination of public transit fares across mid-sized cities.":               "Public transit fares across mid-sized cities.",
		"this group explores the craft of letterpress printing.":                        "Craft of letterpress printing.",
		"This group explores iOS widget layouts and their limits.":                      "iOS widget layouts and their limits.",
		"A collection of resources covering eBPF probes for tracing syscalls.":          "eBPF probes for tracing syscalls.",
		"This group covers The Beatles' albums and their recording sessions.":           "The Beatles' albums and their recording sessions.",
		"THIS GROUP COVERS the history of tide clocks.":                                 "History of tide clocks.",
		"This group explores how gardeners plan crop rotations.":                        "How gardeners plan crop rotations.",
		"This group explains the rules of contract bridge bidding.":                     "Rules of contract bridge bidding.",
		"These courses focus on beginner pottery and glazing.":                          "Beginner pottery and glazing.",
		"  This collection covers:   knitting patterns for beginners and their yarns. ": "Knitting patterns for beginners and their yarns.",

		// Shown as stored: no stock opening, or too little after one.
		"Sourdough baking, starters and ovens.":           "Sourdough baking, starters and ovens.",
		"Exploring the intersection of tea and ceramics.": "Exploring the intersection of tea and ceramics.",
		"This group covers bread.":                        "This group covers bread.",
		"A collection of":                                 "A collection of",
		"Thistle group care for gardens in clay soil.":    "Thistle group care for gardens in clay soil.",
		"":                          "",
		"<script>alert(1)</script>": "<script>alert(1)</script>",
	} {
		assert.Equal(t, want, summaryText(in), in)
	}
}

// TestCapitalizeFirst: only a first word all in lower case is capitalised.
func TestCapitalizeFirst(t *testing.T) {
	for in, want := range map[string]string{
		"stock markets": "Stock markets", "iOS apps": "iOS apps", "eBPF": "eBPF", "Rust": "Rust",
		"élan vital": "Élan vital", "3d printing": "3d printing", "": "", "x": "X",
	} {
		assert.Equal(t, want, capitalizeFirst(in), in)
	}
}
