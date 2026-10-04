package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// These tests hold the router and the handlers to api/openapi.yaml: the same
// routes, request bodies with the same fields, and responses that validate
// against their documented schemas.

// specPath is the contract, relative to this package.
const specPath = "../../api/openapi.yaml"

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(specPath)
	require.NoError(t, err)
	return doc
}

// specOperations maps "METHOD /path" to each operation the spec documents.
func specOperations(doc *openapi3.T) map[string]*openapi3.Operation {
	ops := map[string]*openapi3.Operation{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			ops[method+" "+path] = op
		}
	}
	return ops
}

// walkSchemas calls fn for every schema reference reachable from doc: the
// component schemas and those of every parameter, request body and
// response, and everything nested in them. A schema reached through several
// references is walked into once.
func walkSchemas(doc *openapi3.T, fn func(*openapi3.SchemaRef)) {
	walked := map[*openapi3.Schema]bool{}
	var visit func(*openapi3.SchemaRef)
	visit = func(ref *openapi3.SchemaRef) {
		if ref == nil || ref.Value == nil {
			return
		}
		fn(ref)
		s := ref.Value
		if walked[s] {
			return
		}
		walked[s] = true
		for _, p := range s.Properties {
			visit(p)
		}
		for _, refs := range []openapi3.SchemaRefs{s.AllOf, s.AnyOf, s.OneOf} {
			for _, r := range refs {
				visit(r)
			}
		}
		visit(s.Items)
		visit(s.Not)
		visit(s.AdditionalProperties.Schema)
	}
	visitContent := func(content openapi3.Content) {
		for _, mt := range content {
			visit(mt.Schema)
		}
	}

	for _, ref := range doc.Components.Schemas {
		visit(ref)
	}
	for _, item := range doc.Paths.Map() {
		params := slices.Clone(item.Parameters)
		for _, op := range item.Operations() {
			params = append(params, op.Parameters...)
			if op.RequestBody != nil {
				visitContent(op.RequestBody.Value.Content)
			}
			for _, resp := range op.Responses.Map() {
				visitContent(resp.Value.Content)
			}
		}
		for _, p := range params {
			visit(p.Value.Schema)
		}
	}
}

// strictSpec loads the spec for validating responses. Every object schema
// that declares properties and says nothing of others refuses undeclared
// ones, so a field the server sends and the spec omits fails. Only the test
// is strict: the spec leaves objects open, since clients ignore unknown
// fields. Every $ref is resolved in place too: the JSON Schema 2020-12
// validator compiles the schema's JSON on its own, and one that still
// refers to #/components would fail to compile and quietly fall back to the
// OpenAPI 3.0 validator.
func strictSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc := loadSpec(t)
	closed := false
	walkSchemas(doc, func(ref *openapi3.SchemaRef) {
		ref.Ref = ""
		s := ref.Value
		if len(s.Properties) > 0 && s.AdditionalProperties.Has == nil && s.AdditionalProperties.Schema == nil {
			s.AdditionalProperties.Has = &closed
		}
	})
	return doc
}

// validationOptions validate as JSON Schema 2020-12, which OpenAPI 3.1 uses,
// and enforce format: uuid, which kin-openapi leaves unchecked by default.
var validationOptions = []openapi3.SchemaValidationOption{
	openapi3.EnableJSONSchema2020(),
	openapi3.VisitAsResponse(),
	openapi3.MultiErrors(),
	openapi3.WithStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC4122)),
}

