package ask

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/textutil"
)

// passageCandidates is how many of a source's chunks, nearest the
// question first, its passages are chosen among: a median page holds 5
// chunks (p90 21, measured on the operator's library), and the nearest
// few hold what the question asks while a source's allowance shows at
// most two or three whole.
const passageCandidates = 4

// gapMark joins two parts of a chunk with text left out between them.
const gapMark = " … "

// Passage is a part of a page's text an answer was written from, as the
// prompt showed it: its text, and the section of the page it is from (the
// headings it sits under), "" when the page names none.
type Passage struct {
	Text    string
	Section string
}

// candidate is a chunk a source's passages may be chosen from: its
// position in its page, its text and its section.
type candidate struct {
	ord           int
	text, section string
}

// documentContexts are what each document's chunks' contexts were built
// from (indexer.Context), by ID, from what the sources read: its title,
// bookmarks (newest saved first) and fields' site.
func documentContexts(hits []search.Hit, fields map[string]store.DocumentFields,
	bookmarks map[string][]*store.Bookmark) map[string]indexer.DocumentContext {
	out := make(map[string]indexer.DocumentContext, len(hits))
	for _, h := range hits {
		doc := h.Document
		c := indexer.DocumentContext{URL: doc.URL}
		if doc.Title != nil {
			c.Title = *doc.Title
		}
		for _, b := range bookmarks[doc.ID] {
			c.Bookmarks = append(c.Bookmarks, docmeta.Bookmark{Title: deref(b.Title), FolderPath: deref(b.FolderPath)})
		}
		if f, ok := fields[doc.ID]; ok {
			c.Site = &store.DocumentSite{Name: f.SiteName, Host: f.Host}
		}
		out[doc.ID] = c
	}
	return out
}

// deref is *s, or "" for nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// candidates are each readable hit's passage candidates, by document: its
// passageCandidates chunks nearest vector, the question's own (one
// RankChunks over them all), else, without a vector or a ranking, the
// chunks the search matched, in its order; each read back once (one
// GetByIDs) for its position and section. A ranking that fails is logged
// at WARN the first time, never with what was asked, and the ask goes on
// with the search's chunks.
func (a *Asker) candidates(ctx context.Context, vector []float32, hits []search.Hit,
	docs map[string]indexer.DocumentContext) (map[string][]candidate, error) {
	ranked := a.rankChunks(ctx, vector, hits)
	chosen := map[string][]string{}
	var ids []string
	for _, h := range hits {
		doc := h.Document.ID
		picks := ranked[doc]
		if len(picks) == 0 {
			for _, c := range h.Chunks {
				picks = append(picks, c.ChunkID)
			}
		}
		chosen[doc] = picks
		ids = append(ids, picks...)
	}
	read := map[string]*store.Chunk{}
	if a.stores.Chunks != nil && len(ids) > 0 {
		chunks, err := a.stores.Chunks.GetByIDs(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("read the sources' chunks: %w", err)
		}
		for _, c := range chunks {
			read[c.ID] = c
		}
	}
	matched := map[string]string{}
	for _, h := range hits {
		for _, c := range h.Chunks {
			matched[c.ChunkID] = c.Text
		}
	}
	out := make(map[string][]candidate, len(hits))
	for _, h := range hits {
		doc := h.Document.ID
		for i, id := range chosen[doc] {
			switch c, ok := read[id]; {
			case ok:
				out[doc] = append(out[doc], candidate{ord: c.Ord, text: c.Text,
					section: indexer.HeadingPath(c.Context, docs[doc])})
			case matched[id] != "":
				// A chunk a re-index removed since the search: its text as
				// the search read it, before the rest.
				out[doc] = append(out[doc], candidate{ord: i - len(chosen[doc]), text: matched[id]})
			}
		}
	}
	return out, nil
}

// rankChunks are each of hits' documents' passageCandidates chunks
// nearest vector, by ID, nearest first; nil without a vector, a chunk
// store, or when the ranking fails or reads none.
func (a *Asker) rankChunks(ctx context.Context, vector []float32, hits []search.Hit) map[string][]string {
	if vector == nil || a.stores.Chunks == nil || len(hits) == 0 {
		return nil
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.Document.ID
	}
	ranked, err := a.stores.Chunks.RankChunks(ctx, a.stores.TenantID, vector, ids)
	if err != nil {
		if ctx.Err() == nil {
			level := slog.LevelDebug
			if a.rankWarned.CompareAndSwap(false, true) {
				level = slog.LevelWarn
			}
			a.log.Log(ctx, level, "ask: the sources' chunks couldn't be ranked by the question; their passages "+
				"are the search's", "err", err)
		}
		return nil
	}
	out := map[string][]string{}
	for _, c := range ranked {
		if len(out[c.DocumentID]) < passageCandidates {
			out[c.DocumentID] = append(out[c.DocumentID], c.ChunkID)
		}
	}
	return out
}

