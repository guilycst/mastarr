# X-06 Arr write-adapter correction round 1

## Assignment

- Task ID and title: X-06 correction round 1, close the Arr write-adapter
  preview-binding, read-back identity, error-sanitization and registration
  default findings.
- Owner/agent: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Correction base checkpoint: `bae0af0bd446dfa3e5df7e8f15a53a861aede6a1`.
- Prior product: `d131422a7319be22ccc6fba17a54a047c89e25d8`.
- Prior review receipt: `fbf2ecd106e50d89bf20c93cd926a52cd8e86f42`.
- Branch/worktree: shared `main`; coordinator owns
  `docs/execution/state.json`.
- Owned product paths: `internal/adapters/arr/write/` and
  `tests/fixtures/arr/write/`.
- Owned handoff path: this file.
- Dependencies: X-05 write-safety evidence and the existing root Arr
  registration/import ports. X-20 remains the standalone Sonarr/Radarr client
  migration lane.
- Acceptance contributions: A-12, A-13, A-16, A-17 and A-33.
- Product commit: `8e811aa460d12eb374a623e87fb1d7531e015b3d`.

## Correction result

`Import` now observes current Arr state before any command and requires a
root-composed `PreviewResolver` to return an exact server-observed preview.
The adapter binds the immutable preview revision, selected source files,
media IDs, subtitle roles and rejection set to the requested import. Missing,
forged, omitted, duplicate or rejected selections fail closed before command
dispatch. A public constructor with the unresolved G-01 capability still
returns `already_satisfied` for materialized state and `unsupported` for a
changed state without sending a native write. The synthetic package
constructor injects a resolver only to exercise this protocol model; it is not
a production write enablement or native Arr evidence.

Sonarr and Radarr read-back is identity-safe. Sonarr rejects missing nested
file identity, contradictory path or size for one file ID, and distinct file
IDs claiming one mapped target; a valid one-file, multi-episode association is
retained. Radarr requires the returned title and movie-file IDs to match the
requested movie, including rejecting missing or foreign movie associations.
Contradictory observations become unknown/incomplete and cannot produce a
false `already_satisfied` result.

Transport and response-body cancellation/deadline failures are sanitized to a
stable Arr error while retaining `errors.Is` identity for
`context.Canceled` and `context.DeadlineExceeded`. URL, endpoint and private
transport details do not escape through the adapter. New registration payloads
default `monitored` to false only when unspecified, preserve an explicit true
or false value, and keep no-search options enabled. Existing fields remain
preserved through the read-before-write path.

No live service, credential, private coordinate or real media inventory was
used. The product contains only synthetic `httptest` fixtures and remains
read-before-write plus fail-closed while G-01 native Arr no-overwrite evidence
is unresolved. The standalone-client boundary and generated-type isolation are
unchanged.

## Verification

All commands ran from the correction product tree ending at
`8e811aa460d12eb374a623e87fb1d7531e015b3d`, using synthetic fixtures.

| Command or scenario | Result / exit status | Evidence |
| --- | --- | --- |
| `GOWORK=off go test -mod=readonly -count=1 ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Exit 0 | Arr write adapter and fixture packages |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Exit 0; three race repetitions | `client_test.go`, fixture tests |
| `GOWORK=off go vet -mod=readonly ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Exit 0 | Focused packages |
| Focused correction tests for exact preview binding, identity collisions and sanitized wrapped context/body errors | Exit 0; all table cases passed | `TestImportRequiresExactServerPreviewBindingBeforeCommand`, `TestArrWriteRejectsContradictoryReadbackIdentity`, `TestArrWriteSanitizesWrappedContextErrors`, `TestArrWriteSanitizesWrappedContextBodyErrors` |
| `GOWORK=off go test -mod=readonly -count=1 ./internal/adapters/arr/write ./tests/fixtures/arr/write ./internal/adapters/arr/read` | Exit 0 | Root Arr read/write seam |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Exit 0 | Repeated Arr race gate |
| `GOWORK=off go vet -mod=readonly ./internal/adapters/arr/write ./tests/fixtures/arr/write && GOWORK=off go mod verify` | Exit 0; all modules verified | Root module verification |
| `GOWORK=off go test -mod=readonly -timeout=180s ./...` | Exit 0 | Full root test suite |
| `GOWORK=off ./scripts/check-lint.sh && python3 scripts/check-architecture.py && python3 scripts/check_planning.py` | Exit 0; lint/import boundaries, architecture and planning checks passed; planning reported 53 tasks, 60 acceptance cases and resolved local links | Root guardrails |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; generation, staged generation, API/Vacuum, architecture, formatting and targeted tests passed; Vacuum reported 100/100 with zero warnings/errors | Fast guardrail runner |
| `GOWORK=off GOPROXY=off GOSUMDB=off .githooks/pre-commit` before handoff commit | Exit 0; fast guardrail hook passed with staged-byte API/Vacuum validation | `.githooks/pre-commit` |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0 | Linux amd64 build |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0 | Linux arm64 build |
| `git diff --check` and `gofmt -w internal/adapters/arr/write/*.go tests/fixtures/arr/write/*.go` | Exit 0; no formatting or whitespace errors | Owned product paths |

The full `./scripts/check-guardrails.sh --ci` aggregate was not rerun in this
correction lane; C-06 owns the repository-wide CI matrix. No live Arr
capability is enabled by this correction. G-01 remains the blocker for native
registration/import writes.

## Review and integration

- Findings addressed: R1 P1 forged or incomplete preview binding; R2 P1
  contradictory Sonarr/Radarr read-back identity; R3 P2 unsanitized wrapped
  context and transport errors; R4 P2 loss of explicit `monitored=true`.
- Regression evidence: exact preview revision/selection/rejection tables,
  Sonarr collision and valid multi-episode tests, Radarr title/file identity
  tests, wrapped transport/body context tests and explicit monitoring opt-in
  test all pass under repeated race execution.
- Final independent review: pending against the exact product commit above.
- Coordinator state update: pending; this lane did not edit `state.json`.

## Resume checkpoint

- Current state: correction product is committed; its public write capabilities
  remain read-before-write and fail-closed under G-01.
- Next safe action: commit this handoff separately, then run independent review
  against the exact product tree and this handoff.
- Outstanding blocker: resolve G-01 with versioned native Arr coordination and
  atomic no-overwrite evidence before changing the public capability gates.
- No conflicting files or unknown work were removed.
