# C-04 independent review, round five

## Decision

`changes_requested`.

R3d and R3e are closed. R3f closes the narrow post-mutation happy path for
connection, storage-root and path-mapping PATCH, and R3g releases a proven
unassembled route without dispatch. R2b and the earlier R1/R2a/R3b/R4/R5/R6
fixes remain closed.

Four P1 findings remain:

1. the built-in recovery owner cannot distinguish successful lost PATCH from
   rejected stale PATCH and converts rejected request into durable 200;
2. CREATE recovery cannot distinguish successful creation from duplicate
   conflict and converts rejected request into durable 201;
3. recovered and ordinary durable replays discard `ETag`, so the promised
   current precondition token is absent;
4. `cmd/mastarr` still assembles no post-dispatch recovery owner for
   nonconfiguration mutations, and even the built-in connection credential
   PATCH explicitly has no terminal owner. These attempts remain pending
   forever after lost completion.

No blind redispatch was observed. The first two findings instead fail open by
claiming an effect that the reviewed request did not perform; the third fails
closed without a production reconciliation path.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author.
- Coordinator base: `326d07b16f7527cd1995bc88c965daf059ec28d4`.
- Product commit: `1b5cb04be4dab3ade4e8f433e0a1aa7f6e3da475`.
- Product tree: `851dee964e9ed36bc0881e52fa8af6a5793970c2`.
- Handoff commit: `33bdebe24f45167deef317d0a45b219ed41e5304`;
  direct parent is product commit.
- Reviewed handoff:
  `docs/execution/handoffs/C-04-correction-round4.md`.
- Product diff SHA-256 for `internal/transport/` and `cmd/mastarr/` from base:
  `e84f33f378729aa29f99eaade3acef4ac2972359122617f4a7c9d7a19fda634c`.
- Product archive SHA-256 for those paths:
  `0059e3d8f48e01ed26194e784168bd418d10581bc554ac6b2e2896595e609abb`.
- Correction handoff SHA-256:
  `7a1ef68fd68153b802f4bb815aafac35abf50dbe5224c493c03f9044651cff57`.
- Key source SHA-256 values:
  - `internal/transport/idempotency_recovery.go`:
    `cdff5068c3c69940ff78af34edf6ae5bd99f97ae7bfab5b7894dd5db34e3929f`;
  - `internal/transport/server.go`:
    `a196457dd54254ddd983c13859414e8f53312f2a18458023d199846022f80997`;
  - `cmd/mastarr/persistence.go`:
    `36e95566dd28da28868c2e363d16cb379a12cf74025336945446197829ae630b`;
  - `cmd/mastarr/main.go`:
    `4955ede298f496603716a60118fdcee7cfed4c1c16747ac570e2a63f1b640e03`.
- Review ran in clean detached worktree. Temporary synthetic probe files and
  build outputs were removed before receipt. No product or state file changed.

## Closed prior findings

### R3d closed: production SQLite release carries reservation timestamp

`reserveIdempotency` now captures one request-local timestamp and
`releaseCurrentIdempotency` requires and forwards it. SQLite reserve,
completion and release reject an empty timestamp. The public SQLite regression
was inspected and rerun repeatedly: first rollback returned 503 with a durable
released event containing `CreatedAt` and `AttemptID`; exact-key retry acquired
a new attempt, returned 201 and completed durably. Changed digest remains a
conflict.

### R3e closed: rollback release is conservative

PATCH classification now releases only when authoritative reloaded revision
equals the exact request precondition. Changed revision stays materialized and
pending; invalid or unreadable evidence remains unknown and pending. CREATE
presence remains pending rather than being treated as no-effect. Path-mapping
DELETE checks active absence, API ownership and exact retained revision.
Inspection and repeated focused/race tests found no release from present,
changed, foreign or unreadable evidence.

### Narrow R3f and R3g paths closed

- Successful lost PATCH read-back for connection, storage-root and
  path-mapping returns current body/revision without a second mutation.
- Retired path-mapping read-back requires retained API record and exact
  original revision.
- All 20 OpenAPI mutation route patterns have an explicit unassembled check.
  Immutable dependency snapshots prevent a route from changing assembly state
  during one request. A strict transport-generated `not_ready` or
  `service_unavailable` response releases only when no service could have been
  called. Injected unavailable response stays pending.
- R2b manager/credential ownership remains generation-bound and closes old
  crypt material only after swap. R1/R2a/R3b/R4/R5/R6 tests and full gate
  remained green.

## Findings

### R3f-a — P1: recovery turns rejected stale PATCH into 200 success

`sameConnectionPatch`, `sameStorageRootPatch` and `samePathMappingPatch` at
`internal/transport/idempotency_recovery.go:242-280` now validate only that
the submitted `If-Match` is syntactically strong. If current fields match the
request body, recovery reports success regardless of whether the original
precondition ever matched.

Independent real-SQLite fresh-manager probe:

1. create connection revision R1, then update it to revision R2 with label
   `after`;
2. directly submit `PATCH {"label":"after"}` with stale `If-Match: R1` and
   observe HTTP 412;
3. submit same request/key through SQLite persistence whose completion write
   is lost; client observes HTTP 503 and reservation remains durable;
4. construct a fresh manager from durable snapshot R2 and a fresh server over
   same SQLite database;
5. exact same request/key returns HTTP 200 and stores a replayable completion,
   although original request was rejected and performed no mutation.

The same ambiguity applies to all three PATCH families. Matching desired
fields and a syntactically valid old ETag do not prove that this attempt
created current revision. Durable request intent needs an attempt-bound outcome
or resource marker that distinguishes successful mutation from stale/no-op
rejection.

