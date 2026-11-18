package modelgate

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trafficGate is a gate of feature on model at server, recording into
// traffic.
func trafficGate(traffic *Traffic, server, model string) *Gate {
	return New(context.Background(), nil, Options{Words: testWords, Model: model, Server: server, Traffic: traffic})
}

// TestTraffic_HeldSince: a call that began at a time is held when another
// feature's long call of the same model on the same server was in flight
// at some point since: one in flight now, or one that ended after it
// began. A long call that ended before, an ordinary call, the gate's own
// long call, and another model's or server's are no hold; SharedSince
// counts another feature's ordinary calls too.
func TestTraffic_HeldSince(t *testing.T) {
	const own, other, model2, server2 = "own", "other", "another model", "another server"
	for _, tc := range []struct {
		name         string
		by           string // the gate that makes the call
		long         bool
		ended        string // "" in flight, "since" after the timed call began, "before" it
		held, shared bool
	}{
		{name: "a long call in flight", by: other, long: true, held: true, shared: true},
		{name: "a long call that ended since", by: other, long: true, ended: "since", held: true, shared: true},
		{name: "a long call that ended before", by: other, long: true, ended: "before"},
		{name: "an ordinary call", by: other, shared: true},
		{name: "an ordinary call that ended since", by: other, ended: "since", shared: true},
		{name: "an ordinary call that ended before", by: other, ended: "before"},
		{name: "the gate's own long call", by: own, long: true},
		{name: "another model's long call", by: model2, long: true},
		{name: "another server's long call", by: server2, long: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				traffic := &Traffic{}
				gates := map[string]*Gate{
					own:     trafficGate(traffic, "http://localhost:11434", "m:1"),
					other:   trafficGate(traffic, "http://localhost:11434/", "m:1"),
					model2:  trafficGate(traffic, "http://localhost:11434", "m:2"),
					server2: trafficGate(traffic, "http://studio.example:11434", "m:1"),
				}
				call := gates[tc.by].Use
				if tc.long {
					call = gates[tc.by].Hold
				}
				end := call()
				if tc.ended == "before" {
					end()
					time.Sleep(time.Second)
				}
				began := time.Now()
				time.Sleep(time.Second)
				if tc.ended == "since" {
					end()
					time.Sleep(time.Second)
				}
				assert.Equal(t, tc.held, gates[own].HeldSince(began), "held")
				assert.Equal(t, tc.shared, gates[own].SharedSince(began), "shared")
				end()
			})
		})
	}
}

// TestTraffic_None: a gate without traffic records nothing and is never
// held.
func TestTraffic_None(t *testing.T) {
	g := trafficGate(nil, "", "m:1")
	began := time.Now()
	g.Hold()()
	g.Use()()
	assert.False(t, g.HeldSince(began))
	assert.False(t, g.SharedSince(began))
}

// TestGate_SlowCheckWhileHeld: a check whose probe ran out of its time
// while another feature's long call held the model waited for that call,
// which says nothing of the model's speed: it holds nothing, and the
// check runs again after RetryAfter.
func TestGate_SlowCheckWhileHeld(t *testing.T) {
	traffic := &Traffic{}
	other := trafficGate(traffic, "http://localhost:11434", "m:1")
	r := newRig(t, &fakeModel{loaded: true}, func(o *Options) {
		o.Traffic, o.Server = traffic, "http://localhost:11434"
	})
	r.setCheck(func(context.Context) Verdict {
		end := other.Hold()
		defer end()
		return Verdict{Probe: "p", Slow: true, Err: errors.New("took longer than 14s")}
	})
	_, _ = r.ready(t)
	ref, ok := r.ready(t)
	assert.False(t, ok)
	assert.Equal(t, Unavailable, ref.Kind, "%+v", ref)
	assert.Zero(t, r.g.State().TimeoutHoldUntil, "no hold")
	assert.NotEqual(t, "timeout:check", r.g.State().Failure.Cause)

	r.setCheck(func(context.Context) Verdict { return Verdict{} })
	r.clock.advance(RetryAfter)
	_, _ = r.ready(t)
	ref, ok = r.ready(t)
	require.True(t, ok, "checked again a minute after: %+v", ref)
	assert.Equal(t, int32(2), r.checks.Load())
}