// validateJSON checks that body is JSON schema accepts.
func validateJSON(t *testing.T, schema *openapi3.Schema, body, what string) {
	t.Helper()
	encoded, err := json.Marshal(schema)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"$ref"`, "%s: the schema must be self-contained", what)
	var v any
	require.NoError(t, json.Unmarshal([]byte(body), &v), "%s: %s", what, body)
	assert.NoError(t, schema.VisitJSON(v, validationOptions...), "%s: %s", what, body)
}

func TestOpenAPI_Valid(t *testing.T) {
	doc := loadSpec(t)
	require.NoError(t, doc.Validate(context.Background()))
	walkSchemas(doc, func(ref *openapi3.SchemaRef) {
		// nullable is OpenAPI 3.0; a 3.1 validator ignores it, and the API
		// omits unset fields rather than sending null.
		assert.False(t, ref.Value.Nullable, "a schema uses nullable: %s %s", ref.Ref, ref.Value.Description)
	})
	assert.Contains(t, doc.Info.Description, "the API never sends `null`")
}

// TestOpenAPI_FailureCauseEnum: the spec names every failure cause the
// store has, in its order, so a new cause can't reach the wire unnoticed.
func TestOpenAPI_FailureCauseEnum(t *testing.T) {
	causes := store.FailureCauses()
	want := make([]any, len(causes))
	for i, c := range causes {
		want[i] = string(c)
	}
	assert.Equal(t, want, loadSpec(t).Components.Schemas["FailureCause"].Value.Enum)
}

// TestOpenAPI_StrictValidation: the validation the contract tests rely on
// catches what it must, so a green run means something.
func TestOpenAPI_StrictValidation(t *testing.T) {
	schemas := strictSpec(t).Components.Schemas
	jobRef := schemas["JobRef"].Value
	id := uuid.NewString()

	validateJSON(t, jobRef, `{"job_id":"`+id+`"}`, "JobRef")
	for name, body := range map[string]string{
		"an undeclared field": `{"job_id":"` + id + `","tenant_id":"local"}`,
		"a non-UUID ID":       `{"job_id":"job-1"}`,
		"null":                `{"job_id":null}`,
		"a missing field":     `{}`,
	} {
		var v any
		require.NoError(t, json.Unmarshal([]byte(body), &v))
		assert.Error(t, jobRef.VisitJSON(v, validationOptions...), name)
	}
	var v any
	require.NoError(t, json.Unmarshal([]byte(`{"documents_by_state":{"fetched":2},"version":"v","bookmarks_total":0,"documents_total":2}`), &v))
	assert.NoError(t, schemas["Stats"].Value.VisitJSON(v, validationOptions...), "declared maps stay open")
}

// TestOpenAPI_RoutesMatchRouter: every /v1 route is documented and every
// documented operation is routed. The dashboard's pages are outside the
// contract; TestDashboard_Routes pins them.
func TestOpenAPI_RoutesMatchRouter(t *testing.T) {
	router, err := newRouter(Deps{Log: slog.New(slog.DiscardHandler), TenantID: "local"}, testOrigin(t),
		testDashboard(t, pagesOn))
	require.NoError(t, err)

	var routed []string
	require.NoError(t, chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/v1/") {
			// A subrouter's root is reported with a trailing slash, and also
			// answers without one.
			routed = append(routed, method+" "+strings.TrimSuffix(route, "/"))
		}
		return nil
	}))
	slices.Sort(routed)
	assert.Equal(t, slices.Sorted(maps.Keys(specOperations(loadSpec(t)))), routed)
}

// TestOpenAPI_RequestTypesMatchSchemas: the fields the handlers decode are
// the fields the spec documents, so the strict decoder rejects nothing the
// spec offers and accepts nothing it doesn't.
func TestOpenAPI_RequestTypesMatchSchemas(t *testing.T) {
	ops := specOperations(loadSpec(t))
	for _, tc := range []struct {
		op  string
		typ reflect.Type
	}{
		{"POST /v1/bookmarks", reflect.TypeFor[CreateBookmarkRequest]()},
		{"POST /v1/bookmarks/import", reflect.TypeFor[ImportRequest]()},
		{"POST /v1/search", reflect.TypeFor[SearchRequest]()},
		{"PUT /v1/queue", reflect.TypeFor[QueueUpdateRequest]()},
	} {
		t.Run(tc.op, func(t *testing.T) {
			op := ops[tc.op]
			require.NotNil(t, op)
			body := op.RequestBody.Value.Content.Get("application/json")
			require.NotNil(t, body)
			assertFieldsMatch(t, tc.op, tc.typ, body.Schema.Value)
		})
	}
}

// assertFieldsMatch compares the JSON fields of struct type typ with
// schema's properties, and recurses into fields that are structs or slices
// of them.
func assertFieldsMatch(t *testing.T, path string, typ reflect.Type, schema *openapi3.Schema) {
	t.Helper()
	fields := map[string]reflect.Type{}
	for f := range typ.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.IsExported() && name != "-" {
			fields[name] = f.Type
		}
	}
	assert.Equal(t, slices.Sorted(maps.Keys(schema.Properties)), slices.Sorted(maps.Keys(fields)), path)

	for name, ft := range fields {
		prop := schema.Properties[name]
		if prop == nil {
			continue
		}
		s := prop.Value
		if ft.Kind() == reflect.Slice {
			ft, s = ft.Elem(), s.Items.Value
		}
		if ft.Kind() == reflect.Struct && ft != reflect.TypeFor[time.Time]() {
			assertFieldsMatch(t, path+"."+name, ft, s)
		}
	}
}

// exchange is one request the contract test makes: the operation it is an
// instance of and the status it must get.
type exchange struct {
	op     string // "METHOD /path/{template}", as in the spec
	req    request
	status int
}

// TestOpenAPI_ResponsesMatchSchemas drives every documented operation over
// seeded fixtures and validates each real response against the spec: first
// a few while the daemon is starting, then all of them once it is ready.
func TestOpenAPI_ResponsesMatchSchemas(t *testing.T) {
	// Drifted, both parts of the build changed and a sample showed it.
	monitor := &driftMonitor{report: drifted(time.Now())}
	s := newStartingTestServer(t, func(d *Deps) {
		// A query mentioning "offline" can't be embedded, so its search
		// degrades to keyword results with a warning.
		emb := embedFunc(func(_ context.Context, texts []string) ([][]float32, error) {
			if strings.Contains(texts[0], "offline") {
				return nil, errors.New("ollama unreachable")
			}
			return [][]float32{unitVec()}, nil
		})
		d.Search = search.New(d.Chunks, d.Documents, emb, search.Config{Log: slog.New(slog.DiscardHandler)})
		d.Embedder = pingingEmbedder{err: fmt.Errorf("%w: connection refused", ollama.ErrUnreachable)}
		d.Drift = monitor
		d.Bookmarks = unsavableBookmark{BookmarkStore: d.Bookmarks, url: "https://example.com/unsavable"}
		d.YouTubeFetcher = "/opt/homebrew/bin/yt-dlp"
		// Paused Jina, with every optional field set.
		d.Upstreams = func() []fetcher.UpstreamHealth {
			now := time.Now()
			return []fetcher.UpstreamHealth{{
				Name: "jina", Enabled: true, State: fetcher.UpstreamPaused,
				LastSuccess: now.Add(-time.Hour), LastFailure: now.Add(-time.Minute),
				LastFailureClass: fetcher.CallChallenged, Window: 15 * time.Minute,
				Recent: map[fetcher.CallClass]int{fetcher.CallOK: 12, fetcher.CallJudged: 3,
					fetcher.CallRefused: 2, fetcher.CallChallenged: 1},
				CooldownUntil: now.Add(9 * time.Minute),
				SitePauses:    []fetcher.SitePause{{Site: "twitter.com", Until: now.Add(58 * time.Minute)}},
			}}
		}
	})
	f := seedContractFixtures(t, s)
	savedCursor, err := encodeCursor(store.PageKey{At: time.Now(), ID: uuid.NewString()},
		bookmarkCursorOrder(store.BookmarkOrderSaved))
	require.NoError(t, err)
	doc := strictSpec(t)
	ops := specOperations(doc)
	jsonBody := func(method, path, body string) request {
		return request{method: method, path: path, contentType: "application/json", body: body}
	}
	get := func(path string) request { return request{method: http.MethodGet, path: path} }
	post := func(path string) request { return request{method: http.MethodPost, path: path} }
	unknown := uuid.NewString()
	exercised := map[string]bool{}
	seen := propertiesSeen{}
	run := func(exchanges []exchange) {
		t.Helper()
		for _, ex := range exchanges {
			what := ex.req.method + " " + ex.req.path
			resp := s.do(t, ex.req)
			require.Equal(t, ex.status, resp.status, "%s: %s", what, resp.body)
			op := ops[ex.op]
			require.NotNil(t, op, "%s is not in the spec", ex.op)
			exercised[ex.op] = true
			checkResponse(t, op, resp, what, seen)
		}
	}

	// Starting, part way through its migrations: healthz answers with the
	// Starting schema, and every other operation with its default problem.
	s.startup.SetMigrating(6)
	s.startup.MigrationApplied()
	run([]exchange{
		{"GET /v1/healthz", get("/v1/healthz"), http.StatusServiceUnavailable},
		{"GET /v1/stats", get("/v1/stats"), http.StatusServiceUnavailable},
		{"POST /v1/documents/refetch-all", post("/v1/documents/refetch-all"), http.StatusServiceUnavailable},
	})
	s.ready(t)

	// Before the first rebuild: no run, nothing grouped, and no rebuild
	// underway.
	run([]exchange{
		{"GET /v1/interests", get("/v1/interests"), http.StatusOK},
		{"GET /v1/interests/unsorted", get("/v1/interests/unsorted"), http.StatusOK},
		{"GET /v1/interests/changes", get("/v1/interests/changes"), http.StatusOK},
		{"GET /v1/interests/map", get("/v1/interests/map"), http.StatusNotFound},
	})
	in := seedInterestFixtures(t, s, f)

	// In order: the writes come after the reads of what they change.
	run([]exchange{
		{"GET /v1/healthz", get("/v1/healthz"), http.StatusOK},
		{"GET /v1/stats", get("/v1/stats"), http.StatusOK},
		{"GET /v1/metrics", get("/v1/metrics"), http.StatusOK},

		{"GET /v1/bookmarks", get("/v1/bookmarks?limit=1"), http.StatusOK},
		{"GET /v1/bookmarks", get("/v1/bookmarks?order=saved&limit=1"), http.StatusOK},
		{"GET /v1/bookmarks", get("/v1/bookmarks?order=saved"), http.StatusOK},
		{"GET /v1/bookmarks", get("/v1/bookmarks?order=saved&state=failed&host=example.com&cause=anti_bot"),
			http.StatusOK},
		{"GET /v1/bookmarks", get("/v1/bookmarks?order=created&content_type=unknown&folder=/Reading"), http.StatusOK},
		{"GET /v1/bookmarks", get("/v1/bookmarks?order=bogus"), http.StatusBadRequest},
		{"GET /v1/bookmarks", get("/v1/bookmarks?state=bogus"), http.StatusBadRequest},
		{"GET /v1/bookmarks", get("/v1/bookmarks?cursor=" + savedCursor), http.StatusBadRequest},
		{"GET /v1/bookmarks/{id}", get("/v1/bookmarks/" + f.bookmark), http.StatusOK},
		{"POST /v1/bookmarks", jsonBody(http.MethodPost, "/v1/bookmarks",
			`{"url":"https://example.com/new","title":"New","folder_path":"/Reading","tags":["go"]}`), http.StatusCreated},
		{"POST /v1/bookmarks", jsonBody(http.MethodPost, "/v1/bookmarks", `{"url":"https://example.com/b"}`),
			http.StatusCreated}, // a known document: job_id is empty
		{"POST /v1/bookmarks", jsonBody(http.MethodPost, "/v1/bookmarks", `{"url":"https://example.com/b"}`),
			http.StatusConflict},
		{"POST /v1/bookmarks", request{method: http.MethodPost, path: "/v1/bookmarks", contentType: "text/plain",
			body: `{"url":"https://example.com/c"}`}, http.StatusUnsupportedMediaType},
		{"POST /v1/bookmarks/import", jsonBody(http.MethodPost, "/v1/bookmarks/import",
			`{"source":"html","bookmarks":[{"url":"https://example.com/imported","saved_at":"2024-01-01T00:00:00Z"},`+
				`{"url":"javascript:alert(1)"},{"url":"https://example.com/unsavable"}]}`), http.StatusOK},
		{"POST /v1/bookmarks/import", jsonBody(http.MethodPost, "/v1/bookmarks/import",
			`{"source":"chrome","dry_run":true,"bookmarks":[{"url":"https://example.com/counted"}]}`), http.StatusOK},
		{"DELETE /v1/bookmarks/{id}", request{method: http.MethodDelete, path: "/v1/bookmarks/" + f.bookmark}, http.StatusNoContent},

		{"GET /v1/documents", get("/v1/documents?limit=2"), http.StatusOK},
		{"GET /v1/documents", get("/v1/documents?state=fetched"), http.StatusOK},
		{"GET /v1/documents", get("/v1/documents?state=failed"), http.StatusOK},
		{"GET /v1/documents", get("/v1/documents?state=bogus"), http.StatusBadRequest},
		{"GET /v1/documents", get("/v1/documents?content_type=unknown"), http.StatusOK},
		{"GET /v1/documents", get("/v1/documents?content_type=bogus"), http.StatusBadRequest},
		{"GET /v1/documents", get("/v1/documents?host=example.com&state=fetched"), http.StatusOK},
		{"GET /v1/documents", get("/v1/documents?folder=/Reading&limit=1"), http.StatusOK},
		{"GET /v1/documents", get("/v1/documents?cause=anti_bot"), http.StatusOK},
		{"GET /v1/documents", get("/v1/documents?cause=bogus"), http.StatusBadRequest},
		{"GET /v1/documents/{id}", get("/v1/documents/" + f.fetched), http.StatusOK},
		{"GET /v1/documents/{id}", get("/v1/documents/" + f.failed), http.StatusOK},
		{"GET /v1/documents/lookup", get("/v1/documents/lookup?url=https://EXAMPLE.com/a%23top"), http.StatusOK},
		{"GET /v1/documents/lookup", get("/v1/documents/lookup?url=https://example.com/nowhere"), http.StatusNotFound},
		{"GET /v1/documents/lookup", get("/v1/documents/lookup?url=ftp://example.com/a"), http.StatusBadRequest},
		{"GET /v1/documents/{id}", get("/v1/documents/" + unknown), http.StatusNotFound},
		{"GET /v1/documents/{id}/content", get("/v1/documents/" + f.fetched + "/content"), http.StatusOK},
		{"GET /v1/documents/{id}/related", get("/v1/documents/" + f.fetched + "/related"), http.StatusOK},
		{"POST /v1/search", jsonBody(http.MethodPost, "/v1/search", `{"query":"kafka","k":5}`), http.StatusOK},
		{"POST /v1/search", jsonBody(http.MethodPost, "/v1/search", `{"query":"kafka","k":1,"offset":1}`), http.StatusOK},
		{"POST /v1/search", jsonBody(http.MethodPost, "/v1/search", `{"query":"kafka","k":10,"offset":91}`),
			http.StatusBadRequest},
		{"POST /v1/search", jsonBody(http.MethodPost, "/v1/search", `{"query":"kafka offline"}`), http.StatusOK},
		{"GET /v1/failures", get("/v1/failures"), http.StatusOK},

		{"GET /v1/interests", get("/v1/interests"), http.StatusOK},
		{"GET /v1/interests", get("/v1/interests?level=interest"), http.StatusOK},
		{"GET /v1/interests", get("/v1/interests?offset=1"), http.StatusOK},
		{"GET /v1/interests", get("/v1/interests?offset=-1"), http.StatusBadRequest},
		{"GET /v1/interests", get("/v1/interests?level=area"), http.StatusBadRequest},
		{"GET /v1/interests/{id}", get("/v1/interests/" + in.a1), http.StatusOK},
		{"GET /v1/interests/{id}", get("/v1/interests/" + in.i1), http.StatusOK},
		{"GET /v1/interests/{id}", get("/v1/interests/" + in.i4), http.StatusOK},
		{"GET /v1/interests/{id}", get("/v1/interests/" + in.i1 + "?offset=1"), http.StatusOK},
		{"GET /v1/interests/{id}", get("/v1/interests/" + in.i1 + "?offset=x"), http.StatusBadRequest},
		{"GET /v1/interests/{id}", get("/v1/interests/" + in.i2), http.StatusGone},
		{"GET /v1/interests/{id}", get("/v1/interests/" + unknown), http.StatusNotFound},
		{"GET /v1/interests/unsorted", get("/v1/interests/unsorted"), http.StatusOK},
		{"GET /v1/interests/unsorted", get("/v1/interests/unsorted?offset=-1"), http.StatusBadRequest},
		{"GET /v1/interests/changes", get("/v1/interests/changes"), http.StatusOK},
		{"GET /v1/interests/map", get("/v1/interests/map"), http.StatusNotFound}, // drawn before maps
		{"POST /v1/interests/rebuild", post("/v1/interests/rebuild"), http.StatusAccepted},

		{"GET /v1/jobs", get("/v1/jobs?status=failed"), http.StatusOK},
		{"GET /v1/jobs", get("/v1/jobs?status=done"), http.StatusOK},
		{"GET /v1/jobs", get("/v1/jobs?limit=1"), http.StatusOK},
		{"GET /v1/jobs", get("/v1/jobs?document_id=" + f.failed), http.StatusOK},
		{"GET /v1/jobs/{id}", get("/v1/jobs/" + f.failedJob), http.StatusOK},

		{"POST /v1/documents/refetch-all", post("/v1/documents/refetch-all?cause=dead_link"), http.StatusBadRequest},
		{"POST /v1/documents/refetch-all", post("/v1/documents/refetch-all?cause=anti_bot"), http.StatusAccepted},
		{"POST /v1/documents/{id}/refetch", post("/v1/documents/" + f.dead + "/refetch"), http.StatusConflict},
		{"POST /v1/documents/{id}/refetch", post("/v1/documents/" + f.failed + "/refetch"), http.StatusAccepted},
		{"POST /v1/documents/refetch-all", post("/v1/documents/refetch-all?state=failed"), http.StatusAccepted},
		{"POST /v1/documents/{id}/reindex", post("/v1/documents/" + f.fetched + "/reindex"), http.StatusAccepted},
		{"POST /v1/documents/reindex-all", post("/v1/documents/reindex-all"), http.StatusAccepted},
		{"DELETE /v1/jobs", request{method: http.MethodDelete, path: "/v1/jobs?status=failed"}, http.StatusOK},

		{"GET /v1/queue", get("/v1/queue"), http.StatusOK},
		{"PUT /v1/queue", jsonBody(http.MethodPut, "/v1/queue", `{"paused":true}`), http.StatusOK},
		{"PUT /v1/queue", jsonBody(http.MethodPut, "/v1/queue",
			`{"paused":false,"throttle":"gentle","schedule":"`+windowExcludingNow()+`"}`), http.StatusOK},
		{"PUT /v1/queue", jsonBody(http.MethodPut, "/v1/queue", `{"throttle":"fast"}`), http.StatusBadRequest},
		{"PUT /v1/queue", jsonBody(http.MethodPut, "/v1/queue", `{"schedule":"off"}`), http.StatusOK},
		{"PUT /v1/queue", jsonBody(http.MethodPut, "/v1/queue", `{"keep_awake":true}`), http.StatusOK},
		{"PUT /v1/queue", jsonBody(http.MethodPut, "/v1/queue", `{"keep_awake":"yes"}`), http.StatusBadRequest},
	})
	// A drift no sample could verify.
	monitor.set(driftedUnverified(time.Now()))
	run([]exchange{{"GET /v1/healthz", get("/v1/healthz"), http.StatusOK}})

	// A rebuild whose map failed, then one that drew its map.
	seedFailedMap(t, s, f)
	run([]exchange{{"GET /v1/interests/map", get("/v1/interests/map"), http.StatusNotFound}})
	seedMap(t, s, f)
	run([]exchange{{"GET /v1/interests/map", get("/v1/interests/map"), http.StatusOK}})

	assert.Equal(t, slices.Sorted(maps.Keys(ops)), slices.Sorted(maps.Keys(exercised)),
		"every documented operation is exercised")
	assert.Empty(t, seen.missing(doc, slices.Collect(maps.Values(ops))),
		"every response property is sent by some exchange; seed a fixture that sets it")

	// An unsupported method has no operation; its problem is still one.
	resp := s.do(t, request{method: http.MethodPut, path: "/v1/bookmarks"})
	require.Equal(t, http.StatusMethodNotAllowed, resp.status)
	validateJSON(t, doc.Components.Schemas["Problem"].Value, resp.body, "PUT /v1/bookmarks")
}

// windowExcludingNow is a daily window on the local clock, the daemon's,
// from two hours from now to three: it never holds now, so a queue
// scheduled to it is closed until it opens.
func windowExcludingNow() string {
	now := time.Now()
	m := now.Hour()*60 + now.Minute()
	const day = 24 * 60
	return store.DailyWindow{Start: (m + 120) % day, End: (m + 180) % day}.String()
}

// checkResponse checks that op documents resp's status and content type,
// and that a JSON body validates against the documented schema, recording
// in seen the properties it carried. A success must be listed; an error may
// fall to the default response.
func checkResponse(t *testing.T, op *openapi3.Operation, resp response, what string, seen propertiesSeen) {
	t.Helper()
	ref := op.Responses.Status(resp.status)
	if ref == nil && resp.status >= http.StatusBadRequest {
		ref = op.Responses.Default()
	}
	require.NotNil(t, ref, "%s: status %d is not documented", what, resp.status)
	if len(ref.Value.Content) == 0 {
		assert.Empty(t, resp.body, "%s: the response documents no body", what)
		return
	}
	mediaType, _, err := mime.ParseMediaType(resp.contentType)
	require.NoError(t, err, what)
	content := ref.Value.Content.Get(mediaType)
	require.NotNil(t, content, "%s: %d %s is not documented", what, resp.status, mediaType)
	if mediaType == "application/json" || mediaType == "application/problem+json" {
		validateJSON(t, content.Schema.Value, resp.body, what)
		var v any
		require.NoError(t, json.Unmarshal([]byte(resp.body), &v))
		seen.record(content.Schema.Value, v)
	}
}

// propertiesSeen records, per response schema, the properties that appeared
// in validated responses. strictSpec resolves $refs in place, so every use
// of a component schema is the same *openapi3.Schema and shares one entry.
type propertiesSeen map[*openapi3.Schema]map[string]bool

// record walks v, a decoded response, alongside schema.
func (seen propertiesSeen) record(schema *openapi3.Schema, v any) {
	switch v := v.(type) {
	case map[string]any:
		for name, value := range v {
			prop := schema.Properties[name]
			if prop == nil {
				if extra := schema.AdditionalProperties.Schema; extra != nil {
					seen.record(extra.Value, value)
				}
				continue
			}
			if seen[schema] == nil {
				seen[schema] = map[string]bool{}
			}
			seen[schema][name] = true
			seen.record(prop.Value, value)
		}
	case []any:
		if schema.Items != nil {
			for _, item := range v {
				seen.record(schema.Items.Value, item)
			}
		}
	}
}

// missing lists, as "Schema.property", every property declared by the
// response schemas of ops that no recorded response carried. Schemas are
// named after their component, and a nested inline one after the property
// or items that hold it.
func (seen propertiesSeen) missing(doc *openapi3.T, ops []*openapi3.Operation) []string {
	names := map[*openapi3.Schema]string{}
	for name, ref := range doc.Components.Schemas {
		names[ref.Value] = name
	}
	var out []string
	walked := map[*openapi3.Schema]bool{}
	var walk func(s *openapi3.Schema, name string)
	walk = func(s *openapi3.Schema, name string) {
		if component, ok := names[s]; ok {
			name = component
		}
		if walked[s] {
			return
		}
		walked[s] = true
		for _, prop := range slices.Sorted(maps.Keys(s.Properties)) {
			if !seen[s][prop] {
				out = append(out, name+"."+prop)
			}
			walk(s.Properties[prop].Value, name+"."+prop)
		}
		if s.Items != nil {
			walk(s.Items.Value, name+"[]")
		}
		if extra := s.AdditionalProperties.Schema; extra != nil {
			walk(extra.Value, name+"{}")
		}
	}
	for _, op := range ops {
		for status, resp := range op.Responses.Map() {
			for mediaType, content := range resp.Value.Content {
				if content.Schema != nil {
					walk(content.Schema.Value, fmt.Sprintf("%s %s", status, mediaType))
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// contractFixtures are what the contract test's requests name.
type contractFixtures struct {
	fetched, failed, dead string // documents
	bookmark              string
	failedJob             string
	// Documents the interest fixtures group: a titled one with content,
	// the failed one, untitled and named by its bookmark, an indexed
	// untitled one, also named by its bookmark, and a titled one.
	titled, named, untitled, plain *store.Document
}

// seedContractFixtures fills s with a little of everything, so that every
// property a response schema declares shows up in some response, which
// TestOpenAPI_ResponsesMatchSchemas enforces: two indexed documents with
// titles and content, one of them with every optional metadata column and
// an extraction error message, an indexed untitled one, a document failed
// as anti-bot and a dead one, four bookmarks (one with a folder and tags,
// one of each untitled document with a title of its own), a failed job and
// done ones. The interests come later (seedInterestFixtures), after the
// requests of a library never grouped.
func seedContractFixtures(t *testing.T, s *testServer) contractFixtures {
	t.Helper()
	ctx := context.Background()
	// indexed seeds a fetched document of text, titled unless title is empty.
	indexed := func(url, title, text string) (*store.Document, *store.DocumentExtraction) {
		doc := s.seedDocument(t, url, store.DocStateFetched)
		markdown := text
		if title != "" {
			_, err := s.db.Exec(`UPDATE documents SET title = ? WHERE id = ?`, title, doc.ID)
			require.NoError(t, err)
			markdown = "# " + title + "\n\n" + text
		}
		ext := s.seedContent(t, doc, markdown)
		require.NoError(t, s.deps.Chunks.ReplaceForDocument(ctx, doc.ID, ext.ID, title, nil,
			[]store.ChunkInput{{Text: text, Embedding: unitVec()}}))
		return doc, ext
	}
	a, aExt := indexed("https://example.com/a", "Kafka partitions", "kafka partitions and consumer groups")
	b, _ := indexed("https://example.com/b", "Kafka brokers", "kafka brokers and replication")
	untitled, _ := indexed("https://example.com/untitled", "", "kafka connect sinks")
	_, err := s.db.Exec(`UPDATE documents SET url_canonical = 'https://example.com/a/', author = 'Ada',
		published_at = '2024-01-02T03:04:05.000Z', language = 'en', word_count = 6 WHERE id = ?`, a.ID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE document_extractions SET error_message = 'truncated at the size cap' WHERE id = ?`, aExt.ID)
	require.NoError(t, err)
	done, err := store.NewDocumentJob("local", store.JobKindFetch, a.ID)
	require.NoError(t, err)
	done.Status = store.JobStatusDone
	require.NoError(t, s.deps.Queue.Enqueue(ctx, done))

	failed := s.seedFailedDocument(t, "https://example.com/failed", store.FailureCauseAntiBot)
	job, err := store.NewDocumentJob("local", store.JobKindFetch, failed.ID)
	require.NoError(t, err)
	job.Status = store.JobStatusFailed
	require.NoError(t, s.deps.Queue.Enqueue(ctx, job))
	_, err = s.db.Exec(`UPDATE jobs SET last_error = 'HTTP 503' WHERE id = ?`, job.ID)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO jobs (id, tenant_id, kind, payload, status, started_at, updated_at)
		VALUES (?, 'local', 'index', '{}', 'done', strftime('%Y-%m-%dT%H:%M:%fZ','now','-2 seconds'),
		        strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, uuid.NewString())
	require.NoError(t, err)
	dead := s.seedDocument(t, "https://example.com/dead", store.DocStateDead)
	// A job due later, which the queue counts apart, with when it is due.
	require.NoError(t, s.deps.Queue.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindIndex,
		RunAfter: time.Now().Add(24 * time.Minute)}))

	folder, title := "/Reading/Kafka", "A"
	bookmark := &store.Bookmark{TenantID: "local", URL: a.URL, Title: &title, FolderPath: &folder,
		Tags: []string{"kafka"}, Source: store.SourceChrome, SavedAt: time.Now().UTC()}
	_, err = s.deps.Bookmarks.Ingest(ctx, bookmark)
	require.NoError(t, err)
	// A second bookmark, so a page of one has a next page.
	_, err = s.deps.Bookmarks.Ingest(ctx, &store.Bookmark{TenantID: "local", URL: b.URL,
		Source: store.SourceFirefox, SavedAt: time.Now().UTC()})
	require.NoError(t, err)
	// The failed document's, which names it in the document list and
	// carries its failure in the bookmark list.
	blockedTitle := "A blocked page"
	_, err = s.deps.Bookmarks.Ingest(ctx, &store.Bookmark{TenantID: "local", URL: failed.URL, Title: &blockedTitle,
		Source: store.SourceSafari, SavedAt: time.Now().UTC().Add(-time.Hour)})
	require.NoError(t, err)
	// The untitled indexed document's, which names it among search hits.
	connectTitle := "Kafka Connect notes"
	_, err = s.deps.Bookmarks.Ingest(ctx, &store.Bookmark{TenantID: "local", URL: untitled.URL, Title: &connectTitle,
		Source: store.SourceChrome, SavedAt: time.Now().UTC().Add(-2 * time.Hour)})
	require.NoError(t, err)

	return contractFixtures{fetched: a.ID, failed: failed.ID, dead: dead.ID, bookmark: bookmark.ID,
		failedJob: job.ID, titled: a, named: failed, untitled: untitled, plain: b}
}

