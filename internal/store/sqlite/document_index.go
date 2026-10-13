package sqlite

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/samsar/curio/internal/store"
)

// DocumentIndex implements store.DocumentIndex over document_fields, its
// FTS index, document_cards and document_cards_vec. The rows are written
// by refreshFields, in the transactions of the writes that change what
// they are derived from; this type searches them and keeps the cards'
// vectors.
type DocumentIndex struct {
	db  *DB
	dim int // the card vectors' width, the home's
}

var _ store.DocumentIndex = (*DocumentIndex)(nil)

// NewDocumentIndex constructs the index. dim is the home's embedding
// width, which EnsureVectorIndex gives document_cards_vec; SearchCards and
// PutCardVectors reject a vector of any other length.
func NewDocumentIndex(db *DB, dim int) *DocumentIndex {
	return &DocumentIndex{db: db, dim: dim}
}

// The weights bm25 gives a match in each of the fields' columns. A folder
// names many documents and says less about any one of them than its own
// title, so it counts half.
const (
	fieldsTitleWeight          = 1.0
	fieldsBookmarkTitlesWeight = 1.0
	fieldsFoldersWeight        = 0.5
	fieldsSiteWeight           = 1.0
	fieldsAuthorWeight         = 1.0
)

// The weights the title list's bm25 gives each column: what a document
// was titled and saved as, and nothing else, the columns its match is
// held to (titlesColumns).
const (
	titlesTitleWeight          = 1.0
	titlesBookmarkTitlesWeight = 1.0
	titlesFoldersWeight        = 0.0
	titlesSiteWeight           = 0.0
	titlesAuthorWeight         = 0.0
)

// titlesColumns is the FTS5 column filter SearchTitles' match is held to.
const titlesColumns = "{title bookmark_titles}"

// fieldsBM25 and titlesBM25 are bm25() over document_fields_fts with the
// fields list's and the title list's column weights.
var (
	fieldsBM25 = documentFieldsBM25(fieldsTitleWeight, fieldsBookmarkTitlesWeight, fieldsFoldersWeight,
		fieldsSiteWeight, fieldsAuthorWeight)
	titlesBM25 = documentFieldsBM25(titlesTitleWeight, titlesBookmarkTitlesWeight, titlesFoldersWeight,
		titlesSiteWeight, titlesAuthorWeight)
)

// documentFieldsBM25 is bm25() over document_fields_fts with weights for
// its columns, in the order the index declares them.
func documentFieldsBM25(title, bookmarkTitles, folders, site, author float64) string {
	weights := []string{formatWeight(title), formatWeight(bookmarkTitles), formatWeight(folders), formatWeight(site),
		formatWeight(author)}
	return "bm25(document_fields_fts, " + strings.Join(weights, ", ") + ")"
}

func formatWeight(w float64) string { return strconv.FormatFloat(w, 'f', -1, 64) }

// fieldsQuery builds SearchFields' query (documentFieldsQuery).
func fieldsQuery(tenantID, query string, limit int, filters store.SearchFilters) (string, []any) {
	return documentFieldsQuery(fieldsBM25, query, tenantID, limit, filters)
}

// titlesQuery builds SearchTitles' query: query held to the title
// columns, ranked by their bm25 alone (documentFieldsQuery).
func titlesQuery(tenantID, query string, limit int, filters store.SearchFilters) (string, []any) {
	return documentFieldsQuery(titlesBM25, titlesColumns+" : ("+query+")", tenantID, limit, filters)
}

// documentFieldsQuery builds a search of the documents' fields, ranked by
// rank, a bm25() over document_fields_fts: it starts from the FTS MATCH
// of match, reaches each hit's fields by rowid (document_fields.seq) and
// its document by primary key for the tenant and the filters. No state is
// left out: a document's fields describe what it was saved as, which
// stays true whatever its fetch found. Equal scores are ordered by seq,
// so the ranking is the same on every search.
func documentFieldsQuery(rank, match, tenantID string, limit int, filters store.SearchFilters) (string, []any) {
	filterSQL, filterArgs := buildFilterClause(filters)
	q := `
	SELECT f.document_id, ` + rank + ` AS score
	FROM document_fields_fts
	JOIN document_fields f ON f.seq = document_fields_fts.rowid
	JOIN documents d       ON d.id = f.document_id
	WHERE document_fields_fts MATCH ?
	  AND d.tenant_id = ?` + filterSQL + `
	ORDER BY score, f.seq
	LIMIT ?`
	args := slices.Concat([]any{match, tenantID}, filterArgs)
	return q, append(args, limit)
}

