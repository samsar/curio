package insight

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/store"
)

// groupLabel is what a rebuild names a group: its label, the label's source
// and when it was made. relabeled marks a carried identity named anew,
// whose label the commit writes.
type groupLabel struct {
	label     Label
	source    store.LabelSource
	at        *time.Time
	relabeled bool
}

// labelStats count the labels a rebuild made, by source.
type labelStats struct{ llm, terms int }

// label names the groups of gr that need a name, honoring cfg.Labeling with
// a graceful fallback to deterministic term labels, and returns every
// group's label, index-aligned with gr.groups: a carried group keeps its
// identity's label unless it is named anew.
//
// A group is named when it is new; when it is carried without a label
// (labeling was off); when it carries a term label and the LLM is wanted,
// keeping that label unless the LLM answers; and when two carried labels in
// one scope (an area's interests, the areas, or every interest in the flat
// shape) share a LabelKey, the smaller group of the two. Interests are named
// before areas, area by area, largest first, each seeing its area's name
// and its siblings' names so far; then the areas, largest first, each from
// its interests' names. No two labels in a scope share a key: a reply that
// repeats one is asked again once, with the name it took, and then the
// group gets a term label, which skips its siblings' words.
//
// The LLM is asked until it fails in a way that would repeat — unreachable,
// an HTTP error, a timeout, or the run's labeling budget running out — and
// then isn't called again this run: every remaining group gets a term label
// at once instead of waiting out the same failure N times. An unparseable
// reply costs only that group its LLM label. Labeling stays sequential: a
// local Ollama serializes generation anyway and competes with index
// embeddings, and the budget plus largest-first order bound the wait and
// spend it where it matters most. If ctx itself ends, the run fails rather
// than completing with fallback labels.
func (e *Engine) label(ctx context.Context, gr *grouped) ([]groupLabel, labelStats, error) {
	l := &labeling{e: e, ctx: ctx, gr: gr, out: make([]groupLabel, len(gr.groups)),
		titles: &titleCache{docs: e.docs, gr: gr, titles: map[int][]string{}, next: map[int]int{}}}
	for k, g := range gr.groups {
		if c := g.carried; c != nil {
			l.out[k] = groupLabel{label: Label{Name: c.Label, Summary: c.Summary}, source: c.LabelSource, at: c.LabeledAt}
		}
	}
	if e.cfg.Labeling == LabelingOff {
		return l.out, labelStats{}, nil
	}
	if e.cfg.Labeling == LabelingLLM {
		l.llm = e.llmLabeler
	}
	// Every term label counts as a fallback when LLM labels were wanted,
	// including the groups never offered to the model once it was switched
	// off, so the warning reports how many lack an LLM name.
	l.wantLLM = l.llm != nil
	var cancel context.CancelFunc
	l.budget, cancel = context.WithTimeout(ctx, e.cfg.LabelingTimeout)
	defer cancel()

	l.scopes = gr.scopes()
	need, keep := l.plan()
	order := gr.labelOrder(l.scopes, need)
	for _, k := range order {
		if err := ctx.Err(); err != nil {
			return nil, labelStats{}, fmt.Errorf("label groups: %w", err)
		}
		info, err := l.info(k)
		if err != nil {
			return nil, labelStats{}, err
		}
		if err := l.name(k, info, keep[k]); err != nil {
			return nil, labelStats{}, err
		}
	}
	if l.fellBack > 0 {
		e.log.Warn("llm labeling fell back to term labels",
			"groups", l.fellBack, "of", len(order), "reason", l.reason)
	}
	return l.out, l.stats, nil
}

// labeling is one rebuild's labels in the making.
type labeling struct {
	e      *Engine
	ctx    context.Context
	budget context.Context // ctx bounded by the run's labeling budget
	gr     *grouped
	scopes [][]int // gr.scopes()
	titles *titleCache
	// llm is the LLM labeler while it is asked, nil once it failed in a
	// way that would repeat, or when it isn't wanted.
	llm      Labeler
	wantLLM  bool
	out      []groupLabel
	stats    labelStats
	fellBack int
	reason   error
}

