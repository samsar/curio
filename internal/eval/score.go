package eval

// QueryScore is one query's ranking scored at fixed depths: the first page
// of results, 10, and the whole ranking a search makes, 100 deep.
type QueryScore struct {
	RecallAt10  float64 `json:"recall_at_10"`
	RecallAt100 float64 `json:"recall_at_100"`
	// HighRecallAt10 is recall@10 of the judgments at MaxGrade alone, what
	// the query is after; nil for a query without one.
	HighRecallAt10 *float64 `json:"high_recall_at_10"`
	NDCGAt10       float64  `json:"ndcg_at_10"`
	RR             float64  `json:"rr"`
	SuccessAt1     float64  `json:"success_at_1"`
	SuccessAt5     float64  `json:"success_at_5"`
	SuccessAt10    float64  `json:"success_at_10"`
	// FirstRank is the rank of the first judged document, 0 for none.
	FirstRank int `json:"first_rank"`
}

// ScoreQuery scores ranked, best first, against q's judgments.
func ScoreQuery(q Query, ranked []string) QueryScore {
	grades := q.Grades()
	s := QueryScore{
		RecallAt10:  RecallAtK(ranked, grades, 10),
		RecallAt100: RecallAtK(ranked, grades, 100),
		NDCGAt10:    NDCGAtK(ranked, grades, 10),
		RR:          ReciprocalRank(ranked, grades),
		SuccessAt1:  SuccessAtK(ranked, grades, 1),
		SuccessAt5:  SuccessAtK(ranked, grades, 5),
		SuccessAt10: SuccessAtK(ranked, grades, 10),
		FirstRank:   FirstRank(ranked, grades),
	}
	if high := grades.AtLeast(MaxGrade); len(high) > 0 {
		r := RecallAtK(ranked, high, 10)
		s.HighRecallAt10 = &r
	}
	return s
}

// GroupType is what a group of queries has in common.
type GroupType string

const (
	GroupAll  GroupType = "all"  // every query
	GroupKind GroupType = "kind" // the queries of one kind
	GroupTag  GroupType = "tag"  // the queries with one tag
)

// Group is a set of queries scored together.
type Group struct {
	Type GroupType
	// Name is the kind or the tag; empty for all.
	Name string
	// Queries are indexes into the set's queries, in file order.
	Queries []int
}

// Groups are the set's groups: all, then each kind in the order it first
// appears, then each tag likewise. A query without a kind is in no kind's
// group, one with two tags in both tags' groups, and a kind and a tag of
// the same name are two groups.
func Groups(qs *QuerySet) []Group {
	groups := []Group{{Type: GroupAll}}
	kinds := map[string]int{}
	var tagGroups []Group
	tags := map[string]int{}
	for i, q := range qs.Queries {
		groups[0].Queries = append(groups[0].Queries, i)
		if q.Kind != "" {
			k, ok := kinds[q.Kind]
			if !ok {
				k = len(groups)
				kinds[q.Kind] = k
				groups = append(groups, Group{Type: GroupKind, Name: q.Kind})
			}
			groups[k].Queries = append(groups[k].Queries, i)
		}
		for _, tag := range q.Tags {
			t, ok := tags[tag]
			if !ok {
				t = len(tagGroups)
				tags[tag] = t
				tagGroups = append(tagGroups, Group{Type: GroupTag, Name: tag})
			}
			tagGroups[t].Queries = append(tagGroups[t].Queries, i)
		}
	}
	return append(groups, tagGroups...)
}

// GroupScore is a group's queries' mean scores.
type GroupScore struct {
	Type GroupType `json:"type"`
	Name string    `json:"name"`
	N    int       `json:"n"`
	// NHigh counts the queries with a judgment at MaxGrade, which
	// HighRecallAt10 is the mean over: nil when there are none.
	NHigh          int      `json:"n_high"`
	RecallAt10     float64  `json:"recall_at_10"`
	RecallAt100    float64  `json:"recall_at_100"`
	HighRecallAt10 *float64 `json:"high_recall_at_10"`
	MRR            float64  `json:"mrr"`
	NDCGAt10       float64  `json:"ndcg_at_10"`
	SuccessAt1     float64  `json:"success_at_1"`
	SuccessAt5     float64  `json:"success_at_5"`
	SuccessAt10    float64  `json:"success_at_10"`
}

// Summarize is g's mean scores, scores[i] being query i's.
func Summarize(g Group, scores []QueryScore) GroupScore {
	out := GroupScore{Type: g.Type, Name: g.Name, N: len(g.Queries)}
	if out.N == 0 {
		return out
	}
	var high float64
	for _, i := range g.Queries {
		s := scores[i]
		out.RecallAt10 += s.RecallAt10
		out.RecallAt100 += s.RecallAt100
		out.MRR += s.RR
		out.NDCGAt10 += s.NDCGAt10
		out.SuccessAt1 += s.SuccessAt1
		out.SuccessAt5 += s.SuccessAt5
		out.SuccessAt10 += s.SuccessAt10
		if s.HighRecallAt10 != nil {
			out.NHigh++
			high += *s.HighRecallAt10
		}
	}
	n := float64(out.N)
	out.RecallAt10 /= n
	out.RecallAt100 /= n
	out.MRR /= n
	out.NDCGAt10 /= n
	out.SuccessAt1 /= n
	out.SuccessAt5 /= n
	out.SuccessAt10 /= n
	if out.NHigh > 0 {
		high /= float64(out.NHigh)
		out.HighRecallAt10 = &high
	}
	return out
}
