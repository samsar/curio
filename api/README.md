# API

`openapi.yaml` is the contract for the daemon's HTTP+JSON API, and tests
hold the daemon to it. `internal/api/openapi_test.go` checks that:

- the spec is valid OpenAPI 3.1, with none of 3.0's `nullable`;
- the router serves exactly the operations the spec documents;
- the request bodies the handlers decode have exactly the documented
  fields;
- every operation's real responses, over seeded fixtures, have a
  documented status and content type and validate against their schemas
  (JSON Schema 2020-12), with undeclared fields treated as errors.

A route, field or status added without the spec fails those tests.

The clients are written by hand: `internal/client` for the CLI and the
`curio-mcp` sidecar. Code generation is deferred (see "Codegen").

## Conventions

- **IDs** are UUIDs (string, `format: uuid`).
- **Timestamps** are RFC 3339, UTC.
- **Errors** follow RFC 7807 with `application/problem+json`. Every
  response carries an `X-Request-Id` header; a problem repeats it as
  `request_id` and names the request path in `instance`. The daemon logs
  every 5xx with its request ID, so `curio daemon logs` finds the cause.
  A 404 names what is missing (`document "…" not found`).
- **Pagination** on `GET /v1/bookmarks`, `/v1/documents` and `/v1/jobs` is
  cursor-based, not offset. A response carries `next_cursor` exactly when
  another page follows; pass it back as `?cursor=` for that page. Documents
  and jobs come most recently updated first, bookmarks newest first. Pages
  never overlap and rows inserted during a walk don't shift it; a row
  updated mid-walk moves ahead of the cursor and is not revisited. Cursors
  are opaque and may be invalidated by a daemon upgrade: an invalid one is
  a 400, and the client starts the walk again. There is no `total`.
- **Startup**: the daemon answers from the moment it binds its port. Until
  it is ready (while it migrates its database, say), every request gets
  `503` with a `Retry-After` header and a problem of type
  `urn:curio:problem:daemon-starting`; on `/v1/healthz` that problem also
  carries `pid`, `home` and `version`, so a client knows the daemon is its
  own, and `phase` (`initializing` or `migrating`) with `migrations:
  { applied, total }` while it migrates. Nothing a starting daemon refused
  has run, so clients send it again once healthz answers `200`. See
  [`../docs/decisions.md`](../docs/decisions.md) "Daemon startup: a
  starting API while migrating, clients that wait on progress".
- **Async work** responds `202 Accepted`: `{ job_id }` for single-target
  operations (refetch, reindex, interests rebuild), `{ jobs_enqueued }` for
  the bulk `refetch-all` and `reindex-all`, which have no parent job.
  Clients poll `GET /v1/jobs/{id}` for a job's progress; the bulk
  operations are watched through `GET /v1/jobs` or `GET /v1/stats`.
  Imports are synchronous per request (`200` with what the batch did); the
  fetches they enqueue are jobs like any other.
- **Auth**: there is none, and no token. The daemon binds loopback only
  and trusts local processes. It refuses browser-originated requests:
  `Host` must be a loopback name on the daemon's port (403 otherwise, which
  stops DNS rebinding), any `Origin` other than the daemon's own gets 403,
  and request bodies must be `application/json` (415 otherwise) and at most
  1 MiB, or 32 MiB for imports (413 otherwise). Body-less POSTs need no
  Content-Type. Hosted-mode auth is deferred; see
  [`../docs/decisions.md`](../docs/decisions.md) "Local API: loopback only,
  no token, browsers shut out".

## Why HTTP+JSON and not gRPC

See [`../docs/decisions.md`](../docs/decisions.md#transport-http--json).

## Codegen

Deferred. The handlers and `internal/client` are hand-written, and the
contract tests above keep them and the spec from drifting apart. When a
generator earns its place, the candidates are:

- Go server stubs + types: `oapi-codegen` (MIT licensed, clean output)
- TypeScript client (for any future web UI): `openapi-typescript`

## Versioning

The path prefix `/v1` is the major version. Breaking changes bump to `/v2`
and run side-by-side until clients migrate. Additive changes (new fields, new
endpoints, new enum values) don't bump the version.

- **Responses are read tolerantly.** Clients ignore fields and enum values
  they don't know; the server may add them within `/v1`. Unset optional
  fields are omitted, never `null`.
- **Requests are strict.** The server rejects an unknown field with 400,
  because a field it ignored would be a filter or knob silently not applied.
  Clients send only the optional fields they set, so an older daemon rejects
  only a request that uses a feature it lacks. That 400 names the field and
  the daemon's version and says to restart the daemon: after an upgrade the
  old one may still be running (`curio daemon stop`; the next command starts
  the new one).
- **500 details keep the raw error text.** The clients are the local
  operator's own tools (see "Local API" in the decisions log), and every
  problem carries `request_id`.
