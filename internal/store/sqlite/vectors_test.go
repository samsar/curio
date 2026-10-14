package sqlite

import (
	"context"
	"slices"
	"testing"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// migratedDB is a database the migrations alone made: chunks_vec as 001
// creates it, 768 wide.
func migratedDB(t *testing.T) *DB {
	t.Helper()
	db, _ := openUnmigrated(t)
	_, err := Migrate(context.Background(), db)
	require.NoError(t, err)
	return db
}

func vectorWidth(t *testing.T, db *DB) int {
	t.Helper()
	width, err := VectorIndexWidth(context.Background(), db)
	require.NoError(t, err)
	return width
}

func cardVectorWidth(t *testing.T, db *DB) int {
	t.Helper()
	width, err := vectorIndexWidth(context.Background(), db, "document_cards_vec")
	require.NoError(t, err)
	return width
}

// dropVectorIndexSQL drops chunks_vec, for tests that start without one.
var dropVectorIndexSQL = vectorIndexes[0].dropSQL()

// schemaVersion is SQLite's schema cookie, which every DDL statement bumps.
func schemaVersion(t *testing.T, db *DB) int {
	t.Helper()
	var v int
	require.NoError(t, db.QueryRow(`PRAGMA schema_version`).Scan(&v))
	return v
}

func vectorCount(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM chunks_vec`).Scan(&n))
	return n
}

func TestEnsureVectorIndex_ResizesTheMigrationsEmptyTable(t *testing.T) {
	db := migratedDB(t)
	require.Equal(t, 768, vectorWidth(t, db))
	require.Zero(t, cardVectorWidth(t, db), "the migrations create no card vectors")

	require.NoError(t, EnsureVectorIndex(context.Background(), db, 1024))
	assert.Equal(t, 1024, vectorWidth(t, db))
	assert.Equal(t, 1024, cardVectorWidth(t, db), "the cards' at the same width")
}

func TestEnsureVectorIndex_SameWidthIssuesNoDDL(t *testing.T) {
	db := migratedDB(t)
	require.NoError(t, EnsureVectorIndex(context.Background(), db, 768))
	before := schemaVersion(t, db)

	require.NoError(t, EnsureVectorIndex(context.Background(), db, 768))
	assert.Equal(t, before, schemaVersion(t, db), "nothing was dropped or created")
	assert.Equal(t, 768, vectorWidth(t, db))
	assert.Equal(t, 768, cardVectorWidth(t, db))
}

// TestEnsureVectorIndex_CardVectors: the card vector table comes with the
// trigger that deletes a card's vector with its card, and every time it
// is created, the cards it doesn't hold vectors of become stale; one that
// holds vectors of another width is refused, as chunks_vec is.
func TestEnsureVectorIndex_CardVectors(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	require.NoError(t, EnsureVectorIndex(ctx, db, 768))
	var trigger string
	require.NoError(t, db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'trg_document_cards_delete'`).Scan(&trigger))
	assert.Contains(t, trigger, "DELETE FROM document_cards_vec WHERE document_id = old.document_id")

	ids := seedDocs(t, db, "local", "https://example.com/a", "https://example.com/b")
	_, err := db.Exec(`UPDATE document_cards SET embedded_text = text, embedded_at = 'then'`)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE document_cards_vec`)
	require.NoError(t, err)
	require.NoError(t, EnsureVectorIndex(ctx, db, 768))
	slices.Sort(ids)
	assert.Equal(t, [][]any{{ids[0], nil, nil}, {ids[1], nil, nil}}, dumpRows(t, db,
		`SELECT document_id, embedded_text, embedded_at FROM document_cards ORDER BY document_id`),
		"a new table: every card stale")

	vec, err := sqlitevec.SerializeFloat32(make([]float32, 768))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO document_cards_vec (document_id, embedding) VALUES (?, ?)`, ids[0], vec)
	require.NoError(t, err)
	before := schemaVersion(t, db)
	err = EnsureVectorIndex(ctx, db, 1024)
	var widthErr *VectorWidthError
	require.ErrorAs(t, err, &widthErr)
	assert.Equal(t, VectorWidthError{Path: db.Path(), Table: "document_cards_vec", Have: 768, Want: 1024}, *widthErr)
	assert.Equal(t, before, schemaVersion(t, db), "nothing changed")

	_, err = db.Exec(`DELETE FROM documents WHERE id = ?`, ids[0])
	require.NoError(t, err)
	var vectors int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM document_cards_vec`).Scan(&vectors))
	assert.Zero(t, vectors, "the card's vector went with its document")
}

func TestEnsureVectorIndex_CreatesAMissingTable(t *testing.T) {
	db := migratedDB(t)
	_, err := db.Exec(dropVectorIndexSQL)
	require.NoError(t, err)
	require.Zero(t, vectorWidth(t, db))

	require.NoError(t, EnsureVectorIndex(context.Background(), db, 384))
	assert.Equal(t, 384, vectorWidth(t, db))
}

// TestEnsureVectorIndex_RefusesATableWithVectors: a table of another width
// that holds vectors is never rebuilt, which would discard them.
func TestEnsureVectorIndex_RefusesATableWithVectors(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	ids := seedDocs(t, db, "local", "https://example.com/a")
	require.NoError(t, NewChunks(db, 768).ReplaceForDocument(ctx, ids[0], latestExtractionID(t, db, ids[0]), "", nil,
		[]store.ChunkInput{{Text: "kept", Embedding: make([]float32, 768)}}))
	before := schemaVersion(t, db)

	err := EnsureVectorIndex(ctx, db, 1024)
	var widthErr *VectorWidthError
	require.ErrorAs(t, err, &widthErr)
	assert.Equal(t, VectorWidthError{Path: db.Path(), Table: "chunks_vec", Have: 768, Want: 1024}, *widthErr)
	for _, want := range []string{db.Path(), "chunks_vec", "768-dimensional", "embedding width is 1024"} {
		assert.Contains(t, err.Error(), want)
	}
	assert.Equal(t, before, schemaVersion(t, db), "nothing changed")
	assert.Equal(t, 768, vectorWidth(t, db))
	assert.Equal(t, 1, vectorCount(t, db))
}

func TestEnsureVectorIndex_RejectsWidthsTheIndexCantTake(t *testing.T) {
	db := migratedDB(t)
	before := schemaVersion(t, db)
	for _, dim := range []int{0, -1, store.MaxEmbeddingDim + 1} {
		err := EnsureVectorIndex(context.Background(), db, dim)
		require.Error(t, err, dim)
		assert.Contains(t, err.Error(), "must be in [1, 8192]")
	}
	assert.Equal(t, before, schemaVersion(t, db))
}

// TestEnsureVectorIndex_UnreadableDeclaration: a chunks_vec whose CREATE
// statement declares no width is an error, not a guess.
func TestEnsureVectorIndex_UnreadableDeclaration(t *testing.T) {
	db := migratedDB(t)
	_, err := db.Exec(dropVectorIndexSQL + `; CREATE TABLE chunks_vec (chunk_id TEXT PRIMARY KEY, vector BLOB)`)
	require.NoError(t, err)

	err = EnsureVectorIndex(context.Background(), db, 1024)
	require.ErrorContains(t, err, "vector index declaration has no embedding width")
	assert.Contains(t, err.Error(), "vector BLOB")
}

// TestEnsureVectorIndex_RebuiltTableWorksWithTheStore: after a resize the
// chunks delete trigger still reaches chunks_vec, so replacing a document's
// chunks leaves exactly the new vectors, and vec0 takes only the new width.
func TestEnsureVectorIndex_RebuiltTableWorksWithTheStore(t *testing.T) {
	ctx := context.Background()
	db := migratedDB(t)
	const dim = 384
	require.NoError(t, EnsureVectorIndex(ctx, db, dim))
	ch := NewChunks(db, dim)
	ids := seedDocs(t, db, "local", "https://example.com/a")
	ext := latestExtractionID(t, db, ids[0])
	vec := make([]float32, dim)

	require.NoError(t, ch.ReplaceForDocument(ctx, ids[0], ext, "", nil,
		[]store.ChunkInput{{Text: "one", Embedding: vec}, {Text: "two", Embedding: vec}, {Text: "three", Embedding: vec}}))
	require.NoError(t, ch.ReplaceForDocument(ctx, ids[0], ext, "", nil,
		[]store.ChunkInput{{Text: "four", Embedding: vec}}))
	assert.Equal(t, 1, vectorCount(t, db))
	var orphans int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM chunks_vec v
		WHERE NOT EXISTS (SELECT 1 FROM chunks c WHERE c.id = v.chunk_id)`).Scan(&orphans))
	assert.Zero(t, orphans, "every vector left belongs to a chunk")

	old, err := sqlitevec.SerializeFloat32(make([]float32, 768))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO chunks_vec (chunk_id, embedding) VALUES ('x', ?)`, old)
	assert.ErrorContains(t, err, "Dimension mismatch")
}
