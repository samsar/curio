package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
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
// same transaction queues the cards job that embeds it, CardsJobDelay
// ahead.
//
// A document's display site name is agreed across its host: its own
// declared name, else the name its host's documents agree on
// (docmeta.AgreedNameOf). Each row keeps what it contributes to that
// agreement, and triggers count the contributions of a host's rows in
// site_name_votes (migration 019), so agreeing reads the host's two
// leaders, one indexed seek each, whatever the host's size. Once the
// refreshed rows are written, every host they are on or left is agreed
// again, and its documents without a declared name that hold another name
// are renamed: a read of two ranges of idx_document_fields_site that finds
// none unless the agreement changed. A write so costs the same on a host of
// ten documents as on one of ten thousand; only a change of the name
// agreed rewrites the host's siblings, each one's site and card.

// The statements of refreshFields, which runs them in its caller's
// transaction, all but the rare rename prepared once per call
// (newFieldsTx).
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
	// leaderSQL reads the name of one kind (voteDeclared, voteSuffix) the
	// most of a host's documents hold, ties in byte order, and how many
	// hold it: the first row of idx_site_name_votes_lead. Its args are the
	// tenant, the host and the kind.
	leaderSQL = `SELECT name, votes FROM site_name_votes WHERE tenant_id = ? AND host = ? AND kind = ?
	ORDER BY votes DESC, name LIMIT 1`
	// misnamedSQL reads the fields a card is made from of a host's
	// documents without a declared name that hold another name than ?3,
	// the one the host agrees on. The inequality is written as two ranges,
	// each a seek of idx_document_fields_site, and they hold no row unless
	// the agreement changed since the host's rows were written. Its other
	// args are the tenant and the host.
	misnamedSQL = `SELECT document_id, title, bookmark_titles, folders, author FROM document_fields
	WHERE tenant_id = ?1 AND host = ?2 AND declared_site = '' AND (site_name < ?3 OR site_name > ?3)`
	// renameSQL gives the documents misnamedSQL reads the name ?3 and the
	// site it is searched by, ?4. It walks the host's documents without a
	// declared name, and runs only when misnamedSQL found some, so it is
	// never prepared ahead.
	renameSQL = `UPDATE document_fields SET site_name = ?3, site = ?4
	WHERE tenant_id = ?1 AND host = ?2 AND declared_site = '' AND (site_name < ?3 OR site_name > ?3)`
	// upsertFieldsSQL writes a document's fields, and writes nothing when
	// they are what is stored: no row change, no trigger fired.
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
	// upsertCardSQL writes a document's card text, and writes nothing when
	// it is what is stored, so an unchanged card stays embedded.
	upsertCardSQL = `
	INSERT INTO document_cards (document_id, text) VALUES (?, ?)
	ON CONFLICT (document_id) DO UPDATE SET text = excluded.text
	WHERE text IS NOT excluded.text`
)

// The kinds of site_name_votes: a host's declared names and its title
// suffixes.
const (
	voteDeclared = "declared"
	voteSuffix   = "suffix"
)

// fieldsTx refreshes fields in one transaction.
type fieldsTx struct {
	tx       *sql.Tx
	tenantID string
	// The statements a refresh runs for every document and every host,
	// prepared once per refresh (newFieldsTx). Preparing a statement that
	// writes document_fields compiles its triggers, and the driver caches
	// no statement, so preparing them once a document doubled the startup
	// refresh of thousands of documents.
	inputs, bookmarks, leader, misnamed, upsertFields, upsertCard *sql.Stmt
	// cardsChanged is set once a card's text changed.
	cardsChanged bool
}

// newFieldsTx prepares a refresh of the tenant's fields inside tx. close
// releases its statements.
func newFieldsTx(ctx context.Context, tx *sql.Tx, tenantID string) (*fieldsTx, error) {
	var err error
	prepare := func(query string) *sql.Stmt {
		if err != nil {
			return nil
		}
		var st *sql.Stmt
		st, err = tx.PrepareContext(ctx, query)
		return st
	}
	f := &fieldsTx{tx: tx, tenantID: tenantID}
	f.inputs = prepare(fieldsInputsSQL)
	f.bookmarks = prepare(fieldsBookmarksSQL)
	f.leader = prepare(leaderSQL)
	f.misnamed = prepare(misnamedSQL)
	f.upsertFields = prepare(upsertFieldsSQL)
	f.upsertCard = prepare(upsertCardSQL)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("prepare the refresh's statements: %w", err), f.close())
	}
	return f, nil
}

// close closes the statements prepared.
func (f *fieldsTx) close() error {
	var errs []error
	for _, st := range []*sql.Stmt{f.inputs, f.bookmarks, f.leader, f.misnamed, f.upsertFields, f.upsertCard} {
		if st != nil {
			errs = append(errs, st.Close())
		}
	}
	return errors.Join(errs...)
}