// SearchFields implements store.DocumentIndex.
func (x *DocumentIndex) SearchFields(ctx context.Context, tenantID, query string, limit int,
	f store.SearchFilters) ([]store.DocumentHit, error) {
	if strings.TrimSpace(query) == "" || limit <= 0 {
		return nil, nil
	}
	q, args := fieldsQuery(tenantID, query, limit, f)
	hits, err := x.searchDocumentFields(ctx, q, args)
	if err != nil {
		return nil, fmt.Errorf("search fields: %w", err)
	}
	return hits, nil
}

// SearchTitles implements store.DocumentIndex.
func (x *DocumentIndex) SearchTitles(ctx context.Context, tenantID, query string, limit int,
	f store.SearchFilters) ([]store.DocumentHit, error) {
	if strings.TrimSpace(query) == "" || limit <= 0 {
		return nil, nil
	}
	q, args := titlesQuery(tenantID, query, limit, f)
	hits, err := x.searchDocumentFields(ctx, q, args)
	if err != nil {
		return nil, fmt.Errorf("search titles: %w", err)
	}
	return hits, nil
}

// searchDocumentFields runs a documentFieldsQuery. bm25() is lower for a
// better match, so a hit's score is its negation, higher better.
func (x *DocumentIndex) searchDocumentFields(ctx context.Context, q string, args []any) ([]store.DocumentHit, error) {
	rows, err := x.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.DocumentHit
	for rows.Next() {
		var h store.DocumentHit
		var bm25 float64
		if err := rows.Scan(&h.DocumentID, &bm25); err != nil {
			return nil, err
		}
		h.Score = -bm25
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// cardsQuery builds SearchCards' query: a KNN MATCH on
// document_cards_vec, then each hit's document by primary key for the
// tenant and the filters, which sqlite-vec applies after its k cutoff,
// hence the over-fetch. Like vectorQuery's, its order can have no second
// key (sqlite-vec refuses one); SearchCards breaks ties.
func cardsQuery(tenantID string, embedding []byte, limit int, filters store.SearchFilters) (string, []any) {
	filterSQL, filterArgs := buildFilterClause(filters)
	q := `
	SELECT v.document_id, v.distance
	FROM document_cards_vec v
	JOIN documents d ON d.id = v.document_id
	WHERE v.embedding MATCH ? AND k = ?
	  AND d.tenant_id = ?` + filterSQL + `
	ORDER BY v.distance`
	k := min(limit*10, maxVectorOverfetch)
	return q, slices.Concat([]any{embedding, k, tenantID}, filterArgs)
}

// SearchCards implements store.DocumentIndex. sqlite-vec returns L2
// distance; a hit's score is 1/(1+distance), higher better, as
// VectorSearch scores a chunk.
func (x *DocumentIndex) SearchCards(ctx context.Context, tenantID string, embedding []float32, limit int,
	f store.SearchFilters) ([]store.DocumentHit, error) {
	if len(embedding) != x.dim {
		return nil, fmt.Errorf("search cards: embedding length %d != configured dim %d", len(embedding), x.dim)
	}
	if limit <= 0 {
		return nil, nil
	}
	serialized, err := sqlitevec.SerializeFloat32(embedding)
	if err != nil {
		return nil, fmt.Errorf("serialize query embedding: %w", err)
	}
	q, args := cardsQuery(tenantID, serialized, limit, f)
	rows, err := x.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search cards: %w", err)
	}
	defer rows.Close()
	type near struct {
		id       string
		distance float64
	}
	var found []near
	for rows.Next() {
		var n near
		if err := rows.Scan(&n.id, &n.distance); err != nil {
			return nil, fmt.Errorf("search cards: %w", err)
		}
		found = append(found, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search cards: %w", err)
	}
	slices.SortFunc(found, func(a, b near) int {
		return cmp.Or(cmp.Compare(a.distance, b.distance), strings.Compare(a.id, b.id))
	})
	out := make([]store.DocumentHit, 0, min(limit, len(found)))
	for _, n := range found[:min(limit, len(found))] {
		score := 1 / (1 + n.distance)
		if math.IsNaN(score) {
			score = 0
		}
		out = append(out, store.DocumentHit{DocumentID: n.id, Score: score})
	}
	return out, nil
}

// staleCardsSQL pages through the tenant's stale cards by document,
// walking idx_document_cards_stale, which holds the stale ones alone. The
// tenant is checked on each card's document by primary key (+d.tenant_id):
// written plainly, SQLite walks the tenant's documents instead. Its args
// are the cursor, the tenant and the limit.
const staleCardsSQL = `SELECT c.document_id, c.text FROM document_cards c
	JOIN documents d ON d.id = c.document_id
	WHERE c.embedded_text IS NOT c.text AND c.document_id > ? AND +d.tenant_id = ?
	ORDER BY c.document_id
	LIMIT ?`

// StaleCards implements store.DocumentIndex.
func (x *DocumentIndex) StaleCards(ctx context.Context, tenantID, after string, limit int) ([]store.Card, error) {
	rows, err := x.db.QueryContext(ctx, staleCardsSQL, after, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("stale cards: %w", err)
	}
	defer rows.Close()
	var out []store.Card
	for rows.Next() {
		var c store.Card
		if err := rows.Scan(&c.DocumentID, &c.Text); err != nil {
			return nil, fmt.Errorf("stale cards: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stale cards: %w", err)
	}
	return out, nil
}

// The statements of PutCardVectors. A card is marked embedded only while
// its text is the text embedded, and only then does its vector change:
// vec0 has no upsert, so the old vector is deleted, a point lookup by
// primary key, and the new one inserted.
const (
	markCardEmbeddedSQL = `UPDATE document_cards SET embedded_text = ?1, embedded_at = ` + sqlNow + `
	WHERE document_id = ?2 AND text = ?1`
	deleteCardVectorSQL = `DELETE FROM document_cards_vec WHERE document_id = ?`
	insertCardVectorSQL = `INSERT INTO document_cards_vec (document_id, embedding) VALUES (?, ?)`
)

// PutCardVectors implements store.DocumentIndex.
func (x *DocumentIndex) PutCardVectors(ctx context.Context, cards []store.CardVector) (int, error) {
	for _, c := range cards {
		if len(c.Embedding) != x.dim {
			return 0, fmt.Errorf("put card vectors: document %s: embedding length %d != configured dim %d",
				c.DocumentID, len(c.Embedding), x.dim)
		}
	}
	if len(cards) == 0 {
		return 0, nil
	}
	written := 0
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("put card vectors: begin: %w", err)
	}
	defer tx.Rollback()
	for _, c := range cards {
		res, err := tx.ExecContext(ctx, markCardEmbeddedSQL, c.Text, c.DocumentID)
		if err != nil {
			return 0, fmt.Errorf("put card vectors: mark document %s's card embedded: %w", c.DocumentID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("put card vectors: %w", err)
		}
		if n == 0 {
			continue // the card changed since it was read: it stays stale
		}
		vec, err := sqlitevec.SerializeFloat32(c.Embedding)
		if err != nil {
			return 0, fmt.Errorf("put card vectors: serialize document %s's: %w", c.DocumentID, err)
		}
		if _, err := tx.ExecContext(ctx, deleteCardVectorSQL, c.DocumentID); err != nil {
			return 0, fmt.Errorf("put card vectors: delete document %s's old one: %w", c.DocumentID, err)
		}
		if _, err := tx.ExecContext(ctx, insertCardVectorSQL, c.DocumentID, vec); err != nil {
			return 0, fmt.Errorf("put card vectors: insert document %s's: %w", c.DocumentID, err)
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("put card vectors: commit: %w", err)
	}
	return written, nil
}

// resetCardsSQL makes the tenant's embedded cards stale, reaching them
// from the tenant's documents.
const resetCardsSQL = `UPDATE document_cards SET embedded_text = NULL
	WHERE embedded_text IS NOT NULL AND document_id IN (SELECT id FROM documents WHERE tenant_id = ?)`

// ResetCards implements store.DocumentIndex.
func (x *DocumentIndex) ResetCards(ctx context.Context, tenantID string) (int, error) {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("reset cards: begin: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, resetCardsSQL, tenantID)
	if err != nil {
		return 0, fmt.Errorf("reset cards: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reset cards: %w", err)
	}
	if err := x.db.queueCards(ctx, tx, tenantID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("reset cards: commit: %w", err)
	}
	return int(n), nil
}
