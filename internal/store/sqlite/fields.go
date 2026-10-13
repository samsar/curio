package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/store"
)

// A document's search fields and its card are derived from the document,
// its current extraction and its bookmarks (internal/docmeta), and kept in
// step by the store itself: every write that changes what they are
// derived from calls refreshFields in its own transaction, so no caller
// can forget to, and the derived rows commit or roll back with the write.
// refreshFields is the only writer of document_fields and of
// document_cards.text. A card whose text it changes is stale, and the
// same transaction queues the cards job that embeds it.
//
// A document's display site name is agreed across its host: its own
// declared name, else the name its host's documents agree on
// (docmeta.AgreedName). Each row keeps what it contributes to that
// agreement, so agreeing reads only the host's rows, through
// idx_document_fields_host, and a document whose write changes the name
// agreed has every sibling without a declared name of its own rewritten.

// The statements of refreshFields, which runs them in its caller's
// transaction.
const (
	// fieldsInputsSQL reads what a document's fields are derived from: its
	// URL, title and author, the site name its current extraction's page
	// declared (a string under the meta path, ?1), and the host its fields
	// were last derived under, NULL when they never were. Its other args
	// are the document and the tenant.
	fieldsInputsSQL = `
	SELECT d.url, coalesce(d.title, ''), coalesce(d.author, ''),
	       CASE WHEN json_type(e.extraction_meta, ?1) = 'text' THEN json_extract(e.extraction_meta, ?1) ELSE '' END,
	       f.host
	FROM documents d
	LEFT JOIN document_extractions e ON e.id = d.current_extraction_id
	LEFT JOIN document_fields f ON f.document_id = d.id
	WHERE d.id = ?2 AND d.tenant_id = ?3`
	// fieldsBookmarksSQL reads a document's bookmarks oldest saved first,
	// in idx_bookmarks_document's order.
	fieldsBookmarksSQL = `SELECT coalesce(title, ''), coalesce(folder_path, '') FROM bookmarks
	WHERE document_id = ? ORDER BY saved_at, id`
	// hostFieldsSQL reads what each of a host's documents contributes to
	// the name the host agrees on, and the name it has, through
	// idx_document_fields_host. Its args are the tenant and the host.
	hostFieldsSQL = `SELECT document_id, declared_site, title_suffixes, site_name FROM document_fields
	WHERE tenant_id = ? AND host = ?`
	// siblingFieldsSQL reads the fields a sibling's card is made from.
	siblingFieldsSQL = `SELECT title_suffixes, title, bookmark_titles, folders, author FROM document_fields
	WHERE document_id = ?`
	// upsertFieldsSQL writes a document's fields, and writes nothing when
	// they are what is stored: no row change, no index update.
	upsertFieldsSQL = `
	INSERT INTO document_fields (document_id, tenant_id, host, declared_site, title_suffixes, site_name, title,
		bookmark_titles, folders, site, author, rules)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (document_id) DO UPDATE SET
		tenant_id = excluded.tenant_id, host = excluded.host, declared_site = excluded.declared_site,
		title_suffixes = excluded.title_suffixes, site_name = excluded.site_name, title = excluded.title,
		bookmark_titles = excluded.bookmark_titles, folders = excluded.folders, site = excluded.site,
		author = excluded.author, rules = excluded.rules
	WHERE (tenant_id, host, declared_site, title_suffixes, site_name, title, bookmark_titles, folders, site, author,
		rules)
		IS NOT (excluded.tenant_id, excluded.host, excluded.declared_site, excluded.title_suffixes,
		excluded.site_name, excluded.title, excluded.bookmark_titles, excluded.folders, excluded.site,
		excluded.author, excluded.rules)`
	// setSiteSQL gives a sibling the name its host now agrees on. Its args
	// are the name, the site and the document.
	setSiteSQL = `UPDATE document_fields SET site_name = ?, site = ? WHERE document_id = ?`
	// upsertCardSQL writes a document's card text, and writes nothing when
	// it is what is stored, so an unchanged card stays embedded.
	upsertCardSQL = `
	INSERT INTO document_cards (document_id, text) VALUES (?, ?)
	ON CONFLICT (document_id) DO UPDATE SET text = excluded.text
	WHERE text IS NOT excluded.text`
)

// fieldsTx refreshes fields in one transaction.
type fieldsTx struct {
	tx       *sql.Tx
	tenantID string
	// cardsChanged is set once a card's text changed.
	cardsChanged bool
}

// derivedDoc is a document refreshFields derived.
type derivedDoc struct {
	id  string
	own docmeta.Own
}

// hostRow is what one of a host's documents contributes to the name the
// host agrees on, and the name it has.
type hostRow struct {
	declared, suffixes, siteName string
}