// questionWords are the words a passage is fitted by: lowercased runs of
// letters or digits of texts (the question, a plan's rewrites and names)
// of at least 3 runes or holding a digit, less the English stopwords.
func questionWords(texts []string) map[string]bool {
	out := map[string]bool{}
	for _, t := range texts {
		for _, w := range wordsOf(t) {
			if (len([]rune(w)) >= 3 || strings.ContainsFunc(w, unicode.IsDigit)) && !routeStopwords[w] &&
				!selfWords[w] {
				out[w] = true
			}
		}
	}
	return out
}

// wordsOf are text's lowercased runs of letters and digits.
func wordsOf(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// paragraphBreak separates a chunk's paragraphs: a blank line.
var paragraphBreak = regexp.MustCompile(`\n[ \t]*\n`)

// fitPassages are a source's passages within left quarter-tokens, from
// cands, best first: each whole while it fits, then the first that
// doesn't, fitted by its paragraphs (fitParagraphs), and nothing after
// it. The passages are written in the page's order. It returns their
// lines, the passages as shown, and what is left.
func fitPassages(cands []candidate, left int, words map[string]bool) (string, []Passage, int) {
	type chosen struct {
		ord int
		p   Passage
	}
	var picked []chosen
	for _, c := range cands {
		p := Passage{Text: textutil.NeutralizeWhole(c.text), Section: textutil.Neutralize(c.section, sectionBytes)}
		if p.Text == "" {
			continue
		}
		if cost := textutil.TokenQuarters(passageLine(p)); cost <= left {
			picked = append(picked, chosen{c.ord, p})
			left -= cost
			continue
		}
		p.Text = fitParagraphs(c.text, left-textutil.TokenQuarters(passageLine(Passage{Section: p.Section})), words)
		if p.Text != "" {
			picked = append(picked, chosen{c.ord, p})
			left -= textutil.TokenQuarters(passageLine(p))
		}
		break
	}
	if len(picked) == 0 {
		return "", nil, left
	}
	slices.SortStableFunc(picked, func(a, b chosen) int { return cmp.Compare(a.ord, b.ord) })
	var b strings.Builder
	shown := make([]Passage, 0, len(picked))
	for _, c := range picked {
		b.WriteString(passageLine(c.p))
		shown = append(shown, c.p)
	}
	return b.String(), shown, left
}

// passageLine is p as a source's line.
func passageLine(p Passage) string {
	if p.Section != "" {
		return "passage (" + p.Section + "): " + p.Text + "\n"
	}
	return "passage: " + p.Text + "\n"
}

// fitParagraphs is the part of text, a chunk, that best answers words
// within quarters: its paragraphs (separated by blank lines), each
// neutralized, chosen by how many distinct words each holds, most first,
// then by position, while they fit; the first that doesn't, when nothing
// is chosen yet or it is the next by score, cut from its start to what is
// left, and nothing after it. The chosen are joined in their order, a gap
// between two that weren't adjacent marked by gapMark.
func fitParagraphs(text string, quarters int, words map[string]bool) string {
	if quarters <= 0 {
		return ""
	}
	var paras []string
	for _, p := range paragraphBreak.Split(text, -1) {
		if p = textutil.NeutralizeWhole(p); p != "" {
			paras = append(paras, p)
		}
	}
	order := make([]int, len(paras))
	scores := make([]int, len(paras))
	for i, p := range paras {
		order[i] = i
		seen := map[string]bool{}
		for _, w := range wordsOf(p) {
			if words[w] && !seen[w] {
				seen[w] = true
				scores[i]++
			}
		}
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(scores[b], scores[a]) })
	return joinParagraphs(paras, order, quarters)
}

// joinParagraphs is paras joined in their order, of those order picks
// first while the whole costs at most quarters; the first that doesn't
// fit is cut from its start to what is left, and nothing after it.
func joinParagraphs(paras []string, order []int, quarters int) string {
	chosen := map[int]string{}
	cost := func() int { return textutil.TokenQuarters(joinChosen(paras, chosen)) }
	for _, i := range order {
		chosen[i] = paras[i]
		if cost() <= quarters {
			continue
		}
		delete(chosen, i)
		left := quarters - cost() - textutil.TokenQuarters(gapMark)
		if cut := strings.TrimSpace(fitQuarters(paras[i], left)); cut != "" {
			chosen[i] = cut
		}
		break
	}
	return joinChosen(paras, chosen)
}

// joinChosen joins the chosen paragraphs, by index, in their order: one
// space between neighbours, gapMark across a paragraph left out.
func joinChosen(paras []string, chosen map[int]string) string {
	var b strings.Builder
	last := -1
	for i := range paras {
		p, ok := chosen[i]
		if !ok {
			continue
		}
		switch {
		case last < 0:
		case i == last+1:
			b.WriteByte(' ')
		default:
			b.WriteString(gapMark)
		}
		b.WriteString(p)
		last = i
	}
	return b.String()
}

// fitQuarters is the longest prefix of s, cut on a rune boundary, that
// costs at most quarters.
func fitQuarters(s string, quarters int) string {
	if quarters <= 0 {
		return ""
	}
	cost := 0
	for i, r := range s {
		c := textutil.TokenQuarters(string(r))
		if cost+c > quarters {
			return s[:i]
		}
		cost += c
	}
	return s
}
