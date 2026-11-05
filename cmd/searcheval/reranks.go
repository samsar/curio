package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/rerank"
)

// answer is one call of the reranker's model as -reranks-out records it
// and -reranks replays it: the key it answers (callKey), the method, the
// listwise order the model gave or the pointwise call's top tokens, and
// how long the call took. It holds no page text, but the file is a
// record of the owner's searches all the same: keep it out of the
// repository.
type answer struct {
	Key    string                  `json:"key"`
	Method rerank.Method           `json:"method"`
	Order  json.RawMessage         `json:"order,omitempty"`
	Tokens []generator.TokenChoice `json:"tokens,omitempty"`
	TookMS float64                 `json:"took_ms"`
}

// errNotRecorded is a call the -reranks file has no answer to, with no
// -reranks-out to ask the model and record one.
var errNotRecorded = errors.New("no recorded answer")

// callKey is the hex SHA-256 of what decides a call's answer: the model,
// the method, the prompts' version, the generation options that change a
// reply, and the exact prompt. A change of any is another call.
func callKey(model string, m rerank.Method, opts string, prompt string) string {
	sum := sha256.Sum256([]byte(model + "\n" + string(m) + "\n" + rerank.PromptVersion + "\n" + opts + "\n" + prompt))
	return hex.EncodeToString(sum[:])
}

// answerBook is the reranker's model as the tool asks it: each call is
// answered from the book when it holds the call's key, else by the model
// (live), its answer recorded when the book writes one; with no model, a
// call the book doesn't hold is errNotRecorded. It times every call, and
// keeps, until take, the recorded time of the calls it answered and the
// time they took in this run. One call at a time: the tool asks
// sequentially, as the daemon does.
type answerBook struct {
	generator.Generator
	model  string
	jsonG  generator.JSONGenerator    // nil without a model
	logpG  generator.LogprobGenerator // nil without a model
	warmer generator.Warmer           // nil without a model
	live   bool                       // a call the book doesn't hold asks the model
	out    *bufio.Writer              // where answers are recorded; nil when none is
	file   *os.File

	mu       sync.Mutex
	known    map[string]answer
	recorded time.Duration
	measured time.Duration
	missed   bool
	asked    int
	replayed int
}

// newAnswerBook builds the book over gen (nil for a replay that asks no
// model), with the answers in replayPath ("" for none), recording what the
// model answers to outPath ("" for no record), opened to append, 0600.
func newAnswerBook(gen generator.Generator, model, replayPath, outPath string) (*answerBook, error) {
	b := &answerBook{Generator: gen, model: model, known: map[string]answer{}, live: gen != nil}
	if gen != nil {
		b.jsonG, _ = gen.(generator.JSONGenerator)
		b.logpG, _ = gen.(generator.LogprobGenerator)
		b.warmer, _ = gen.(generator.Warmer)
	}
	if replayPath != "" {
		if err := b.read(replayPath); err != nil {
			return nil, err
		}
	}
	if outPath != "" {
		f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("the -reranks-out file: %w", err)
		}
		b.file, b.out = f, bufio.NewWriter(f)
	}
	return b, nil
}

// read loads the answers of a -reranks file, a JSON line each.
func (b *answerBook) read(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("the -reranks file: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for line := 1; sc.Scan(); line++ {
		var a answer
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			return fmt.Errorf("the -reranks file %s, line %d: %w", path, line, err)
		}
		b.known[a.Key] = a
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read the -reranks file %s: %w", path, err)
	}
	return nil
}

// Close writes out what was recorded.
func (b *answerBook) Close() error {
	if b.file == nil {
		return nil
	}
	if err := b.out.Flush(); err != nil {
		return errors.Join(fmt.Errorf("write the -reranks-out file: %w", err), b.file.Close())
	}
	return b.file.Close()
}

// Model implements generator.Generator.
func (b *answerBook) Model() string { return b.model }

// Ping implements generator.Generator: a book without a model answers it.
func (b *answerBook) Ping(ctx context.Context) error {
	if b.Generator == nil {
		return nil
	}
	return b.Generator.Ping(ctx)
}

