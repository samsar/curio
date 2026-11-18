package modelgate

import (
	"strings"
	"sync"
	"time"
)

// Traffic is what each model on each server is busy with, by the gate whose
// feature called it: the calls in flight and when the last ended, and of
// them the long ones (an answer, a judgment, a check that writes answers),
// which hold the model for seconds. Ollama runs a model's requests one at a
// time, so a plan asked while an answer streams waits for it, and can run
// past its own timeout through no slowness of the model's; such a timeout
// must not count toward holding the planner (HeldSince). One Traffic is
// shared by every gate of a process; a gate without one records nothing and
// sees nothing. The zero value is ready to use, and it is safe for
// concurrent use.
//
// Times are time.Now readings, monotonic: nothing reports or stores them,
// and a call's began, which callers pass, is read the same way.
type Traffic struct {
	mu    sync.Mutex
	lines map[line]map[*Gate]*calls
}

// line is a model on a server: requests to it share its one queue.
type line struct{ server, model string }

// calls are one gate's calls of a model: in flight, and of them the long
// ones, and when the last of each ended.
type calls struct {
	inFlight, long      int
	lastEnd, lastLongAt time.Time
}

// lineOf is model on server as Traffic keys it: the server as
// config.Config.SharesModel compares it, a trailing slash aside.
func lineOf(server, model string) line {
	return line{server: strings.TrimRight(server, "/"), model: model}
}

// begin marks a call of g's beginning, long or not, and returns the func
// that marks it ended; ending it again does nothing.
func (t *Traffic) begin(g *Gate, long bool) (end func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lines == nil {
		t.lines = map[line]map[*Gate]*calls{}
	}
	l := lineOf(g.server, g.model)
	if t.lines[l] == nil {
		t.lines[l] = map[*Gate]*calls{}
	}
	c := t.lines[l][g]
	if c == nil {
		c = &calls{}
		t.lines[l][g] = c
	}
	c.inFlight++
	if long {
		c.long++
	}
	return sync.OnceFunc(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		now := time.Now()
		c.inFlight--
		c.lastEnd = now
		if long {
			c.long--
			c.lastLongAt = now
		}
	})
}

// since reports whether another gate's call of g's model on g's server,
// a long one when long, was in flight at some point since began: one in
// flight now, or one that ended after it.
func (t *Traffic) since(g *Gate, began time.Time, long bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for other, c := range t.lines[lineOf(g.server, g.model)] {
		switch {
		case other == g:
		case long && (c.long > 0 || !c.lastLongAt.Before(began)):
			return true
		case !long && (c.inFlight > 0 || !c.lastEnd.Before(began)):
			return true
		}
	}
	return false
}

// Hold marks a long call of the model beginning: one that holds it for
// seconds (an answer, a judgment of a claim's passages, a fitness check
// whose probes write answers), and returns the func that marks it ended,
// which its callers defer. Another feature's call that runs past its
// timeout meanwhile isn't counted toward holding it (HeldSince). It marks
// no use of the model (Use does, around each request).
func (g *Gate) Hold() (end func()) {
	if g.traffic == nil {
		return func() {}
	}
	return g.traffic.begin(g, true)
}

// HeldSince reports whether another feature's long call (Hold) held the
// model on its server at some point since began, a time.Now reading: a
// call of this one's that ran past its timeout meanwhile waited for it,
// which says nothing of the model's speed.
func (g *Gate) HeldSince(began time.Time) bool {
	return g.traffic != nil && g.traffic.since(g, began, true)
}

// SharedSince reports whether another feature called the model on its
// server at some point since began, a time.Now reading: a long call of
// this one's that ran past its deadline meanwhile shared the model, which
// says nothing certain of its speed.
func (g *Gate) SharedSince(began time.Time) bool {
	return g.traffic != nil && g.traffic.since(g, began, false)
}
