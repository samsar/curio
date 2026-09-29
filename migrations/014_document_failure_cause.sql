-- +goose Up
-- +goose StatementBegin

-- Why a failed or dead document failed: one of store.FailureCauses
-- (dead_link, anti_bot, login_wall, ...). The permanent-failure hook writes
-- it from the error the job gave up with (fetcher.FailureCause; index for
-- an index job), and every write that moves a document on clears it: a
-- refetch, a successful fetch, a successful index. It is set exactly when
-- the document is failed or dead, and a dead document's is dead_link; the
-- store, the only writer, keeps that (internal/store/sqlite/documents.go).
--
-- No CHECK constraint, unlike the other enums: causes will grow, and
-- changing a CHECK means rebuilding documents, the table most others
-- reference (README.md). The store checks the value on every write.
--
-- Adding a column and an index rebuilds nothing, so this runs inside
-- goose's transaction.

ALTER TABLE documents ADD COLUMN failure_cause TEXT;

-- The documents that failed before this migration take their cause from
-- the error of their most recent failed job, read by rules frozen for the
-- wording errors had until now. From here on the fetcher classifies the
-- error before it is text, so these rules never need to follow a new
-- wording. They mirror fetcher.FailureCause:
--
--   - A dead document is a dead link, whatever its error says.
--   - No failed job (pruned, say), or one without an error: other.
--   - An index job's failure: index.
--   - Otherwise e is the error less the worker's prefix, "permanent
--     failure: " (19 characters) or "fetch failed: " (14). A Jina-led e,
--     "jina: … (after native: …)", speaks for the target when Jina gave a
--     verdict about it: a refusal, an answer that isn't the page, a status
--     the target gave it, a body over the cap. Any other Jina failure is
--     Jina's own trouble, and o, the origin's part after " (after "
--     (8 characters), decides. For every other e, o is e.
--   - o is read by its shape: a host-cache hit by the verdict it cached,
--     then the native, youtube, github and dispatcher errors, then
--     timeouts, then any other transport failure (network).
--
-- updated_at is left alone, unlike in every UPDATE the store runs: the
-- backfill records failures that already happened, and bumping the column
-- would reorder the Library, newest first, around the migration. On the
-- author's library (7,467 documents, 2,969 failed or dead) the migration
-- took about 160 ms.

UPDATE documents SET failure_cause = c.cause
FROM (
    SELECT id,
        CASE
            WHEN state = 'dead' THEN 'dead_link'
            WHEN kind IS NULL OR e IS NULL THEN 'other'
            WHEN kind = 'index' THEN 'index'

            WHEN e LIKE 'jina: refused the target: %' THEN 'jina_refused'
            WHEN e LIKE 'jina: answer is not the page: login wall%' THEN 'login_wall'
            WHEN e LIKE 'jina: answer is not the page: origin blocked the request%'
              OR e LIKE 'jina: answer is not the page: target answered HTTP 403 %'
              OR e LIKE 'jina: answer is not the page: target answered HTTP 503 %' THEN 'anti_bot'
            WHEN e LIKE 'jina: answer is not the page: target answered HTTP %' THEN 'http_error'
            WHEN e LIKE 'jina: target failed for now: target answered HTTP 429 %' THEN 'rate_limited'
            WHEN e LIKE 'jina: target failed for now: %' THEN 'http_error'
            WHEN e LIKE 'jina: read body: response too large%' THEN 'too_large'
            WHEN o IS NULL THEN 'other'

            WHEN o LIKE 'native: origin blocked the request (likely anti-bot) (cached: %' THEN 'anti_bot'
            WHEN o LIKE 'native: host unreachable (cached: %' THEN 'unreachable'
            WHEN o LIKE 'native: login wall or thin content (cached: %' THEN 'login_wall'

            WHEN o LIKE 'native: fetch: invalid TLS certificate%' THEN 'tls'
            WHEN o LIKE 'native: fetch: host unreachable%' THEN 'unreachable'
            WHEN o LIKE '%response too large (limit %' THEN 'too_large'
            WHEN o LIKE 'native: HTTP 403 Forbidden: offsite login wall%'
              OR o LIKE 'native: HTTP 503 %: offsite login wall%'
              OR o LIKE 'native: offsite login wall%'
              OR o LIKE 'native: site-wide login wall%'
              OR o LIKE 'native: login wall or thin content%' THEN 'login_wall'
            WHEN o LIKE 'native: HTTP 403 Forbidden: origin blocked the request%'
              OR o LIKE 'native: HTTP 503 %: origin blocked the request%'
              OR o LIKE 'native: origin blocked the request%' THEN 'anti_bot'
            WHEN o LIKE 'native: HTTP 429%' THEN 'rate_limited'
            WHEN o LIKE 'native: HTTP %' OR o LIKE 'native: server error page%' THEN 'http_error'
            WHEN o LIKE 'native: unsupported content type%'
              OR o LIKE 'native: pdf not extractable locally%' THEN 'unsupported'

            WHEN o LIKE 'youtube: cannot extract video ID%'
              OR o LIKE 'github: not a recognized GitHub URL%'
              OR o LIKE 'github: unsupported URL type%'
              OR o LIKE 'no fetcher for %' THEN 'unsupported'
            WHEN o LIKE 'youtube: not run, rate-limit cooldown%'
              OR o LIKE 'youtube: %(rate limited, yt-dlp runs paused%'
              OR o LIKE 'github: %rate-limit cooldown%'
              OR o LIKE 'github: %: rate limited: HTTP %' THEN 'rate_limited'
            WHEN o LIKE 'github: %: HTTP %' OR o LIKE 'github: wiki page %' THEN 'http_error'

            WHEN o LIKE '%Client.Timeout exceeded%' OR o LIKE '%i/o timeout%'
              OR o LIKE '%context deadline exceeded%' OR o LIKE '%TLS handshake timeout%'
              OR o LIKE '%: timed out after %' THEN 'timeout'
            WHEN o LIKE 'native: fetch: %' OR o LIKE 'github: Get %' THEN 'network'
            ELSE 'other'
        END AS cause
    FROM (
        SELECT id, state, kind, e,
            CASE
                WHEN e NOT LIKE 'jina: %' THEN e
                WHEN instr(e, ' (after native: ') > 0 THEN substr(e, instr(e, ' (after native: ') + 8)
            END AS o
        FROM (
            SELECT d.id, d.state, j.kind,
                CASE
                    WHEN j.last_error LIKE 'permanent failure: %' THEN substr(j.last_error, 20)
                    WHEN j.last_error LIKE 'fetch failed: %' THEN substr(j.last_error, 15)
                    ELSE j.last_error
                END AS e
            FROM documents d
            LEFT JOIN jobs j ON j.id = (
                SELECT id FROM jobs
                WHERE document_id = d.id AND status = 'failed'
                ORDER BY updated_at DESC, id DESC
                LIMIT 1)
            WHERE d.state IN ('failed', 'dead')
        )
    )
) c
WHERE c.id = documents.id;

-- The cause filters read it: a page of one cause and the pages after it,
-- the refetch of one cause, the failure summary. Partial, so the documents
-- that never failed, most of a library, cost it nothing.
CREATE INDEX idx_documents_tenant_cause_updated ON documents(tenant_id, failure_cause, updated_at, id)
    WHERE failure_cause IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX idx_documents_tenant_cause_updated;
ALTER TABLE documents DROP COLUMN failure_cause;

-- +goose StatementEnd
