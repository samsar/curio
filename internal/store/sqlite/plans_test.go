package sqlite

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// These tests pin the query plans of the store's hot queries. curio never
// runs ANALYZE, so SQLite plans from its heuristics and the schema alone,
// and the plans are stable. Each case runs the SQL the store runs, built by
// the same constant or builder, so a change to a query or an index that
// loses its plan fails here rather than in a slow `curio docs`.

// queryPlan returns the detail column of EXPLAIN QUERY PLAN for q, one
// line per plan row.
func queryPlan(t *testing.T, db *DB, q string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+q, args...)
	require.NoError(t, err)
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		lines = append(lines, detail)
	}
	require.NoError(t, rows.Err())
	return strings.Join(lines, "\n")
}

// deleteDocumentSQL is how a document would be deleted. No store method
// deletes one yet; the case pins the foreign-key action on jobs it runs.
const deleteDocumentSQL = `DELETE FROM documents WHERE id = ?`

type planCase struct {
	name  string
	query string
	args  []any
	// first, if set, is how the plan's first row must start: the table
	// the query is driven from.
	first string
	// want are substrings the plan must contain: an index and the
	// constraints it is searched with.
	want []string
	// sorts allows a temporary b-tree. Every other plan must read its
	// rows in order.
	sorts bool
	// avoid are substrings the plan must not contain: indexes that would
	// read far more rows than the query needs.
	avoid []string
}

// listPlanCases pins the three paged lists: for every filter, the first
// page and a page after a cursor both walk their index in the list's order,
// the cursor page from the keyset constraint, with no sort.
func listPlanCases(t *testing.T) []planCase {
	after := store.PageKey{At: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), ID: "last-row"}
	return slices.Concat(listPagePlanCases(t, "first page", store.PageKey{}),
		listPagePlanCases(t, "cursor page", after))
}

// listPagePlanCases are the list plan cases for one page, named page: the
// one after the key after.
func listPagePlanCases(t *testing.T, page string, after store.PageKey) []planCase {
	keyset := func(ts string) string {
		if after.IsZero() {
			return ""
		}
		return " AND (" + ts + ",id)<(?,?)"
	}
	docs := func(name string, opts store.ListDocumentsOpts, index string) planCase {
		opts.After = after
		q, args := listDocumentsQuery("local", opts)
		want := []string{
			index + keyset("updated_at") + ")",
			"SEARCH j USING INDEX idx_jobs_document (document_id=? AND status=?)",
			untitledBookmarkTitle,
		}
		if opts.Folder != "" {
			want = append(want, "SEARCH b EXISTS USING INDEX idx_bookmarks_document (document_id=?)")
		}
		return planCase{name: name + ", " + page, query: q, args: args, want: want,
			avoid: []string{"idx_bookmarks_tenant_"}}
	}
	const (
		byTenant = "SEARCH d USING INDEX idx_documents_tenant_updated (tenant_id=?"
		byState  = "SEARCH d USING INDEX idx_documents_tenant_state_updated (tenant_id=? AND state=?"
		// A cause seeks its own documents in the partial index of the
		// documents that failed, whatever else filters the list.
		byCause = "SEARCH d USING INDEX idx_documents_tenant_cause_updated (tenant_id=? AND failure_cause=?"
	)
	fetched := store.DocStateFetched
	antiBot := store.FailureCauseAntiBot
	jobs := func(name string, opts store.ListJobsOpts, index string) planCase {
		opts.After = after
		q, args := listJobsQuery("local", opts)
		return planCase{name: name + ", " + page, query: q, args: args, want: []string{
			index + keyset("updated_at") + ")",
			"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)",
		}}
	}
	// A document's jobs are a handful, so sorting them is cheap; walking a
	// tenant index to find them is not.
	jobsOfDocument := func(name string, opts store.ListJobsOpts, constraints string) planCase {
		opts.After, opts.DocumentID = after, "doc"
		q, args := listJobsQuery("local", opts)
		return planCase{name: name + ", " + page, query: q, args: args,
			first: "SEARCH j USING INDEX idx_jobs_document (" + constraints,
			want:  []string{"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)"},
			sorts: true,
			avoid: []string{"idx_jobs_tenant_"},
		}
	}
	// Each order walks its own index under every filter, reaching the
	// document by its primary key and its last error through
	// idx_jobs_document.
	bookmarks := func(name string, opts store.ListBookmarksOpts) []planCase {
		orders := []struct {
			order     store.BookmarkOrder
			index, ts string
		}{
			{store.BookmarkOrderCreated, "idx_bookmarks_tenant_created", "created_at"},
			{store.BookmarkOrderSaved, "idx_bookmarks_tenant_saved", "saved_at"},
		}
		cases := make([]planCase, 0, len(orders))
		for _, o := range orders {
			opts.Order, opts.After = o.order, after
			q, args, err := listBookmarksQuery("local", opts)
			require.NoError(t, err)
			cases = append(cases, planCase{name: name + " in " + string(o.order) + " order, " + page, query: q,
				args: args, want: []string{
					"SEARCH b USING INDEX " + o.index + " (tenant_id=?" + keyset(o.ts) + ")",
					"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)",
					"SEARCH j USING INDEX idx_jobs_document (document_id=? AND status=?)",
				}})
		}
		return cases
	}
	return slices.Concat([]planCase{
		docs("ListWithLastError", store.ListDocumentsOpts{}, byTenant),
		docs("ListWithLastError by state", store.ListDocumentsOpts{State: fetched}, byState),
		docs("ListWithLastError by content type", store.ListDocumentsOpts{ContentType: store.ContentTypePDF}, byTenant),
		docs("ListWithLastError by state and content type",
			store.ListDocumentsOpts{State: fetched, ContentType: store.ContentTypePDF}, byState),
		docs("ListWithLastError by host", store.ListDocumentsOpts{Host: "example.com"}, byTenant),
		docs("ListWithLastError by state and host", store.ListDocumentsOpts{State: fetched, Host: "example.com"}, byState),
		docs("ListWithLastError by folder", store.ListDocumentsOpts{Folder: "/Tech/AI"}, byTenant),
		docs("ListWithLastError by state and folder", store.ListDocumentsOpts{State: fetched, Folder: "/Tech/AI"}, byState),
		docs("ListWithLastError by content type, host and folder", store.ListDocumentsOpts{
			ContentType: store.ContentTypeArticle, Host: "example.com", Folder: "/Tech"}, byTenant),
		docs("ListWithLastError by every filter", store.ListDocumentsOpts{State: fetched,
			ContentType: store.ContentTypeArticle, Host: "example.com", Folder: "/Tech"}, byState),
		docs("ListWithLastError by cause", store.ListDocumentsOpts{Cause: antiBot}, byCause),
		docs("ListWithLastError by cause and state",
			store.ListDocumentsOpts{Cause: antiBot, State: store.DocStateFailed}, byCause),
		docs("ListWithLastError by cause and host", store.ListDocumentsOpts{Cause: antiBot, Host: "example.com"}, byCause),
		docs("ListWithLastError by cause and folder", store.ListDocumentsOpts{Cause: antiBot, Folder: "/Tech"}, byCause),
		jobs("ListWithDoc", store.ListJobsOpts{},
			"SEARCH j USING INDEX idx_jobs_tenant_updated (tenant_id=?"),
		jobs("ListWithDoc by status", store.ListJobsOpts{Status: store.JobStatusDone},
			"SEARCH j USING INDEX idx_jobs_tenant_status_updated (tenant_id=? AND status=?"),
		jobs("ListWithDoc by kind", store.ListJobsOpts{Kind: store.JobKindFetch},
			"SEARCH j USING INDEX idx_jobs_tenant_updated (tenant_id=?"),
		jobs("ListWithDoc by status and kind", store.ListJobsOpts{Status: store.JobStatusDone, Kind: store.JobKindFetch},
			"SEARCH j USING INDEX idx_jobs_tenant_status_updated (tenant_id=? AND status=?"),
		jobsOfDocument("ListWithDoc by document", store.ListJobsOpts{}, "document_id=?"),
		jobsOfDocument("ListWithDoc by document and status", store.ListJobsOpts{Status: store.JobStatusFailed},
			"document_id=? AND status=?"),
		jobsOfDocument("ListWithDoc by document and kind", store.ListJobsOpts{Kind: store.JobKindFetch}, "document_id=?"),
	},
		bookmarks("Bookmarks.List", store.ListBookmarksOpts{}),
		bookmarks("Bookmarks.List by source", store.ListBookmarksOpts{Source: store.SourceChrome}),
		bookmarks("Bookmarks.List by folder", store.ListBookmarksOpts{FolderPath: "/Tech/AI"}),
		bookmarks("Bookmarks.List by host", store.ListBookmarksOpts{Host: "example.com"}),
		bookmarks("Bookmarks.List by state", store.ListBookmarksOpts{State: fetched}),
		bookmarks("Bookmarks.List by content type", store.ListBookmarksOpts{ContentType: store.ContentTypePDF}),
		bookmarks("Bookmarks.List by cause", store.ListBookmarksOpts{Cause: antiBot}),
		bookmarks("Bookmarks.List by every filter", store.ListBookmarksOpts{Source: store.SourceChrome,
			FolderPath: "/Tech", Host: "example.com", State: store.DocStateFailed, ContentType: store.ContentTypeArticle,
			Cause: antiBot}),
	)
}