### R3f-b — P1: recovery turns duplicate CREATE conflict into 201

Create recovery has the same evidence gap. `sameConnectionCreate`,
`sameStorageRootCreate` and `samePathMappingCreate` compare current visible
fields to request fields but do not bind resource creation to pending attempt.
For connection credentials, it checks only presence of managed credentials,
not exact secret identity.

Independent real-SQLite fresh-manager probe:

1. create an existing connection;
2. directly submit identical CREATE and observe HTTP 409 conflict;
3. lose completion persistence for same keyed request and observe HTTP 503;
4. restart from durable configuration and same SQLite idempotency database;
5. exact same request/key returns HTTP 201 and records successful completion,
   although request created nothing.

Fake-store probes reproduced both R3f-a and R3f-b 50/50 ordinary and 20/20
race iterations. Real-SQLite probes reproduced both 20/20 ordinary and 10/10
race iterations. Final SQLite probe source SHA-256 was
`e9ae015fdf6cb94f02e73571195f5257d27ccb9fe8e6c1ad2db432f8627d057e`;
the smaller transport probe source SHA-256 was
`6d2060dcc6028cc04c7ebaf1d3e9ab23098f18ac97e715beffc8551dfe234b90`.

### R3f-c — P1: recovered response drops current ETag

Real-SQLite PATCH probe returned current revision in JSON but no `ETag`
header. `configurationRecoveryRecord` supplies `ETag`, then
`sanitizeReplayHeaders` calls `http.CanonicalHeaderKey`. Go canonicalizes that
name as `Etag`, while whitelist at `internal/transport/server.go:2386-2395`
accepts only literal `ETag`. The header is therefore dropped from recovery and
ordinary durable replay.

This violates generated response contract and A-39 precondition workflow.
Caller cannot use promised current header for next conditional mutation. The
product recovery test checks body marker and revision stability but never
asserts returned header.

### R3g-a — P1: assembled mutations still lack terminal recovery owner

Known unassembled failure now releases safely, closing narrow prior key-burn
case. Post-dispatch coverage remains incomplete:

- `cmd/mastarr` never supplies `IdempotencyRecovery` or calls
  `SetIdempotencyRecovery`; default owner handles configuration only;
- every nonconfiguration mutation returns no recovery decision after a lost
  completion unless an external assembler injects an owner;
- connection credential PATCH explicitly returns no decision at
  `internal/transport/idempotency_recovery.go:66-70`;
- correction handoff itself records this remaining boundary.

Such attempts return stable 409 pending and do not redispatch, but no
production component can reconcile or complete them after restart. Callback
seam alone is not a terminal owner. Every assembled mutation needs a
route-specific authoritative owner, or a proven pre-effect/no-effect release,
before durable reservation can be considered complete.

## Checks

- `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/transport ./cmd/mastarr -count=10 -timeout=300s`:
  passed; transport `0.746s`, process package `10.870s`.
- Same package set with `-race -count=5 -timeout=420s`: passed; transport
  `1.887s`, process package `46.435s`.
- Named correction regression set, ordinary `-count=20`: passed; transport
  `0.455s`, process package `1.960s`.
- Named correction set with `-race -count=10`: passed; transport `1.463s`,
  process package `16.920s`.
- Independent fake-store rejected-operation probes: passed as regression
  detectors 50 ordinary iterations (`0.459s`) and 20 race iterations
  (`1.627s`).
- Independent real-SQLite fresh-manager rejected-operation probes: passed as
  regression detectors 20 ordinary iterations (`2.904s`) and 10 race
  iterations (`30.637s`).
- `GOWORK=off GOPROXY=off GOSUMDB=off go vet ./internal/transport ./cmd/mastarr`:
  passed.
- `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify`: passed, `all modules verified`.
- Linux amd64 and arm64 CGO-free `./cmd/mastarr` builds: passed.
- Architecture check passed. Planning reported `53 tasks, 60 acceptance cases`
  with local links resolved.
- No tracked `go.work`, local `replace`, migration, schema, OpenAPI or generated
  artifact drift was found. `git diff --check` passed.
- Full offline
  `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci`:
  exit 0 with final marker `guardrail checks passed (ci)`. Generation and
  staged generation, root and standalone Vacuum, API, lint, architecture,
  ordinary/race tests, vet, module verification and Linux cross-builds passed.
  Root storage race completed in `199.587s`; trash race completed in `66.395s`.

Generic gate does not exercise false-success and missing-ETag cases above.

## Acceptance disposition

- A-38: accepted for corrected rollback/release contribution; no source
  ownership regression found.
- A-39: blocked. Rejected stale PATCH becomes 200 and recovered/current ETag is
  absent.
- A-42: accepted for C-04 credential ownership/redaction contribution. Exact
  credential PATCH recovery remains part of R3g-a idempotency blocker.
- A-46: accepted. No application authentication or trusted forged actor was
  introduced.
- A-47: accepted. Conservative origin/CORS behavior remains green.
- A-56: blocked. Durable recovery can falsely terminalize rejected operations,
  and assembled mutation families lack route-specific terminal owners.

## Integration recommendation

Do not mark C-04 complete. Preserve product and receipt. Bind recovery to an
attempt-specific handler/effect outcome so stale PATCH and duplicate CREATE
cannot be inferred from coincident current state; preserve `ETag` through
canonical header sanitization; assemble a terminal recovery owner for every
durable mutation, including credential PATCH and nonconfiguration routes.
Rerun exact real-SQLite probes plus full offline gate after correction.
