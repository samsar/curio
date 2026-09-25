package sqlite

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

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
}

// listPlanCases pins the three paged lists: for every filter, the first
// page and a page after a cursor both walk their index in the list's order,
// the cursor page from the keyset constraint, with no sort.
func listPlanCases() []planCase {
	after := store.PageKey{At: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), ID: "last-row"}
	return slices.Concat(listPagePlanCases("first page", store.PageKey{}), listPagePlanCases("cursor page", after))
}

// listPagePlanCases are the list plan cases for one page, named page: the
// one after the key after.
func listPagePlanCases(page string, after store.PageKey) []planCase {
	keyset := func(ts string) string {
		if after.IsZero() {
			return ""
		}
		return " AND (" + ts + ",id)<(?,?)"
	}
	docs := func(name string, opts store.ListDocumentsOpts, index string) planCase {
		opts.After = after
		q, args := listDocumentsQuery("local", opts)
		return planCase{name: name + ", " + page, query: q, args: args, want: []string{
			index + keyset("updated_at") + ")",
			"SEARCH j USING INDEX idx_jobs_document (document_id=? AND status=?)",
		}}
	}
	jobs := func(name string, opts store.ListJobsOpts, index string) planCase {
		opts.After = after
		q, args := listJobsQuery("local", opts)
		return planCase{name: name + ", " + page, query: q, args: args, want: []string{
			index + keyset("updated_at") + ")",
			"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)",
		}}
	}
	bookmarks := func(name string, opts store.ListBookmarksOpts) planCase {
		opts.After = after
		q, args := listBookmarksQuery("local", opts)
		return planCase{name: name + ", " + page, query: q, args: args, want: []string{
			"SEARCH b USING INDEX idx_bookmarks_tenant_created (tenant_id=?" + keyset("created_at") + ")",
			"SEARCH d USING INDEX sqlite_autoindex_documents_1 (id=?)",
		}}
	}
	return []planCase{
		docs("ListWithLastError", store.ListDocumentsOpts{},
			"SEARCH d USING INDEX idx_documents_tenant_updated (tenant_id=?"),
		docs("ListWithLastError by state", store.ListDocumentsOpts{State: store.DocStateFetched},
			"SEARCH d USING INDEX idx_documents_tenant_state_updated (tenant_id=? AND state=?"),
		jobs("ListWithDoc", store.ListJobsOpts{},
			"SEARCH j USING INDEX idx_jobs_tenant_updated (tenant_id=?"),
		jobs("ListWithDoc by status", store.ListJobsOpts{Status: store.JobStatusDone},
			"SEARCH j USING INDEX idx_jobs_tenant_status_updated (tenant_id=? AND status=?"),
		jobs("ListWithDoc by kind", store.ListJobsOpts{Kind: store.JobKindFetch},
			"SEARCH j USING INDEX idx_jobs_tenant_updated (tenant_id=?"),
		jobs("ListWithDoc by status and kind", store.ListJobsOpts{Status: store.JobStatusDone, Kind: store.JobKindFetch},
			"SEARCH j USING INDEX idx_jobs_tenant_status_updated (tenant_id=? AND status=?"),
		bookmarks("Bookmarks.List", store.ListBookmarksOpts{}),
		bookmarks("Bookmarks.List by source", store.ListBookmarksOpts{Source: store.SourceChrome}),
		bookmarks("Bookmarks.List by folder", store.ListBookmarksOpts{FolderPath: "/Tech/AI"}),
	}
}

func TestQueryPlans(t *testing.T) {
	db := newTestDB(t)
	claimArgs := []any{store.JobStatusRunning, "now", "now", store.JobStatusPending, "now", store.JobKindFetch}
	bm25Q, bm25Args := bm25Query("local", `"kafka"`, 10, store.SearchFilters{})

	cases := []planCase{
		{
			name:  "ClaimNext one kind",
			query: claimSQL(1), args: claimArgs,
			want: []string{"SEARCH jobs USING INDEX idx_jobs_claim (status=? AND kind=? AND run_after<?)"},
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
			query: resetStatesSQL(2), args: []any{store.DocStatePending, "local", store.DocStateFailed, store.DocStateFetched},
			want: []string{"INDEX idx_documents_tenant_state_updated (tenant_id=? AND state=?)"},
		},
		{
			name:  "ReplaceForDocument delete",
			query: deleteDocumentChunksSQL, args: []any{"doc"},
			want: []string{"INDEX idx_chunks_document (document_id=?)"},
		},
		{
			name:  "BM25Search",
			query: bm25Q, args: bm25Args,
			first: "SCAN chunks_fts VIRTUAL TABLE",
			want:  []string{"SEARCH c USING INTEGER PRIMARY KEY (rowid=?)"},
			// ORDER BY the bm25 score.
			sorts: true,
		},
		{
			name:  "document delete reaches its jobs",
			query: deleteDocumentSQL, args: []any{"doc"},
			want: []string{"SEARCH jobs USING COVERING INDEX idx_jobs_document (document_id=?)"},
		},
	}
	for _, tc := range slices.Concat(cases, listPlanCases()) {
		t.Run(tc.name, func(t *testing.T) {
			plan := queryPlan(t, db, tc.query, tc.args...)
			assert.True(t, strings.HasPrefix(plan, tc.first), "plan starts with %q:\n%s", tc.first, plan)
			for _, want := range tc.want {
				assert.Contains(t, plan, want)
			}
			if !tc.sorts {
				assert.NotContains(t, plan, "USE TEMP B-TREE")
			}
		})
	}
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