// untitledBookmarkTitle is the plan row of an untitled document's bookmark
// title (bookmarkTitleSQL): a seek of the document's bookmarks, read in
// saved order, never a walk of a tenant index.
const untitledBookmarkTitle = "SEARCH b USING INDEX idx_bookmarks_document (document_id=?)"

func TestQueryPlans(t *testing.T) {
	db := newTestDB(t)
	claimArgs := []any{store.JobStatusRunning, "now", "now", store.JobStatusPending, "now", store.JobKindFetch}
	bm25Q, bm25Args := bm25Query("local", `"kafka"`, 10, store.SearchFilters{})
	bm25SourceQ, bm25SourceArgs := bm25Query("local", `"kafka"`, 10,
		store.SearchFilters{Source: []string{store.SourceChrome}})
	vecQ, vecArgs := vectorQuery("local", queryVector(t), overfetchK(800), store.SearchFilters{})
	getJobQ, getJobArgs := getJobWithDocQuery("local", "job")
	getDocQ, getDocArgs := getDocumentWithErrorQuery("local", "doc")
	snippetsQ, snippetsArgs := snippetsQuery(`"kafka"`, []string{"a", "b", "c"})

	cases := []planCase{
		{
			name:  "ClaimNext one kind",
			query: claimSQL(1), args: claimArgs,
			want: []string{"SEARCH jobs USING INDEX idx_jobs_claim (status=? AND kind=? AND run_after<?)"},
		},
		{
			name:  "MarkDone",
			query: markDoneSQL, args: []any{store.JobStatusDone, "job", store.JobStatusRunning},
			want: []string{"SEARCH jobs USING INDEX sqlite_autoindex_jobs_1 (id=?)"},
		},
		{
			name:  "Defer",
			query: deferJobSQL,
			args:  []any{store.JobStatusPending, "later", "waiting", "now", "job", store.JobStatusRunning},
			want:  []string{"SEARCH jobs USING INDEX sqlite_autoindex_jobs_1 (id=?)"},
		},
		{
			name:  "RecoverOrphans fail",
			query: failOrphansSQL(2),
			args:  []any{store.JobStatusFailed, "error", "now", store.JobStatusRunning, 5, store.JobKindFetch, store.JobKindIndex},
			want:  []string{"INDEX idx_jobs_claim (status=? AND kind=?)"},
		},
		{
			name:  "RecoverOrphans requeue",
			query: requeueOrphansSQL(2),
			args:  []any{store.JobStatusPending, "now", "now", store.JobStatusRunning, store.JobKindFetch, store.JobKindIndex},
			want:  []string{"INDEX idx_jobs_claim (status=? AND kind=?)"},
		},
		{
			name:  "Documents.GetByURL",
			query: "SELECT " + documentColumns + " FROM documents WHERE " + getByURLWhere,
			args:  []any{"local", "https://example.com/x"},
			want:  []string{"SEARCH documents USING INDEX sqlite_autoindex_documents_2 (tenant_id=? AND url=?)"},
		},
		{
			name:  "GetWithDoc",
			query: getJobQ, args: getJobArgs,
			first: "SEARCH j USING INDEX sqlite_autoindex_jobs_1 (id=?)",
			want:  []string{"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)"},
		},
		{
			name:  "MetricsByKind durations",
			query: metricsDurationsSQL, args: []any{"local", "cutoff"},
			want: []string{"INDEX idx_jobs_tenant_status_updated (tenant_id=? AND status=? AND updated_at>?)"},
			// GROUP BY kind over the window's rows, and the percentile ranks.
			sorts: true,
		},
		{
			name:  "PruneOlderThan",
			query: pruneJobsSQL, args: []any{"local", store.JobStatusDone, store.JobStatusFailed, "cutoff"},
			want: []string{"INDEX idx_jobs_tenant_status_updated (tenant_id=? AND status=? AND updated_at<?)"},
		},
		{
			name:  "DeleteByStatus",
			query: deleteJobsByStatusSQL, args: []any{"local", store.JobStatusDone},
			want: []string{"INDEX idx_jobs_tenant_status_updated (tenant_id=? AND status=?)"},
		},
		{
			name:  "CountByStatus",
			query: countJobsSQL, args: []any{"local"},
			want: []string{"INDEX idx_jobs_tenant_status_updated (tenant_id=?)"},
		},
		{
			name:  "QueueCounts",
			query: queueCountsSQL, args: []any{store.JobStatusPending, store.JobStatusRunning, "now"},
			want: []string{"SEARCH jobs USING COVERING INDEX idx_jobs_claim (status=?)"},
		},
		{
			name:  "QueueSettings.Get",
			query: getQueueSettingsSQL,
			want:  []string{"SEARCH queue_settings USING INTEGER PRIMARY KEY (rowid=?)"},
		},
		{
			name:  "CountByState",
			query: countDocumentsSQL, args: []any{"local"},
			want: []string{"INDEX idx_documents_tenant_state_updated (tenant_id=?)"},
		},

		{
			name:  "ListIDsWithContent",
			query: listIDsWithContentSQL, args: []any{"local", store.DocStateFetched},
			want: []string{"INDEX idx_documents_tenant_state_updated (tenant_id=? AND state=?)"},
		},
		{
			name:  "DocumentVectors",
			query: documentVectorsSQL, args: []any{"local", store.DocStateFetched},
			// The index ends in id, so it covers the documents side.
			want: []string{"SEARCH d USING COVERING INDEX idx_documents_tenant_state_updated (tenant_id=? AND state=?)"},
			// ORDER BY document, ord across the joined chunks.
			sorts: true,
		},
		{
			// The fetched documents' chunks once, to rank their lengths;
			// a chunk picked for each row of picked, never for each
			// candidate document; then each sampled chunk by its seq. Its
			// vector is a point lookup
			// (TestQueryPlans_SampleChunksReadsVectorsByID).
			name:  "SampleChunks",
			query: sampleChunksSQL, args: []any{"local", store.DocStateFetched, 16, 48},
			want: []string{
				"SEARCH d USING COVERING INDEX idx_documents_tenant_state_updated (tenant_id=? AND state=?)",
				"SEARCH c USING INDEX idx_chunks_document (document_id=?)",
				"SEARCH c EXISTS USING COVERING INDEX idx_chunks_document (document_id=?)",
				"SCAN p\nSEARCH c USING INTEGER PRIMARY KEY (rowid=?)\nCORRELATED SCALAR SUBQUERY",
				"SEARCH c USING INTEGER PRIMARY KEY (rowid=?)",
			},
			// The length ranking, the shuffles and each document's chunk
			// pick.
			sorts: true,
		},
		{
			name:  "MarkFetched",
			query: markFetchedSQL, args: []any{store.DocStateFetched, 100, "doc"},
			first: "SEARCH documents USING INDEX sqlite_autoindex_documents_1 (id=?)",
		},
		{
			name:  "LastIndexedAt",
			query: lastIndexedSQL, args: []any{"local"},
			first: "SEARCH documents USING COVERING INDEX idx_documents_tenant_indexed (tenant_id=?)",
		},
		{
			name:  "RequeueFetchByStates",
			query: resetStatesSQL(2, false),
			args:  []any{store.DocStatePending, "local", store.DocStateFailed, store.DocStateFetched},
			want:  []string{"INDEX idx_documents_tenant_state_updated (tenant_id=? AND state=?)"},
		},
		{
			name:  "RequeueFetchByStates by cause",
			query: resetStatesSQL(3, true),
			args: []any{store.DocStatePending, "local", store.DocStatePending, store.DocStateFetched, store.DocStateFailed,
				store.FailureCauseAntiBot},
			want: []string{"INDEX idx_documents_tenant_cause_updated (tenant_id=? AND failure_cause=?)"},
		},
		{
			name:  "FailureSummary",
			query: failureSummarySQL, args: []any{"local"},
			want: []string{"SEARCH documents USING INDEX idx_documents_tenant_cause_updated (tenant_id=? AND failure_cause>?)"},
		},
		{
			// A document's few chunks, sorted by ord.
			name:  "ReplaceForDocument chunks",
			query: documentChunksSQL, args: []any{"doc"},
			first: "SEARCH chunks USING INDEX idx_chunks_document (document_id=?)",
			sorts: true,
		},
		{
			name:  "ReplaceForDocument update",
			query: updateChunkSQL, args: unbound(updateChunkSQL),
			first: "SEARCH chunks USING INTEGER PRIMARY KEY (rowid=?)",
		},
		{
			name:  "ReplaceForDocument delete",
			query: deleteChunksFromSQL, args: []any{"doc", 3},
			first: "SEARCH chunks USING INDEX idx_chunks_document (document_id=?)",
		},
		{
			// The document predicates, the state exclusion included, are
			// checked on each hit through the primary keys.
			name:  "BM25Search",
			query: bm25Q, args: bm25Args,
			first: "SCAN chunks_fts VIRTUAL TABLE",
			want: []string{
				"SEARCH c USING INTEGER PRIMARY KEY (rowid=?)",
				"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)",
			},
			// ORDER BY the bm25 score.
			sorts: true,
		},
		{
			// The source filter's EXISTS seeks the hit's document's
			// bookmarks, however many bookmarks the tenant has.
			name:  "BM25Search by source",
			query: bm25SourceQ, args: bm25SourceArgs,
			first: "SCAN chunks_fts VIRTUAL TABLE",
			want:  []string{"SEARCH b EXISTS USING INDEX idx_bookmarks_document (document_id=?)"},
			sorts: true,
			avoid: []string{"idx_bookmarks_tenant_"},
		},
		{
			// Each chunk's FTS row by rowid, its seq found by its ID, never
			// a walk of every row the MATCH finds: FTS5 reports the rowid
			// equality as '=' and the MATCH as 'M'.
			name:  "Snippets",
			query: snippetsQ, args: snippetsArgs,
			first: "SCAN chunks_fts VIRTUAL TABLE INDEX 0:=M",
			want: []string{
				"SEARCH chunks USING COVERING INDEX sqlite_autoindex_chunks_1 (id=?)",
				"SEARCH c USING INTEGER PRIMARY KEY (rowid=?)",
			},
		},
		{
			// A KNN scan of chunks_vec (plan kind 3, see
			// TestQueryPlans_ChunkVectorWritesArePointLookups), then the same
			// per-hit lookups.
			name:  "VectorSearch",
			query: vecQ, args: vecArgs,
			first: "SCAN v VIRTUAL TABLE INDEX 0:3",
			want: []string{
				"SEARCH c USING INDEX sqlite_autoindex_chunks_1 (id=?)",
				"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)",
			},
			// ORDER BY distance.
			sorts: true,
		},
		{
			// Either tenant index covers the count equally; SQLite takes the
			// saved one.
			name:  "Bookmarks.Count",
			query: countBookmarksSQL, args: []any{"local"},
			want: []string{"SEARCH bookmarks USING COVERING INDEX idx_bookmarks_tenant_saved (tenant_id=?)"},
		},
		{
			// Driven by the URLs given, each looked up in the unique
			// indexes Ingest's inserts conflict on.
			name:  "PreviewIngest",
			query: previewIngestSQL, args: []any{"local", "local", store.SourceChrome, `["https://example.com/a"]`},
			first: "SCAN j",
			want: []string{
				"SEARCH d USING COVERING INDEX sqlite_autoindex_documents_2 (tenant_id=? AND url=?)",
				"SEARCH b USING COVERING INDEX sqlite_autoindex_bookmarks_2 (tenant_id=? AND url=? AND source=?)",
			},
		},
		{
			name:  "TagsForDocument",
			query: tagsForDocumentSQL, args: []any{"local", "doc"},
			want: []string{"SEARCH bookmarks USING INDEX idx_bookmarks_document (document_id=?)"},
		},
		{
			name:  "GetWithLastError",
			query: getDocQ, args: getDocArgs,
			first: "SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)",
			want:  []string{"SEARCH j USING INDEX idx_jobs_document (document_id=? AND status=?)", untitledBookmarkTitle},
			avoid: []string{"idx_bookmarks_tenant_"},
		},
		{
			// The IDs' own documents, each by its primary key. Written with
			// d.tenant_id = ?, the same query walks every document the
			// tenant has in idx_documents_tenant_state_updated instead.
			name:  "GetByIDsWithLastError",
			query: getDocumentsWithErrorSQL, args: []any{store.JobStatusFailed, `["a","b","c"]`, "local"},
			first: "SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)",
			want: []string{
				"SEARCH e USING INDEX sqlite_autoindex_document_extractions_1 (id=?)",
				"SEARCH j USING INDEX idx_jobs_document (document_id=? AND status=?)",
				untitledBookmarkTitle,
			},
			avoid: []string{"idx_documents_tenant_", "idx_bookmarks_tenant_"},
		},
		{
			// The document's bookmarks, read in the order they are listed.
			name:  "ListByDocument",
			query: listBookmarksByDocumentSQL, args: []any{"local", "doc"},
			first: "SEARCH bookmarks USING INDEX idx_bookmarks_document (document_id=?)",
			avoid: []string{"idx_bookmarks_tenant_"},
		},
		{
			// Each document's bookmarks by its ID, never a walk of the
			// tenant's bookmarks; the few found are sorted.
			name:  "ListByDocuments",
			query: listBookmarksByDocumentsSQL, args: []any{`["a","b","c"]`, "local"},
			want:  []string{"SEARCH bookmarks USING INDEX idx_bookmarks_document (document_id=?)"},
			sorts: true,
			avoid: []string{"idx_bookmarks_tenant_"},
		},
		{
			// A tenant's bookmarks, read in saved order from the index
			// that holds them in it: no sort, whatever their number.
			name:  "ClassifySavedDates read",
			query: savedDateRowsSQL, args: []any{"local"},
			first: "SEARCH bookmarks USING INDEX idx_bookmarks_tenant_saved (tenant_id=?)",
		},
		{
			// One source's the same way, the source checked on each row.
			name:  "ClassifySavedDates read of a source",
			query: savedDateSourceRowsSQL, args: []any{"local", store.SourceChrome},
			first: "SEARCH bookmarks USING INDEX idx_bookmarks_tenant_saved (tenant_id=?)",
		},
		{
			name:  "ClassifySavedDates write",
			query: setSavedDateSQL, args: []any{store.SavedDateExact, "bookmark"},
			first: "SEARCH bookmarks USING INDEX sqlite_autoindex_bookmarks_1 (id=?)",
		},
		{
			// The tenant's Safari bookmarks still marked dated, sought
			// through a tenant index, never a scan of every tenant's.
			name:  "EnsureSavedDates marks Safari dateless",
			query: markDatelessSQL, args: []any{"local", store.SourceSafari},
			first: "SEARCH bookmarks USING INDEX ",
			want:  []string{"(tenant_id=?"},
		},
		{
			// The tenants in order, from a tenant index alone.
			name:  "EnsureSavedDates tenants",
			query: bookmarkTenantsSQL,
			first: "SCAN bookmarks USING COVERING INDEX idx_bookmarks_tenant_",
		},
		{
			// The foreign-key actions reach the document's rows in each
			// table by its own index, never a scan of every run's.
			name:  "document delete reaches its jobs",
			query: deleteDocumentSQL, args: []any{"doc"},
			want: []string{
				"SEARCH jobs USING COVERING INDEX idx_jobs_document (document_id=?)",
				"SEARCH interest_assignments USING COVERING INDEX idx_interest_assignments_document (document_id=?)",
				"SEARCH interest_placements USING COVERING INDEX idx_interest_placements_document (document_id=?)",
			},
		},
		{
			// The chunks of an older context version, one covering range,
			// listed (and bloom-filtered) for the tenant's fetched
			// documents.
			name:  "DocumentsToReembed",
			query: documentsToReembedSQL, args: []any{1, "local", store.DocStateFetched},
			first: "SEARCH d USING INDEX idx_documents_tenant_state_updated (tenant_id=? AND state=?)",
			want:  []string{"SEARCH chunks USING COVERING INDEX idx_chunks_context_version (context_version<?)"},
		},
		{
			// The range again, each chunk's document checked against a
			// list of the fetched ones, never a seek of idx_chunks_document
			// per document; the distinct documents counted in a b-tree.
			name:  "CountToReembed",
			query: countToReembedSQL, args: []any{1, "local", store.DocStateFetched},
			want: []string{
				"SEARCH c USING COVERING INDEX idx_chunks_context_version (context_version<?)",
				"LIST SUBQUERY 1",
				"SEARCH d USING INDEX idx_documents_tenant_state_updated (tenant_id=? AND state=?)",
			},
			avoid: []string{"idx_chunks_document"},
			sorts: true,
		},
		{
			// The running and the pending index jobs, each status a range
			// of the claim index, the pending read once for all of them and
			// the due.
			name:  "EnqueueReembed queued",
			query: reembedQueuedSQL,
			args:  []any{store.JobStatusRunning, store.JobStatusPending, store.JobKindIndex, "now"},
			want:  []string{"SEARCH jobs USING INDEX idx_jobs_claim (status=? AND kind=?)"},
			avoid: []string{"SCAN jobs"},
		},
		{
			// The range in order, each chunk's document by its key and its
			// index jobs, and its re-embeds failed since a time, a seek
			// each, stopping at the documents it needs; run once for the
			// documents that never failed (the zero time), and again for
			// those whose wait is over when that finds too few.
			name:  "EnqueueReembed next",
			query: nextToReembedSQL,
			args: []any{1, "local", store.DocStateFetched, store.JobStatusPending, 2, store.JobKindIndex,
				store.JobStatusRunning, store.JobStatusFailed, "now"},
			first: "SEARCH c USING COVERING INDEX idx_chunks_context_version (context_version<?)",
			want: []string{
				"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)",
				"SEARCH j USING INDEX idx_jobs_document (document_id=? AND status=?)",
				"SEARCH f USING INDEX idx_jobs_document (document_id=? AND status=? AND updated_at>?)",
			},
			// DISTINCT, as the rows go.
			sorts: true,
		},
		{
			name:  "ReembedFailures",
			query: reembedFailuresSQL, args: []any{"doc", store.JobStatusFailed, store.JobKindIndex},
			first: "SEARCH jobs USING INDEX idx_jobs_document (document_id=? AND status=?)",
		},
		{
			// The tenant's documents indexed since, from the first, never a
			// walk of its documents or of the jobs.
			name:  "ReembedFailures others indexed",
			query: othersIndexedSQL, args: []any{"local", "now", "doc"},
			want:  []string{"SEARCH documents USING INDEX idx_documents_tenant_indexed (tenant_id=? AND indexed_at>?)"},
			avoid: []string{"SCAN documents"},
		},
		{
			name:  "StaleContext",
			query: staleContextSQL, args: []any{"doc", 1},
			want:  []string{"SEARCH chunks USING INDEX idx_chunks_document (document_id=?)"},
			avoid: []string{"idx_chunks_context_version"},
		},
		{
			name:  "ReplaceIndexed guard",
			query: indexGuardSQL, args: []any{"doc"},
			first: "SEARCH documents USING INDEX sqlite_autoindex_documents_1 (id=?)",
		},
		{
			name:  "MarkIndexed",
			query: markIndexedSQL, args: []any{store.DocStateFetched, 100, "doc", "ext"},
			first: "SEARCH documents USING INDEX sqlite_autoindex_documents_1 (id=?)",
		},
		{
			name:  "MarkIndexed a re-embed",
			query: markReembeddedSQL, args: []any{100, "doc", "ext", store.DocStateFetched},
			first: "SEARCH documents USING INDEX sqlite_autoindex_documents_1 (id=?)",
		},
		{
			name:  "MarkIndexed superseded",
			query: documentExistsSQL, args: []any{"doc"},
			want: []string{"SEARCH documents USING COVERING INDEX sqlite_autoindex_documents_1 (id=?)"},
		},
		{
			name:  "KeepEmbedded",
			query: keepEmbeddedSQL, args: []any{1, "doc"},
			first: "SEARCH chunks USING INDEX idx_chunks_document (document_id=?)",
		},
		{
			// The pending check seeks the pending jobs of the kind, never a
			// walk of the tenant's pending jobs (+tenant_id).
			name:  "EnqueueOnce insert",
			query: insertJobOnceSQL, args: []any{"job", "local", store.JobKindCluster, "{}", store.JobStatusPending, 0, "now"},
			want:  []string{"SEARCH jobs USING INDEX idx_jobs_claim (status=? AND kind=?)"},
			avoid: []string{"idx_jobs_tenant_"},
		},
		{
			name:  "EnqueueOnce pending job",
			query: pendingJobSQL, args: []any{store.JobStatusPending, store.JobKindCluster, "local"},
			first: "SEARCH jobs USING INDEX idx_jobs_claim (status=? AND kind=?)",
			avoid: []string{"idx_jobs_tenant_"},
		},
	}
	for _, tc := range slices.Concat(cases, listPlanCases(t), insightPlanCases(), documentIndexPlanCases(t),
		voteTriggerPlanCases(t, db), searchFilterPlanCases(t)) {
		t.Run(tc.name, func(t *testing.T) {
			plan := queryPlan(t, db, tc.query, tc.args...)
			assert.True(t, strings.HasPrefix(plan, tc.first), "plan starts with %q:\n%s", tc.first, plan)
			for _, want := range tc.want {
				assert.Contains(t, plan, want)
			}
			if !tc.sorts {
				assert.NotContains(t, plan, "USE TEMP B-TREE")
			}
			for _, avoid := range tc.avoid {
				assert.NotContains(t, plan, avoid)
			}
		})
	}
}