// GenerateJSON implements generator.JSONGenerator: a listwise call.
func (b *answerBook) GenerateJSON(ctx context.Context, prompt string, schema json.RawMessage,
	opts generator.Options) (json.RawMessage, error) {
	key := callKey(b.model, rerank.MethodListwise, optionsKey(opts)+"\n"+string(schema), prompt)
	if a, ok := b.lookup(key); ok {
		return a.Order, nil
	}
	if !b.live || b.jsonG == nil {
		return nil, b.miss()
	}
	began := time.Now()
	raw, err := b.jsonG.GenerateJSON(ctx, prompt, schema, opts)
	if err != nil {
		return nil, err
	}
	return raw, b.record(answer{Key: key, Method: rerank.MethodListwise, Order: raw, TookMS: ms(time.Since(began))})
}

// FirstTokens implements generator.LogprobGenerator: a pointwise call.
func (b *answerBook) FirstTokens(ctx context.Context, prompt string, raw bool, top int,
	opts generator.Options) ([]generator.TokenChoice, error) {
	key := callKey(b.model, rerank.MethodPointwise, optionsKey(opts)+"\nraw "+strconv.FormatBool(raw)+" top "+
		strconv.Itoa(top), prompt)
	if a, ok := b.lookup(key); ok {
		return a.Tokens, nil
	}
	if !b.live || b.logpG == nil {
		return nil, b.miss()
	}
	began := time.Now()
	tokens, err := b.logpG.FirstTokens(ctx, prompt, raw, top, opts)
	if err != nil {
		return nil, err
	}
	return tokens, b.record(answer{Key: key, Method: rerank.MethodPointwise, Tokens: tokens,
		TookMS: ms(time.Since(began))})
}

// optionsKey is what of a call's options changes its reply: its
// temperature and its token cap, never its keep-alive.
func optionsKey(o generator.Options) string {
	temp := "default"
	if o.Temperature != nil {
		temp = strconv.FormatFloat(*o.Temperature, 'g', -1, 64)
	}
	return "temperature " + temp + " max_tokens " + strconv.Itoa(o.MaxTokens)
}

// lookup is the book's answer to key, counted as replayed: its recorded
// time counts, and it took none of this run's.
func (b *answerBook) lookup(key string) (answer, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.known[key]
	if ok {
		b.replayed++
		b.recorded += fromMS(a.TookMS)
	}
	return a, ok
}

// miss marks a call the book couldn't answer.
func (b *answerBook) miss() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.missed = true
	return errNotRecorded
}

// record keeps a's answer, appends it to the record, and counts its time.
func (b *answerBook) record(a answer) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.known[a.Key] = a
	b.asked++
	took := fromMS(a.TookMS)
	b.recorded += took
	b.measured += took
	if b.out == nil {
		return nil
	}
	line, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encode an answer: %w", err)
	}
	if _, err := b.out.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write the -reranks-out file: %w", err)
	}
	return nil
}

// take returns, and resets, the calls' recorded time, the time they took
// in this run, and whether one of them had no answer.
func (b *answerBook) take() (recorded, measured time.Duration, missed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	recorded, measured, missed = b.recorded, b.measured, b.missed
	b.recorded, b.measured, b.missed = 0, 0, false
	return recorded, measured, missed
}

// Loaded, Warm and Unload are the model's, for the tool's own warm-up and
// unload; a book without a model has none to load.
func (b *answerBook) Loaded(ctx context.Context) (bool, error) {
	if b.warmer == nil {
		return true, nil
	}
	return b.warmer.Loaded(ctx)
}

func (b *answerBook) Warm(ctx context.Context, keepAlive time.Duration) error {
	if b.warmer == nil {
		return nil
	}
	return b.warmer.Warm(ctx, keepAlive)
}

func (b *answerBook) Unload(ctx context.Context) error {
	if b.warmer == nil {
		return nil
	}
	return b.warmer.Unload(ctx)
}
