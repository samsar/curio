package sqlite

import (
	"database/sql"
	"slices"
	"strings"
	"sync"

	"github.com/samsar/curio/internal/store"
)

// enqueueSignal wakes goroutines waiting for jobs of some kinds to become
// claimable (store.JobQueue.Enqueued). A wait takes the channel for its set
// of kinds; notify closes the channel of every set holding a notified kind,
// and the next wait on that set gets a new one. Waits on the same set share
// a channel, so a worker pool costs one entry however many goroutines run
// it, and nothing is started per wait.
type enqueueSignal struct {
	mu      sync.Mutex
	waiting map[string]*kindWait // by kindsKey
}

type kindWait struct {
	kinds []store.JobKind // empty: any kind
	ch    chan struct{}
}

func newEnqueueSignal() *enqueueSignal {
	return &enqueueSignal{waiting: map[string]*kindWait{}}
}

// wait returns the channel the next notify of one of kinds closes; any
// kind when kinds is empty.
func (s *enqueueSignal) wait(kinds []store.JobKind) <-chan struct{} {
	key := kindsKey(kinds)
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.waiting[key]
	if !ok {
		w = &kindWait{kinds: slices.Clone(kinds), ch: make(chan struct{})}
		s.waiting[key] = w
	}
	return w.ch
}

// notify wakes the waits on any of kinds. Call it only once the jobs are
// committed, so a woken worker can claim them.
func (s *enqueueSignal) notify(kinds ...store.JobKind) {
	if len(kinds) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, w := range s.waiting {
		if len(w.kinds) == 0 || slices.ContainsFunc(kinds, func(k store.JobKind) bool {
			return slices.Contains(w.kinds, k)
		}) {
			close(w.ch)
			delete(s.waiting, key)
		}
	}
}

// kindsKey identifies a set of kinds, whatever their order.
func kindsKey(kinds []store.JobKind) string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	slices.Sort(names)
	return strings.Join(slices.Compact(names), ",")
}

// commitNotify commits tx, then wakes the waits on kinds: a signal sent
// before the commit could wake a worker that finds nothing to claim yet,
// and one for a transaction that rolls back would be for nothing.
func (d *DB) commitNotify(tx *sql.Tx, kinds ...store.JobKind) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	d.enqueued.notify(kinds...)
	return nil
}