// searchFilterPlanCases pin the lists under the filters: the text lists
// stay driven by their MATCH, each filter one seek of the document's
// bookmarks or fields, never a walk of a tenant's; a narrowing filter's
// KNN counts and ranks the rows it leaves from the tenant's documents,
// each one's chunks or card by its key, and its over-fetch keeps the
// unfiltered KNN's plan. See docs/decisions.md "Saved dates and search
// filters".
func searchFilterPlanCases(t *testing.T) []planCase {
	after := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	before := after.AddDate(1, 0, 0)
	saved := store.SearchFilters{SavedAfter: after, SavedBefore: before}
	published := store.SearchFilters{PublishedAfter: after, PublishedBefore: before, IncludeUndated: true}
	every := store.SearchFilters{ContentType: []string{"article"}, Host: []string{"example.com"},
		Source: []string{store.SourceChrome}, Site: []string{"example.com"}, Folder: []string{"/Tech"},
		SavedAfter: after, SavedBefore: before, PublishedAfter: after, PublishedBefore: before, IncludeUndated: true}
	const (
		document   = "SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)"
		savedSeek  = "SEARCH sb EXISTS USING INDEX idx_bookmarks_document (document_id=? AND saved_at>? AND saved_at<?)"
		trustSeek  = "SEARCH pb USING INDEX idx_bookmarks_document (document_id=? AND saved_at<?)"
		folderSeek = "SEARCH fb EXISTS USING INDEX idx_bookmarks_document (document_id=?)"
		siteSeek   = "SEARCH sf EXISTS USING INDEX sqlite_autoindex_document_fields_1 (document_id=?)"
		// The tenant's documents, or with predicates that read their rows
		// (content type, URL, published date), the narrowest tenant index
		// SQLite takes for them, which reads the same rows.
		tenantWalk = "SEARCH d USING COVERING INDEX idx_documents_tenant_state_updated (tenant_id=?)"
		anyTenant  = "INDEX idx_documents_tenant_"
		exactKNN   = "SCAN v VIRTUAL TABLE INDEX 0:3{___}___[___"
		chunkSeek  = "SEARCH fc USING INDEX idx_chunks_document (document_id=?)"
		// The count reads the chunks' keys alone.
		chunkCount = "SEARCH fc USING COVERING INDEX idx_chunks_document (document_id=?)"
		cardSeek   = "SEARCH fc USING INDEX sqlite_autoindex_document_cards_1 (document_id=?)"
	)
	bookmarkTenants := []string{"idx_bookmarks_tenant_"}
	// Every filter's seeks, a document's own by its key.
	everySeek := []string{"SEARCH sb USING INDEX idx_bookmarks_document (document_id=? AND saved_at>? AND saved_at<?)",
		"SEARCH ub USING INDEX idx_bookmarks_document (document_id=?)", trustSeek,
		"SEARCH b EXISTS USING INDEX idx_bookmarks_document (document_id=?)", folderSeek, siteSeek, document}
	var cases []planCase
	add := func(tc planCase, build func() (string, []any)) {
		tc.query, tc.args = build()
		cases = append(cases, tc)
	}
	add(planCase{name: "BM25Search by saved window", first: "SCAN chunks_fts VIRTUAL TABLE", want: []string{savedSeek},
		sorts: true, avoid: bookmarkTenants},
		func() (string, []any) { return bm25Query("local", `"kafka"`, 800, saved) })
	add(planCase{name: "BM25Search by published window", first: "SCAN chunks_fts VIRTUAL TABLE",
		want: []string{trustSeek}, sorts: true, avoid: bookmarkTenants},
		func() (string, []any) { return bm25Query("local", `"kafka"`, 800, published) })
	add(planCase{name: "BM25Search by site and folder", first: "SCAN chunks_fts VIRTUAL TABLE",
		want: []string{folderSeek, siteSeek}, sorts: true, avoid: bookmarkTenants},
		func() (string, []any) {
			return bm25Query("local", `"kafka"`, 800, store.SearchFilters{Site: []string{"example.com"},
				Folder: []string{"/Tech"}})
		})
	add(planCase{name: "SearchFields by every filter", first: "SCAN document_fields_fts VIRTUAL TABLE", want: everySeek,
		sorts: true, avoid: append(bookmarkTenants, anyTenant)},
		func() (string, []any) { return fieldsQuery("local", `"kafka"`, 200, every) })
	add(planCase{name: "SearchTitles by every filter", first: "SCAN document_fields_fts VIRTUAL TABLE", want: everySeek,
		sorts: true, avoid: append(bookmarkTenants, anyTenant)},
		func() (string, []any) { return titlesQuery("local", `"kafka" AND "retention"`, 200, every) })
	add(planCase{name: "VectorSearch count", want: []string{tenantWalk, savedSeek, chunkCount},
		avoid: []string{"SCAN d", "SCAN fc"}},
		func() (string, []any) { return chunkSet("local", saved).countSQL() })
	add(planCase{name: "VectorSearch count by every filter", want: []string{anyTenant, chunkCount},
		avoid: []string{"SCAN d", "SCAN fc"}},
		func() (string, []any) { return chunkSet("local", every).countSQL() })
	add(planCase{name: "VectorSearch exact", first: exactKNN,
		want:  []string{"LIST SUBQUERY", tenantWalk, savedSeek, chunkSeek, "SEARCH c USING INDEX sqlite_autoindex_chunks_1 (id=?)"},
		sorts: true, avoid: []string{"SCAN chunks", "SCAN d", "SCAN fc"}},
		func() (string, []any) { return exactVectorQuery(queryVector(t), 800, chunkSet("local", saved)) })
	add(planCase{name: "VectorSearch exact, its check", want: []string{tenantWalk, savedSeek, chunkSeek},
		avoid: []string{"SCAN d", "SCAN fc"}},
		func() (string, []any) { return chunkSet("local", saved).existsSQL() })
	add(planCase{name: "VectorSearch over the bound", first: "SCAN v VIRTUAL TABLE INDEX 0:3",
		want:  []string{"SEARCH c USING INDEX sqlite_autoindex_chunks_1 (id=?)", document, savedSeek},
		sorts: true, avoid: bookmarkTenants},
		func() (string, []any) { return vectorQuery("local", queryVector(t), vec0MaxK, saved) })
	// The share of the index a set over the bound is, against every row
	// the index holds: the chunks' smallest index, and the cards' table,
	// one row a document.
	add(planCase{name: "VectorSearch over the bound, the index's rows",
		first: "SCAN chunks USING COVERING INDEX idx_chunks_document"},
		func() (string, []any) { return chunkIndexCountSQL, nil })
	add(planCase{name: "SearchCards over the bound, the index's rows", first: "SCAN document_cards"},
		func() (string, []any) { return cardIndexCountSQL, nil })
	add(planCase{name: "SearchCards count", want: []string{tenantWalk, savedSeek, cardSeek},
		avoid: []string{"SCAN d", "SCAN fc"}},
		func() (string, []any) { return cardSet("local", saved).countSQL() })
	add(planCase{name: "SearchCards exact", first: exactKNN,
		want:  []string{"LIST SUBQUERY", tenantWalk, savedSeek, cardSeek},
		sorts: true, avoid: []string{"SCAN d", "SCAN fc"}},
		func() (string, []any) { return exactCardsQuery(queryVector(t), 2000, cardSet("local", saved)) })
	add(planCase{name: "SearchCards exact, its check", want: []string{tenantWalk, savedSeek, cardSeek},
		avoid: []string{"SCAN d", "SCAN fc"}},
		func() (string, []any) { return cardSet("local", saved).existsSQL() })
	add(planCase{name: "SearchCards over the bound by every filter", first: "SCAN v VIRTUAL TABLE INDEX 0:3",
		want: []string{document}, sorts: true, avoid: append(bookmarkTenants, anyTenant)},
		func() (string, []any) { return cardsQuery("local", queryVector(t), vec0MaxK, every) })
	add(planCase{name: "CountUndated", first: tenantWalk,
		want: []string{"SEARCH ub USING INDEX idx_bookmarks_document (document_id=?)"}, avoid: []string{"SCAN"}},
		func() (string, []any) { return undatedQuery("local", saved) })
	add(planCase{name: "CountUndated by every filter", first: "SEARCH d USING", want: append([]string{anyTenant}, everySeek[:6]...),
		avoid: append(bookmarkTenants, "SCAN")},
		func() (string, []any) { return undatedQuery("local", every) })
	return cases
}

