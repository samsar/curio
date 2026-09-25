# API

`openapi.yaml` is the source of truth for the daemon's HTTP+JSON API.

All clients — the `curio` CLI, the `curio-mcp` sidecar, and any future web
UI — codegen their request/response types from this spec.

## Conventions

- **IDs** are UUIDs (string, `format: uuid`).
- **Timestamps** are RFC 3339, UTC.
- **Errors** follow RFC 7807 with `application/problem+json`. Every
  response carries an `X-Request-Id` header; a problem repeats it as
  `request_id` and names the request path in `instance`. The daemon logs
  every 5xx with its request ID, so `curio daemon logs` finds the cause.
  A 404 names what is missing (`document "…" not found`).
- **Pagination** on list endpoints is cursor-based, not offset:
  responses include `next_cursor` (opaque string) and an approximate `total`.
  Clients pass `?cursor=<value>` to fetch the next page.
- **Async work** responds `202 Accepted`: `{ job_id }` for single-target
  operations (refetch, reindex, interests rebuild), `{ jobs_enqueued }` for
  the bulk `refetch-all` and `reindex-all`, which have no parent job.
  Clients watch `GET /v1/jobs` for progress.
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

Recommended generators when we get there:

- Go server stubs + types: `oapi-codegen` (Hugo's, MIT licensed, clean output)
- TypeScript client (for any future web UI): `openapi-typescript`

Codegen is deferred until M0 — the spec is the contract, hand-written handlers
are fine to start.

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
