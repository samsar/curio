package rerank

// Apply lays order, the head's document IDs as a first page's rerank
// returned them, over ranking, the first ranking's document IDs, asking no
// model: the head is the order's IDs found among ranking's first
// len(order), in the order's order, then the rest of those first
// len(order) in first-stage order; then the rest of ranking as it was.
// stale counts the order's IDs not among them: documents that left the
// head since (deleted, or ranked lower now). A request's later pages are
// windows of what Apply returns, so they tile one ranking.
func Apply(ranking, order []string) (out []string, stale int) {
	n := min(len(order), len(ranking))
	head := make(map[string]bool, n)
	for _, id := range ranking[:n] {
		head[id] = true
	}
	out = make([]string, 0, len(ranking))
	placed := make(map[string]bool, n)
	for _, id := range order {
		if !head[id] || placed[id] {
			stale++
			continue
		}
		placed[id] = true
		out = append(out, id)
	}
	for _, id := range ranking[:n] {
		if !placed[id] {
			out = append(out, id)
		}
	}
	return append(out, ranking[n:]...), stale
}
