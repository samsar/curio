package sqlite

import (
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
	vecQ, vecArgs := vectorQuery("local", queryVector(t), 10, store.SearchFilters{})
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
			name:  "ReplaceForDocument delete",
			query: deleteDocumentChunksSQL, args: []any{"doc"},
			want: []string{"INDEX idx_chunks_document (document_id=?)"},
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
			// TestQueryPlans_ChunkVectorDeleteIsPointLookup), then the same
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
			// idx_clusters_run gives the size order; only clusters of the
			// same size are sorted, by cohesion and ID.
			name:  "ListClusters",
			query: listClustersSQL, args: []any{"run", 24, 48},
			first: "SEARCH clusters USING INDEX idx_clusters_run (run_id=?)",
			want:  []string{"USE TEMP B-TREE FOR LAST 2 TERMS OF ORDER BY"},
			sorts: true,
		},
		{
			// A cluster's members, sorted by similarity: no index holds
			// them in that order (see "Interests page by offset within a
			// run").
			name:  "ClusterMembers",
			query: clusterMembersSQL, args: []any{"cluster", 50, 50},
			first: "SEARCH cluster_documents USING INDEX sqlite_autoindex_cluster_documents_1 (cluster_id=?)",
			sorts: true,
			avoid: []string{"idx_cluster_documents_document"},
		},
		{
			// The document's bookmarks, read in the order they are listed.
			name:  "ListByDocument",
			query: listBookmarksByDocumentSQL, args: []any{"local", "doc"},
			first: "SEARCH bookmarks USING INDEX idx_bookmarks_document (document_id=?)",
			avoid: []string{"idx_bookmarks_tenant_"},
		},
		{
			name:  "document delete reaches its jobs",
			query: deleteDocumentSQL, args: []any{"doc"},
			want: []string{"SEARCH jobs USING COVERING INDEX idx_jobs_document (document_id=?)"},
		},
	}
	for _, tc := range slices.Concat(cases, listPlanCases(t)) {
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

// queryVector is a query embedding in sqlite-vec's format.
func queryVector(t *testing.T) []byte {
	t.Helper()
	v, err := sqlitevec.SerializeFloat32(fillVec(0.1))
	require.NoError(t, err)
	return v
}

// TestQueryPlans_ChunkVectorDeleteIsPointLookup: the chunks delete trigger
// removes a chunk's vector by its primary key. vec0 reports its plan as
// "INDEX <idxNum>:<idxStr>", and the first character of idxStr is the plan
// kind (sqlite-vec v0.1.6): '1' a full scan, '2' a point lookup, '3' KNN.
// Any other form of the delete, `chunk_id IN (subquery)` included, scans
// every vector.
func TestQueryPlans_ChunkVectorDeleteIsPointLookup(t *testing.T) {
	db := newTestDB(t)
	var trigger string
	require.NoError(t, db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'trg_chunks_delete'`).Scan(&trigger))
	require.Contains(t, trigger, "DELETE FROM chunks_vec WHERE chunk_id = old.id")

	plan := queryPlan(t, db, `DELETE FROM chunks_vec WHERE chunk_id = ?`, "chunk")
	assert.Regexp(t, regexp.MustCompile(`SCAN chunks_vec VIRTUAL TABLE INDEX \d+:2`), plan)
}
