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
// cluster pool always has clusterPoolSize, and the cards pool
// cardsPoolSize.
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
//   - index: the index handler and the same hook, and once a job's outcome
//     is recorded the re-embed is topped up (Deps.Reembed), so the next
//     document follows at once.
//   - cluster: corpus-wide clustering on one goroutine, so it neither
//     starves the others nor runs twice at once. Its hook records a
//     rebuild the daemon left unfinished, and once a job's outcome is
//     recorded the interest scheduler is asked to check, so its state
//     moves on from rebuilding at once.
//   - cards: the stale document cards embedded on one goroutine, so card
//     work never takes an index slot and runs once at a time. Its hook
//     marks no document: it queues the next cards job an hour ahead while
//     cards are stale, so a job that failed for good leaves none without
//     a job.
func NewPools(d Deps, sizes PoolSizes, opts WorkerOptions) []Pool {
	fetch := NewWorker(d.Queue, opts)
	fetch.Register(store.JobKindFetch, fetchHandler(d))
	fetch.OnPermanentFailure(store.JobKindFetch, markDocFailed(d))

	index := NewWorker(d.Queue, opts)
	index.Register(store.JobKindIndex, indexHandler(d))
	index.OnPermanentFailure(store.JobKindIndex, markDocFailed(d))
	index.OnFinished(store.JobKindIndex, d.Reembed.Kick)

	cluster := NewWorker(d.Queue, opts)
	cluster.Register(store.JobKindCluster, clusterHandler(d))
	cluster.OnPermanentFailure(store.JobKindCluster, abandonedRebuild(d))
	cluster.OnFinished(store.JobKindCluster, func() { kick(d) })

	cards := NewWorker(d.Queue, opts)
	cards.Register(store.JobKindCards, cardsHandler(d))
	cards.OnPermanentFailure(store.JobKindCards, retryCards(d))

	return []Pool{
		{Name: "fetch", Kind: store.JobKindFetch, Worker: fetch, Size: sizes.Fetch},
		{Name: "index", Kind: store.JobKindIndex, Worker: index, Size: sizes.Index},
		{Name: "cluster", Kind: store.JobKindCluster, Worker: cluster, Size: clusterPoolSize},
		{Name: "cards", Kind: store.JobKindCards, Worker: cards, Size: cardsPoolSize},
	}
}
