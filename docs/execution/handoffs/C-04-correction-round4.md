# C-04 correction round four handoff

## Assignment

- Task ID and title: C-04, generated REST transport and process startup; correction round four
- Owner: `/root/x05_implementer`
- Independent reviewer: `/root/x05_reviewer`
- Correction dispatch commit: `9854935aa297492c07221881a6052884ee642f27`
- Clean coordinator base/state pin: `326d07b16f7527cd1995bc88c965daf059ec28d4`
- Prior review receipt: `d9e36c574bb03991cb5607e12cd0255ddd940fe4`
- Branch/worktree: shared `main` checkout
- Owned product paths: `internal/transport/`, `cmd/mastarr/`
- Handoff path: `docs/execution/handoffs/C-04-correction-round4.md`
- Product commit: `1b5cb04be4dab3ade4e8f433e0a1aa7f6e3da475`
- Coordinator-owned state was not edited

## Correction scope

This checkpoint closes the four independent transport findings while keeping
the existing strict generated boundary, request-local idempotency state,
sanitized errors, and configuration gate.

### R3d: timestamped durable release

Every reservation now records one request-local `CreatedAt` at acquisition.
The exact timestamp and owner `AttemptID` are carried into an append-only
SQLite release event. SQLite reserve, release and completion paths validate
the timestamp and read it back from the immutable row. A real public
SQLite-backed storage-root route regression proves that a known rollback
releases the reservation, permits an exact same-key retry, and records a
completed retry without losing the original identity.

### R3e: conservative configuration rollback classification

After a configuration persistence failure, the write gate still reloads the
durable manager before it is released. A valid request with an unchanged
pre-mutation revision and an authoritative active read is classified as
no-effect and may release its exact reservation. A changed or otherwise
present target stays pending; an unreadable target also stays pending. Create
operations no longer release merely because a row with the requested ID is
present. Path-mapping DELETE now proves an API-owned retired row using the
active-versus-retained lookup and the exact original revision. No uncertain
or conflicting state is released from a field mismatch or a revision change.

### R3f: post-mutation PATCH read-back

The built-in recovery owner treats a strong `If-Match` as the request's
precondition identity, then validates the requested fields against current
read-back state. It does not require the pre-mutation revision to equal the
post-mutation revision. A changed revision with matching requested fields, or
an otherwise proven no-op, produces the current resource and current ETag;
the recovery path never invokes a second PATCH. Synthetic fresh-manager
lost-completion tests cover connection, storage-root and path-mapping PATCH
responses, including preservation of the post-mutation revision.

### R3g: known pre-effect route failures

The policy releases a durable reservation only for a strict, sanitized
`service_unavailable` or `not_ready` problem returned by a route proven to be
unassembled before dispatch. Injected dependencies are excluded from this
classification, so a service that returns an unavailable error after doing
work remains unreplayable pending evidence. A public route regression proves
an unassembled connection POST releases and a fresh assembled server retries
the exact key with one dispatch. A companion regression proves an injected
unavailable service remains pending. Generic persistence failures, upstream
failures and other 503 responses remain pending.

## Verification

All checks used synthetic IDs, endpoints, bodies and temporary SQLite roots.
No credentials, private coordinates, live services or media data were used.

| Command or scenario | Result / exit status | Evidence |
| --- | --- | --- |
| `gofmt -w internal/transport/... cmd/mastarr/...` | passed, exit 0 | Owned Go files formatted |
| `git diff --check` | passed, exit 0 | Product diff hygiene |
| `GOWORK=off go test ./internal/transport ./cmd/mastarr -count=1 -timeout=180s` | passed, exit 0 | Focused transport/startup suite |
| `GOWORK=off go test -race ./internal/transport ./cmd/mastarr -count=2 -timeout=240s` | passed, exit 0 | Repeated race suite |
| `GOWORK=off go test ./... -count=1 -timeout=240s` | passed, exit 0 | Root package matrix |
| `GOWORK=off go vet ./...` | passed, exit 0 | Root vet |
| `GOWORK=off go mod verify` | passed: `all modules verified` | Root module verification |
| `TestPublicSQLiteRollbackReleasePersistsCreatedAtAndAllowsRetry` | passed | Production SQLite persistence and public route release/retry |
| `TestDefaultPatchRecoveryRecognizesPostMutationRevision` | passed for connection, storage-root and path-mapping cases | Fresh-manager read-back without a second PATCH |
| `TestDefaultPathMappingDeleteRecoveryUsesRetainedRevision` | passed | Retained API mapping identity and exact revision |
| `TestKnownPreEffectRouteFailureReleasesDurableKey` | passed | Unassembled route release and one assembled retry dispatch |
| `TestInjectedUnavailableRouteRemainsPending` | passed | Injected service unavailable response is not released |
| `GOWORK=off go test -race ./internal/transport ./cmd/mastarr -run 'Test(DefaultPatchRecovery|ConfigurationFailureClassifier|KnownPreEffect|InjectedUnavailable|PublicSQLiteRollback)' -count=5 -timeout=240s` | passed, exit 0 | Repeated focused correction probes |
| Product pre-commit hook | passed, exit 0 | Generation, staged generation, API/Vacuum, architecture and fast Go checks |
| `env GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | passed, exit 0; final marker `guardrail checks passed (ci)` | Full offline generation, staged generation, Vacuum for bundle and standalone contracts, lint/architecture, root/UI/tools/client tests, race, vet, module verification and cross-build checks |

## Boundaries and blockers

- The existing `IdempotencyRecovery` callback remains the production seam for
  nonconfiguration application mutations. When an application route has no
  assembled owner, its pending or unreplayable response remains durable and
  cannot blind-redispatch. This checkpoint does not invent read-back logic for
  action, discovery, media, trash or workflow services.
- G-01 Arr native registration/import writes remain fail-closed, F-05
  unsupported filesystem operations remain fail-closed, and Seerr remains
  read-only. No native or live write was enabled by this correction.
- No schema, API contract, client module, `go.work` or local replace directive
  changed.

## Resume checkpoint

- Product tree: `1b5cb04be4dab3ade4e8f433e0a1aa7f6e3da475`
- Product checks are green and the exact tree is ready for independent review.
- Next action: commit this handoff separately, then coordinator records both
  SHAs in `docs/execution/state.json` and dispatches independent review.
- No state files or unrelated files were edited.
