package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/urlutil"
)

// question is one entry of the question file: an id, unique; its kind;
// the question; the pages that answer it (normalized URLs; none for an
// unanswerable or a library question); the facts a good answer states,
// one to four; the route it should take, when it names one; and a note.
type question struct {
	ID       string   `yaml:"id"`
	Kind     string   `yaml:"kind"`
	Question string   `yaml:"question"`
	Sources  []string `yaml:"sources"`
	Facts    []string `yaml:"facts"`
	Mode     string   `yaml:"mode"`
	Note     string   `yaml:"note"`
}

// The question kinds.
var kinds = []string{"one", "multi", "evidence", "library", "list", "unanswerable"}

// The modes a question may name.
var modes = []string{string(ask.RouteEvidence), string(ask.RouteInterests), string(ask.RouteList)}

// questionFile is the question file as parsed, and the SHA-256 of the
// bytes parsed.
type questionFile struct {
	path      string
	sha256    string
	questions []question
}

// readQuestions reads and parses the question file once.
func readQuestions(path string) (questionFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return questionFile{}, fmt.Errorf("the question file: %w", err)
	}
	qs, err := parseQuestions(data, path)
	if err != nil {
		return questionFile{}, err
	}
	sum := sha256.Sum256(data)
	return questionFile{path: path, sha256: hex.EncodeToString(sum[:]), questions: qs}, nil
}

// questionKeys are a question's fields, as the file spells them.
var questionKeys = []string{"id", "kind", "question", "sources", "facts", "mode", "note"}

// parseQuestions reads a question file strictly: one YAML document whose
// only key is questions, each question of the known fields alone, none
// repeated, aliases resolved, across questions too. An error names the
// file, and the question's index and id.
func parseQuestions(data []byte, name string) ([]question, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var file struct {
		Questions []yaml.Node `yaml:"questions"`
	}
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse the questions %q: %w", name, err)
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse the questions %q: a second YAML document (a question file is one)", name)
	}
	if len(file.Questions) == 0 {
		return nil, fmt.Errorf("the questions %q hold no question", name)
	}
	out := make([]question, 0, len(file.Questions))
	ids := map[string]int{}
	for i := range file.Questions {
		node := resolve(&file.Questions[i])
		q, err := parseQuestion(node)
		ident := ""
		if q.ID != "" {
			ident = fmt.Sprintf(" (%s)", q.ID)
		}
		if err != nil {
			return nil, fmt.Errorf("the questions %q: question %d%s, line %d: %w", name, i, ident, node.Line, err)
		}
		if first, dup := ids[q.ID]; dup {
			return nil, fmt.Errorf("the questions %q: questions %d and %d have the same id %q", name, first, i, q.ID)
		}
		ids[q.ID] = i
		out = append(out, q)
	}
	return out, nil
}

// resolve is the node an alias names, or node itself.
func resolve(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return node.Alias
	}
	return node
}

// parseQuestion decodes and checks one question.
func parseQuestion(node *yaml.Node) (question, error) {
	var q question
	if err := checkKeys(node); err != nil {
		return q, err
	}
	// Decoding the node itself resolves its aliases through the whole
	// document, which a node re-encoded alone would lose; it refuses a
	// repeated key, and checkKeys an unknown one.
	if err := node.Decode(&q); err != nil {
		return q, fmt.Errorf("decode: %w", err)
	}
	switch {
	case q.ID == "":
		return q, errors.New("id is required")
	case !slices.Contains(kinds, q.Kind):
		return q, fmt.Errorf("kind %q isn't one of %v", q.Kind, kinds)
	case q.Question == "":
		return q, errors.New("question is required")
	case len(q.Facts) < 1 || len(q.Facts) > 4:
		return q, fmt.Errorf("%d facts, one to four", len(q.Facts))
	case q.Mode != "" && !slices.Contains(modes, q.Mode):
		return q, fmt.Errorf("mode %q isn't one of %v", q.Mode, modes)
	case (q.Kind == "unanswerable" || q.Kind == "library") && len(q.Sources) > 0:
		return q, fmt.Errorf("a %s question lists no sources", q.Kind)
	}
	for i, u := range q.Sources {
		n, err := urlutil.Normalize(u)
		if err != nil {
			return q, fmt.Errorf("sources[%d]: %w", i, err)
		}
		q.Sources[i] = n
	}
	return q, nil
}

// checkKeys fails unless node is a mapping of questionKeys alone.
func checkKeys(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("a question is a mapping of %s", strings.Join(questionKeys, ", "))
	}
	for i := 0; i < len(node.Content); i += 2 {
		if key := node.Content[i]; !slices.Contains(questionKeys, key.Value) {
			return fmt.Errorf("line %d: unknown key %q (a question has %s)", key.Line, key.Value,
				strings.Join(questionKeys, ", "))
		}
	}
	return nil
}

// answerable reports whether q has an answer among its pages.
func (q question) answerable() bool { return q.Kind != "unanswerable" }

// wantRoute is the route q should take: its mode, the answer route when
// it names none.
func (q question) wantRoute() string {
	if q.Mode == "" {
		return string(ask.RouteAnswer)
	}
	return q.Mode
}
