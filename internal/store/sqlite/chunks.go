package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
)

// Chunks implements store.ChunkStore. Owns three coupled tables:
//
//	chunks      — canonical text rows, FK to document + extraction
//	chunks_fts  — FTS5 index over chunks (external content, rowid = seq)
//	chunks_vec  — sqlite-vec virtual table for ANN search
//
// Triggers on chunks keep chunks_fts in step and delete a chunk's vector
// with it (migration 008), so every way a chunk goes takes its derived rows
// along. Inserting a vector is the one write left to the store, since the
// embedding is not a chunks column. Queries read from one virtual table at
// a time and JOIN to documents for tenant scoping.
type Chunks struct {
	db  *DB
	dim int // vec dimension; must match the chunks_vec schema and the embedder
}

var _ store.ChunkStore = (*Chunks)(nil)

// NewChunks constructs the store. dim is the home's embedding width, which
// EnsureVectorIndex gives chunks_vec; ReplaceForDocument and VectorSearch
// reject an embedding of any other length.
func NewChunks(db *DB, dim int) *Chunks {
	return &Chunks{db: db, dim: dim}
}

// ReplaceForDocument implements store.ChunkStore, by position: the
// document's chunks are read in order, the chunk at each position the
// document keeps is rewritten in place, its row by primary key and its
// vector by chunk ID, positions past the old count are inserted, and rows
// past the new count deleted, their FTS entries and vectors through the
// delete trigger. A document's rows are always 0 to n-1 of one extraction,
// written here alone, and every kept row moves to its position's ord
// before the next, so the new extraction's (extraction_id, ord) is never
// taken.
//
// In place because sqlite-vec (v0.1.6) inserts only into its newest chunk
// and never reuses a deleted vector's slot: deleting a document's chunks
// and inserting them again grew chunks_vec by a slot per chunk at every
// re-index (re-embedding the owner's library that way doubled it, 50 vec0
// chunks to 100, and slowed every vector search by a third). A row whose
// text, title and tags stay as they were writes nothing to chunks_fts
// either (trg_chunks_update, migration 021).
func (s *Chunks) ReplaceForDocument(
	ctx context.Context,
	documentID, extractionID, title string,
	tags []string,
	chunks []store.ChunkInput,
) error {
	return s.replace(ctx, documentID, extractionID, nil, title, tags, chunks)
}

// ReplaceIndexed implements store.ChunkStore: ReplaceForDocument, after
// checking guard (checkIndexGuard) in its transaction, which holds the
// write lock from its BEGIN, so no refetch can move the document on between
// the check and the write.
func (s *Chunks) ReplaceIndexed(ctx context.Context, documentID string, guard store.IndexGuard, title string,
	tags []string, chunks []store.ChunkInput) error {
	return s.replace(ctx, documentID, guard.ExtractionID, &guard, title, tags, chunks)
}