// seedInterestFixtures commits two rebuilds of an areas-shaped library,
// the second of which split, merged, moved, dissolved and created (see
// eventsFixture): the titled document and the named one are an interest's
// members, the plain one its loose fit, and the untitled one and a titled
// one are unsorted; a document is placed into the interest since, and one
// into Unsorted; and the scheduler's state has every field set, as no one
// check could, so the state's every property is sent.
func seedInterestFixtures(t *testing.T, s *testServer, f contractFixtures) eventsFixture {
	t.Helper()
	unsorted := s.seedDocument(t, "https://example.com/unsorted", store.DocStateFetched)
	_, err := s.db.Exec(`UPDATE documents SET title = 'Far from everything' WHERE id = ?`, unsorted.ID)
	require.NoError(t, err)
	e := s.seedEvents(t, []*store.Document{f.titled, f.named}, f.plain, f.untitled, unsorted)
	s.placeDocument(t, e.second, e.i1, s.seedDocument(t, "https://example.com/placed", store.DocStateFetched))
	s.placeDocument(t, e.second, "", s.seedDocument(t, "https://example.com/placed-unsorted", store.DocStateFetched))
	now := time.Now()
	s.interests.set(insight.Snapshot{State: insight.StateFailing, LastRebuildAt: now.Add(-time.Hour),
		LastKind: store.RunKindWarm, LastTrigger: store.RunTriggerAuto, Changed: 271, RebuildAt: 263,
		DueSince: now.Add(-20 * time.Minute), FreshOwed: string(store.FreshReindex), HeldReason: "the embeddings drifted",
		RetryAt: now.Add(15 * time.Minute), LastError: "ollama unreachable: connection refused", CheckedAt: now})
	return e
}