// plan reports which groups need a name, and which of those keep their
// carried term label unless the LLM names them.
func (l *labeling) plan() (need, keep []bool) {
	gr := l.gr
	need, keep = make([]bool, len(gr.groups)), make([]bool, len(gr.groups))
	for k, g := range gr.groups {
		switch c := g.carried; {
		case c == nil, c.Label == "":
			need[k] = true
		case l.wantLLM && c.LabelSource == store.LabelSourceTerms:
			need[k], keep[k] = true, true
		}
	}
	for _, scope := range l.scopes {
		byKey := make(map[string][]int)
		for _, k := range scope {
			if c := gr.groups[k].carried; c != nil && LabelKey(c.Label) != "" {
				byKey[LabelKey(c.Label)] = append(byKey[LabelKey(c.Label)], k)
			}
		}
		for _, ks := range byKey {
			slices.SortFunc(ks, func(a, b int) int {
				return cmp.Or(cmp.Compare(gr.groups[b].size(), gr.groups[a].size()),
					strings.Compare(gr.groups[a].id, gr.groups[b].id))
			})
			for _, k := range ks[1:] {
				need[k], keep[k] = true, false
			}
		}
	}
	return need, keep
}

// scopes are the sets of groups whose labels must differ, in gr.groups'
// order: each area's interests, indexed by the area, then the areas, then
// the interests in no area (all of them in the flat shape).
func (gr *grouped) scopes() [][]int {
	scopes := make([][]int, gr.numAreas+2)
	areas, free := gr.numAreas, gr.numAreas+1
	scopes[areas] = identityIndexes(gr.numAreas)
	for k := gr.numAreas; k < len(gr.groups); k++ {
		if p := gr.groups[k].parent; p >= 0 {
			scopes[p] = append(scopes[p], k)
		} else {
			scopes[free] = append(scopes[free], k)
		}
	}
	return scopes
}

// scopeOf is the scope group k's label must differ within, one of scopes.
func (gr *grouped) scopeOf(scopes [][]int, k int) []int {
	switch g := gr.groups[k]; {
	case g.level == store.InterestLevelArea:
		return scopes[gr.numAreas]
	case g.parent >= 0:
		return scopes[g.parent]
	default:
		return scopes[gr.numAreas+1]
	}
}

// bySize sorts groups largest first, keeping their order on ties: the
// grouping numbers them largest first already, ties by their first
// member's ID.
func (gr *grouped) bySize(ks []int) []int {
	slices.SortStableFunc(ks, func(a, b int) int { return cmp.Compare(gr.groups[b].size(), gr.groups[a].size()) })
	return ks
}

// labelOrder is the order the groups that need a name are named in: area
// by area, largest first, each area's interests largest first, then the
// areas largest first; in the flat shape, the interests largest first.
// Groups are numbered largest first already, so a stable sort by size
// keeps that tie-break.
func (gr *grouped) labelOrder(scopes [][]int, need []bool) []int {
	needed := func(ks []int) []int { return slices.DeleteFunc(slices.Clone(ks), func(k int) bool { return !need[k] }) }
	var order []int
	areas := gr.bySize(slices.Clone(scopes[gr.numAreas]))
	for _, a := range areas {
		order = append(order, gr.bySize(needed(scopes[a]))...)
	}
	order = append(order, gr.bySize(needed(scopes[gr.numAreas+1]))...)
	return append(order, needed(areas)...)
}

// info is what the labeler is told about group k: its titles and size,
// the names its scope has so far, its area's name for an interest, and
// its interests for an area.
func (l *labeling) info(k int) (ClusterInfo, error) {
	gr := l.gr
	g := gr.groups[k]
	info := ClusterInfo{Size: g.size()}
	for _, s := range gr.scopeOf(l.scopes, k) {
		if name := l.out[s].label.Name; s != k && name != "" {
			info.Siblings = append(info.Siblings, name)
		}
	}
	if g.level == store.InterestLevelInterest {
		if g.parent >= 0 {
			info.Area = l.out[g.parent].label.Name
		}
		titles, err := l.titles.get(l.ctx, k, l.e.cfg.TitlesPerCluster)
		info.Titles = titles
		return info, err
	}
	children := gr.bySize(slices.Clone(l.scopes[k]))
	info.Children = make([]ChildInfo, 0, len(children))
	for _, c := range children {
		info.Children = append(info.Children, ChildInfo{Name: l.out[c].label.Name, Size: gr.groups[c].size()})
	}
	titles, err := l.titles.roundRobin(l.ctx, children, l.e.cfg.TitlesPerCluster)
	info.Titles = titles
	return info, err
}

