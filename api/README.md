# API

`openapi.yaml` is the source of truth for the daemon's HTTP+JSON API.

All clients — the `curio` CLI, the `curio-mcp` sidecar, and any future web
UI — codegen their request/response types from this spec.

## Conventions

- **IDs** are UUIDs (string, `format: uuid`).
- **Timestamps** are RFC 3339, UTC.
- **Errors** follow RFC 7807 with `application/problem+json`.
- **Pagination** on list endpoints is cursor-based, not offset:
  responses include `next_cursor` (opaque string) and an approximate `total`.
  Clients pass `?cursor=<value>` to fetch the next page.
- **Async work** (import, refetch, reindex) responds `202 Accepted` with a
  `{ job_id }` body. Clients poll `/v1/jobs/{id}` for status.
- **Auth** is a no-op middleware stub in v1. Hosted mode swaps it in.

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
endpoints, new enum values) don't bump the version — clients are expected to
ignore unknown fields.