// seedFailedMap commits a rebuild whose map failed.
func seedFailedMap(t *testing.T, s *testServer, f contractFixtures) {
	t.Helper()
	r := s.newRun(t, store.InterestShapeFlat)
	r.interest("Kafka brokers", "", 0, []*store.Document{f.plain}, nil)
	r.c.Outcome.Map = &store.RunMap{Status: store.MapFailed, Error: "the map took longer than 2m0s",
		Params: []byte(`{"seed":1}`), Took: 2 * time.Minute}
	r.commit(t)
}

// seedMap commits a rebuild that drew its map: an area of two labelled
// interests, one with a loose fit, an unsorted document with its nearest
// interest, and a document placed since.
func seedMap(t *testing.T, s *testServer, f contractFixtures) {
	t.Helper()
	docs := s.docs(t, "mapped", 4)
	r := s.newRun(t, store.InterestShapeAreas)
	area := r.area("Streaming")
	kafka := r.interest("Kafka", area, 0, []*store.Document{f.titled, docs[0]}, []*store.Document{docs[1]})
	r.interest("Joins", area, 0, docs[2:3], nil)
	r.unsorted(kafka, f.untitled)
	r.mapped()
	s.placeMapped(t, r.commit(t), kafka, docs[3], mapPlace(500, 121, 301))
}

// TestOpenAPI_MapOffMatchesTheSchema: the map's 404 with the map off is
// the documented operation's response.
func TestOpenAPI_MapOffMatchesTheSchema(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.MapOff = true })
	op := specOperations(strictSpec(t))["GET /v1/interests/map"]
	require.NotNil(t, op)
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests/map"})
	require.Equal(t, http.StatusNotFound, resp.status, resp.body)
	checkResponse(t, op, resp, "GET /v1/interests/map with the map off", propertiesSeen{})
}

// unsavableBookmark is a bookmark store that can't save url, for an import
// that reports an error.
type unsavableBookmark struct {
	store.BookmarkStore
	url string
}

func (u unsavableBookmark) Ingest(ctx context.Context, b *store.Bookmark) (store.IngestResult, error) {
	if b.URL == u.url {
		return store.IngestResult{}, errors.New("database is locked")
	}
	return u.BookmarkStore.Ingest(ctx, b)
}
