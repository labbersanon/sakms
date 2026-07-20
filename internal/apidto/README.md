# internal/apidto — the frontend DTO boundary

This package is sakms's curated, exported request/response DTO layer for
the future SolidJS frontend's generated TypeScript API client. See
`.omc/plans/frontend-redesign-seerr.md` (Stage 0, Guardrail #4) for the
full plan context this package implements.

## Why a separate package, and why now (Stage 0)

Today's handlers (`internal/api`) decode/encode JSON directly against
unexported request structs (`upsertConnectionRequest`) and raw domain
structs from other internal packages (`tmdb.Item`, `connections.Summary`,
`auth.APIKeyStatus`). Generating TypeScript straight from `internal/api`
would have two problems:

1. A **reflection-based** codegen tool can't see unexported types at all —
   it would either error or silently skip `upsertConnectionRequest`
   entirely, hiding exactly the field (`APIKey *string`) whose semantics
   matter most.
2. A **source-parsing** tool pointed at `internal/api` directly *can* see
   unexported types, but would emit a TypeScript interface for every
   exported-or-not struct across the whole handler package — far more than
   a frontend should ever import, and the generated client's shape would
   shift on every unrelated handler change.

`internal/apidto` is the fix: a small, hand-picked, **exported** set of
types, kept in its own package so a codegen tool can be pointed at exactly
this and nothing else.

## What's here vs. what's NOT here (Stage 0 scope)

This is a **starting point**, not a full inventory of sakms's API surface.
It covers exactly Stage 1's toolchain-proving slice, per the plan:

- Auth boot: setup, login, auth-status, auth-mode, OIDC config, API-key
  management (`internal/api/auth.go`, `oidc.go`, `authmode.go`,
  `apikey.go`, `setup.go` — all four auth muxes constructed in
  `cmd/sakms/main.go`: `NewAuthMux`, `NewOIDCMux`, `NewAuthModeMux`,
  `NewAPIKeyMux`).
- Discover, read-only: poster items (`internal/api/discover.go`,
  `adultdiscover.go`) and availability badges
  (`internal/api/availability.go`).

One exception, included ahead of its stage: `ConnectionSummary` /
`ConnectionUpsertRequest` (Settings/Connections — Stage 4 UI). These are
here now because `ConnectionUpsertRequest.APIKey`'s three-state semantics
(below) is the single highest-risk mapping rule this whole DTO boundary
exists to get right, and it needed to be proven against the chosen codegen
tool as part of *choosing* that tool — not discovered as a surprise once
Stage 4's frontend work starts.

Everything else — Rename/Purge/Dedup/Tag, Search/grab, Settings' other
fields, Advanced Settings — is explicitly **out of scope** for Stage 0.
Guardrail #4's own language: "the DTO set grows per-stage... Stage 0
establishes the codegen *mechanism* and the *initial* (auth + Discover-read)
DTO set." Add to this package as each later stage's frontend view is
actually built, not preemptively.

## What Stage 0 deliberately does NOT do

The types in `dto.go` are currently **parallel copies** of shapes already
produced elsewhere (`internal/api`'s `authStatusResponse`,
`oidcStatusResponse`, `tmdb.Item`, `auth.APIKeyStatus`, etc.) — same JSON
field names and types, so a future handler swap is a type substitution,
not a wire-format change.

Stage 0 does **not** wire any handler to return an `apidto.*` type, and does
**not** write mapper functions between domain types and these DTOs. There is
no frontend yet to prove a wiring change against, and the auth handlers in
particular sit next to this project's single highest-risk failure mode
(total, unrecoverable-except-via-break-glass lockout — see Guardrail #2).
Touching working, tested auth code for zero functional benefit right now is
pure downside. **Stage 1 is expected to converge the real handlers onto
these exact types** as it builds the toolchain slice that actually consumes
them — at that point the parallel definitions in `internal/api` collapse
into this package as the single source of truth.

## Three-state secret mapping rule (Guardrail #5)

`ConnectionUpsertRequest.APIKey *string` (mirroring the real
`internal/api.upsertConnectionRequest.APIKey`) must express three distinct
client intents through ordinary JSON, because the server **never** sends a
stored secret back to a client (`ConnectionSummary` only ever exposes
`hasApiKey`/`keySuffix`, never the key):

| Wire state | Meaning |
|---|---|
| `apiKey` key **absent** from the JSON body | **Preserve** the currently stored secret, unchanged. |
| `apiKey: ""` | **Clear** the stored secret (e.g. switching to a service that needs none, like Ollama). |
| `apiKey: "sk-..."` (non-empty) | **Set/replace** the stored secret. |

This is exactly what the real `internal/api/connections.go` code already
does (`json.Decode`'s standard behavior for a `*string` field: the pointer
stays `nil` only when the key is missing from the payload; `omitempty` only
affects *marshaling* a response, never *decoding* a request) — Stage 0
copies the semantics, it does not invent new ones.

### What tygo actually generates for this field (verified, not assumed)

```typescript
export interface ConnectionUpsertRequest {
  url: string;
  username?: string;
  /**
   * ... (full three-state rule, from the Go doc comment — tygo preserves
   * field-level doc comments as JSDoc)
   */
  apiKey?: string;
}
```

**TypeScript cannot express the three-way distinction as a type.** Both
"the `apiKey` property is absent from the object" and "the `apiKey`
property is present with value `''`" are valid values of `apiKey?: string`
— the type system does not distinguish them, and no source-parsing (or
reflection-based) codegen tool can invent a distinction the target language
doesn't have. This was verified empirically against tygo before it was
selected, not assumed from its README.

**The safety net is therefore the prose rule above, carried into the
generated `.ts` as a doc comment on the field (see
`internal/apidto/ts/dto.gen.ts`), plus this explicit, load-bearing
requirement for any frontend code that builds this request body:**

> An untouched / blank API-key input field in the UI **must** result in
> the `apiKey` property being **omitted** from the request object entirely
> (e.g. build the request with `delete req.apiKey` or a conditional
> spread, never `apiKey: ""`, when the operator didn't touch that field) —
> sending `apiKey: ""` for an untouched field silently wipes the stored
> secret. This is the exact bug Guardrail #5 exists to prevent, and it is
> a frontend-code discipline requirement, not something the generated
> type can enforce for you.

`OIDCConfigRequest.ClientSecret`, by contrast, is a **plain, non-pointer**
required field — every `PUT /api/auth/oidc` call must supply the full OIDC
config; there is no partial-update/preserve mode for OIDC secrets today, so
it needs no three-state handling.

## Codegen tool: tygo (github.com/gzuidhof/tygo)

Chosen against the plan's explicit evaluation criteria:

1. **Source-parsing, not reflection-based** — tygo uses
   `golang.org/x/tools/go/packages` to parse real Go source (AST-level),
   the same class of tool `go vet`/`gopls` use. It never needs a running
   Go process or reflection over live values, so it sees unexported types,
   comments, and exact field ordering — reflection-based JSON-schema-driven
   generators cannot.
2. **Package-scoped** — tygo is pointed at exactly one Go import path
   (`internal/apidto/gen.SourcePackage`, currently
   `github.com/labbersanon/sakms/internal/apidto`) via its `Packages`
   config list. It only ever sees what's in that package — never
   `internal/api`, never any other `internal/*` domain package — so the
   curation this package exists for actually holds at generation time, not
   just by convention.
3. **Doc comments carried into the output** — verified in the generated
   `dto.gen.ts` above: package, type, and field-level Go doc comments
   become TSDoc/JSDoc comments in the `.ts` output. This is what lets the
   three-state secret rule ride directly on the generated type, not live
   only in a README a frontend developer might not read.
4. **`*T`/`omitempty` handling, verified empirically** (not assumed from
   documentation) — a Go `*string` field became `apiKey?: string` in the
   generated output, confirming: (a) tygo correctly treats a pointer as
   optional, and (b) as covered above, no codegen tool can do better than
   this for a Go three-state pointer field — TypeScript's type system has
   no third state to map to.
5. **Deterministic output** — verified empirically: running generation
   twice against unchanged Go source produces byte-identical `.ts` output
   (no timestamps, no unstable map/set ordering). This determinism is what
   makes the drift-detection gate below possible as a simple byte compare.
6. **Library API, not CLI-only** — `github.com/gzuidhof/tygo/tygo` exposes
   `tygo.New(*tygo.Config).Generate() error` as a Go function, so both the
   local regeneration command (`cmd/gendto`) and the drift-detection test
   (`internal/apidto/gen/generate_test.go`) call the same code path
   in-process — no shelling out to a CLI binary, no PATH dependency.

## Build-fails-on-drift gate

`internal/apidto/gen/generate_test.go`'s `TestNoDrift` — part of the normal
`go test ./...` run — regenerates the TypeScript from the current
`internal/apidto` Go source into a temp file and byte-compares it against
the committed `internal/apidto/ts/dto.gen.ts`. Any difference fails the
test (and therefore `go test ./...`, and therefore the build), with an
explicit message telling you to run `go run ./cmd/gendto` and commit the
result.

This is intentionally a **pure Go** gate (no `git diff`, no shell-out to
`git status`) — it only reads files already on disk, so it behaves
identically in CI, a plain checkout, or any environment regardless of VCS
state.

### Regenerating after a DTO change

```sh
go run ./cmd/gendto
git add internal/apidto/ts/dto.gen.ts
```

`cmd/gendto` and `internal/apidto/gen` are never imported by `cmd/sakms` —
tygo (and its `golang.org/x/tools/go/packages` dependency, a real Go
source parser) is a `go.mod` dependency of this module, but it never links
into the production `sakms` binary, since only packages actually imported
by a build's `main` package get compiled in.

## Output location

Generated output lives at `internal/apidto/ts/dto.gen.ts`, deliberately
**outside** `internal/web/static` (the `//go:embed` directory the Go
binary serves — see Guardrail #6). This file is a generated-but-committed
source artifact (analogous to protobuf-generated stubs), not a build
artifact of the future Vite frontend bundle — it is meant to be imported
by that frontend's TypeScript source once Stage 1 scaffolds it, the same
way any other generated API-client types file would be.