// refreshFields derives the fields and cards of the tenant's documents ids
// again from what they are derived from now, inside tx, the transaction of
// the write that changed them. It agrees each affected host's site name
// again, the host a document is on and any it has left, and rewrites the
// host's other documents that take the agreed name. A document that no
// longer exists is skipped: its rows went with it. When any card's text
// changed, it queues a cards job, at most one pending per tenant, and
// reports whether it queued one, for the caller's commit to wake the
// cards worker (DB.commitNotify).
func refreshFields(ctx context.Context, tx *sql.Tx, tenantID string, ids ...string) (cardsQueued bool, err error) {
	f := &fieldsTx{tx: tx, tenantID: tenantID}

	refreshed := map[string]bool{}
	onHost := map[string][]derivedDoc{}
	hosts := map[string]bool{}
	for _, id := range ids {
		if refreshed[id] {
			continue
		}
		refreshed[id] = true
		own, oldHost, ok, err := f.derive(ctx, id)
		if err != nil {
			return false, fmt.Errorf("derive the fields of document %s: %w", id, err)
		}
		if !ok {
			continue
		}
		onHost[own.Host] = append(onHost[own.Host], derivedDoc{id: id, own: own})
		hosts[own.Host] = true
		if oldHost.Valid {
			hosts[oldHost.String] = true
		}
	}
	for _, host := range slices.Sorted(maps.Keys(hosts)) {
		if err := f.agree(ctx, host, onHost[host], refreshed); err != nil {
			return false, fmt.Errorf("agree the site name of host %q: %w", host, err)
		}
	}
	if !f.cardsChanged {
		return false, nil
	}
	return queueCards(ctx, tx, tenantID)
}

// derive reads document id's inputs and derives what it says of itself.
// oldHost is the host its stored fields name, when it has any; ok is
// false when the tenant has no such document.
func (f *fieldsTx) derive(ctx context.Context, id string) (own docmeta.Own, oldHost sql.NullString, ok bool, err error) {
	var d docmeta.Document
	err = f.tx.QueryRowContext(ctx, fieldsInputsSQL, "$."+store.ExtractionMetaSiteName, id, f.tenantID).
		Scan(&d.URL, &d.Title, &d.Author, &d.SiteName, &oldHost)
	if errors.Is(err, sql.ErrNoRows) {
		return docmeta.Own{}, sql.NullString{}, false, nil
	}
	if err != nil {
		return docmeta.Own{}, sql.NullString{}, false, fmt.Errorf("read the document: %w", err)
	}
	if d.Bookmarks, err = f.bookmarks(ctx, id); err != nil {
		return docmeta.Own{}, sql.NullString{}, false, err
	}
	return docmeta.Derive(d), oldHost, true, nil
}