// documentIndexPlanCases pin the document lists' searches, the cards
// job's reads and writes, and the statements that keep the fields and
// cards in step (see "Metadata search: a fields index and a card per
// document"). Each document's derived rows are reached by its keys, and a
// host's agreement by the leaders of its votes and two ranges of its rows
// that hold none unless the agreement changed, so a write's reads don't
// grow with its host, let alone the library.
func documentIndexPlanCases(t *testing.T) []planCase {
	fieldsQ, fieldsArgs := fieldsQuery("local", `"kafka"`, 200, store.SearchFilters{})
	fieldsSourceQ, fieldsSourceArgs := fieldsQuery("local", `"kafka"`, 200,
		store.SearchFilters{Source: []string{store.SourceChrome}})
	titlesQ, titlesArgs := titlesQuery("local", `"kafka" AND "retention"`, 200, store.SearchFilters{})
	cardsQ, cardsArgs := cardsQuery("local", queryVector(t), overfetchK(200), store.SearchFilters{})
	const (
		document     = "SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)"
		fieldsByDoc  = "SEARCH document_fields USING INDEX sqlite_autoindex_document_fields_1 (document_id=?)"
		cardByDoc    = "SEARCH document_cards USING INDEX sqlite_autoindex_document_cards_1 (document_id=?)"
		noTableScans = "SCAN"
	)
	return []planCase{
		{
			// The MATCH drives; each hit's fields by rowid and document by
			// primary key, whatever the document's state.
			name:  "SearchFields",
			query: fieldsQ, args: fieldsArgs,
			first: "SCAN document_fields_fts VIRTUAL TABLE",
			want:  []string{"SEARCH f USING INTEGER PRIMARY KEY (rowid=?)", document},
			// ORDER BY the bm25 score.
			sorts: true,
		},
		{
			name:  "SearchFields by source",
			query: fieldsSourceQ, args: fieldsSourceArgs,
			first: "SCAN document_fields_fts VIRTUAL TABLE",
			want:  []string{"SEARCH b EXISTS USING INDEX idx_bookmarks_document (document_id=?)"},
			sorts: true,
			avoid: []string{"idx_bookmarks_tenant_"},
		},
		{
			// As SearchFields: the MATCH, held to the title columns, drives.
			name:  "SearchTitles",
			query: titlesQ, args: titlesArgs,
			first: "SCAN document_fields_fts VIRTUAL TABLE",
			want:  []string{"SEARCH f USING INTEGER PRIMARY KEY (rowid=?)", document},
			sorts: true,
		},
		{
			// A KNN scan of document_cards_vec, then each card's document
			// by primary key.
			name:  "SearchCards",
			query: cardsQ, args: cardsArgs,
			first: "SCAN v VIRTUAL TABLE INDEX 0:3",
			want:  []string{document},
			sorts: true,
		},
		{
			// The stale cards alone, from the cursor, in order.
			name:  "StaleCards",
			query: staleCardsSQL, args: []any{"after", "local", 32},
			first: "SEARCH c USING INDEX idx_document_cards_stale (document_id>?)",
			want:  []string{document},
			avoid: []string{"idx_documents_tenant_"},
		},
		{
			// The stale cards alone, never every document: 3 ms with the
			// owner's 7,557 cards all stale.
			name:  "CountStaleCards",
			query: countStaleCardsSQL, args: []any{"local"},
			first: "SCAN c USING INDEX idx_document_cards_stale",
			want:  []string{document},
			avoid: []string{"idx_documents_tenant_"},
		},
		{
			name:  "PutCardVectors mark",
			query: markCardEmbeddedSQL, args: []any{"text", "doc"},
			first: cardByDoc,
		},
		{
			name:  "MarkUnembeddable mark",
			query: markCardUnembeddableSQL, args: []any{"text", "doc"},
			first: cardByDoc,
		},
		{
			// The document's fields row by its unique key.
			name:  "Site",
			query: siteSQL, args: []any{"doc", "local"},
			first: "SEARCH document_fields USING INDEX sqlite_autoindex_document_fields_1 (document_id=?)",
		},
		{
			// The tenant's documents, each card by its primary key.
			name:  "ResetCards",
			query: resetCardsSQL, args: []any{"local"},
			first: cardByDoc,
			want:  []string{"LIST SUBQUERY", "SEARCH documents USING COVERING INDEX idx_documents_tenant_"},
		},
		{
			name:  "refreshFields inputs",
			query: fieldsInputsSQL, args: []any{"$.site", "doc", "local"},
			first: document,
			want: []string{
				"SEARCH e USING INDEX sqlite_autoindex_document_extractions_1 (id=?)",
				"SEARCH f USING INDEX sqlite_autoindex_document_fields_1 (document_id=?)",
			},
		},
		{
			// In the order the fields list them, with no sort.
			name:  "refreshFields bookmarks",
			query: fieldsBookmarksSQL, args: []any{"doc"},
			first: "SEARCH bookmarks USING INDEX idx_bookmarks_document (document_id=?)",
		},
		{
			// One seek: the first row of the host's names of a kind, in the
			// index's order, whatever the host's size.
			name:  "refreshFields leader",
			query: leaderSQL, args: []any{"local", "example.com", voteSuffix},
			first: "SEARCH site_name_votes USING COVERING INDEX idx_site_name_votes_lead (tenant_id=? AND host=? AND kind=?)",
		},
		{
			// Two ranges of the host's undeclared rows, around the agreed
			// name: empty unless the agreement changed.
			name:  "refreshFields misnamed",
			query: misnamedSQL, args: []any{"local", "example.com", "Name"},
			first: "MULTI-INDEX OR",
			want: []string{
				"SEARCH document_fields USING INDEX idx_document_fields_site (tenant_id=? AND host=? AND declared_site=? AND site_name<?)",
				"SEARCH document_fields USING INDEX idx_document_fields_site (tenant_id=? AND host=? AND declared_site=? AND site_name>?)",
			},
		},
		{
			// The host's undeclared rows, never the library's: run only on
			// a change of the agreed name.
			name:  "refreshFields rename",
			query: renameSQL, args: []any{"local", "example.com", "Name", "Name example"},
			first: "SEARCH document_fields USING COVERING INDEX idx_document_fields_site (tenant_id=? AND host=? AND declared_site=?)",
		},
		{
			// An upsert finds its conflict through the unique key it names.
			name:  "refreshFields fields upsert",
			query: upsertFieldsSQL, args: unbound(upsertFieldsSQL),
			avoid: []string{noTableScans},
		},
		{
			name:  "refreshFields card upsert",
			query: upsertCardSQL, args: []any{"doc", "text"},
			avoid: []string{noTableScans},
		},
		{
			// Every document once, its fields by primary key.
			name:  "EnsureDocumentFields documents",
			query: unrefreshedSQL, args: []any{1},
			first: "SCAN d",
			want:  []string{"SEARCH f USING INDEX sqlite_autoindex_document_fields_1 (document_id=?) LEFT-JOIN"},
		},
		{
			// The stale cards alone, never every document.
			name:  "EnsureDocumentFields stale cards",
			query: staleCardsByTenantSQL,
			first: "SCAN c USING INDEX idx_document_cards_stale",
			want:  []string{document},
			// GROUP BY tenant.
			sorts: true,
		},
	}
}