// derivedDoc is a document refreshFields derived.
type derivedDoc struct {
	id  string
	own docmeta.Own
}

// refreshFields derives the fields and cards of the tenant's documents ids
// again from what they are derived from now, inside tx, the transaction of
// the write that changed them. Each document is written under the name its
// host agrees on; then every host a document is on or has left is agreed
// again, and its documents that take the agreed name are renamed when it
// changed. A document that no longer exists is skipped: its rows went with
// it. When any card's text changed, it queues the tenant's cards job
// (queueCards).
func (d *DB) refreshFields(ctx context.Context, tx *sql.Tx, tenantID string, ids ...string) (err error) {
	f, err := newFieldsTx(ctx, tx, tenantID)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.close()) }()

	on, hosts, err := f.deriveAll(ctx, ids)
	if err != nil {
		return err
	}
	for _, host := range hosts {
		if err := f.writeAll(ctx, host, on[host]); err != nil {
			return fmt.Errorf("write the fields of host %q's documents: %w", host, err)
		}
	}
	for _, host := range hosts {
		if err := f.rename(ctx, host); err != nil {
			return fmt.Errorf("give host %q's documents the name it agrees on: %w", host, err)
		}
	}
	if !f.cardsChanged {
		return nil
	}
	return d.queueCards(ctx, tx, tenantID)
}

// deriveAll derives what each of the documents ids says of itself, once
// each. on holds them by the host they are on; hosts are every host the
// refresh changes, those they are on and those whose stored fields they
// leave, sorted.
func (f *fieldsTx) deriveAll(ctx context.Context, ids []string) (on map[string][]derivedDoc, hosts []string,
	err error) {
	on = map[string][]derivedDoc{}
	changed := map[string]bool{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		own, oldHost, ok, err := f.derive(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("derive the fields of document %s: %w", id, err)
		}
		if !ok {
			continue
		}
		on[own.Host] = append(on[own.Host], derivedDoc{id: id, own: own})
		changed[own.Host] = true
		if oldHost.Valid {
			changed[oldHost.String] = true
		}
	}
	return on, slices.Sorted(maps.Keys(changed)), nil
}

// derive reads document id's inputs and derives what it says of itself.
// oldHost is the host its stored fields name, when it has any; ok is
// false when the tenant has no such document.
func (f *fieldsTx) derive(ctx context.Context, id string) (own docmeta.Own, oldHost sql.NullString, ok bool, err error) {
	var d docmeta.Document
	err = f.inputs.QueryRowContext(ctx, "$."+store.ExtractionMetaSiteName, id, f.tenantID).
		Scan(&d.URL, &d.Title, &d.Author, &d.SiteName, &oldHost)
	if errors.Is(err, sql.ErrNoRows) {
		return docmeta.Own{}, sql.NullString{}, false, nil
	}
	if err != nil {
		return docmeta.Own{}, sql.NullString{}, false, fmt.Errorf("read the document: %w", err)
	}
	if d.Bookmarks, err = f.readBookmarks(ctx, id); err != nil {
		return docmeta.Own{}, sql.NullString{}, false, err
	}
	return docmeta.Derive(d), oldHost, true, nil
}

// readBookmarks reads document id's bookmarks, oldest saved first.
func (f *fieldsTx) readBookmarks(ctx context.Context, id string) ([]docmeta.Bookmark, error) {
	rows, err := f.bookmarks.QueryContext(ctx, id)
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

// writeAll writes the fields and cards of docs, the refreshed documents on
// host, under the name the host agrees on before they are written. Their
// own votes may move it; rename then corrects them.
func (f *fieldsTx) writeAll(ctx context.Context, host string, docs []derivedDoc) error {
	if len(docs) == 0 {
		return nil
	}
	agreed, err := f.agreed(ctx, host)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := f.write(ctx, d.id, d.own.Fields(agreed)); err != nil {
			return fmt.Errorf("document %s: %w", d.id, err)
		}
	}
	return nil
}

// agreed is the name host agrees on now, from its leaders.
func (f *fieldsTx) agreed(ctx context.Context, host string) (string, error) {
	declared, err := f.readLeader(ctx, host, voteDeclared)
	if err != nil {
		return "", err
	}
	suffix, err := f.readLeader(ctx, host, voteSuffix)
	if err != nil {
		return "", err
	}
	return docmeta.AgreedNameOf(host, declared, suffix), nil
}

// readLeader reads host's leading name of kind, the zero Leader when none
// of its documents holds one.
func (f *fieldsTx) readLeader(ctx context.Context, host, kind string) (docmeta.Leader, error) {
	var l docmeta.Leader
	err := f.leader.QueryRowContext(ctx, f.tenantID, host, kind).Scan(&l.Name, &l.Votes)
	if errors.Is(err, sql.ErrNoRows) {
		return docmeta.Leader{}, nil
	}
	if err != nil {
		return docmeta.Leader{}, fmt.Errorf("read the leading %s name: %w", kind, err)
	}
	return l, nil
}