// name names group k: by the LLM while it answers with a name its scope
// hasn't taken, else by terms, unless keep holds the group's carried term
// label.
func (l *labeling) name(k int, info ClusterInfo, keep bool) error {
	taken := make(map[string]bool, len(info.Siblings))
	for _, s := range info.Siblings {
		if key := LabelKey(s); key != "" {
			taken[key] = true
		}
	}
	if l.llm != nil {
		lab, ok, err := l.ask(info, taken)
		if err != nil {
			return err
		}
		if ok {
			l.set(k, lab, store.LabelSourceLLM)
			l.stats.llm++
			return nil
		}
	}
	if l.wantLLM {
		l.fellBack++
	}
	if keep {
		return nil
	}
	lab := l.e.termLabeler.label(info)
	if taken[LabelKey(lab.Name)] {
		// Words a sibling's name doesn't use can still spell its key
		// (a combining mark splits a word for LabelKey, not for tokenize).
		lab = Label{}
	}
	if lab.Name == "" {
		if l.out[k].label.Name != "" {
			l.set(k, Label{}, "")
		}
		return nil
	}
	l.set(k, lab, store.LabelSourceTerms)
	l.stats.terms++
	return nil
}

// ask asks the LLM for a name its scope hasn't taken, twice at most: a
// reply that repeats a name is asked again, told the name it took. It
// reports whether it got one; an error is ctx ending, which fails the run.
func (l *labeling) ask(info ClusterInfo, taken map[string]bool) (Label, bool, error) {
	for range 2 {
		lab, err := l.llm.Label(l.budget, info)
		if err == nil && lab.Name == "" {
			err = fmt.Errorf("%w: empty name", ErrUnparseableLabel)
		}
		switch {
		case err == nil:
			if key := LabelKey(lab.Name); key != "" && !taken[key] {
				return lab, true, nil
			}
			info.Taken = lab.Name
			continue
		case l.ctx.Err() != nil:
			return Label{}, false, fmt.Errorf("label groups: %w", l.ctx.Err())
		case !errors.Is(err, ErrUnparseableLabel):
			l.llm = nil
		}
		// Only this reply was unusable, or the model is off for the run.
		l.reason = err
		return Label{}, false, nil
	}
	// Both replies took a name: the group falls back, and only now is that
	// the reason. A retry that found a free name leaves it alone.
	l.reason = fmt.Errorf("the name %q was taken", info.Taken)
	return Label{}, false, nil
}

// set gives group k label, made now by source ("" for none).
func (l *labeling) set(k int, lab Label, source store.LabelSource) {
	var at *time.Time
	if source != "" {
		now := time.Now().UTC()
		at = &now
	}
	l.out[k] = groupLabel{label: lab, source: source, at: at, relabeled: l.gr.groups[k].carried != nil}
}

// titleCache reads groups' titles as labels ask for them, most central
// member first. A document deleted since its vector was read is skipped;
// any other store error fails the run. A document with no title is named
// by its URL.
type titleCache struct {
	docs   store.DocumentStore
	gr     *grouped
	titles map[int][]string // by group
	next   map[int]int      // the next member to read, by group
}

// get returns up to n titles of group k.
func (c *titleCache) get(ctx context.Context, k, n int) ([]string, error) {
	members := c.gr.groups[k].members
	for len(c.titles[k]) < n && c.next[k] < len(members) {
		id := c.gr.ids[members[c.next[k]]]
		c.next[k]++
		d, err := c.docs.GetByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load title of document %s: %w", id, err)
		}
		title := d.URL
		if d.Title != nil && *d.Title != "" {
			title = *d.Title
		}
		c.titles[k] = append(c.titles[k], title)
	}
	return c.titles[k][:min(n, len(c.titles[k]))], nil
}

// roundRobin returns up to n titles of groups: each one's most central,
// then each one's second, and so on.
func (c *titleCache) roundRobin(ctx context.Context, groups []int, n int) ([]string, error) {
	var out []string
	for rank := 0; len(out) < n; rank++ {
		more := false
		for _, k := range groups {
			titles, err := c.get(ctx, k, rank+1)
			if err != nil {
				return nil, err
			}
			if len(titles) > rank {
				out, more = append(out, titles[rank]), true
				if len(out) == n {
					break
				}
			}
		}
		if !more {
			break
		}
	}
	return out, nil
}