// voteTriggerPlanCases pin the statements of migration 019's vote
// triggers, each planned on its own, since EXPLAIN QUERY PLAN of a write
// shows none of the triggers it fires. Each statement is read from its
// trigger as the database holds it, with its old.* and new.* references
// bound as parameters, so a pin follows the trigger's text. A withdrawal
// reaches its vote rows by the primary key, one seek a name, never a range
// of the host's names in idx_site_name_votes_lead (what "votes > 1" would
// read there grows with the names two of the host's documents hold); an
// addition is driven by the row's own values, its conflict found by the
// primary key.
func voteTriggerPlanCases(t *testing.T, db *DB) []planCase {
	const byKey = "SEARCH site_name_votes USING PRIMARY KEY (tenant_id=? AND host=? AND kind=? AND name=?)"
	values := map[string]any{"tenant_id": "local", "host": "example.com", "declared_site": "Example",
		"title_suffixes": `["Example","Example Notes"]`}
	ref := regexp.MustCompile(`\b(?:old|new)\.(\w+)`)
	body := regexp.MustCompile(`(?s)\bBEGIN\b(.*)\bEND$`)
	triggers := []struct {
		name       string
		statements int
	}{
		{"trg_document_fields_votes_insert", 2},
		{"trg_document_fields_votes_delete", 4},
		{"trg_document_fields_votes_update", 6},
	}
	cases := make([]planCase, 0, 12)
	for _, tr := range triggers {
		var text string
		require.NoError(t, db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`,
			tr.name).Scan(&text))
		m := body.FindStringSubmatch(text)
		require.NotNil(t, m, "the body of %s:\n%s", tr.name, text)
		var statements []string
		for s := range strings.SplitSeq(m[1], ";") {
			if s = strings.TrimSpace(s); s != "" {
				statements = append(statements, s)
			}
		}
		require.Len(t, statements, tr.statements, "the statements of %s", tr.name)
		for i, s := range statements {
			var args []any
			for _, r := range ref.FindAllStringSubmatch(s, -1) {
				v, ok := values[r[1]]
				require.True(t, ok, "a value for %s in %s", r[0], tr.name)
				args = append(args, v)
			}
			verb, _, _ := strings.Cut(s, " ")
			kind := voteDeclared
			if strings.Contains(s, "'"+voteSuffix+"'") {
				kind = voteSuffix
			}
			tc := planCase{name: fmt.Sprintf("%s %d: %s %s", tr.name, i+1, verb, kind),
				query: ref.ReplaceAllString(s, "?"), args: args,
				avoid: []string{"idx_site_name_votes_lead", "SCAN site_name_votes"}}
			switch {
			case verb == "INSERT" && kind == voteSuffix:
				// DISTINCT over the row's own array.
				tc.first, tc.sorts = "SCAN json_each VIRTUAL TABLE", true
			case verb == "INSERT":
				tc.first = "SCAN CONSTANT ROW"
			case kind == voteSuffix:
				tc.first = byKey
				tc.want = []string{"LIST SUBQUERY", "SCAN json_each VIRTUAL TABLE"}
			default:
				tc.first = byKey
			}
			cases = append(cases, tc)
		}
	}
	return cases
}

// insightPlanCases pin the interest store's statements (see "Interests:
// two levels, stable identities, automatic rebuilds"). Pages are read in
// their index's order: only the interests of every area, read for a flat
// list, and the latest run of any status, which ties on the rowid, sort.
func insightPlanCases() []planCase {
	const (
		groupsList = "SEARCH g USING INDEX idx_interest_groups_list "
		identity   = "SEARCH i USING INDEX sqlite_autoindex_interests_1 (id=?)"
		parent     = "SEARCH p USING INDEX sqlite_autoindex_interests_1 (id=?) LEFT-JOIN"
		runByID    = "SEARCH interest_runs USING INDEX sqlite_autoindex_interest_runs_1 (id=?)"
		// What tells a placement off its run's map (offMapSQL): the run
		// and the interest's group, by their primary keys.
		runOfPlacement   = "SEARCH r USING INDEX sqlite_autoindex_interest_runs_1 (id=?)"
		groupOfPlacement = "SEARCH g USING INDEX sqlite_autoindex_interest_groups_1 (run_id=? AND interest_id=?) LEFT-JOIN"
		// The upserts of insight_state (OweFresh, RecordFailure) insert
		// VALUES and conflict on this primary key; they have no plan.
		stateByTenant = "SEARCH insight_state USING INDEX sqlite_autoindex_insight_state_1 (tenant_id=?)"
	)
	done := store.InterestRunDone
	return []planCase{
		{name: "GetRun", query: getRunSQL, args: []any{"run"}, first: runByID},
		{name: "run status", query: runStatusSQL, args: []any{"run"}, first: runByID},
		{name: "CommitRun finish", query: commitRunSQL, args: unbound(commitRunSQL), first: runByID},
		{name: "FailRun", query: failRunSQL, args: unbound(failRunSQL), first: runByID},
		{
			name: "CommitRun relabel", query: relabelInterestSQL, args: unbound(relabelInterestSQL),
			first: "SEARCH interests USING INDEX sqlite_autoindex_interests_1 (id=?)",
		},
		{
			name: "LatestRun by status", query: latestRunSQL(runColumns, true), args: []any{"local", done},
			first: "SEARCH interest_runs USING INDEX idx_interest_runs_tenant_status (tenant_id=? AND status=?)",
			sorts: true,
		},
		{
			name: "LatestRun", query: latestRunSQL(runColumns, false), args: []any{"local"},
			first: "SEARCH interest_runs USING INDEX idx_interest_runs_tenant_status (tenant_id=?)",
			sorts: true,
		},
		{
			name: "TopGroups", query: topGroupsSQL, args: []any{"run", 24, 48},
			first: groupsList + "(run_id=? AND parent_id=?)", want: []string{identity, parent},
		},
		{
			// The JSON array's areas, each read in order.
			name: "ChildGroups", query: childGroupsSQL, args: []any{"run", `["a","b"]`},
			first: groupsList + "(run_id=? AND parent_id=?)", want: []string{"LIST SUBQUERY", identity},
		},
		{
			name: "NestedGroups", query: nestedGroupsSQL, args: []any{"run", 24, 48},
			first: groupsList + "(run_id=? AND parent_id>?)", want: []string{identity, parent},
			sorts: true,
		},
		{
			name: "GetGroup", query: getGroupSQL, args: []any{"run", "interest"},
			first: "SEARCH g USING INDEX sqlite_autoindex_interest_groups_1 (run_id=? AND interest_id=?)",
			want:  []string{identity, parent},
		},
		{
			name: "RunGroups", query: runGroupsSQL, args: []any{"run"},
			first: "SEARCH g USING INDEX idx_interest_groups_list (run_id=?)", want: []string{identity},
		},
		{
			name: "Members", query: membersSQL, args: []any{"run", "interest", store.InterestFitMember, 50, 50},
			first: "SEARCH interest_assignments USING COVERING INDEX idx_interest_assignments_list " +
				"(run_id=? AND interest_id=? AND fit=?)",
		},
		{
			name: "Unsorted", query: unsortedSQL, args: []any{"run", store.InterestFitUnsorted, 50, 50},
			first: "SEARCH a USING INDEX idx_interest_assignments_list (run_id=? AND interest_id=? AND fit=?)",
			want:  []string{"SEARCH n USING INDEX sqlite_autoindex_interests_1 (id=?) LEFT-JOIN"},
		},
		{
			name: "RunAssignments", query: runAssignmentsSQL, args: []any{"run"},
			first: "SEARCH interest_assignments USING INDEX", want: []string{"(run_id=?)"},
		},
		{
			name: "RunLineage", query: runLineageSQL, args: []any{"run"},
			first: "SEARCH interest_lineage USING INDEX sqlite_autoindex_interest_lineage_1 (run_id=?)",
		},
		{
			name: "Successors", query: successorsSQL, args: []any{"run", "old"},
			first: "SEARCH interest_lineage USING INDEX sqlite_autoindex_interest_lineage_1 (run_id=? AND old_id=?)",
		},
		{
			name: "GetInterest", query: getInterestSQL, args: []any{"interest"},
			first: "SEARCH interests USING INDEX sqlite_autoindex_interests_1 (id=?)",
		},
		{
			name: "GetInterests", query: getInterestsSQL, args: []any{`["a","b"]`, "local"},
			first: "SEARCH interests USING INDEX sqlite_autoindex_interests_1 (id=?)", avoid: []string{"idx_interests_retired"},
		},
		{
			name: "CreatedBy", query: createdBySQL, args: []any{"run"},
			first: "SEARCH g USING COVERING INDEX sqlite_autoindex_interest_groups_1 (run_id=?)", want: []string{identity},
		},
		{
			name: "RetiredBy", query: retiredBySQL, args: []any{"local", "run"},
			first: "SEARCH interests USING INDEX idx_interests_retired (tenant_id=? AND retired_run_id=?)",
		},
		{
			name: "Placements", query: placementsSQL(false), args: []any{"run", "interest", 20},
			first: "SEARCH interest_placements USING INDEX idx_interest_placements_list (run_id=? AND interest_id=?)",
		},
		{
			name: "Placements in Unsorted", query: placementsSQL(true), args: []any{"run", 20},
			first: "SEARCH interest_placements USING INDEX idx_interest_placements_list (run_id=? AND interest_id=?)",
		},
		{
			name: "PlacementCounts", query: placementCountsSQL, args: []any{"run"},
			first: "SEARCH interest_placements USING COVERING INDEX idx_interest_placements_list (run_id=?)",
		},
		{
			// Each identity checked against the run's groups by its
			// primary key.
			name:  "CommitRun retire",
			query: retireInterestsSQL, args: []any{"now", "run", "now", "local", "run"},
			first: "SCAN interests",
			want:  []string{"SEARCH g USING COVERING INDEX sqlite_autoindex_interest_groups_1 (run_id=? AND interest_id=?)"},
		},
		{
			// The runs' groups, assignments and placements by their run.
			name: "PruneRunsExcept", query: pruneRunsSQL(2), args: []any{"local", "a", "b"},
			first: "SEARCH interest_runs USING INDEX idx_interest_runs_tenant_status (tenant_id=?)",
			want: []string{
				"SEARCH interest_groups USING COVERING INDEX sqlite_autoindex_interest_groups_1 (run_id=?)",
				"SEARCH interest_assignments USING COVERING INDEX sqlite_autoindex_interest_assignments_1 (run_id=?)",
				"SEARCH interest_placements USING COVERING INDEX idx_interest_placements_list (run_id=?)",
			},
		},
		{
			// The foreign keys of each deleted identity scan the tables
			// that reference it: measured at about 1 ms an identity on
			// the owner's library, once a rebuild, so no index serves
			// them.
			name: "PruneRetired", query: pruneRetiredSQL, args: []any{"local", "cutoff"},
			first: "SEARCH interests USING INDEX idx_interests_retired (tenant_id=?)",
		},
		{
			// The lineage, which the trim itself keeps small.
			name: "TrimLineage", query: trimLineageSQL, args: []any{"run", "local"},
			first: "SCAN interest_lineage",
		},
		{
			// The documents indexed since, twice, and the run's assigned
			// documents in the states that left it, each checked against
			// the run's assignments; never the run's assignments joined
			// to the tenant's documents.
			name:  "Changes",
			query: changesSQL,
			args: []any{"local", "since", store.DocStateFetched, "run", store.DocStatePending, store.DocStateFailed,
				store.DocStateDead},
			want: []string{
				"SEARCH d USING INDEX idx_documents_tenant_indexed (tenant_id=? AND indexed_at>?)\nCORRELATED SCALAR SUBQUERY",
				"SEARCH d USING INDEX idx_documents_tenant_indexed (tenant_id=? AND indexed_at>?)\n" + assignment("a EXISTS"),
				"SEARCH d USING COVERING INDEX idx_documents_tenant_state_updated (tenant_id=? AND state=?)\n" +
					assignment("a EXISTS"),
				"SEARCH interest_assignments USING COVERING INDEX sqlite_autoindex_interest_assignments_1 (run_id=?)",
			},
			avoid: []string{"SCAN d", "SCAN a", "SCAN interest_assignments"},
		},
		{name: "State", query: insightStateSQL, args: []any{"local"}, first: stateByTenant},
		{name: "clear failures", query: clearFailuresSQL, args: []any{"now", "local"}, first: stateByTenant},
		{
			name: "consume the fresh rebuild owed", query: consumeFreshSQL, args: []any{"now", "local", "run", false, "reindex"},
			first: stateByTenant, want: []string{runByID},
		},
		{
			name: "RecordAbandoned", query: abandonRunsSQL, args: unbound(abandonRunsSQL),
			first: "SEARCH interest_runs USING INDEX idx_interest_runs_tenant_status (tenant_id=? AND status=?)",
		},
		{name: "Assigned", query: assignedSQL, args: []any{"run", "doc"}, want: []string{assignment("interest_assignments")}},
		{
			// A document page's line: the run, the document's assignment
			// and placement, each interest's group for its area, and the
			// labels, every one by its primary key.
			name: "DocumentPlace", query: documentPlaceSQL, args: []any{"run", "doc"},
			first: "SEARCH r USING COVERING INDEX sqlite_autoindex_interest_runs_1 (id=?)",
			want: []string{
				"SEARCH a USING INDEX sqlite_autoindex_interest_assignments_1 (run_id=? AND document_id=?) LEFT-JOIN",
				"SEARCH pl USING INDEX sqlite_autoindex_interest_placements_1 (run_id=? AND document_id=?) LEFT-JOIN",
				"SEARCH ag USING INDEX sqlite_autoindex_interest_groups_1 (run_id=? AND interest_id=?) LEFT-JOIN",
				"SEARCH pg USING INDEX sqlite_autoindex_interest_groups_1 (run_id=? AND interest_id=?) LEFT-JOIN",
				"SEARCH ai USING INDEX sqlite_autoindex_interests_1 (id=?) LEFT-JOIN",
				"SEARCH ap USING INDEX sqlite_autoindex_interests_1 (id=?) LEFT-JOIN",
				"SEARCH an USING INDEX sqlite_autoindex_interests_1 (id=?) LEFT-JOIN",
				"SEARCH pi USING INDEX sqlite_autoindex_interests_1 (id=?) LEFT-JOIN",
				"SEARCH pp USING INDEX sqlite_autoindex_interests_1 (id=?) LEFT-JOIN",
			},
			avoid: []string{"SCAN"},
		},
		{
			// The guard: the latest done run, which ties on the rowid, and
			// the document's assignment and row.
			name:  "PlaceDocument",
			query: placeDocumentSQL, args: []any{"run", "doc", "interest", 0.5, "now", "local", done, 1.0, 2.0, 3.0, 4.0},
			want: []string{
				"SEARCH documents EXISTS USING COVERING INDEX sqlite_autoindex_documents_1 (id=?)",
				"SEARCH interest_runs USING INDEX idx_interest_runs_tenant_status (tenant_id=? AND status=?)",
				assignment("interest_assignments"),
			},
			sorts: true,
		},
		{
			// The guard, and on a conflict the placement's run and its
			// interest's group, to tell whether it is off the run's map.
			name: "PlaceMany", query: placeOrRepairSQL, args: []any{"run", "doc", "interest", 0.5, "now", 1.0, 2.0, 3.0, 4.0},
			want: []string{
				"SEARCH documents EXISTS USING COVERING INDEX sqlite_autoindex_documents_1 (id=?)",
				assignment("interest_assignments"),
				runOfPlacement, groupOfPlacement,
			},
			avoid: []string{"SCAN"},
		},
		{
			// A run's assigned documents in document ID order, from its
			// primary key, each document by its own and an untitled one's
			// bookmarks by theirs: never a walk of the tenant's documents.
			name: "MapDocuments assigned", query: mapAssignedSQL, args: []any{"run", store.DocStateFailed, store.DocStateDead},
			first: "SEARCH a USING INDEX sqlite_autoindex_interest_assignments_1 (run_id=?)",
			want:  []string{"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)", untitledBookmarkTitle},
			avoid: []string{"SCAN d", "idx_documents_tenant_", "idx_bookmarks_tenant_"},
		},
		{
			name: "MapDocuments placed", query: mapPlacedSQL, args: []any{"run", store.DocStateFailed, store.DocStateDead},
			first: "SEARCH p USING INDEX sqlite_autoindex_interest_placements_1 (run_id=?)",
			want:  []string{"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)", untitledBookmarkTitle},
			avoid: []string{"SCAN d", "idx_documents_tenant_", "idx_bookmarks_tenant_"},
		},
		{
			// Each document of the array in the run's assignments and
			// placements, by their primary keys.
			name: "MapPositions", query: mapPositionsSQL, args: []any{"run", `["a","b"]`},
			first: "SCAN j VIRTUAL TABLE INDEX",
			want: []string{
				"SEARCH a USING INDEX sqlite_autoindex_interest_assignments_1 (run_id=? AND document_id=?) LEFT-JOIN",
				"SEARCH p USING INDEX sqlite_autoindex_interest_placements_1 (run_id=? AND document_id=?) LEFT-JOIN",
			},
		},
		{
			// The documents indexed since, each checked against the run's
			// assignments and placements, and a placement's run and group,
			// by their primary keys.
			name: "Unplaced", query: unplacedSQL, args: []any{"local", "since", store.DocStateFetched, "run"},
			first: "SEARCH d USING INDEX idx_documents_tenant_indexed (tenant_id=? AND indexed_at>?)",
			want: []string{
				assignment("a"),
				"SEARCH p USING INDEX sqlite_autoindex_interest_placements_1 (run_id=? AND document_id=?) LEFT-JOIN",
				runOfPlacement, groupOfPlacement,
			},
			avoid: []string{"SCAN"},
		},
	}
}

// assignment is the plan row of a search for one assignment of a run's,
// named name, by its primary key.
func assignment(name string) string {
	return "SEARCH " + name + " USING COVERING INDEX sqlite_autoindex_interest_assignments_1 (run_id=? AND document_id=?)"
}

// unbound is a NULL for each of query's parameters, for a statement whose
// plan its WHERE's shape decides alone.
func unbound(query string) []any { return make([]any, strings.Count(query, "?")) }

// queryVector is a query embedding in sqlite-vec's format.
func queryVector(t *testing.T) []byte {
	t.Helper()
	v, err := sqlitevec.SerializeFloat32(fillVec(0.1))
	require.NoError(t, err)
	return v
}

// TestQueryPlans_ChunkVectorWritesArePointLookups: the chunks delete
// trigger removes a chunk's vector by its primary key, and a re-index
// updates a kept chunk's vector by it. vec0 reports its plan as
// "INDEX <idxNum>:<idxStr>", and the first character of idxStr is the plan
// kind (sqlite-vec v0.1.6): '1' a full scan, '2' a point lookup, '3' KNN.
// Any other form of the delete, `chunk_id IN (subquery)` included, scans
// every vector.
func TestQueryPlans_ChunkVectorWritesArePointLookups(t *testing.T) {
	db := newTestDB(t)
	var trigger string
	require.NoError(t, db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'trg_chunks_delete'`).Scan(&trigger))
	require.Contains(t, trigger, "DELETE FROM chunks_vec WHERE chunk_id = old.id")

	plan := queryPlan(t, db, `DELETE FROM chunks_vec WHERE chunk_id = ?`, "chunk")
	assert.Regexp(t, regexp.MustCompile(`SCAN chunks_vec VIRTUAL TABLE INDEX \d+:2`), plan)
	plan = queryPlan(t, db, updateChunkVectorSQL, queryVector(t), "chunk")
	assert.Regexp(t, regexp.MustCompile(`SCAN chunks_vec VIRTUAL TABLE INDEX -?\d+:2`), plan,
		"a re-index updates each kept chunk's vector in place, by its ID")
}

