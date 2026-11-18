package ask

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/modelgate"
)

// gateWords are how the gate's records and reasons name the answer writer.
var gateWords = modelgate.Words{Feature: "ask", Role: "answer writer", Calls: "answers", Resumes: "answering",
	Fallback: "questions get no answer", UnfitFix: UnfitFix}

// UnfitFix is what to do about an answer writer's model that failed its
// check.
const UnfitFix = "set generation.roles.answers.model to a model that passes, or ask.enabled: false"

// streamIdle bounds the silence between two pieces of an answer: Ollama
// writes several a second while it streams, so half a minute without one
// is a server or a connection that hung.
const streamIdle = 30 * time.Second

// withSlot runs fn holding the writer's slot, once it is free while actx
// lasts, saying so while it waits: an answer's model work, its judgment
// and its stream, never interleaves with another ask's. The slot is a
// long call of the model (modelgate.Gate.Hold): a plan or a rerank asked
// meanwhile waits for it in Ollama's queue.
func (r *run) withSlot(ctx, actx context.Context, fn func() (Done, error)) (Done, error) {
	waitBegan := time.Now()
	free, err := r.takeSlot(actx)
	r.timings.Wait += time.Since(waitBegan)
	if err != nil {
		return Done{}, err
	}
	if !free {
		return r.ended(ctx, actx, actx.Err())
	}
	defer func() { <-r.a.slot }()
	end := r.a.gate.Hold()
	defer end()
	return fn()
}

// stream writes the answer p asks for, citing its sources, rows or groups
// as its route does, on actx, under the writer's slot, and returns how it
// ended. A prompt the provider refuses as longer than the model's context
// is asked once more without its last source, when it has a shorter:
// refused before a word is written, nothing was sent of it. The retry is
// noted as its own cause, at WARN the first time since the last answer.
func (r *run) stream(ctx, actx context.Context, p writerPrompt) (Done, error) {
	a := r.a
	if err := r.emit(Progress{Stage: StageWriting, Model: a.opts.Model.Model}); err != nil {
		return Done{}, err
	}
	var text strings.Builder
	var emitErr error
	var usage generator.Usage
	write := func(prompt string) (generator.StreamEnd, error) {
		r.streamed = time.Now()
		return a.generate(actx, prompt, a.opts.MaxTokens, &usage, func(piece string) error {
			if text.Len() == 0 {
				r.timings.FirstToken = time.Since(r.began)
			}
			text.WriteString(piece)
			if err := r.emit(Delta{Text: piece}); err != nil {
				emitErr = err
				return err
			}
			return nil
		})
	}
	end, err := write(p.text)
	if errors.Is(err, generator.ErrPromptTooLong) && text.Len() == 0 && p.shorter != nil {
		a.gate.Note("retry:too_long", "ask: an answer's prompt ran past the answer writer's context; asking again "+
			"without its last source", err)
		p = *p.shorter
		r.sources = p.n
		end, err = write(p.text)
	}
	r.timings.PromptEval = usage.PromptEval
	switch {
	case emitErr != nil:
		return Done{}, emitErr
	case err != nil:
		done, err := r.failed(ctx, actx, err)
		done.Partial = text.Len() > 0
		return done, err
	}
	c := CitationsOf(text.String(), r.route, p.n)
	done := Done{Status: StatusAnswered, Citations: c.Cited, Markers: c.Markers, InvalidCitations: c.Invalid,
		Abstained: c.Abstains, Uncited: c.Uncited}
	if end.Truncated {
		done.Status = StatusTruncated
		done.Reason = fmt.Sprintf("the answer ran past %d tokens (ask.max_tokens)", a.opts.MaxTokens)
	}
	a.gate.Answered()
	a.gate.Succeeded()
	return done, nil
}

// takeSlot takes the writer's slot, waiting for it while actx lasts and
// saying so: whether it got it, and an emit's failure.
func (r *run) takeSlot(actx context.Context) (bool, error) {
	select {
	case r.a.slot <- struct{}{}:
		return true, nil
	default:
	}
	r.waited = true
	if err := r.emit(Progress{Stage: StageWaiting, Model: r.a.opts.Model.Model}); err != nil {
		return false, err
	}
	select {
	case r.a.slot <- struct{}{}:
		return true, nil
	case <-actx.Done():
		return false, nil
	}
}

// generate streams the writer's answer to prompt, greedily, at most
// maxTokens, on ctx, as a call of the model, reporting what it cost in
// usage (nil asks for none).
func (a *Asker) generate(ctx context.Context, prompt string, maxTokens int, usage *generator.Usage,
	emit func(string) error) (generator.StreamEnd, error) {
	end := a.gate.Use()
	defer end()
	return a.gen.GenerateStream(ctx, prompt, generator.Options{Temperature: new(0.0), MaxTokens: maxTokens,
		KeepAlive: a.gate.KeepAlive(), Idle: streamIdle, Usage: usage}, emit)
}

// failed is how an answer the model didn't finish ends: the caller's
// context ending is its error; the daemon's stop and an outage are
// StatusUnavailable, the ask's deadline StatusTimeout, which counts toward
// holding Ask unless the ask spent part of its time waiting on the model
// or another answer, or another feature called the model while the answer
// was written (a search's plan or rerank, which shares Ollama's queue
// with it). Each cause is logged at WARN the first time since the last
// answer, at DEBUG after.
func (r *run) failed(ctx, actx context.Context, err error) (Done, error) {
	a := r.a
	if actx.Err() != nil {
		done, err := r.ended(ctx, actx, err)
		if err == nil && done.Status == StatusTimeout {
			a.gate.Note("timeout", "ask: an answer took longer than its ask's timeout", nil)
			if !r.waited && !a.gate.SharedSince(r.streamed) {
				a.gate.TimedOut(a.opts.Timeout)
			}
		}
		return done, err
	}
	if errors.Is(err, generator.ErrPromptTooLong) {
		// The prompt alone is refused (Ollama counts no answer tokens in
		// it), and it fills the context by construction, so no key makes
		// it shorter: the pages' text took more tokens than curio counts
		// any text at.
		a.gate.Note("unavailable:too_long", "ask: an answer's prompt ran past the answer writer's context", err)
		return Done{Status: StatusUnavailable, Reason: fmt.Sprintf("the pages' text ran past the answer writer's "+
			"context (%v): it takes more tokens than curio counts for any text, which no setting changes", err)}, nil
	}
	a.gate.Note("unavailable:generate", "ask: the answer writer's model is unavailable", err)
	if errors.Is(err, generator.ErrModelMissing) {
		return Done{Status: StatusUnavailable, Reason: a.gate.NotPulled()}, nil
	}
	return Done{Status: StatusUnavailable, Reason: err.Error()}, nil
}
