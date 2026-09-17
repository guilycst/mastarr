# U-02 correction round three handoff

## Identity

- Task: U-02 correction round three, discovery, inventory and matching views.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Correction base and dispatch checkpoint: `1900d1f67e3ec7a5d0a383ab9fec30fc5da99af9`.
- Prior product: `6bd6c9c188074ec8c5e05b33f3723f39aad16927`.
- Prior correction handoff: `55c957eec5d2f7c68ca5eb25b5043f82522f6471`.
- Review receipt: `a81e58c5fc5a97e8205dabaa071fdb3783c31f78`.
- Product commit: `d832964b35cf2cfe2adf2b39d91e62c2f46bd745`.
- Handoff commit: this file, committed separately after the product.
- Owned product path: `ui/internal/inventory/`.

## Corrections

### R2b — usable detail back links

Detail pages now construct back links from the route's validated list state
only. Cursor, limit and the appropriate route filter (`rootId`, `kind` or
`connectionId`) remain available, while media identity drafts and discovery
association/candidate controls stay in the detail form flow. Generated links
therefore pass the list parser instead of sending detail-only keys to list
routes. Synthetic regressions follow generated discovery and media links back
through the handler and verify both preserved list context and omitted draft
fields.

### R5 — collection-sized draft bounds

Dynamic parser bounds are now derived from the renderer's accepted collection
limits: seven controls for each of up to 1,000 files and eight controls for
each of up to 100 candidates. Family-specific index bounds accept every index
the renderer can emit, while response-bound validation still rejects indexes
that are absent from the returned discovery. The aggregate field limit and an
8 MiB encoded-query limit remain finite and cover URL escaping and the largest
supported candidate episode field. A regression renders 257 files and 33
candidates, submits every generated control including the final indexes, and
verifies reload, list context and stable ordering.

### R5d — route-scoped fixed draft vocabulary

Fixed detail fields are accepted only on media details; discovery uses its
response-bound dynamic controls and downloads/descriptors reject unrelated
identity, selection, episode, subtitle and kind keys before reading data.
Media fixed fields validate booleans, enum kind and provider identity values,
alongside bounded UTF-8 input. Synthetic regressions cover rejected unrelated
route keys, invalid media values and a valid media draft.

All earlier corrections remain in place: exact requested/detail identity
binding, complete candidate/association/media draft round-trip, 256-episode
and score precision, coverage window retention and zero-time rejection,
strict dynamic grammar and response-index binding, evidence/unknown rendering,
valid option markup, escaped private no-store/noindex HTML, GET/HEAD-only
routing and no mutation paths. No API contract, generated client, root
internals, database, upstream, media path, credential or live-service files
were changed.

## Verification

Commands were run from the product tree with the stated working directory and
environment.

| Check | Result |
| --- | --- |
| `cd ui && gofmt -w internal/inventory/inventory.go internal/inventory/render.go internal/inventory/inventory_test.go` | PASS |
| `GOWORK=off go test ./internal/inventory -count=1 -timeout=120s` in `ui/` | PASS |
| `GOWORK=off go test -race ./internal/inventory -count=1 -timeout=180s` in `ui/` | PASS |
| `GOWORK=off go vet ./internal/inventory` in `ui/` | PASS |
| `GOWORK=off go test ./... -count=1 -timeout=180s` in `ui/` | PASS |
| `GOWORK=off go test -race ./... -count=1 -timeout=240s` in `ui/` | PASS |
| `GOWORK=off go vet ./...` in `ui/` | PASS |
| `GOWORK=off go mod verify` and `GOWORK=off go mod tidy -diff` in `ui/` | PASS; verified modules and no manifest diff |
| Product commit staged hook | PASS; generation/API/Vacuum, architecture, root/UI checks and fast guardrails |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; final `guardrail checks passed (ci)`, including generation, six-contract Vacuum, lint, architecture, all module ordinary/race/vet checks, module verification and Linux amd64/arm64 CGO-free builds |
| `git diff --check` | PASS |

The synthetic regressions use in-memory readers and loopback `httptest`
servers only. Browser-level composition and live-service behavior remain
outside this package's verification scope.
