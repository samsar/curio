package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// errLocked is a classification's failure, as a database another writer
// holds past the busy timeout gives.
var errLocked = errors.New("database is locked")

// failingClassify is a bookmark store whose classifications fail while
// failing is set, and otherwise run through the store.
type failingClassify struct {
	store.BookmarkStore
	failing atomic.Bool
}

func (f *failingClassify) ClassifySavedDates(ctx context.Context, tenantID, source string) (
	store.SavedDatesClassified, error) {
	if f.failing.Load() {
		return store.SavedDatesClassified{}, errLocked
	}
	return f.BookmarkStore.ClassifySavedDates(ctx, tenantID, source)
}

// warnings are the messages of rec's WARN records that start with prefix.
func (h *logRecorder) warnings(prefix string) []string {
	var out []string
	for _, r := range h.at(slog.LevelWarn) {
		if msg := r["msg"].(string); strings.HasPrefix(msg, prefix) {
			out = append(out, msg)
		}
	}
	return out
}

// healthSavedDates is healthz's saved_dates.
func (s *testServer) healthSavedDates(t *testing.T) SavedDates {
	t.Helper()
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/healthz"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got Health
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	return got.SavedDates
}

// TestImportBookmarks_FailedClassificationIsOwed: an import whose
// classification fails still answers 200, with one WARN; the bookmarks it
// stored claim no date, so a date window leaves them out and counts them
// undated, and healthz owes their source a classification, which the
// retry classifies, paying it.
func TestImportBookmarks_FailedClassificationIsOwed(t *testing.T) {
	rec := &logRecorder{}
	var (
		bms        *failingClassify
		classifier *SavedDateClassifier
	)
	s := newSearchServer(t, okEmbedder(), search.Config{BM25Weight: 1, FieldsWeight: 1}, func(d *Deps) {
		bms = &failingClassify{BookmarkStore: d.Bookmarks}
		bms.failing.Store(true)
		d.Bookmarks = bms
		d.Log = slog.New(rec)
		classifier = NewSavedDateClassifier(context.Background(), SavedDateOptions{Bookmarks: bms, Log: d.Log})
		d.SavedDates = classifier
	})
	// Three saves seconds apart in 2024: exact, once classified.
	saved := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	lone := make([]ImportBookmark, 3)
	for i := range lone {
		lone[i] = ImportBookmark{URL: fmt.Sprintf("https://example.com/sourdough/%d", i),
			Title: "Sourdough notes", SavedAt: saved.Add(time.Duration(i) * 5 * time.Second)}
	}
	got := s.postImport(t, ImportRequest{Source: store.SourceChrome, Bookmarks: lone})
	assert.Equal(t, 3, got.Created)
	assert.Len(t, rec.warnings("import: couldn't classify the saved dates"), 1)
	for url, class := range s.savedDates(t, store.SourceChrome) {
		assert.Equal(t, store.SavedDateUnknown, class, url)
	}
	owed := s.healthSavedDates(t).Owed
	require.Len(t, owed, 1)
	assert.Equal(t, store.SourceChrome, owed[0].Source)
	assert.Equal(t, errLocked.Error(), owed[0].Error)
	assert.False(t, owed[0].Since.IsZero())

	const windowed = `{"query":"sourdough","filters":{"saved_after":"2024-01-01","saved_before":"2025-01-01"}}`
	res := decodeSearch(t, s.search(t, windowed))
	assert.Empty(t, res.Items, "an unclassified bookmark is in no window")
	require.NotNil(t, res.Undated)
	assert.Equal(t, 3+3, *res.Undated, "the kafka pages, with no bookmark, and the unclassified ones")

	bms.failing.Store(false)
	classifier.RetryOwed(context.Background())
	for url, class := range s.savedDates(t, store.SourceChrome) {
		assert.Equal(t, store.SavedDateExact, class, url)
	}
	assert.Empty(t, s.healthSavedDates(t).Owed)
	res = decodeSearch(t, s.search(t, windowed))
	assert.Len(t, res.Items, 3)
	assert.Equal(t, 3, *res.Undated)
	assert.Len(t, rec.warnings("import: couldn't classify the saved dates"), 1, "the retry's success warns nothing")
}

