package jobs

import "github.com/samsar/curio/internal/store"

// Pool is one Worker, the kind of job it claims, and how many goroutines
// run it.
type Pool struct {
	Name   string
	Kind   store.JobKind
	Worker *Worker
	Size   int
}

// PoolSizes is how many goroutines run the fetch and index pools. The
// cluster pool always has clusterPoolSize.
type PoolSizes struct {
	Fetch, Index int
}

// clusterPoolSize is the cluster pool's one goroutine: clustering is
// corpus-wide, so two runs at once would only redo each other's work.
const clusterPoolSize = 1

// NewPools builds the daemon's worker pools over d.Queue. Each pool's
// workers claim only their own kind, so network-bound fetches can run wide
// without starving Ollama-bound indexing: with one FIFO pool for both, an
// import was measured finishing 3296 fetches while only 55 index jobs ran.
// Every pool claims through opts.Gate.
//
//   - fetch: the fetch handler, plus the hook that marks the document
//     failed, or dead, when its job gives up.
//   - index: the index handler and the same hook.
//   - cluster: corpus-wide clustering on one goroutine, so it neither
//     starves the others nor runs twice at once. No hook: there is no
//     document to mark.
func NewPools(d Deps, sizes PoolSizes, opts WorkerOptions) []Pool {
	fetch := NewWorker(d.Queue, opts)
	fetch.Register(store.JobKindFetch, fetchHandler(d))
	fetch.OnPermanentFailure(store.JobKindFetch, markDocFailed(d))

	index := NewWorker(d.Queue, opts)
	index.Register(store.JobKindIndex, indexHandler(d))
	index.OnPermanentFailure(store.JobKindIndex, markDocFailed(d))

	cluster := NewWorker(d.Queue, opts)
	cluster.Register(store.JobKindCluster, clusterHandler(d))

	return []Pool{
		{Name: "fetch", Kind: store.JobKindFetch, Worker: fetch, Size: sizes.Fetch},
		{Name: "index", Kind: store.JobKindIndex, Worker: index, Size: sizes.Index},
		{Name: "cluster", Kind: store.JobKindCluster, Worker: cluster, Size: clusterPoolSize},
	}
}
