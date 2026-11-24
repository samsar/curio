package ui

import (
	"slices"
	"strconv"
	"time"
)

// The search page's modes: what its box does with the words in it.
// ModeSearch, the default, searches them as they are typed; ModeAsk asks
// them as a question on Enter (static/ask.js).
const (
	ModeSearch = "search"
	ModeAsk    = "ask"
)

// ModeParam is the search page's query parameter of its mode. An address
// in Ask mode names its question and the mode alone, and is asked nothing
// as it loads: only Enter asks.
const ModeParam = "mode"

// AskOff is why the search page can't ask a question, as the page words
// it: AskCan when it can, holds and a model not checked yet included,
// whose answer's note then says why it didn't come.
type AskOff string

// Why the page can't ask.
const (
	AskCan          AskOff = ""
	AskOffSet       AskOff = "off"            // ask.enabled: false, or no asker
	AskOffByDefault AskOff = "off_by_default" // ask.enabled left out, a model not measured as the answer writer
	AskWithheld     AskOff = "withheld"       // the question would leave for a server the answers role doesn't name
	AskNoStream     AskOff = "no_stream"      // the answers role's provider can't stream
	AskUnfit        AskOff = "unfit"          // the writer's model failed its check
)

// Asking is what the search page knows of Ask, read once for a whole page
// from the asker's memory alone: why it can't ask (AskCan when it can),
// the answers role's model, how long an ask may take, the longest
// question it takes, in bytes, and every status an ask can end with
// (ask.Statuses), which the page words for ask.js.
type Asking struct {
	Off         AskOff
	Model       string
	Timeout     time.Duration
	MaxQuestion int
	Statuses    []string
}

// The notes ask.js shows beside a status's, each Go's words, never the
// daemon's: an answer's flags (partial, uncited, invalid citations); the
// evidence route's pages judged with none for or against the claim, or
// not judged; an ask that ended without its done event, by why: the
// daemon's own failure (its error event), a refusal before the stream,
// by kind (the question, curio busy with other asks, curio starting, any
// other), the connection lost, or the answer left for Search; and the
// line naming the request, which the daemon's log names too.
const (
	askNotePartial  = "partial"
	askNoteUncited  = "uncited"
	askNoteInvalid  = "invalid"
	askNoteUnsided  = "unsided"
	askNoteUnjudged = "unjudged"
	askNoteFailed   = "failed"
	askNoteQuestion = "question"
	askNoteBusy     = "busy"
	askNoteStarting = "starting"
	askNoteRefused  = "refused"
	askNoteLost     = "lost"
	askNoteStopped  = "stopped"
	askNoteRequest  = "request"
)

// askNotes are the notes ask.js shows beside the statuses'.
var askNotes = []string{askNotePartial, askNoteUncited, askNoteInvalid, askNoteUnsided, askNoteUnjudged,
	askNoteFailed, askNoteQuestion, askNoteBusy, askNoteStarting, askNoteRefused, askNoteLost, askNoteStopped,
	askNoteRequest}

// startingProblem is the problem type of a starting daemon's every answer
// (api.StartingProblemType, which a test holds it to), which tells ask.js
// a 503 of a daemon starting from one of a daemon busy with other asks.
const startingProblem = "urn:curio:problem:daemon-starting"

// The words of the search page's box and its button in each mode.
const (
	searchLabel    = "Search"
	askLabel       = "Ask"
	askPlaceholder = "Ask your library a question"
)

// CanAsk reports whether the page offers Ask: its switch, the answer's
// shell and ask.js.
func (s Search) CanAsk() bool { return s.Asking != nil && s.Asking.Off == AskCan }

// AskMode reports whether the page opens in Ask mode.
func (s Search) AskMode() bool { return s.Mode == ModeAsk }

// DataMode is the mode the page opens in, as its data-mode names it, and
// app.css shows the search's results or the answer by.
func (s Search) DataMode() string {
	if s.AskMode() {
		return ModeAsk
	}
	return ModeSearch
}

// ModeToggle is one of the two buttons of the switch between searching
// and asking: its id, the mode it selects, and the words the box's button
// and placeholder take in that mode, which ask.js sets as it switches.
type ModeToggle struct {
	ID, Mode, Label, Placeholder string
	Pressed                      bool
}

// Toggles are the switch between searching and asking, the page's mode
// pressed; none when the page can't ask. The box and its button carry
// Search's words in either mode (Placeholder): ask.js, which alone asks,
// gives them Ask's.
func (s Search) Toggles() []ModeToggle {
	if !s.CanAsk() {
		return nil
	}
	return []ModeToggle{
		{ID: "mode-search", Mode: ModeSearch, Label: searchLabel, Placeholder: s.Placeholder(),
			Pressed: !s.AskMode()},
		{ID: "mode-ask", Mode: ModeAsk, Label: askLabel, Placeholder: askPlaceholder, Pressed: s.AskMode()},
	}
}

// AskShell is where ask.js writes an answer: the addresses it asks and
// links to, built here so that it holds none of its own, the problem type
// of a starting daemon, and the notes it shows, in Go's words, by key.
type AskShell struct {
	Notes []AskNote
}