// rename gives host's documents without a declared name of their own the
// name the host agrees on now, when any holds another: their site name and
// site in one statement, then each one's card. A host whose agreement
// didn't change has none, and costs the read.
func (f *fieldsTx) rename(ctx context.Context, host string) error {
	agreed, err := f.agreed(ctx, host)
	if err != nil {
		return err
	}
	misnamed, err := f.readMisnamed(ctx, host, agreed)
	if err != nil {
		return err
	}
	if len(misnamed) == 0 {
		return nil
	}
	// Every document of the host without a declared name is searched by
	// the same site: the agreed name and the host's words.
	site := docmeta.Own{Host: host}.Fields(agreed).Site
	if _, err := f.tx.ExecContext(ctx, renameSQL, f.tenantID, host, agreed, site); err != nil {
		return fmt.Errorf("write their site: %w", err)
	}
	for _, d := range misnamed {
		if err := f.writeCard(ctx, d.id, d.own.Fields(agreed).Card()); err != nil {
			return fmt.Errorf("document %s: %w", d.id, err)
		}
	}
	return nil
}

// readMisnamed reads what the cards of host's documents that take its
// agreed name and hold another than agreed are made from.
func (f *fieldsTx) readMisnamed(ctx context.Context, host, agreed string) ([]derivedDoc, error) {
	rows, err := f.misnamed.QueryContext(ctx, f.tenantID, host, agreed)
	if err != nil {
		return nil, fmt.Errorf("read the documents to rename: %w", err)
	}
	defer rows.Close()
	var out []derivedDoc
	for rows.Next() {
		d := derivedDoc{own: docmeta.Own{Host: host}}
		if err := rows.Scan(&d.id, &d.own.Title, &d.own.BookmarkTitles, &d.own.Folders, &d.own.Author); err != nil {
			return nil, fmt.Errorf("read the documents to rename: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the documents to rename: %w", err)
	}
	return out, nil
}

// write stores document id's fields and its card.
func (f *fieldsTx) write(ctx context.Context, id string, fields docmeta.Fields) error {
	suffixes, err := suffixesJSON(fields.TitleSuffixes)
	if err != nil {
		return err
	}
	if _, err := f.upsertFields.ExecContext(ctx, id, f.tenantID, fields.Host, fields.DeclaredSite, suffixes,
		fields.SiteName, fields.Title, fields.BookmarkTitles, fields.Folders, fields.Site, fields.Author,
		docmeta.RulesVersion); err != nil {
		return fmt.Errorf("write its fields: %w", err)
	}
	return f.writeCard(ctx, id, fields.Card())
}

// suffixesJSON is list, title suffixes one a line as docmeta.Own holds
// them, as the JSON array document_fields keeps, which the vote triggers
// split.
func suffixesJSON(list string) (string, error) {
	suffixes := []string{}
	if list != "" {
		suffixes = strings.Split(list, "\n")
	}
	b, err := json.Marshal(suffixes)
	if err != nil {
		return "", fmt.Errorf("encode its title suffixes: %w", err)
	}
	return string(b), nil
}

// writeCard stores document id's card text, noting whether it changed.
func (f *fieldsTx) writeCard(ctx context.Context, id, text string) error {
	res, err := f.upsertCard.ExecContext(ctx, id, text)
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

// CardsJobDelay is how far ahead a write that changed a card queues the
// cards job. It is longer than the gaps between the card-changing writes
// of a burst (an import's ingests, then its fetches seconds apart), so a
// burst shares one job, where a job run at once would embed a card or two
// and leave a row in jobs for every fetch. And it is short against what
// waits on it: the fields and title lists find a page saved a moment ago,
// in the transaction that saved it, so only the cards list waits for its
// vector.
const CardsJobDelay = 10 * time.Second

// queueCards queues the tenant's cards job inside tx, run_after the DB's
// cards window ahead, unless one is pending already (JobQueue.EnqueueOnce's
// rule): every card change inside the window finds that job pending and
// shares it. It wakes no worker, since the job isn't claimable yet; the
// cards pool's idle poll finds it once it is due, as it finds a deferred
// job.
func (d *DB) queueCards(ctx context.Context, tx *sql.Tx, tenantID string) error {
	job := &store.Job{TenantID: tenantID, Kind: store.JobKindCards, RunAfter: time.Now().UTC().Add(d.cardsJobDelay)}
	err := insertJobWith(ctx, tx, insertJobOnceSQL, job)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("queue the cards job: %w", err)
	}
	return nil
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
	if err := db.refreshFields(ctx, tx, b.tenantID, b.ids...); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
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
	if err := db.queueCards(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("queue the cards job: commit: %w", err)
	}
	return nil
}