// replace writes chunks as the document's, from extractionID, by position
// (ReplaceForDocument), once guard, when there is one, holds.
func (s *Chunks) replace(ctx context.Context, documentID, extractionID string, guard *store.IndexGuard, title string,
	tags []string, chunks []store.ChunkInput) error {
	if documentID == "" {
		return errors.New("chunks: document_id required")
	}
	if extractionID == "" {
		return errors.New("chunks: extraction_id required")
	}
	for i, c := range chunks {
		if len(c.Embedding) != s.dim {
			return fmt.Errorf("chunks[%d]: embedding length %d != configured dim %d",
				i, len(c.Embedding), s.dim)
		}
	}

	tagsStr := ""
	if len(tags) > 0 {
		b, err := json.Marshal(tags)
		if err != nil {
			return fmt.Errorf("encode tags: %w", err)
		}
		tagsStr = string(b)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if guard != nil {
		if err := checkIndexGuard(ctx, tx, documentID, *guard); err != nil {
			return err
		}
	}
	kept, err := documentChunks(ctx, tx, documentID)
	if err != nil {
		return err
	}
	row := chunkRow{documentID: documentID, extractionID: extractionID, title: title, tags: tagsStr}
	if err := writeChunks(ctx, tx, row, kept, chunks); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, deleteChunksFromSQL, documentID, len(chunks)); err != nil {
		return fmt.Errorf("delete the chunks past %d: %w", len(chunks), err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chunks: %w", err)
	}
	return nil
}

// indexGuardSQL reads what an index write is held to, the document's
// current extraction and state, by its primary key.
const indexGuardSQL = `SELECT current_extraction_id, state FROM documents WHERE id = ?`

// checkIndexGuard is ErrSuperseded unless guard.ExtractionID is the
// document's current extraction and, for a re-embed, the document is
// fetched; ErrNotFound when there is no such document.
func checkIndexGuard(ctx context.Context, tx *sql.Tx, documentID string, guard store.IndexGuard) error {
	var (
		current sql.NullString
		state   store.DocState
	)
	err := tx.QueryRowContext(ctx, indexGuardSQL, documentID).Scan(&current, &state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("index document %s: %w", documentID, store.ErrNotFound)
	case err != nil:
		return fmt.Errorf("index document %s: read what it was indexed from: %w", documentID, err)
	case current.String != guard.ExtractionID:
		return fmt.Errorf("index document %s from extraction %s, now %s: %w", documentID, guard.ExtractionID,
			current.String, store.ErrSuperseded)
	case guard.Reembed && state != store.DocStateFetched:
		return fmt.Errorf("re-embed document %s, now %s: %w", documentID, state, store.ErrSuperseded)
	}
	return nil
}

// The statements of ReplaceForDocument. A document's chunks are read
// through idx_chunks_document and sorted, a handful of rows; a kept row is
// rewritten by its seq, the INTEGER PRIMARY KEY, and its vector by chunk
// ID, a vec0 point lookup; the rows past the new count are deleted through
// idx_chunks_document, the delete trigger taking each one's FTS entry and
// vector by rowid and chunk ID, so nothing scans chunks_fts or chunks_vec.
const (
	documentChunksSQL = `SELECT seq, id FROM chunks WHERE document_id = ? ORDER BY ord`
	updateChunkSQL    = `UPDATE chunks SET extraction_id = ?, ord = ?, text = ?, token_count = ?, title = ?, tags = ?,
		context = ?, context_version = ? WHERE seq = ?`
	insertChunkSQL = `INSERT INTO chunks (id, document_id, extraction_id, ord, text, token_count, title, tags, context,
		context_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	updateChunkVectorSQL = `UPDATE chunks_vec SET embedding = ? WHERE chunk_id = ?`
	insertChunkVectorSQL = `INSERT INTO chunks_vec (chunk_id, embedding) VALUES (?, ?)`
	deleteChunksFromSQL  = `DELETE FROM chunks WHERE document_id = ? AND ord >= ?`
)

// storedChunk is a stored chunk's keys: its seq, the FTS rowid, and its
// ID, its vector's key.
type storedChunk struct {
	seq int64
	id  string
}

// documentChunks reads documentID's chunks' keys in order.
func documentChunks(ctx context.Context, tx *sql.Tx, documentID string) ([]storedChunk, error) {
	rows, err := tx.QueryContext(ctx, documentChunksSQL, documentID)
	if err != nil {
		return nil, fmt.Errorf("read the document's chunks: %w", err)
	}
	defer rows.Close()
	var out []storedChunk
	for rows.Next() {
		var c storedChunk
		if err := rows.Scan(&c.seq, &c.id); err != nil {
			return nil, fmt.Errorf("scan a chunk of the document: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the document's chunks: %w", err)
	}
	return out, nil
}

// chunkRow is what every chunk of one write shares.
type chunkRow struct {
	documentID, extractionID, title, tags string
}

// writeChunks writes chunks in order, each as a chunk row (which the
// triggers index, with title and tags) and a vector, through statements
// prepared once: in place of kept's chunk at its position, or as a new
// chunk past them.
func writeChunks(ctx context.Context, tx *sql.Tx, row chunkRow, kept []storedChunk,
	chunks []store.ChunkInput) (err error) {
	prepare := func(what, query string) (*sql.Stmt, error) {
		stmt, err := tx.PrepareContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("prepare the %s: %w", what, err)
		}
		return stmt, nil
	}
	updateChunk, err := prepare("chunk update", updateChunkSQL)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, updateChunk.Close()) }()
	insertChunk, err := prepare("chunk insert", insertChunkSQL)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, insertChunk.Close()) }()
	var vectors vectorWriter
	if vectors.update, err = prepare("vector update", updateChunkVectorSQL); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, vectors.update.Close()) }()
	if vectors.insert, err = prepare("vector insert", insertChunkVectorSQL); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, vectors.insert.Close()) }()

	for i, c := range chunks {
		// sqlite-vec needs the embedding in its specific binary format.
		vec, err := sqlitevec.SerializeFloat32(c.Embedding)
		if err != nil {
			return fmt.Errorf("serialize embedding[%d]: %w", i, err)
		}
		if i >= len(kept) {
			id := uuid.NewString()
			if _, err := insertChunk.ExecContext(ctx, id, row.documentID, row.extractionID, i, c.Text,
				c.TokenCount, row.title, row.tags, c.Context, c.ContextVersion); err != nil {
				return fmt.Errorf("insert chunk[%d]: %w", i, err)
			}
			if _, err := vectors.insert.ExecContext(ctx, id, vec); err != nil {
				return fmt.Errorf("insert chunks_vec[%d]: %w", i, err)
			}
			continue
		}
		if _, err := updateChunk.ExecContext(ctx, row.extractionID, i, c.Text, c.TokenCount, row.title, row.tags,
			c.Context, c.ContextVersion, kept[i].seq); err != nil {
			return fmt.Errorf("update chunk[%d]: %w", i, err)
		}
		if err := vectors.put(ctx, kept[i].id, vec); err != nil {
			return fmt.Errorf("chunks_vec[%d]: %w", i, err)
		}
	}
	return nil
}

// vectorWriter writes chunk vectors through its prepared statements.
type vectorWriter struct{ update, insert *sql.Stmt }

// put stores vec as chunk id's vector: in place of the one it has, else
// as a new one, as PutCardVectors does.
func (w vectorWriter) put(ctx context.Context, id string, vec []byte) error {
	res, err := w.update.ExecContext(ctx, vec, id)
	if err != nil {
		return fmt.Errorf("update its vector: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update its vector: %w", err)
	}
	if updated > 0 {
		return nil
	}
	if _, err := w.insert.ExecContext(ctx, id, vec); err != nil {
		return fmt.Errorf("insert its vector: %w", err)
	}
	return nil
}

// BM25Search runs FTS5 MATCH and returns hits ordered by relevance.
//
// FTS5's bm25() returns a negative score (lower = better). We negate it on
// the way out so the surfaced Score follows the "higher = better" convention
// shared with VectorSearch — that way RRF fusion downstream doesn't have to
// know which retriever it's mixing.
func (s *Chunks) BM25Search(ctx context.Context, tenantID, query string, limit int, filters store.SearchFilters) ([]store.ChunkHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}

	q, args := bm25Query(tenantID, query, limit, filters)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("bm25 search: %w", err)
	}
	defer rows.Close()

	var out []store.ChunkHit
	for rows.Next() {
		var (
			h       store.ChunkHit
			bm25Neg float64
		)
		if err := rows.Scan(&h.ChunkID, &h.DocumentID, &bm25Neg); err != nil {
			return nil, fmt.Errorf("scan bm25 hit: %w", err)
		}
		// Negate so "higher is better" matches vector convention.
		h.Score = -bm25Neg
		out = append(out, h)
	}
	return out, rows.Err()
}

// bm25Query builds BM25Search's query: it starts from the FTS MATCH and
// reaches each hit's chunk by rowid (chunks.seq, the INTEGER PRIMARY KEY),
// then its document for scoping and filters. It makes no snippet: FTS5
// would make one for every row the query returns, a search's hundreds,
// where a page shows a few (see Snippets).
//
// Equal scores, the same text in two documents, are ordered by seq: search
// ranks again for every page it shows, and a tie's order decides the
// fused scores, so it must come out the same each time.
func bm25Query(tenantID, query string, limit int, filters store.SearchFilters) (string, []any) {
	filterSQL, filterArgs := buildFilterClause(filters)
	q := `
	SELECT c.id, c.document_id, bm25(chunks_fts) AS bm25_score
	FROM chunks_fts
	JOIN chunks c    ON c.seq = chunks_fts.rowid
	JOIN documents d ON d.id = c.document_id
	WHERE chunks_fts MATCH ?
	  AND ` + searchedDocSQL + filterSQL + `
	ORDER BY bm25_score, c.seq
	LIMIT ?`

	args := slices.Concat([]any{query}, searchedDocArgs(tenantID), filterArgs)
	return q, append(args, limit)
}

// snippetSQL is the snippet FTS5 makes of a chunk's text for the query it
// matched: the matched terms between <em> and </em>, … where the text is
// cut, and 32 tokens, about 200 to 300 characters: enough to see a match
// in context without flooding the CLI, whose wrapLines breaks it on word
// boundaries.
const snippetSQL = `snippet(chunks_fts, 0, '<em>', '</em>', '…', 32)`

// Snippets returns the snippet of each of chunkIDs for query, a MATCH
// expression as BM25Search takes it, in one statement. A chunk the query
// doesn't match, or that no longer exists (a re-index that shortened its
// document deleted it since it was retrieved), is absent.
func (s *Chunks) Snippets(ctx context.Context, query string, chunkIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(chunkIDs))
	if len(chunkIDs) == 0 || strings.TrimSpace(query) == "" {
		return out, nil
	}
	q, args := snippetsQuery(query, chunkIDs)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("snippets: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, snippet string
		if err := rows.Scan(&id, &snippet); err != nil {
			return nil, fmt.Errorf("scan snippet: %w", err)
		}
		out[id] = snippet
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snippets: %w", err)
	}
	return out, nil
}

// snippetsQuery builds Snippets' query. It reaches the chunks' FTS rows by
// rowid, each chunk's seq looked up by its ID, so FTS5 makes snippets of
// those rows alone; the MATCH doesn't find rows, it gives snippet() the
// phrases to mark.
func snippetsQuery(query string, chunkIDs []string) (string, []any) {
	q := `
	SELECT c.id, ` + snippetSQL + `
	FROM chunks_fts
	JOIN chunks c ON c.seq = chunks_fts.rowid
	WHERE chunks_fts MATCH ?
	  AND chunks_fts.rowid IN (SELECT seq FROM chunks WHERE id IN (` + placeholders(len(chunkIDs)) + `))`
	return q, appendArgs([]any{query}, chunkIDs)
}

// searchedDocSQL scopes a search to the documents it may return: the
// tenant's, except failed and dead ones. Those keep the chunks of an
// earlier fetch, which no longer say what the URL serves: the page is gone,
// or a refetch found that what was stored was not the page. A refetch that
// succeeds re-indexes the document and brings it back. Pending documents
// stay: one being refetched is searchable until its fetch fails. NOT IN,
// rather than IN over the other states, leaves the planner driving from the
// FTS or vec table. The caller's query MUST alias the documents table as
// `d`; searchedDocArgs are its arguments.
const searchedDocSQL = `d.tenant_id = ? AND d.state NOT IN (?, ?)`

func searchedDocArgs(tenantID string) []any {
	return []any{tenantID, store.DocStateFailed, store.DocStateDead}
}

// VectorSearch runs nearest-neighbor against chunks_vec.
//
// sqlite-vec returns L2 distance (lower = closer). We convert to a 0..1
// similarity-like score via 1/(1+d). Cheap monotonic transform; rank order
// is preserved.
//
// A filter that doesn't narrow reads the nearest neighbours and filters
// them (vectorQuery). One that narrows ranks the chunks it leaves
// exactly (exactVectorQuery) when they are few enough, and otherwise
// filters as many nearest neighbours as their share of the index calls
// for (see knn.go). An exact ranking that finds a listed chunk without a
// vector falls back to that over-fetch, warning once.
func (s *Chunks) VectorSearch(ctx context.Context, tenantID string, embedding []float32, limit int, filters store.SearchFilters) ([]store.ChunkHit, error) {
	if len(embedding) != s.dim {
		return nil, fmt.Errorf("vector search: embedding length %d != configured dim %d", len(embedding), s.dim)
	}
	if limit <= 0 {
		limit = 50
	}

	serialized, err := sqlitevec.SerializeFloat32(embedding)
	if err != nil {
		return nil, fmt.Errorf("serialize query embedding: %w", err)
	}
	plan, err := s.vectorSearchQuery(ctx, tenantID, serialized, limit, filters)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	if plan.path == knnNone {
		return nil, nil
	}
	hits, err := s.vectorHits(ctx, plan.sql, plan.args, limit)
	if err != nil || len(hits) > 0 || plan.path != knnExact {
		return hits, err
	}
	err = s.db.checkExactKNN(ctx, plan.set, "chunk")
	if !errors.Is(err, store.ErrVectorIndexInconsistent) {
		if err != nil {
			return nil, fmt.Errorf("vector search: %w", err)
		}
		return nil, nil
	}
	s.db.warnInconsistent("chunks_vec", err)
	k, err := s.db.sizedOverFetchK(ctx, chunkIndexCountSQL, overfetchK(limit), plan.n)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	q, args := vectorQuery(tenantID, serialized, k, filters)
	return s.vectorHits(ctx, q, args, limit)
}

// vectorSearchQuery plans VectorSearch's statement for limit hits: the
// nearest neighbours filtered by a filter that doesn't narrow, and by one
// that narrows, the exact KNN of the chunks it leaves while they are few
// enough, else the nearest their share of the index calls for, filtered.
func (s *Chunks) vectorSearchQuery(ctx context.Context, tenantID string, embedding []byte, limit int,
	filters store.SearchFilters) (knnQuery, error) {
	if !filters.Narrows() {
		q, args := vectorQuery(tenantID, embedding, overfetchK(limit), filters)
		return knnQuery{path: knnOverFetch, sql: q, args: args}, nil
	}
	set := chunkSet(tenantID, filters)
	path, n, err := s.db.chooseKNNPath(ctx, set)
	if err != nil {
		return knnQuery{}, err
	}
	plan := knnQuery{path: path, set: set, n: n}
	switch path {
	case knnNone:
	case knnOverFetch:
		k, err := s.db.sizedOverFetchK(ctx, chunkIndexCountSQL, overfetchK(limit), n)
		if err != nil {
			return knnQuery{}, err
		}
		plan.sql, plan.args = vectorQuery(tenantID, embedding, k, filters)
	case knnExact:
		plan.sql, plan.args = exactVectorQuery(embedding, min(limit, vec0MaxK), set)
	}
	return plan, nil
}

// vectorHits runs q, a vector search's query of chunks with their
// documents and distances, nearest first, and keeps the first limit.
func (s *Chunks) vectorHits(ctx context.Context, q string, args []any, limit int) ([]store.ChunkHit, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	var out []store.ChunkHit
	for rows.Next() {
		if len(out) >= limit {
			break // cap to the closest `limit` of the (over-fetched) filtered set
		}
		var (
			h        store.ChunkHit
			distance float64
		)
		if err := rows.Scan(&h.ChunkID, &h.DocumentID, &distance); err != nil {
			return nil, fmt.Errorf("scan vector hit: %w", err)
		}
		h.Score = 1.0 / (1.0 + distance)
		if math.IsNaN(h.Score) {
			h.Score = 0
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// vectorQuery builds VectorSearch's query: a KNN MATCH on chunks_vec for
// the nearest k, then each hit's chunk and document for scoping and
// filters.
//
// Unlike bm25Query's, its order has no tie-break: sqlite-vec refuses a
// second ORDER BY key on a KNN query ("Only a single 'ORDER BY distance'
// clause is allowed on vec0 KNN queries"). Two chunks tie only when their
// embeddings are bit for bit the same.
//
// sqlite-vec applies the k-NN cutoff at the index level BEFORE the
// document predicates, and every search has one (searchedDocSQL), so a
// naive k=limit could return almost nothing once they are applied. The
// caller over-fetches neighbors, and VectorSearch keeps the closest limit
// rows that pass, so the fanout stays consistent.
func vectorQuery(tenantID string, embedding []byte, k int, filters store.SearchFilters) (string, []any) {
	filterSQL, filterArgs := buildFilterClause(filters)
	q := `
	SELECT v.chunk_id, c.document_id, v.distance
	FROM chunks_vec v
	JOIN chunks c     ON c.id = v.chunk_id
	JOIN documents d  ON d.id = c.document_id
	WHERE v.embedding MATCH ? AND k = ?
	  AND ` + searchedDocSQL + filterSQL + `
	ORDER BY v.distance`

	return q, slices.Concat([]any{embedding, k}, searchedDocArgs(tenantID), filterArgs)
}

// chunkSet lists the chunks VectorSearch may return under filters: those
// of the tenant's searched documents that pass them, from the tenant's
// documents in idx_documents_tenant_state_updated, each document's chunks
// by idx_chunks_document.
func chunkSet(tenantID string, filters store.SearchFilters) rowSet {
	filterSQL, filterArgs := buildFilterClause(filters)
	return rowSet{
		sql: `SELECT fc.id FROM documents d JOIN chunks fc ON fc.document_id = d.id
		WHERE ` + searchedDocSQL + filterSQL,
		args: slices.Concat(searchedDocArgs(tenantID), filterArgs),
	}
}

// exactVectorQuery builds the KNN of the k nearest of set's chunks alone:
// vec0 takes its primary key IN set, and scans the vectors of those rows
// alone, so the answer is the exact nearest of the set. Every row it
// returns passes the filters set was built from.
func exactVectorQuery(embedding []byte, k int, set rowSet) (string, []any) {
	q := `
	SELECT v.chunk_id, c.document_id, v.distance
	FROM chunks_vec v
	JOIN chunks c ON c.id = v.chunk_id
	WHERE v.embedding MATCH ? AND k = ?
	  AND v.chunk_id IN (` + set.sql + `)
	ORDER BY v.distance`
	return q, slices.Concat([]any{embedding, k}, set.args)
}

// hostPredicate is the condition that urlCol, a URL column, is an http or
// https URL of exactly host, and its arguments. There is no host column,
// so it matches the host segment of the URL: no port or subdomain
// coercion. The host is escaped so its '%', '_' and '\' match only
// themselves; LIKE's ASCII case-folding is right for host names.
func hostPredicate(urlCol, host string) (string, []any) {
	cond := "(" + urlCol + ` LIKE ? ESCAPE '\' OR ` + urlCol + ` LIKE ? ESCAPE '\' OR ` +
		urlCol + " = ? OR " + urlCol + " = ?)"
	return cond, []any{escapeLike("http://"+host+"/") + "%", escapeLike("https://"+host+"/") + "%",
		"http://" + host, "https://" + host}
}

// folderPredicate is the condition that folderCol, a folder path column,
// is folder or a folder under it, and its arguments; ok is false when
// folder, without its trailing "/", is empty, which is no filter. It
// compares byte-wise: '0' is the byte after '/', so [folder+"/",
// folder+"0") holds exactly the paths that start with folder+"/". Unlike
// LIKE there is nothing to escape, and it is case-sensitive like the
// equality.
func folderPredicate(folderCol, folder string) (cond string, args []any, ok bool) {
	folder = strings.TrimRight(folder, "/")
	if folder == "" {
		return "", nil, false
	}
	return "(" + folderCol + " = ? OR (" + folderCol + " >= ? AND " + folderCol + " < ?))",
		[]any{folder, folder + "/", folder + "0"}, true
}

// placeholders returns "?,?,...,?" with n marks.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

// EmbeddingsForDocument reads the stored chunk vectors for a document in
// chunk order. Point-reads on the vec0 virtual table return the raw
// little-endian float32 blob; the Go bindings ship no deserializer, so we
// decode by hand (4 bytes per float, dim floats per chunk).
func (s *Chunks) EmbeddingsForDocument(ctx context.Context, documentID string) ([]store.ChunkEmbedding, error) {
	if documentID == "" {
		return nil, errors.New("chunks: document_id required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.chunk_id, v.embedding
		FROM chunks_vec v
		JOIN chunks c ON c.id = v.chunk_id
		WHERE c.document_id = ?
		ORDER BY c.ord`, documentID)
	if err != nil {
		return nil, fmt.Errorf("embeddings for document: %w", err)
	}
	defer rows.Close()

	var out []store.ChunkEmbedding
	for rows.Next() {
		var (
			ce   store.ChunkEmbedding
			blob []byte
		)
		if err := rows.Scan(&ce.ChunkID, &blob); err != nil {
			return nil, fmt.Errorf("scan embedding: %w", err)
		}
		ce.Embedding, err = decodeVector(blob, s.dim)
		if err != nil {
			return nil, fmt.Errorf("chunk %s: %w", ce.ChunkID, err)
		}
		out = append(out, ce)
	}
	return out, rows.Err()
}

// decodeVector decodes a sqlite-vec point-read blob (raw little-endian
// float32, 4 bytes per component, dim components) into a []float32. The Go
// bindings ship a serializer but no deserializer, so we hand-decode.
func decodeVector(blob []byte, dim int) ([]float32, error) {
	if len(blob) != 4*dim {
		return nil, fmt.Errorf("embedding blob is %d bytes, want %d (dim %d)", len(blob), 4*dim, dim)
	}
	return decodeVectorBlob(blob)
}

// decodeVectorBlob decodes a vector of any width stored in decodeVector's
// layout, nil for an empty or NULL blob. A blob whose length isn't a whole
// number of components is an error.
func decodeVectorBlob(blob []byte) ([]float32, error) {
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("vector blob is %d bytes, not a whole number of float32s", len(blob))
	}
	if len(blob) == 0 {
		return nil, nil
	}
	v := make([]float32, len(blob)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return v, nil
}

// encodeVector stores v in decodeVector's layout, raw little-endian
// float32; an empty v is NULL.
func encodeVector(v []float32) any {
	if len(v) == 0 {
		return nil
	}
	blob := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(x))
	}
	return blob
}

// documentVectorsSQL reads the chunk vectors of the tenant's documents in
// a state, grouped by document in chunk order. It starts from the state's
// documents on idx_documents_tenant_state_updated.
const documentVectorsSQL = `
	SELECT c.document_id, v.embedding
	FROM chunks_vec v
	JOIN chunks c    ON c.id = v.chunk_id
	JOIN documents d ON d.id = c.document_id
	WHERE d.tenant_id = ? AND d.state = ?
	ORDER BY c.document_id, c.ord`

// DocumentVectors returns one mean-pooled vector per fetched document with at
// least one indexed chunk, in a single pass over chunks_vec. Rows are ordered
// by document so we can average each document's chunk vectors as we stream,
// without holding every chunk vector in memory at once.
func (s *Chunks) DocumentVectors(ctx context.Context, tenantID string) ([]store.DocVector, error) {
	if tenantID == "" {
		return nil, errors.New("chunks: tenant_id required")
	}
	rows, err := s.db.QueryContext(ctx, documentVectorsSQL, tenantID, store.DocStateFetched)
	if err != nil {
		return nil, fmt.Errorf("document vectors: %w", err)
	}
	defer rows.Close()

	var (
		out    []store.DocVector
		curDoc string
		mean   store.MeanVector // the current document's chunks so far
	)
	flush := func() {
		if v := mean.Mean(); v != nil {
			out = append(out, store.DocVector{DocumentID: curDoc, Vector: v})
		}
	}

	for rows.Next() {
		var (
			docID string
			blob  []byte
		)
		if err := rows.Scan(&docID, &blob); err != nil {
			return nil, fmt.Errorf("scan document vector: %w", err)
		}
		vec, err := decodeVector(blob, s.dim)
		if err != nil {
			return nil, fmt.Errorf("document %s: %w", docID, err)
		}
		if docID != curDoc {
			flush()
			curDoc, mean = docID, store.MeanVector{}
		}
		if err := mean.Add(vec); err != nil {
			return nil, fmt.Errorf("document %s: %w", docID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
}

// sampleChunksSQL is SampleChunks' statement. Its arguments are the
// tenant, the fetched state and the two counts.
//
// longest ranks each fetched document's chunks by the bytes embedded
// besides the document prefix, their text and context, the lower seq first
// among equals, and keeps the n longest of the documents' first. Lengths
// bunch at the chunker's cap, so the tie-break decides much of the set: a
// re-index keeps each chunk's seq, so the same library samples the same
// chunks every time. Ranking every chunk is the statement's one pass over
// the chunks table.
//
// picked then shuffles the other fetched documents that have chunks and
// keeps n; only then does sample pick one chunk of each, at random. Picked
// in the same SELECT as the shuffle, the chunk would be picked for every
// candidate document. Both CTEs are MATERIALIZED so random() and longest,
// read twice, are each evaluated once.
//
// Each sampled chunk is reached by its seq, and its vector by its ID: a
// point lookup in vec0, where `chunk_id IN (subquery)` scans every vector.
const sampleChunksSQL = `
	WITH longest AS MATERIALIZED (
		SELECT seq, document_id FROM (
			SELECT c.seq, c.document_id, octet_length(c.text) + octet_length(c.context) AS bytes,
			       row_number() OVER (PARTITION BY c.document_id
			                          ORDER BY octet_length(c.text) + octet_length(c.context) DESC, c.seq) AS nth
			FROM documents d JOIN chunks c ON c.document_id = d.id
			WHERE d.tenant_id = ?1 AND d.state = ?2)
		WHERE nth = 1
		ORDER BY bytes DESC, seq
		LIMIT ?3),
	picked AS MATERIALIZED (
		SELECT d.id FROM documents d
		WHERE d.tenant_id = ?1 AND d.state = ?2
		  AND d.id NOT IN (SELECT document_id FROM longest)
		  AND EXISTS (SELECT 1 FROM chunks c WHERE c.document_id = d.id)
		ORDER BY random()
		LIMIT ?4),
	sample(seq) AS (
		SELECT seq FROM longest
		UNION ALL
		SELECT (SELECT c.seq FROM chunks c WHERE c.document_id = p.id ORDER BY random() LIMIT 1) FROM picked p)
	SELECT c.id, c.document_id, c.text, c.context, v.embedding
	FROM sample s
	JOIN chunks c     ON c.seq = s.seq
	JOIN chunks_vec v ON v.chunk_id = c.id`

// SampleChunks reads the sample in one autocommit statement: in WAL a read
// never waits on a writer, and a transaction would add nothing to one
// statement's snapshot.
func (s *Chunks) SampleChunks(ctx context.Context, tenantID string, n store.ChunkSample) ([]store.SampledChunk, error) {
	if tenantID == "" {
		return nil, errors.New("chunks: tenant_id required")
	}
	// SQLite reads a negative LIMIT as none.
	if n.Longest < 0 || n.Random < 0 {
		return nil, fmt.Errorf("chunks: sample sizes must not be negative (longest %d, random %d)", n.Longest, n.Random)
	}
	rows, err := s.db.QueryContext(ctx, sampleChunksSQL, tenantID, store.DocStateFetched, n.Longest, n.Random)
	if err != nil {
		return nil, fmt.Errorf("sample chunks: %w", err)
	}
	defer rows.Close()

	out := make([]store.SampledChunk, 0, n.Longest+n.Random)
	for rows.Next() {
		var (
			c    store.SampledChunk
			blob []byte
		)
		if err := rows.Scan(&c.ChunkID, &c.DocumentID, &c.Text, &c.Context, &blob); err != nil {
			return nil, fmt.Errorf("scan sampled chunk: %w", err)
		}
		if c.Embedding, err = decodeVector(blob, s.dim); err != nil {
			return nil, fmt.Errorf("chunk %s: %w", c.ChunkID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sample chunks: %w", err)
	}
	return out, nil
}

// keepEmbeddedSQL records a document's chunks of an older context version
// as embedded under the version given, through idx_chunks_document. Their
// text, title and tags stay as they are, so trg_chunks_update writes
// nothing to chunks_fts.
const keepEmbeddedSQL = `UPDATE chunks SET context_version = ?1 WHERE document_id = ?2 AND context_version < ?1`

// staleContextSQL is whether a document holds a chunk of an older context
// version, its chunks a seek of idx_chunks_document.
const staleContextSQL = `SELECT EXISTS (SELECT 1 FROM chunks WHERE document_id = ?1 AND context_version < ?2)`

// StaleContext implements store.ChunkStore.
func (s *Chunks) StaleContext(ctx context.Context, documentID string, version int) (bool, error) {
	var stale bool
	if err := s.db.QueryRowContext(ctx, staleContextSQL, documentID, version).Scan(&stale); err != nil {
		return false, fmt.Errorf("read the context version of document %s's chunks: %w", documentID, err)
	}
	return stale, nil
}

// KeepEmbedded implements store.ChunkStore.
func (s *Chunks) KeepEmbedded(ctx context.Context, documentID string, version int) (int, error) {
	res, err := s.db.ExecContext(ctx, keepEmbeddedSQL, version, documentID)
	if err != nil {
		return 0, fmt.Errorf("keep the chunks of document %s as embedded: %w", documentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("keep the chunks of document %s as embedded: %w", documentID, err)
	}
	return int(n), nil
}

// GetByIDs returns the chunks with the given IDs, in arbitrary order.
func (s *Chunks) GetByIDs(ctx context.Context, ids []string) ([]*store.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := `SELECT id, document_id, extraction_id, ord, text, token_count
	      FROM chunks WHERE id IN (` + placeholders(len(ids)) + `)`
	rows, err := s.db.QueryContext(ctx, q, appendArgs(nil, ids)...)
	if err != nil {
		return nil, fmt.Errorf("get chunks: %w", err)
	}
	defer rows.Close()

	var out []*store.Chunk
	for rows.Next() {
		var (
			c          store.Chunk
			tokenCount sql.NullInt64
		)
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.ExtractionID, &c.Ord, &c.Text, &tokenCount); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		if tokenCount.Valid {
			c.TokenCount = int(tokenCount.Int64)
		}
		out = append(out, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get chunks: %w", err)
	}
	return out, nil
}