// AskNote is one note ask.js may show: an ask's status, or one of its
// flags (partial, uncited, invalid citations), and its words. The
// answered status's is the one line said once an answer is whole.
type AskNote struct {
	Key, Text string
}

// Src is where ask.js asks.
func (AskShell) Src() string { return askAPIPath }

// PageHref is the search page, which ask.js keeps its address on.
func (AskShell) PageHref() string { return navHref(NavSearch) }

// DocumentPrefix is a document's page, less its ID.
func (AskShell) DocumentPrefix() string { return documentHref("") }

// InterestPrefix is an area's or an interest's page, less its ID.
func (AskShell) InterestPrefix() string { return interestHref("") }

// UnsortedPage is Unsorted's page.
func (AskShell) UnsortedPage() string { return unsortedHref(1, "") }

// StartingType is the problem type of a starting daemon's answers.
func (AskShell) StartingType() string { return startingProblem }

// Shell is the answer's shell, nil when the page can't ask: a note for
// every status an ask can end with and each of askNotes.
func (s Search) Shell() *AskShell {
	if !s.CanAsk() {
		return nil
	}
	keys := append(slices.Clone(s.Asking.Statuses), askNotes...)
	shell := &AskShell{Notes: make([]AskNote, 0, len(keys))}
	for _, key := range keys {
		shell.Notes = append(shell.Notes, AskNote{Key: key, Text: s.Asking.note(key)})
	}
	return shell
}

// model is the answers role's model, as the notes name it.
func (a Asking) model() string {
	if a.Model == "" {
		return "the answers role's model"
	}
	return a.Model
}

// unfitNote says what to do about a model that failed its check.
func (a Asking) unfitNote() string {
	return a.model() + " didn't pass curio's check for writing answers; set generation.roles.answers.model to " +
		"a model that passes, or ask.enabled: false."
}

// note is the words of an ask's status or flag, from it and the page's
// facts alone, never the daemon's reason: "" for a key it doesn't word,
// which TestAskShell catches for every status.
func (a Asking) note(key string) string {
	switch key {
	case "answered":
		return "Answer ready."
	case "truncated":
		return "The answer ran past its length (ask.max_tokens) and stops there."
	case "no_sources":
		return "curio found nothing in your library to answer this from."
	case "off":
		return "Ask is off now; curio doctor says why."
	case "withheld":
		return "Nothing was sent: the answers role's model runs off this machine, or with another provider, " +
			"which generation.roles.answers doesn't name."
	case "unfit":
		return a.unfitNote()
	case "timeout":
		return "The answer took longer than " + strconv.FormatFloat(a.Timeout.Seconds(), 'f', -1, 64) +
			" s (ask.timeout_seconds), or answers keep doing so and Ask rests a while; curio doctor says what helps."
	case "unavailable":
		return a.model() + " isn't available: it isn't pulled, or Ollama isn't running; curio doctor says why."
	case askNotePartial:
		return "The answer stopped before it was finished."
	case askNoteUncited:
		return "This answer cites none of your pages: check them before relying on it."
	case askNoteInvalid:
		return "Some citations name pages the answer wasn't given; they aren't linked."
	case askNoteUnsided:
		return "None of the pages curio judged supports or contradicts the claim, so there is nothing to answer from."
	case askNoteUnjudged:
		return "The pages couldn't be judged for and against the claim, so it is answered from the pages found."
	case askNoteFailed:
		return "curio failed while answering; the daemon's log says what went wrong."
	case askNoteQuestion:
		if a.MaxQuestion > 0 {
			return "curio couldn't take the question as written: a question is at most " +
				strconv.Itoa(a.MaxQuestion) + " bytes."
		}
		return "curio couldn't take the question as written."
	case askNoteBusy:
		return "curio is answering as many questions as it takes at once; ask again once one of them ends."
	case askNoteStarting:
		return "curio is starting; ask again in a moment."
	case askNoteRefused:
		return "curio didn't take the question."
	case askNoteLost:
		return "The connection to curio was lost before the answer ended; curio status says whether the daemon runs."
	case askNoteStopped:
		return "Switching to Search stopped the answer before it ended; press Enter to ask again."
	case askNoteRequest:
		return "Its request ID in the daemon's log:"
	}
	return ""
}

// AskOffNote is the quiet line of a page whose address asked for Ask mode
// but that can't ask, which searched its words instead: why, and what
// turns Ask on, from the kind alone; "" otherwise.
func (s Search) AskOffNote() string {
	if !s.Asked || s.Asking == nil || s.Asking.Off == AskCan {
		return ""
	}
	const words = "Searched your words: "
	a := s.Asking
	switch a.Off {
	case AskOffByDefault:
		return words + "Ask is off by default for " + a.model() + ", which isn't measured for writing answers; " +
			"ask.enabled: true turns it on (then restart)."
	case AskWithheld:
		return words + "Ask would send your question and pages off this machine, or to another provider, which " +
			"generation.roles.answers doesn't name; name it there, or ask.enabled: false."
	case AskNoStream:
		return words + "Ask is off: the answers role's provider can't stream answers."
	case AskUnfit:
		return words + a.unfitNote()
	case AskOffSet, AskCan:
	}
	return words + "Ask is off (ask.enabled: false)."
}