// bookmarks reads document id's bookmarks, oldest saved first.
func (f *fieldsTx) bookmarks(ctx context.Context, id string) ([]docmeta.Bookmark, error) {
	rows, err := f.tx.QueryContext(ctx, fieldsBookmarksSQL, id)
	if err != nil {
		return nil, fmt.Errorf("read the bookmarks: %w", err)
	}
	defer rows.Close()
	var out []docmeta.Bookmark
	for rows.Next() {
		var b docmeta.Bookmark
		if err := rows.Scan(&b.Title, &b.FolderPath); err != nil {
			return nil, fmt.Errorf("read the bookmarks: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the bookmarks: %w", err)
	}
	return out, nil
}

// agree agrees host's site name over its documents as they are once docs,
// the refreshed documents now on it, are written and the other refreshed
// ones have left it, then writes docs' fields and cards and rewrites every
// other document of the host whose name the agreement changed.
func (f *fieldsTx) agree(ctx context.Context, host string, docs []derivedDoc, refreshed map[string]bool) error {
	rows, err := f.hostRows(ctx, host)
	if err != nil {
		return err
	}
	for id := range refreshed {
		delete(rows, id)
	}
	for _, d := range docs {
		rows[d.id] = hostRow{declared: d.own.DeclaredSite, suffixes: d.own.TitleSuffixes}
	}
	contributions := make([]docmeta.HostDocument, 0, len(rows))
	for _, r := range rows {
		contributions = append(contributions, docmeta.HostDocument{DeclaredSite: r.declared, TitleSuffixes: r.suffixes})
	}
	agreed := docmeta.AgreedName(host, contributions)

	for _, d := range docs {
		if err := f.write(ctx, d.id, d.own.Fields(agreed)); err != nil {
			return fmt.Errorf("write the fields of document %s: %w", d.id, err)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(rows)) {
		if r := rows[id]; !refreshed[id] && r.declared == "" && r.siteName != agreed {
			if err := f.rename(ctx, id, host, agreed); err != nil {
				return fmt.Errorf("give document %s the name its host agrees on: %w", id, err)
			}
		}
	}
	return nil
}

// hostRows reads what each of host's documents contributes to its agreed
// name, by document.
func (f *fieldsTx) hostRows(ctx context.Context, host string) (map[string]hostRow, error) {
	rows, err := f.tx.QueryContext(ctx, hostFieldsSQL, f.tenantID, host)
	if err != nil {
		return nil, fmt.Errorf("read the host's documents: %w", err)
	}
	defer rows.Close()
	out := map[string]hostRow{}
	for rows.Next() {
		var id string
		var r hostRow
		if err := rows.Scan(&id, &r.declared, &r.suffixes, &r.siteName); err != nil {
			return nil, fmt.Errorf("read the host's documents: %w", err)
		}
		out[id] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the host's documents: %w", err)
	}
	return out, nil
}

// rename gives document id, on host and without a declared name of its
// own, the name its host now agrees on: its site, and its card.
func (f *fieldsTx) rename(ctx context.Context, id, host, agreed string) error {
	own := docmeta.Own{Host: host}
	if err := f.tx.QueryRowContext(ctx, siblingFieldsSQL, id).Scan(&own.TitleSuffixes, &own.Title, &own.BookmarkTitles, &own.Folders,
		&own.Author); err != nil {
		return fmt.Errorf("read its fields: %w", err)
	}
	fields := own.Fields(agreed)
	if _, err := f.tx.ExecContext(ctx, setSiteSQL, fields.SiteName, fields.Site, id); err != nil {
		return fmt.Errorf("write its site: %w", err)
	}
	return f.writeCard(ctx, id, fields.Card())
}

// write stores document id's fields and its card.
func (f *fieldsTx) write(ctx context.Context, id string, fields docmeta.Fields) error {
	if _, err := f.tx.ExecContext(ctx, upsertFieldsSQL, id, f.tenantID, fields.Host, fields.DeclaredSite, fields.TitleSuffixes,
		fields.SiteName, fields.Title, fields.BookmarkTitles, fields.Folders, fields.Site, fields.Author,
		docmeta.RulesVersion); err != nil {
		return fmt.Errorf("write its fields: %w", err)
	}
	return f.writeCard(ctx, id, fields.Card())
}

// writeCard stores document id's card text, noting whether it changed.
func (f *fieldsTx) writeCard(ctx context.Context, id, text string) error {
	res, err := f.tx.ExecContext(ctx, upsertCardSQL, id, text)
	if err != nil {
		return fmt.Errorf("write its card: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("write its card: %w", err)
	}
	f.cardsChanged = f.cardsChanged || n > 0
	return nil
}

// queueCards queues the tenant's cards job inside tx, unless one is
// pending already (JobQueue.EnqueueOnce's rule), and reports whether it
// queued one.
func queueCards(ctx context.Context, tx *sql.Tx, tenantID string) (bool, error) {
	err := insertJobWith(ctx, tx, insertJobOnceSQL, &store.Job{TenantID: tenantID, Kind: store.JobKindCards})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("queue the cards job: %w", err)
	}
	return true, nil
}

// cardsWake is the job kinds a commit wakes when queued says a cards job
// was queued: the cards worker's, or none.
func cardsWake(queued bool, kinds ...store.JobKind) []store.JobKind {
	if queued {
		kinds = append(kinds, store.JobKindCards)
	}
	return kinds
}

// maxFieldsBatch caps the documents EnsureDocumentFields refreshes in one
// transaction, so the write lock is held briefly and a run cut short
// keeps what it wrote.
const maxFieldsBatch = 1000

// FieldsRefresh is what EnsureDocumentFields did.
type FieldsRefresh struct {
	// Documents is how many documents' fields it derived, on Hosts hosts.
	Documents, Hosts int
	// CardsStale is how many cards, across tenants, are left to embed.
	CardsStale int
	Took       time.Duration
}

// The statements of EnsureDocumentFields.
const (
	// unrefreshedSQL lists the documents whose fields are missing or were
	// derived by other rules than the arg: every document, with its
	// fields found by primary key.
	unrefreshedSQL = `SELECT d.tenant_id, d.id, d.url FROM documents d
	LEFT JOIN document_fields f ON f.document_id = d.id
	WHERE f.rules IS NOT ?`
	// staleCardsByTenantSQL counts each tenant's stale cards, walking
	// idx_document_cards_stale and each card's document by primary key:
	// the CROSS JOIN keeps SQLite from walking every document instead.
	staleCardsByTenantSQL = `SELECT d.tenant_id, count(*) FROM document_cards c
	CROSS JOIN documents d ON d.id = c.document_id
	WHERE c.embedded_text IS NOT c.text
	GROUP BY d.tenant_id`
)

// EnsureDocumentFields derives the fields and card of every document
// whose fields are missing or were derived by other rules than
// docmeta.RulesVersion, a host at a time: each transaction holds whole
// hosts, or up to maxFieldsBatch documents of a larger one, so a run cut
// short keeps what it committed and the next resumes. Then it queues a
// cards job for every tenant with stale cards. A run with nothing to
// derive writes nothing but those jobs, which queue only when none is
// pending. The daemon runs it at start, after EnsureVectorIndex: what
// applies a change of the rules to a home, and derives a new one's rows.
func EnsureDocumentFields(ctx context.Context, db *DB) (FieldsRefresh, error) {
	began := time.Now()
	batches, docs, hosts, err := unrefreshedBatches(ctx, db)
	if err != nil {
		return FieldsRefresh{}, err
	}
	for _, b := range batches {
		if err := ctx.Err(); err != nil {
			return FieldsRefresh{}, fmt.Errorf("derive document fields: %w", err)
		}
		if err := refreshBatch(ctx, db, b); err != nil {
			return FieldsRefresh{}, fmt.Errorf("derive document fields: %w", err)
		}
	}
	stale, err := queueStaleCards(ctx, db)
	if err != nil {
		return FieldsRefresh{}, err
	}
	return FieldsRefresh{Documents: docs, Hosts: hosts, CardsStale: stale, Took: time.Since(began)}, nil
}

// fieldsBatch is documents of one tenant EnsureDocumentFields refreshes in
// one transaction.
type fieldsBatch struct {
	tenantID string
	ids      []string
}

// unrefreshedBatches lists the documents whose fields need deriving,
// grouped into batches of whole hosts, a host's documents split only when
// they are more than maxFieldsBatch, and counts them and their hosts.
func unrefreshedBatches(ctx context.Context, db *DB) (batches []fieldsBatch, docs, hosts int, err error) {
	rows, err := db.QueryContext(ctx, unrefreshedSQL, docmeta.RulesVersion)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("list documents to derive fields of: %w", err)
	}
	defer rows.Close()
	type hostKey struct{ tenantID, host string }
	byHost := map[hostKey][]string{}
	for rows.Next() {
		var tenantID, id, url string
		if err := rows.Scan(&tenantID, &id, &url); err != nil {
			return nil, 0, 0, fmt.Errorf("list documents to derive fields of: %w", err)
		}
		k := hostKey{tenantID, docmeta.Host(url)}
		byHost[k] = append(byHost[k], id)
		docs++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, fmt.Errorf("list documents to derive fields of: %w", err)
	}
	keys := slices.SortedFunc(maps.Keys(byHost), func(a, b hostKey) int {
		return cmp.Or(strings.Compare(a.tenantID, b.tenantID), strings.Compare(a.host, b.host))
	})
	for _, k := range keys {
		ids := byHost[k]
		slices.Sort(ids)
		for len(ids) > 0 {
			last := len(batches) - 1
			if last < 0 || batches[last].tenantID != k.tenantID || len(batches[last].ids)+len(ids) > maxFieldsBatch {
				batches = append(batches, fieldsBatch{tenantID: k.tenantID})
				last++
			}
			n := min(len(ids), maxFieldsBatch-len(batches[last].ids))
			batches[last].ids = append(batches[last].ids, ids[:n]...)
			ids = ids[n:]
		}
	}
	return batches, docs, len(byHost), nil
}

// refreshBatch derives one batch's fields in a transaction of its own.
func refreshBatch(ctx context.Context, db *DB, b fieldsBatch) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	queued, err := refreshFields(ctx, tx, b.tenantID, b.ids...)
	if err != nil {
		return err
	}
	if err := db.commitNotify(tx, cardsWake(queued)...); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// queueStaleCards queues a cards job for every tenant with stale cards,
// and counts them.
func queueStaleCards(ctx context.Context, db *DB) (int, error) {
	rows, err := db.QueryContext(ctx, staleCardsByTenantSQL)
	if err != nil {
		return 0, fmt.Errorf("count stale cards: %w", err)
	}
	defer rows.Close()
	stale := map[string]int{}
	for rows.Next() {
		var tenantID string
		var n int
		if err := rows.Scan(&tenantID, &n); err != nil {
			return 0, fmt.Errorf("count stale cards: %w", err)
		}
		stale[tenantID] = n
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("count stale cards: %w", err)
	}
	total := 0
	for _, tenantID := range slices.Sorted(maps.Keys(stale)) {
		total += stale[tenantID]
		if err := queueCardsJob(ctx, db, tenantID); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// queueCardsJob queues the tenant's cards job in a transaction of its
// own, unless one is pending.
func queueCardsJob(ctx context.Context, db *DB, tenantID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("queue the cards job: begin: %w", err)
	}
	defer tx.Rollback()
	queued, err := queueCards(ctx, tx, tenantID)
	if err != nil {
		return err
	}
	if err := db.commitNotify(tx, cardsWake(queued)...); err != nil {
		return fmt.Errorf("queue the cards job: commit: %w", err)
	}
	return nil
}