// scriptedClassify is a bookmark store whose classifications answer from
// errs in turn, nil once they run out, counting the calls; one that
// meets a context already ended returns its error.
type scriptedClassify struct {
	store.BookmarkStore
	mu    sync.Mutex
	errs  []error
	calls int
}

func (f *scriptedClassify) ClassifySavedDates(ctx context.Context, _, _ string) (store.SavedDatesClassified, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err := ctx.Err(); err != nil {
		return store.SavedDatesClassified{}, err
	}
	if len(f.errs) == 0 {
		return store.SavedDatesClassified{}, nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return store.SavedDatesClassified{}, err
}

// TestSavedDateClassifier_Owes: a failure owes its source since the
// first one, keeping the last error; a later classification of the
// source pays it, and the owed are listed by source.
func TestSavedDateClassifier_Owes(t *testing.T) {
	bms := &scriptedClassify{errs: []error{errLocked, errors.New("disk I/O error"), errLocked}}
	rec := &logRecorder{}
	checked := time.Date(2026, 10, 14, 9, 0, 0, 0, time.FixedZone("EDT", -4*3600))
	c := NewSavedDateClassifier(context.Background(), SavedDateOptions{Bookmarks: bms, Log: slog.New(rec),
		Start: SavedDates{CheckedAt: checked, Changed: 7, Owed: []OwedClassification{{Source: "ignored"}}}})
	assert.Equal(t, SavedDates{CheckedAt: checked.UTC(), Changed: 7, Owed: []OwedClassification{}}, c.State())

	c.Classify(store.SourceSafari)
	first := c.State().Owed
	require.Len(t, first, 1)
	c.Classify(store.SourceSafari)
	c.Classify(store.SourceChrome)
	owed := c.State().Owed
	require.Len(t, owed, 2)
	assert.Equal(t, store.SourceChrome, owed[0].Source)
	assert.Equal(t, OwedClassification{Source: store.SourceSafari, Since: first[0].Since, Error: "disk I/O error"},
		owed[1], "owed since the first failure, with the last one's error")
	assert.Len(t, rec.warnings("import: couldn't classify"), 3)

	c.Classify(store.SourceSafari)
	assert.Equal(t, []OwedClassification{owed[0]}, c.State().Owed, "a later import's classification pays it")
}

// TestSavedDateClassifier_RunRetries: Run retries what is owed on its
// ticker until a retry succeeds, and stops when its context ends.
func TestSavedDateClassifier_RunRetries(t *testing.T) {
	bms := &scriptedClassify{errs: []error{errLocked, errLocked, errLocked}}
	rec := &logRecorder{}
	c := NewSavedDateClassifier(context.Background(), SavedDateOptions{Bookmarks: bms, Log: slog.New(rec),
		RetryEvery: time.Millisecond})
	c.Classify(store.SourceChrome)
	require.Len(t, c.State().Owed, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	assert.Eventually(t, func() bool { return len(c.State().Owed) == 0 }, 5*time.Second, time.Millisecond)
	cancel()
	<-done
	bms.mu.Lock()
	defer bms.mu.Unlock()
	assert.Equal(t, 4, bms.calls, "the import's, two failed retries and the one that succeeded")
	assert.Len(t, rec.warnings(""), 1, "only the import's failure warns")
}

// TestSavedDateClassifier_StopsWithTheDaemon: a classification runs on the
// daemon's lifetime, so one cut short by the daemon stopping owes nothing
// and warns nothing: the next start classifies every bookmark.
func TestSavedDateClassifier_StopsWithTheDaemon(t *testing.T) {
	bms := &scriptedClassify{}
	rec := &logRecorder{}
	life, stop := context.WithCancel(context.Background())
	c := NewSavedDateClassifier(life, SavedDateOptions{Bookmarks: bms, Log: slog.New(rec)})
	stop()
	c.Classify(store.SourceChrome)
	assert.Empty(t, c.State().Owed)
	assert.Empty(t, rec.warnings(""))
	infos := rec.at(slog.LevelInfo)
	require.Len(t, infos, 1)
	assert.Contains(t, infos[0]["msg"], "its next start classifies them")
}