// TestQueryPlans_CardVectorWritesArePointLookups: the card delete trigger
// and MarkUnembeddable remove a card's vector by its primary key, as the
// chunks' trigger does, and PutCardVectors updates one in place by it.
// vec0 reports an UPDATE's plan with a negative idxNum.
func TestQueryPlans_CardVectorWritesArePointLookups(t *testing.T) {
	db := newTestDB(t)
	var trigger string
	require.NoError(t, db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'trg_document_cards_delete'`).Scan(&trigger))
	require.Contains(t, trigger, "DELETE FROM document_cards_vec WHERE document_id = old.document_id")
	plan := queryPlan(t, db, dropCardVectorSQL, "doc")
	assert.Regexp(t, regexp.MustCompile(`SCAN document_cards_vec VIRTUAL TABLE INDEX -?\d+:2`), plan)

	plan = queryPlan(t, db, updateCardVectorSQL, queryVector(t), "doc")
	assert.Regexp(t, regexp.MustCompile(`SCAN document_cards_vec VIRTUAL TABLE INDEX -?\d+:2`), plan)
}

// TestQueryPlans_SampleChunksReadsVectorsByID: every vector the sample
// reads is a vec0 point lookup, never a scan of every vector.
func TestQueryPlans_SampleChunksReadsVectorsByID(t *testing.T) {
	db := newTestDB(t)
	plan := queryPlan(t, db, sampleChunksSQL, "local", store.DocStateFetched, 16, 48)

	var vec0 []string
	for line := range strings.Lines(plan) {
		if strings.Contains(line, "VIRTUAL TABLE") {
			vec0 = append(vec0, strings.TrimSpace(line))
		}
	}
	require.NotEmpty(t, vec0, "the vectors are read from chunks_vec:\n%s", plan)
	pointLookup := regexp.MustCompile(`^SCAN v VIRTUAL TABLE INDEX \d+:2`)
	for _, line := range vec0 {
		assert.Regexp(t, pointLookup, line)
	}
	assert.NotRegexp(t, regexp.MustCompile(`VIRTUAL TABLE INDEX \d+:1`), plan)
}

// TestQueryPlans_EveryFilterCombination: under every combination of the
// filters' dimensions, the text lists stay driven by their MATCH and the
// card list's over-fetch by its KNN, never by a walk of the tenant's
// documents or bookmarks. It guards documentTenantSQL: with the tenant
// checked on documents' tenant index, SQLite (no ANALYZE) takes two
// filters on a document's bookmarks to leave few enough documents to walk,
// searching the fields index once for each.
func TestQueryPlans_EveryFilterCombination(t *testing.T) {
	db := newTestDB(t)
	after := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	before := after.AddDate(1, 0, 0)
	dimensions := []func(*store.SearchFilters){
		func(f *store.SearchFilters) { f.ContentType = []string{"article"} },
		func(f *store.SearchFilters) { f.Host = []string{"example.com"} },
		func(f *store.SearchFilters) { f.Source = []string{store.SourceChrome} },
		func(f *store.SearchFilters) { f.Site = []string{"example.com"} },
		func(f *store.SearchFilters) { f.Folder = []string{"/Tech"} },
		func(f *store.SearchFilters) { f.SavedAfter, f.SavedBefore = after, before },
		func(f *store.SearchFilters) { f.PublishedAfter, f.PublishedBefore = after, before },
		func(f *store.SearchFilters) { f.IncludeUndated = true },
	}
	for mask := 1; mask < 1<<len(dimensions); mask++ {
		var f store.SearchFilters
		for i, set := range dimensions {
			if mask&(1<<i) != 0 {
				set(&f)
			}
		}
		if f.Validate() != nil {
			continue // include_undated without a date
		}
		for _, list := range []struct {
			name  string
			first string
			build func() (string, []any)
		}{
			{"BM25Search", "SCAN chunks_fts VIRTUAL TABLE",
				func() (string, []any) { return bm25Query("local", `"kafka"`, 800, f) }},
			{"SearchFields", "SCAN document_fields_fts VIRTUAL TABLE",
				func() (string, []any) { return fieldsQuery("local", `"kafka"`, 200, f) }},
			{"SearchTitles", "SCAN document_fields_fts VIRTUAL TABLE",
				func() (string, []any) { return titlesQuery("local", `"kafka" AND "retention"`, 200, f) }},
			{"SearchCards over-fetch", "SCAN v VIRTUAL TABLE INDEX 0:3",
				func() (string, []any) { return cardsQuery("local", queryVector(t), vec0MaxK, f) }},
			{"VectorSearch over-fetch", "SCAN v VIRTUAL TABLE INDEX 0:3",
				func() (string, []any) { return vectorQuery("local", queryVector(t), vec0MaxK, f) }},
		} {
			q, args := list.build()
			plan := queryPlan(t, db, q, args...)
			assert.True(t, strings.HasPrefix(plan, list.first), "%s under %+v:\n%s", list.name, f, plan)
			assert.NotContains(t, plan, "idx_bookmarks_tenant_", "%s under %+v", list.name, f)
		}
	}
}
