package api

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/samsar/curio/internal/store"
)

// SavedDates is what healthz reports of the bookmarks' saved-date
// classes: what the daemon's start did (sqlite.EnsureSavedDates), fixed
// when it started, and the sources owed a classification since. A start
// that couldn't classify still serves, and an owed source is retried, so
// neither changes Status.
type SavedDates struct {
	// CheckedAt is when the start classified every bookmark, Changed how
	// many classes it changed, and Error why it couldn't, when it
	// couldn't: date filters then read the classes the bookmarks had, and
	// the next start classifies them again.
	CheckedAt time.Time `json:"checked_at"`
	Changed   int       `json:"changed"`
	Error     string    `json:"error,omitempty"`
	// Owed are the sources an import stored bookmarks from whose
	// classification failed: those bookmarks claim no date until a retry
	// classifies them. Empty, never null, when none is owed.
	Owed []OwedClassification `json:"owed"`
}

// OwedClassification is one source owed a classification: Since is when
// the first that failed ran, and Error why the last one failed.
type OwedClassification struct {
	Source string    `json:"source"`
	Since  time.Time `json:"since"`
	Error  string    `json:"error"`
}

// classifyTimeout bounds one classification of a source's saved dates: a
// read of its bookmarks and a write of the classes that changed,
// milliseconds for tens of thousands.
const classifyTimeout = 10 * time.Second

// ClassifyRetryEvery is how often the daemon retries the classifications
// owed (SavedDateClassifier.Run).
const ClassifyRetryEvery = time.Minute

// skippedIDsLogged caps the IDs a log line names of the bookmarks a
// classification skipped; the count says how many there are.
const skippedIDsLogged = 10

// SavedDateClassifier classifies a source's saved dates once an import has
// stored bookmarks from it, since each class depends on the bookmarks
// saved around it, and until then those bookmarks claim no date. A
// classification that fails owes its source one, which Run retries until
// one succeeds; a later import's success pays it too, and so does the
// daemon's next start, which classifies every bookmark.
//
// Its classifications run on the daemon's lifetime, not the request's:
// an import's bookmarks are classified whether its client stays or not,
// and a classification stops when the daemon shuts down, so no handler
// outlives the API's shutdown on its account.
type SavedDateClassifier struct {
	bookmarks store.BookmarkStore
	tenantID  string
	life      context.Context
	start     SavedDates
	every     time.Duration
	log       *slog.Logger

	// running serializes the classifications, an import's and the
	// retries', so one that ends never overwrites what a later one found.
	// They are write transactions, which SQLite runs one at a time anyway.
	running sync.Mutex
	mu      sync.Mutex
	owed    map[string]OwedClassification
}

// SavedDateOptions configure a SavedDateClassifier.
type SavedDateOptions struct {
	Bookmarks store.BookmarkStore
	// TenantID is the tenant whose sources it classifies; default
	// store.LocalTenantID.
	TenantID string
	// Start is what the daemon's start did; its Owed is ignored.
	Start SavedDates
	// RetryEvery is how often Run retries; default ClassifyRetryEvery.
	RetryEvery time.Duration
	// Log defaults to slog.Default().
	Log *slog.Logger
}

// NewSavedDateClassifier builds a classifier whose classifications stop
// when life ends: the daemon's lifetime.
func NewSavedDateClassifier(life context.Context, opts SavedDateOptions) *SavedDateClassifier {
	c := &SavedDateClassifier{bookmarks: opts.Bookmarks, tenantID: opts.TenantID, life: life, start: opts.Start,
		every: opts.RetryEvery, log: opts.Log, owed: map[string]OwedClassification{}}
	if c.tenantID == "" {
		c.tenantID = store.LocalTenantID
	}
	if c.every <= 0 {
		c.every = ClassifyRetryEvery
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	c.start.Owed = nil
	return c
}

// Classify classifies source's saved dates after an import stored
// bookmarks from it, within classifyTimeout. A failure is one WARN and
// owes the source a classification, unless the daemon is shutting down:
// its next start classifies every bookmark. The import's response doesn't
// depend on it.
func (c *SavedDateClassifier) Classify(source string) {
	ctx, cancel := context.WithTimeout(c.life, classifyTimeout)
	defer cancel()
	err := c.classify(ctx, source)
	switch {
	case err == nil:
	case c.life.Err() != nil:
		c.log.Info("import: the daemon stopped before the saved dates were classified; its next start classifies "+
			"them", "source", source)
	default:
		c.log.Warn("import: couldn't classify the saved dates; the bookmarks it stored claim no date until a retry "+
			"classifies them", "source", source, "retry_every", c.every, "err", err)
	}
}

// Run retries the owed classifications every RetryEvery until ctx ends.
func (c *SavedDateClassifier) Run(ctx context.Context) {
	tick := time.NewTicker(c.every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.RetryOwed(ctx)
		}
	}
}

// RetryOwed classifies each owed source once, in order, each within
// classifyTimeout, and stops when ctx ends. A retry that succeeds is one
// INFO; one that fails keeps the source owed, its error on healthz.
func (c *SavedDateClassifier) RetryOwed(ctx context.Context) {
	for _, source := range c.owedSources() {
		if ctx.Err() != nil {
			return
		}
		rctx, cancel := context.WithTimeout(ctx, classifyTimeout)
		err := c.classify(rctx, source)
		cancel()
		if err != nil {
			c.log.Debug("saved dates: the retried classification failed", "source", source, "err", err)
			continue
		}
		c.log.Info("saved dates: classified the source a failed classification owed", "source", source)
	}
}

// classify runs one classification of source, settles what source is
// owed, and logs the bookmarks it skipped.
func (c *SavedDateClassifier) classify(ctx context.Context, source string) error {
	c.running.Lock()
	defer c.running.Unlock()
	began := time.Now()
	done, err := c.bookmarks.ClassifySavedDates(ctx, c.tenantID, source)
	c.settle(source, began, err)
	if skipped := done.Skipped; err == nil && len(skipped) > 0 {
		c.log.Warn("saved dates: bookmarks whose saved or stored time doesn't parse are classified unknown",
			"source", source, "skipped", len(skipped), "ids", skipped[:min(len(skipped), skippedIDsLogged)])
	}
	return err
}

// settle records how a classification of source that began at began
// ended: a success pays what the source was owed, and a failure owes it
// one, since the first failure, unless the daemon is stopping.
func (c *SavedDateClassifier) settle(source string, began time.Time, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case err == nil:
		delete(c.owed, source)
	case c.life.Err() == nil:
		owed, ok := c.owed[source]
		if !ok {
			owed = OwedClassification{Source: source, Since: began.UTC()}
		}
		owed.Error = err.Error()
		c.owed[source] = owed
	}
}

// owedSources are the sources owed a classification, in order.
func (c *SavedDateClassifier) owedSources() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Sorted(maps.Keys(c.owed))
}

// State is what healthz reports: the start's classification and the
// sources owed one, by source, times in UTC.
func (c *SavedDateClassifier) State() SavedDates {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.start
	out.CheckedAt = out.CheckedAt.UTC()
	out.Owed = make([]OwedClassification, 0, len(c.owed))
	for _, source := range slices.Sorted(maps.Keys(c.owed)) {
		out.Owed = append(out.Owed, c.owed[source])
	}
	return out
}
