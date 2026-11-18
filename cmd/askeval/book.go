package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/generator"
)

// call is one model call as a book records it: its key (callKey), what
// the model replied, whether it stopped at its cap, and what it cost, the
// prompt's reading among it.
type call struct {
	Type         string `json:"type"` // call
	Key          string `json:"key"`
	Kind         string `json:"kind"` // stream or json
	Text         string `json:"text"`
	Truncated    bool   `json:"truncated,omitempty"`
	TookMS       int64  `json:"took_ms"`
	PromptTokens int    `json:"prompt_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	PromptEvalMS int64  `json:"prompt_eval_ms,omitempty"`
}

// callKey keys a call by everything that changes its reply: the model,
// the prompts' version, the options that shape the reply, and the prompt.
func callKey(model, version, options, prompt string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + version + "\x00" + options + "\x00" + prompt))
	return hex.EncodeToString(sum[:])
}

// askRecord is one question's ask as a book records it: what its
// retrieval came to, the plan as applied and the head's order, which a
// replay sends back as supplied so no planner or reranker is asked, and
// the sources' documents, the status, the markers cited, every stage's
// timing and the answer call's tokens.
type askRecord struct {
	Type         string      `json:"type"` // ask
	Variant      string      `json:"variant"`
	ID           string      `json:"id"`
	Route        string      `json:"route"`
	Plan         *planRecord `json:"plan,omitempty"`
	Order        []string    `json:"order,omitempty"`
	Sources      []string    `json:"sources"`
	Status       string      `json:"status"`
	FirstMS      int64       `json:"first_token_ms"`
	TotalMS      int64       `json:"total_ms"`
	Stages       stageMS     `json:"stages"`
	PromptTokens int         `json:"prompt_tokens,omitempty"`
	OutputTokens int         `json:"output_tokens,omitempty"`
	Answer       string      `json:"answer"`
	Citation     []int       `json:"citations,omitempty"`
	Markers      []string    `json:"markers,omitempty"`
}

// stageMS are an ask's stages' timings, in milliseconds, as its done
// event reports them.
type stageMS struct {
	Wait       int64 `json:"wait_ms"`
	Plan       int64 `json:"plan_ms"`
	Search     int64 `json:"search_ms"`
	Rerank     int64 `json:"rerank_ms"`
	Sources    int64 `json:"sources_ms"`
	Judge      int64 `json:"judge_ms"`
	PromptEval int64 `json:"prompt_eval_ms"`
	FirstToken int64 `json:"first_token_ms"`
	Total      int64 `json:"total_ms"`
}

// stagesOf are t in milliseconds.
func stagesOf(t ask.Timings) stageMS {
	return stageMS{Wait: t.Wait.Milliseconds(), Plan: t.Plan.Milliseconds(), Search: t.Search.Milliseconds(),
		Rerank: t.Rerank.Milliseconds(), Sources: t.Sources.Milliseconds(), Judge: t.Judge.Milliseconds(),
		PromptEval: t.PromptEval.Milliseconds(), FirstToken: t.FirstToken.Milliseconds(), Total: t.Total.Milliseconds()}
}

// book is a file of calls and asks, a JSON line each: those read from
// replay, and those appended to record as they are made. Safe for
// concurrent use.
type book struct {
	mu     sync.Mutex
	calls  map[string]call
	asks   map[string]askRecord // by variant and question
	record io.WriteCloser       // nil records nothing
}

// askKey keys an ask by its variant and question.
func askKey(variant, id string) string { return variant + "/" + id }

// openBook reads the calls of replay ("" for none), and appends new ones
// to record ("" for none), created 0600: they hold what pages say.
func openBook(replay, record string) (*book, error) {
	b := &book{calls: map[string]call{}, asks: map[string]askRecord{}}
	if replay != "" {
		if err := b.read(replay); err != nil {
			return nil, err
		}
	}
	if record != "" {
		f, err := os.OpenFile(record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open the book to record: %w", err)
		}
		b.record = f
	}
	return b, nil
}

// read loads path's calls, a later one of a key winning.
func (b *book) read(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open the book to replay: %w", err)
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	lines.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for n := 1; lines.Scan(); n++ {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(lines.Bytes(), &kind); err != nil {
			return fmt.Errorf("%s:%d: %w", path, n, err)
		}
		switch kind.Type {
		case "call":
			var c call
			if err := json.Unmarshal(lines.Bytes(), &c); err != nil {
				return fmt.Errorf("%s:%d: %w", path, n, err)
			}
			b.calls[c.Key] = c
		case "ask":
			var a askRecord
			if err := json.Unmarshal(lines.Bytes(), &a); err != nil {
				return fmt.Errorf("%s:%d: %w", path, n, err)
			}
			b.asks[askKey(a.Variant, a.ID)] = a
		default:
			return fmt.Errorf("%s:%d: a line of type %q", path, n, kind.Type)
		}
	}
	if err := lines.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

// lookup is the call recorded under key.
func (b *book) lookup(key string) (call, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.calls[key]
	return c, ok
}

// recording reports whether new calls are recorded.
func (b *book) recording() bool { return b.record != nil }

// add keeps c, and appends it to the book being recorded.
func (b *book) add(c call) error {
	c.Type = "call"
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls[c.Key] = c
	return b.appendLocked(c)
}

// addAsk keeps a, and appends it to the book being recorded.
func (b *book) addAsk(a askRecord) error {
	a.Type = "ask"
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asks[askKey(a.Variant, a.ID)] = a
	return b.appendLocked(a)
}

// askOf is the ask recorded for variant's question id.
func (b *book) askOf(variant, id string) (askRecord, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.asks[askKey(variant, id)]
	return a, ok
}

// appendLocked appends v as a line of the book being recorded. b.mu is
// held.
func (b *book) appendLocked(v any) error {
	if b.record == nil {
		return nil
	}
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode a book line: %w", err)
	}
	if _, err := b.record.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("record a book line: %w", err)
	}
	return nil
}

// close closes the book being recorded.
func (b *book) close() error {
	if b.record == nil {
		return nil
	}
	return b.record.Close()
}

// errNotRecorded is a call a book has no answer to, while nothing is
// recorded: the model isn't asked.
var errNotRecorded = errors.New("the book has no answer to this call, and -answers-out isn't set to ask the model")

// writerModel is the answer writer's model when recording: it streams
// answers and judges a claim's passages.
type writerModel interface {
	generator.StreamGenerator
	generator.JSONGenerator
}

// streamBook answers an answer writer's streams and structured calls (the
// evidence route's judgment) from a book, asking the model (live, nil
// while only replaying) for a call it hasn't and recording its reply. The
// question being asked is named in errors. It keeps each question's last
// answer prompt, and what its answer call cost, for the judge and the
// report.
type streamBook struct {
	model, version string
	book           *book
	live           writerModel

	mu       sync.Mutex
	question string
	prompt   string
	cost     call
	// fromBook says the question's answer call was the book's, not made
	// by this run: its first token and total say nothing of the model.
	fromBook bool
}

// asking names the question whose calls come next.
func (s *streamBook) asking(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.question, s.prompt, s.cost, s.fromBook = id, "", call{}, false
}

// answered is the current question's last answer prompt, what its call
// cost (its tokens and its prompt's reading), and whether the book
// answered it rather than the model.
func (s *streamBook) answered() (string, call, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prompt, s.cost, s.fromBook
}

// streamed keeps prompt as the current question's answer prompt, c as
// what it cost, and whether the book answered it.
func (s *streamBook) streamed(prompt string, c call, fromBook bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompt, s.cost, s.fromBook = prompt, c, fromBook
}

func (s *streamBook) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.question
}

// stream answers prompt from the book, or from the live model.
func (s *streamBook) stream(ctx context.Context, prompt string, opts generator.Options,
	emit func(string) error) (generator.StreamEnd, error) {
	key := callKey(s.model, s.version, fmt.Sprintf("max_tokens=%d", opts.MaxTokens), prompt)
	if c, ok := s.book.lookup(key); ok {
		if c.Text != "" {
			if err := emit(c.Text); err != nil {
				return generator.StreamEnd{}, err
			}
		}
		s.streamed(prompt, c, true)
		if opts.Usage != nil {
			opts.Usage.PromptEval = time.Duration(c.PromptEvalMS) * time.Millisecond
		}
		return generator.StreamEnd{Truncated: c.Truncated}, nil
	}
	if s.live == nil || !s.book.recording() {
		return generator.StreamEnd{}, fmt.Errorf("question %s: %w", s.current(), errNotRecorded)
	}
	var text strings.Builder
	var usage generator.Usage
	asked := opts.Usage
	opts.Usage = &usage
	began := time.Now()
	end, err := s.live.GenerateStream(ctx, prompt, opts, func(piece string) error {
		text.WriteString(piece)
		return emit(piece)
	})
	if asked != nil {
		*asked = usage
	}
	if err != nil {
		return end, err
	}
	c := call{Key: key, Kind: "stream", Text: text.String(), Truncated: end.Truncated,
		TookMS: time.Since(began).Milliseconds(), PromptTokens: usage.PromptTokens, OutputTokens: usage.OutputTokens,
		PromptEvalMS: usage.PromptEval.Milliseconds()}
	s.streamed(prompt, c, false)
	return end, s.book.add(c)
}

// json answers a structured call from the book, or from the live model,
// recording its reply, keyed like a stream with its schema among the
// options that shape the reply.
func (s *streamBook) json(ctx context.Context, prompt string, schema json.RawMessage, opts generator.Options) (
	json.RawMessage, error) {
	key := callKey(s.model, s.version, fmt.Sprintf("json max_tokens=%d %s", opts.MaxTokens, schema), prompt)
	if c, ok := s.book.lookup(key); ok {
		return json.RawMessage(c.Text), nil
	}
	if s.live == nil || !s.book.recording() {
		return nil, fmt.Errorf("question %s: %w", s.current(), errNotRecorded)
	}
	began := time.Now()
	raw, err := s.live.GenerateJSON(ctx, prompt, schema, opts)
	if err != nil {
		return nil, err
	}
	return raw, s.book.add(call{Key: key, Kind: "json", Text: string(raw), TookMS: time.Since(began).Milliseconds()})
}

// liveWriter is the answer writer's model, its streams and structured
// calls through a book; it loads, checks and unloads as the model does.
type liveWriter struct {
	*generator.Ollama
	book *streamBook
}

func (w liveWriter) GenerateStream(ctx context.Context, prompt string, opts generator.Options,
	emit func(string) error) (generator.StreamEnd, error) {
	return w.book.stream(ctx, prompt, opts, emit)
}

func (w liveWriter) GenerateJSON(ctx context.Context, prompt string, schema json.RawMessage,
	opts generator.Options) (json.RawMessage, error) {
	return w.book.json(ctx, prompt, schema, opts)
}

// replayWriter answers from a book alone: it has no model to load, so the
// asker takes it as loaded.
type replayWriter struct {
	model string
	book  *streamBook
}

func (replayWriter) Generate(context.Context, string, generator.Options) (string, error) {
	return "", errors.New("a replayed writer only streams")
}
func (w replayWriter) Model() string            { return w.model }
func (replayWriter) Ping(context.Context) error { return nil }
func (w replayWriter) GenerateStream(ctx context.Context, prompt string, opts generator.Options,
	emit func(string) error) (generator.StreamEnd, error) {
	return w.book.stream(ctx, prompt, opts, emit)
}

func (w replayWriter) GenerateJSON(ctx context.Context, prompt string, schema json.RawMessage,
	opts generator.Options) (json.RawMessage, error) {
	return w.book.json(ctx, prompt, schema, opts)
}
