package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
)

// Chunks implements store.ChunkStore. Owns three coupled tables:
//
//	chunks      — canonical text rows, FK to document + extraction
//	chunks_fts  — FTS5 virtual table for BM25 search
//	chunks_vec  — sqlite-vec virtual table for ANN search
//
// All three are kept in sync transactionally by ReplaceForDocument; queries
// read from one virtual table at a time and JOIN to documents for tenant
// scoping.
type Chunks struct {
	db  *DB
	dim int // vec dimension; must match the chunks_vec schema and the embedder
}

var _ store.ChunkStore = (*Chunks)(nil)

// NewChunks constructs the store. dim must match the embedding dimension
// declared at migration time (currently 768 for nomic-embed-text). Mismatches
// produce a runtime error on insert; the daemon should fail fast at startup
// when config disagrees with .curio-meta.json.
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
		return fmt.Errorf("chunks: document_id required")
	}
	if extractionID == "" {
		return fmt.Errorf("chunks: extraction_id required")
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
	defer tx.Rollback() //nolint:errcheck

	// Delete existing chunks for the document. The chunks → chunks_fts
	// and chunks → chunks_vec relationships have no DB-level cascade
	// (virtual tables don't support FK), so we delete from all three.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM chunks_vec WHERE chunk_id IN
			(SELECT id FROM chunks WHERE document_id = ?)`, documentID); err != nil {
		return fmt.Errorf("delete chunks_vec: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM chunks_fts WHERE document_id = ?`, documentID); err != nil {
		return fmt.Errorf("delete chunks_fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chunks WHERE document_id = ?`, documentID); err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}

	// Insert fresh chunks.
	for i, c := range chunks {
		chunkID := uuid.NewString()

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chunks (id, document_id, extraction_id, ord, text, token_count)
			VALUES (?, ?, ?, ?, ?, ?)`,
			chunkID, documentID, extractionID, i, c.Text, c.TokenCount); err != nil {
			return fmt.Errorf("insert chunk[%d]: %w", i, err)
		}

		// FTS5: include the chunk text plus denormalized title/tags so the
		// indexer can boost them without a JOIN at query time.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chunks_fts (text, title, title_search, tags, chunk_id, document_id)
			VALUES (?, ?, ?, ?, ?, ?)`,
			c.Text, title, title, tagsStr, chunkID, documentID); err != nil {
			return fmt.Errorf("insert chunks_fts[%d]: %w", i, err)
		}

		// sqlite-vec needs the embedding in its specific binary format.
		serialized, err := sqlitevec.SerializeFloat32(c.Embedding)
		if err != nil {
			return fmt.Errorf("serialize embedding[%d]: %w", i, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chunks_vec (chunk_id, embedding) VALUES (?, ?)`,
			chunkID, serialized); err != nil {
			return fmt.Errorf("insert chunks_vec[%d]: %w", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chunks: %w", err)
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

	filterSQL, filterArgs := buildFilterClause(filters)

	// snippet() args: column index, open mark, close mark, ellipsis,
	// max tokens. 32 tokens gives ~200-300 char snippets — enough to
	// see the match in context without flooding the CLI. The CLI's
	// wrapLines breaks them across lines on word boundaries.
	q := `
	SELECT fts.chunk_id, fts.document_id, bm25(chunks_fts) AS bm25_score,
	       snippet(chunks_fts, 0, '<em>', '</em>', '…', 32)
	FROM chunks_fts fts
	JOIN documents d ON d.id = fts.document_id
	WHERE chunks_fts MATCH ?
	  AND d.tenant_id = ?` + filterSQL + `
	ORDER BY bm25_score
	LIMIT ?`

	args := make([]any, 0, 3+len(filterArgs))
	args = append(args, query, tenantID)
	args = append(args, filterArgs...)
	args = append(args, limit)

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
			snippet sql.NullString
		)
		if err := rows.Scan(&h.ChunkID, &h.DocumentID, &bm25Neg, &snippet); err != nil {
			return nil, fmt.Errorf("scan bm25 hit: %w", err)
		}
		// Negate so "higher is better" matches vector convention.
		h.Score = -bm25Neg
		if snippet.Valid {
			h.Snippet = snippet.String
		}
		out = append(out, h)
	}
	return out, rows.Err()
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

	// sqlite-vec applies the k-NN cutoff at the index level BEFORE the
	// document predicates, so with filters a naive k=limit could return
	// almost nothing after filtering. Over-fetch neighbors and cap the
	// returned (closest) rows to limit so the fanout stays consistent.
	k := limit
	if !filters.IsEmpty() {
		if k = limit * 10; k > 1000 {
			k = 1000
		}
	}

	filterSQL, filterArgs := buildFilterClause(filters)
	q := `
	SELECT v.chunk_id, c.document_id, v.distance
	FROM chunks_vec v
	JOIN chunks c     ON c.id = v.chunk_id
	JOIN documents d  ON d.id = c.document_id
	WHERE v.embedding MATCH ? AND k = ?
	  AND d.tenant_id = ?` + filterSQL + `
	ORDER BY v.distance`

	args := make([]any, 0, 3+len(filterArgs))
	args = append(args, serialized, k, tenantID)
	args = append(args, filterArgs...)

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
		// No host column, so match the host segment of the URL for http(s).
		// Exact host (the caller passes it); no port/subdomain coercion.
		conds := make([]string, 0, len(f.Host))
		for _, h := range f.Host {
			conds = append(conds, "(d.url LIKE ? OR d.url LIKE ? OR d.url = ? OR d.url = ?)")
			args = append(args, "http://"+h+"/%", "https://"+h+"/%", "http://"+h, "https://"+h)
		}
		sb.WriteString(" AND (" + strings.Join(conds, " OR ") + ")")
	}
	if f.ExcludeDocumentID != "" {
		// NOTE: in KNN mode sqlite-vec applies this predicate AFTER the
		// k cutoff — correctness here depends on VectorSearch's
		// over-fetch when filters are non-empty (which a set
		// ExcludeDocumentID guarantees via IsEmpty).
		sb.WriteString(" AND d.id != ?")
		args = append(args, f.ExcludeDocumentID)
	}
	return sb.String(), args
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
		return nil, fmt.Errorf("chunks: document_id required")
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
		if len(blob) != 4*s.dim {
			return nil, fmt.Errorf("embedding blob for chunk %s is %d bytes, want %d (dim %d)",
				ce.ChunkID, len(blob), 4*s.dim, s.dim)
		}
		ce.Embedding = make([]float32, s.dim)
		for i := range ce.Embedding {
			ce.Embedding[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
		}
		out = append(out, ce)
	}
	return out, rows.Err()
}

// GetByIDs returns chunks in arbitrary order. Used by the search layer to
// pull text for collapsed top-K results.
func (s *Chunks) GetByIDs(ctx context.Context, ids []string) ([]*store.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = strings.TrimRight(placeholders, ",")

	q := `SELECT id, document_id, extraction_id, ord, text, token_count
	      FROM chunks WHERE id IN (` + placeholders + `)`

	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
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
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("chunks: no rows for any of the given ids")
	}
	return out, nil
}
