package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/samsar/curio/internal/generator"
)

// listwise asks once for the candidates' numbers in order, under a schema
// that allows only their numbers.
type listwise struct {
	gen       generator.JSONGenerator
	keepAlive time.Duration
}

// judge asks gen to order shown for question, and reads its answer
// strictly: a candidate it leaves out follows those it ordered, in
// first-stage order. Every candidate is scored: s = 1/ρ.
func (l listwise) judge(ctx context.Context, question string, shown []Candidate, s Settings) (judgement, error) {
	raw, err := l.gen.GenerateJSON(ctx, listwisePrompt(question, shown, s.Inputs), orderSchema(len(shown)),
		generator.Options{Temperature: new(0.0), MaxTokens: maxOrderTokens, KeepAlive: l.keepAlive})
	if err != nil {
		return judgement{}, err
	}
	order, err := decodeOrder(raw, len(shown))
	if err != nil {
		return judgement{}, err
	}
	j := judgement{place: make([]int, len(shown)), score: make([]float64, len(shown)),
		scored: make([]bool, len(shown))}
	next := 1
	for _, i := range order {
		j.place[i] = next
		next++
	}
	for i := range shown {
		if j.place[i] == 0 {
			j.place[i] = next
			next++
		}
		j.score[i], j.scored[i] = 1/float64(j.place[i]), true
	}
	return j, nil
}

// invalidAnswer is an answer that can't be used: rule names what it broke,
// for the log's cause, and reason says it without quoting the answer,
// which can hold the candidates' words.
type invalidAnswer struct {
	rule, reason string
}

func (e *invalidAnswer) Error() string { return e.reason }

// decodeOrder reads a listwise answer for m candidates: one JSON object
// holding order alone, once and spelled so (encoding/json would take
// "ORDER" for it, and the last of two), at least one number, each an
// integer in [1, m] and none repeated. It returns the candidates'
// indexes, from 0, in order.
func decodeOrder(raw json.RawMessage, m int) ([]int, error) {
	notOrder := &invalidAnswer{"shape", "it isn't an order's JSON"}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, notOrder
	}
	var order []int
	keyed := false
	for dec.More() {
		t, err := dec.Token()
		if key, ok := t.(string); err != nil || !ok || key != "order" || keyed {
			return nil, &invalidAnswer{"shape", "it holds a key other than order, or order twice"}
		}
		keyed = true
		if err := dec.Decode(&order); err != nil {
			return nil, notOrder
		}
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, notOrder
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, &invalidAnswer{"shape", "it holds more than one JSON value"}
	}
	if len(order) == 0 {
		return nil, &invalidAnswer{"empty", "it orders no candidate"}
	}
	seen := make([]bool, m)
	out := make([]int, 0, len(order))
	for _, n := range order {
		switch {
		case n < 1 || n > m:
			return nil, &invalidAnswer{"range", fmt.Sprintf("it names candidate %d of %d", n, m)}
		case seen[n-1]:
			return nil, &invalidAnswer{"repeat", fmt.Sprintf("it names candidate %d twice", n)}
		}
		seen[n-1] = true
		out = append(out, n-1)
	}
	return out, nil
}
