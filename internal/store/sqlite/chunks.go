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

func (s *Chunks) ReplaceForDocument(
	ctx context.Context,
	documentID, extractionID, title string,
	tags []string,
	chunks []store.ChunkInput,
) error {
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

	if _, err := tx.ExecContext(ctx, deleteDocumentChunksSQL, documentID); err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}
	if err := insertChunks(ctx, tx, documentID, extractionID, title, tagsStr, chunks); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chunks: %w", err)
	}
	return nil
}

// deleteDocumentChunksSQL deletes a document's chunks through
// idx_chunks_document. The delete trigger takes each chunk's FTS entry and
// vector with it by rowid and chunk ID, so nothing scans chunks_fts or
// chunks_vec.
const deleteDocumentChunksSQL = `DELETE FROM chunks WHERE document_id = ?`

// insertChunks inserts chunks in order, each as a chunk row (which the
// insert trigger indexes, with title and tags) and a vector, through two
// statements prepared once.
func insertChunks(ctx context.Context, tx *sql.Tx, documentID, extractionID, title, tags string,
	chunks []store.ChunkInput) (err error) {
	insChunk, err := tx.PrepareContext(ctx, `
		INSERT INTO chunks (id, document_id, extraction_id, ord, text, token_count, title, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare chunk insert: %w", err)
	}
	defer func() { err = errors.Join(err, insChunk.Close()) }()
	insVec, err := tx.PrepareContext(ctx, `INSERT INTO chunks_vec (chunk_id, embedding) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare vector insert: %w", err)
	}
	defer func() { err = errors.Join(err, insVec.Close()) }()

	for i, c := range chunks {
		chunkID := uuid.NewString()
		if _, err := insChunk.ExecContext(ctx,
			chunkID, documentID, extractionID, i, c.Text, c.TokenCount, title, tags); err != nil {
			return fmt.Errorf("insert chunk[%d]: %w", i, err)
		}
		// sqlite-vec needs the embedding in its specific binary format.
		serialized, err := sqlitevec.SerializeFloat32(c.Embedding)
		if err != nil {
			return fmt.Errorf("serialize embedding[%d]: %w", i, err)
		}
		if _, err := insVec.ExecContext(ctx, chunkID, serialized); err != nil {
			return fmt.Errorf("insert chunks_vec[%d]: %w", i, err)
		}
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
// doesn't match, or that no longer exists (a reindex replaced it since it
// was retrieved), is absent.
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

	q, args := vectorQuery(tenantID, serialized, limit, filters)
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

// vectorQuery builds VectorSearch's query: a KNN MATCH on chunks_vec, then
// each hit's chunk and document for scoping and filters.
//
// Unlike bm25Query's, its order has no tie-break: sqlite-vec refuses a
// second ORDER BY key on a KNN query ("Only a single 'ORDER BY distance'
// clause is allowed on vec0 KNN queries"). Two chunks tie only when their
// embeddings are bit for bit the same.
//
// sqlite-vec applies the k-NN cutoff at the index level BEFORE the
// document predicates, and every search has one (searchedDocSQL), so a
// naive k=limit could return almost nothing once they are applied. The
// query over-fetches neighbors, and VectorSearch keeps the closest limit
// rows that pass, so the fanout stays consistent.
func vectorQuery(tenantID string, embedding []byte, limit int, filters store.SearchFilters) (string, []any) {
	filterSQL, filterArgs := buildFilterClause(filters)
	q := `
	SELECT v.chunk_id, c.document_id, v.distance
	FROM chunks_vec v
	JOIN chunks c     ON c.id = v.chunk_id
	JOIN documents d  ON d.id = c.document_id
	WHERE v.embedding MATCH ? AND k = ?
	  AND ` + searchedDocSQL + filterSQL + `
	ORDER BY v.distance`

	k := min(limit*10, maxVectorOverfetch)
	return q, slices.Concat([]any{embedding, k}, searchedDocArgs(tenantID), filterArgs)
}

// maxVectorOverfetch caps how many neighbors a vector search reads before
// its document predicates apply.
const maxVectorOverfetch = 1000

// buildFilterClause builds the AND-prefixed WHERE conditions and bind args to
// scope a search by content_type / host / source. The caller's query MUST
// alias the documents table as `d`. Returns ("", nil) for an empty filter.
func buildFilterClause(f store.SearchFilters) (string, []any) {
	if f.IsEmpty() {
		return "", nil
	}
	var sb strings.Builder
	var args []any

	if len(f.ContentType) > 0 {
		sb.WriteString(" AND d.content_type IN (" + placeholders(len(f.ContentType)) + ")")
		for _, v := range f.ContentType {
			args = append(args, v)
		}
	}
	if len(f.Source) > 0 {
		sb.WriteString(" AND EXISTS (SELECT 1 FROM bookmarks b" +
			" WHERE b.document_id = d.id AND b.tenant_id = d.tenant_id" +
			" AND b.source IN (" + placeholders(len(f.Source)) + "))")
		for _, v := range f.Source {
			args = append(args, v)
		}
	}
	if len(f.Host) > 0 {
		conds := make([]string, 0, len(f.Host))
		for _, h := range f.Host {
			cond, condArgs := hostPredicate("d.url", h)
			conds = append(conds, cond)
			args = append(args, condArgs...)
		}
		sb.WriteString(" AND (" + strings.Join(conds, " OR ") + ")")
	}
	if f.ExcludeDocumentID != "" {
		// NOTE: in KNN mode sqlite-vec applies this predicate AFTER the
		// k cutoff — correctness here depends on vectorQuery's over-fetch.
		sb.WriteString(" AND d.id != ?")
		args = append(args, f.ExcludeDocumentID)
	}
	return sb.String(), args
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
// longest ranks each fetched document's chunks by length in bytes, the
// lower seq first among equals, and keeps the n longest of the documents'
// first. Lengths bunch at the chunker's cap, so the tie-break decides much
// of the set; seq has no AUTOINCREMENT, so a reindex rewrites a document's
// chunks at the top and the lower seq is the chunk written first. Ranking
// every chunk is the statement's one pass over the chunks table.
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
			SELECT c.seq, c.document_id, octet_length(c.text) AS bytes,
			       row_number() OVER (PARTITION BY c.document_id ORDER BY octet_length(c.text) DESC, c.seq) AS nth
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
	SELECT c.id, c.document_id, c.text, v.embedding
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
		if err := rows.Scan(&c.ChunkID, &c.DocumentID, &c.Text, &blob); err != nil {
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
