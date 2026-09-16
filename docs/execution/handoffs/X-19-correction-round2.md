# X-19 Arr read-adapter correction round 2

## Assignment

- Task ID and title: X-19, migrate the Arr read adapter to standalone Sonarr and Radarr clients; correction round 2.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Correction base product: `4384a873d54df46b6d0f996a3bc03f6a2bde2edd`.
- Coordinator state checkpoint: `7425ca77f70054a57b7b8cdad4d7b6055b1f2613`.
- Prior review receipt: `docs/execution/handoffs/X-19-review-round2.md`.
- Branch/worktree: shared `main`; coordinator owns `docs/execution/state.json`.
- Owned product paths: `internal/adapters/arr/read/`, `tests/fixtures/arr/read/`, root `go.mod`, and `go.sum`.
- Owned handoff path: this file.
- Product commit: `7bb07b5708335701b1884d2acf48e0d3391e4c1a`.
- Dependencies: published Sonarr and Radarr client modules at
  `v0.0.0-20260915230044-873e53ef6729`; no local `replace` or `go.work`.
- Acceptance contributions: A-07, A-08, A-09, A-10, A-16; standalone module policy.

## Correction result

Preview compatibility selection is now row-scoped. Before the old root
decoder can be used, every response row must independently prove complete
product-specific identity:

- Radarr rows require positive candidate and nested movie IDs, valid path,
  relative path, name and nonnegative size; optional movie-file IDs must be
  nonnegative.
- Sonarr rows require positive candidate and series IDs, a nonempty series
  title, at least one episode, positive episode and series IDs, matching nested
  series IDs, nonnegative season and episode numbers, unique episode IDs, and
  valid optional episode-file IDs.

The retained old rejection alias is recognized only when every rejection in
that row uses the old code plus reason/message spelling. An alias on one
unrelated candidate cannot authorize a different incomplete row. Missing or
null nested episode/movie identity therefore remains a typed unknown result
from the native path; the legacy mapper receives no opportunity to skip it.
Complete accepted rows may coexist with a complete rejected row carrying the
legacy alias, preserving the existing synthetic compatibility fixture. A
native rejection using the current `type` field stays on the native path.

The correction remains read-only. No generated upstream DTO, raw response,
credential, endpoint, inventory, filesystem path or write capability crosses
the root boundary. Arr registration/import execution and G-01 remain blocked.

## Verification

Commands ran against product commit
`7bb07b5708335701b1884d2acf48e0d3391e4c1a`, or the staged product tree where
stated. All fixtures are synthetic `httptest` data.

| Command or scenario | Result / exit status | Evidence |
| --- | --- | --- |
| `GOWORK=off go test -mod=readonly ./internal/adapters/arr/read -run 'TestArrNativeLegacyPreview|TestArrNativeMalformedResponsesDoNotUseLegacyLastWinsDecoder|TestArrNative.*IdentityCollisions' -count=1` | Exit 0. | `internal/adapters/arr/read/native_test.go` |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=180s ./internal/adapters/arr/read -run 'TestArrNativeLegacyPreview|TestArrNativeMalformedResponsesDoNotUseLegacyLastWinsDecoder|TestArrNative.*IdentityCollisions'` | Exit 0; all three repetitions passed. | `internal/adapters/arr/read/native_test.go` |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=180s ./internal/adapters/arr/read` | Exit 0; `22.965s`. | Arr adapter package |
| `GOWORK=off go test -mod=readonly ./...` | Exit 0; all root packages passed. | root module |
| `GOWORK=off go vet -mod=readonly ./...` | Exit 0. | root module |
| `GOWORK=off go mod verify` | Exit 0; `all modules verified`. | root module |
| `(cd clients/sonarr && GOWORK=off go test -mod=readonly ./... && GOWORK=off go test -mod=readonly -race -count=3 -timeout=180s ./... && GOWORK=off go vet -mod=readonly ./... && GOWORK=off go mod verify)` | Exit 0; tests, race, vet and module verification passed. | `clients/sonarr/` |
| `(cd clients/radarr && GOWORK=off go test -mod=readonly ./... && GOWORK=off go test -mod=readonly -race -count=3 -timeout=180s ./... && GOWORK=off go vet -mod=readonly ./... && GOWORK=off go mod verify)` | Exit 0; tests, race, vet and module verification passed. | `clients/radarr/` |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./check-generation.sh` in `clients/sonarr` and `clients/radarr` | Exit 0 for both; generated output reproduced offline. | each client `check-generation.sh` |
| `python3 scripts/check_planning.py` | Exit 0; `53 tasks, 60 acceptance cases; local links resolve.` | planning checker |
| `python3 scripts/check-architecture.py` | Exit 0; import boundaries passed. | architecture checker |
| `GOWORK=off ./scripts/check-lint.sh` | Exit 0; all nine module entries reported `0 issues.` | lint/architecture checker |
| `GOWORK=off ./scripts/check-api.sh` | Exit 0; generation and Vacuum quality `100/100`, zero warnings/errors. | API checker |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; generation, staged generation, API/Vacuum, architecture, format and targeted tests passed. | fast guardrail checker |
| Product pre-commit hook during product commit | Exit 0; fast guardrails passed. | `.githooks/pre-commit` |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0. | root module |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0. | root module |

The full aggregate `./scripts/check-guardrails.sh --ci` was not rerun by this
correction lane; C-06 owns that repository-wide matrix. An initial attempt to
run API and guardrail scripts concurrently encountered a transient shared
`.git/index.lock`; the API check was rerun sequentially and passed. No product
check relied on that failed concurrent invocation.

## Review and integration

- Review finding R2a P1: an unrelated candidate's old rejection alias could
  authorize fallback for a selected Sonarr candidate missing nested
  `episode.seriesId`, and the legacy mapper could accept it.
- Fix commit: `7bb07b5708335701b1884d2acf48e0d3391e4c1a`.
- Regression evidence: missing and null Sonarr episode series IDs, missing and
  null Radarr movie references, complete legacy accepted/rejected rows, native
  rejection rows, duplicate native identities and invalid UTF-8 all pass under
  repeated race execution. Incomplete probes make exactly one request and
  return no accepted file; valid legacy fallback makes the expected second read.
- Final reviewer decision: pending independent re-review.
- Coordinator state update: pending; this lane did not edit `state.json`.

## Resume checkpoint

- Product correction is committed at
  `7bb07b5708335701b1884d2acf48e0d3391e4c1a`.
- Next safe action: commit this handoff separately, then re-review against the
  exact product tree.
- Outstanding uncertainty: compatibility fallback intentionally rejects old
  rows lacking complete nested identity; adding another legacy shape requires a
  versioned contract and new evidence. Native Arr writes and G-01 remain open.
- No conflicting writes or unknown files were removed.
