# C-04 independent review, round four

## Decision

`changes_requested`.

R2b is closed: each configuration generation owns a distinct credential
manager, replacement closes the prior generation, and shutdown closes the
active generation once. R1, R3b, R4, R5 and R6 remain closed in the reviewed
scope. R3c is not closed. Four deterministic failures leave durable
idempotency reservations without a safe terminal transition:

1. the production SQLite release record omits required `created_at`, so even a
   read-back-proven rollback cannot release its key;
2. the connection PATCH rollback classifier treats the original, unchanged
   resource as a materialized mutation;
3. successful PATCH read-back requires the pre-mutation `If-Match` value to
   equal the post-mutation revision, so fresh-process recovery cannot complete
   a lost response;
4. nonconfiguration mutations have no production recovery owner, so a known
   pre-effect route failure permanently consumes the key.

These are P1 findings because they break durable same-key retry/reconciliation
and can strand an operation across restart. They do not cause a blind second
dispatch; they fail closed as `idempotency_pending`, but no production path can
resolve that state.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of the product author.
- Product base: `69d0165155982a333c645695c210e42a48f14cac`.
- Product commit: `14b692080f763f36a800fda021374729a00d4d7f`.
- Product tree: `84b94243c63bf9e384c7abbda817ae4cad708039`.
- Handoff commit: `f0c021a75bb4151766ab9cc435c88a5de7508438`;
  direct parent is the product commit.
- Reviewed handoff:
  `docs/execution/handoffs/C-04-correction-round3.md`.
- Product diff SHA-256 for `internal/transport/` and `cmd/mastarr/` from base
  to product: `c1d0d3003765c56cab272760481123ae81d97fe361cf517377141b8ba83b6ec9`.
- Product archive SHA-256 for those paths:
  `258c34492c2824afa31dcf50d72659cd771e6fa4f03a50b8c2fd0f6fcc6a817e`.
- Key reviewed file SHA-256 values:
  - `internal/transport/server.go`:
    `a26bb7c6f9395f8f43989b194f2409197c38b1a19344fedcde3ff74abb4c1dfe`;
  - `internal/transport/idempotency_recovery.go`:
    `5c43c087ae6ca87113bedd9199231b99d6b93f01927e946bc9ee02ac53a25eee`;
  - `cmd/mastarr/persistence.go`:
    `689a5264e3bfc2f783204120fa7e9df344a7ce2eea383fd055c01a0923b74f8f`;
  - correction handoff:
    `ae11222f2437f54e2ff8ef416c78af2faee52f5b47be0b6d0d7e33a5b4d993e6`.
- Review ran in a clean detached worktree. Temporary synthetic probe files
  were removed before this receipt. No product, state, schema, API, adapter,
  client, or live-data file was edited.

## Findings

### R3d — P1: production SQLite cannot persist a release event

`releaseCurrentIdempotency` constructs its release record at
`internal/transport/server.go:2209-2218` without `CreatedAt`.
`sqliteIdempotencyPersistence.release` writes that empty value at
`cmd/mastarr/persistence.go:699`. The unchanged schema requires a non-empty
value at `migrations/000001_initial.up.sql:391`. The insert therefore fails.
`finishConfigurationMutation` ignores the release error at
`internal/transport/server.go:337-338`, after which response completion writes
a non-replayable terminal event and the key remains held.

Independent production-SQLite probe:

1. create a valid storage root and install a SQLite trigger that aborts its
   final update after the in-memory mutation starts;
2. send a keyed mutation through the public generated HTTP route;
3. observe HTTP 503 and zero durable `released` event rows;
4. remove the trigger, construct a fresh server over the same database, and
   replay the exact request/key;
5. observe HTTP 409 `idempotency_pending`; a different key succeeds with
   HTTP 201.

The probe passed as a regression detector 20 ordinary iterations and 10 race
iterations. Its temporary source SHA-256 was
`203ab14b55bfd35cf7c72811da44eb4991c9cabce5d3307f22dc76e9caacea07`.
Existing tests missed the defect: the transport release test uses a fake store
that does not enforce `created_at`, while the SQLite unit test supplies a
prebuilt record that already contains it.

### R3e — P1: PATCH rollback classification cannot release an unchanged resource

For connection PATCH, `configurationEffectMaterialized` first requires the
original `If-Match` to equal the reloaded revision. When it does, it computes
`changed` as requested fields differing from the current resource at
`internal/transport/idempotency_recovery.go:343-347` and returns that value as
`materialized`. After a rolled-back transaction, the authoritative resource
still has the original revision and old field values. A requested new label
therefore produces `materialized=true`, exactly opposite to the no-effect
result needed to release the reservation.

Independent public-route probe used a configuration manager whose PATCH
mutated memory and whose persistence callback returned
`ErrConfigurationStore`; reload returned the original durable manager. First
request returned 503. Exact same key returned 409 pending after reload, while a
different key succeeded. Twenty ordinary and ten race iterations reproduced
the result.

Storage-root and path-mapping classifiers also treat a revision mismatch as
materialized at `internal/transport/idempotency_recovery.go:396-399` and
`:445-448`; correction should cover all PATCH families rather than only the
connection fixture.

### R3f — P1: successful PATCH lost-response recovery is impossible

Fresh-process recovery calls `sameConnectionPatch`, `sameStorageRootPatch`, or
`samePathMappingPatch`. Each requires the request's pre-mutation `If-Match`
revision to equal the current post-mutation revision at
`internal/transport/idempotency_recovery.go:234-272`. A successful PATCH
necessarily publishes a new revision, so the read-back owner cannot recognize
its own completed effect.

Independent probe applied a connection PATCH, then failed completion
persistence. The label and revision changed. A fresh server with completion
persistence restored returned 409 pending for the exact request/key and did
not redispatch. Twenty ordinary and ten race iterations reproduced the result.
This is safe against duplicate mutation but has no terminal recovery path.

### R3g — P1: nonconfiguration reservations have no production owner

`New` installs only `recoverConfigurationIdempotency` when no callback is
provided (`internal/transport/server.go:224-225`). That callback returns no
decision for nonconfiguration routes. `cmd/mastarr` does not assemble a
composite route-specific owner. The handoff also records that
nonconfiguration routes remain pending absent an injected owner.

Independent public-route probe sent a keyed
`POST /api/v1/connection-checks` while its dependency was unassembled. The
known pre-dispatch 503 response consumed the durable reservation. After a fresh
server was assembled with the dependency, exact same key returned 409 pending
with zero dispatch calls; a different key dispatched successfully. Twenty
ordinary and ten race iterations reproduced the result. Temporary transport
probe source SHA-256, including R3e/R3f/R3g, was
`4d0bc8ebcc2b399313899ae91d1e3d04ba84fe6927d68e6f2147e8e1a8597699`.

The callback architecture is sufficient; a new mutable table is not required.
Existing immutable rows can support the boundary if every route has a
production owner, durable request intent is exact, release follows only
authoritative no-effect read-back, and post-dispatch completion follows exact
effect read-back. Current production assembly does not meet those conditions.

## Closed findings and regression review

- R2b closed. `runtimeConfigurationOwner` swaps manager and credential manager
  as one ownership unit (`cmd/mastarr/main.go:54-96`). Reload opens a fresh
  crypt manager and closes it on construction/read/verification failure. The
  replaced manager closes its own crypt manager after the swap; repeated
  shutdown detaches and closes the active pair once. Focused ordinary/race
  tests passed.
- R1 remains closed. Generated route delegation, nil dependency 503 behavior,
  bounded strict decoding and sanitized problems passed the full gate.
- R2a's manager rollback/reload visibility remains closed, but its promised
  same-key release is blocked by R3d/R3e.
- R3b remains closed. Canonical request JSON preserves distinct integer values
  around 2^53 and equivalent object ordering.
- R4, R5 and R6 remain closed. Strong ETag validation, recursive bounds,
  managed credential-state distinction, origin policy, startup/readiness and
  graceful shutdown checks remained green.
- No tracked `go.work`, local `replace`, migration drift, secret, private
  coordinate, live call, or generated-route coverage drift was found.

## Checks

- `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/transport ./cmd/mastarr -count=10 -timeout=300s`:
  passed; transport `0.429s`, process package `4.582s`.
- Same package set with `-race -count=5 -timeout=420s`: passed; transport
  `1.520s`, process package `49.287s`.
- Named correction tests for durable owner recovery, built-in configuration
  recovery, rollback release, SQLite release and credential-owner swap:
  `-count=20` passed; transport `0.418s`, process package `1.988s`.
- Same named set with `-race -count=10`: passed; transport `2.048s`, process
  package `16.949s`.
- Independent transport probe set for PATCH rollback, successful PATCH lost
  completion and unassembled route ownership: ordinary `-count=20` passed in
  `0.460s`; race `-count=10` passed in `1.495s`.
- Independent production-SQLite release probe: ordinary `-count=20` passed in
  `1.690s`; race `-count=10` passed in `16.026s`.
- Root `go vet ./...`, `go mod verify`, lint, architecture and planning checks:
  passed; module verification reported `all modules verified`, lint reported
  zero issues, and planning reported `53 tasks, 60 acceptance cases` with all
  local links resolved.
- Generated route source matrix: 55 generated routes, 55 dependency fields,
  55 server methods and 55 delegation references; no missing, extra or
  duplicate entries.
- Linux CGO-free builds for `./cmd/mastarr` on amd64 and arm64: passed.
- `git diff --check`: passed. No tracked `go.work` or local module replacement.
- Full offline
  `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci`:
  exit 0 with final marker `guardrail checks passed (ci)`. This included clean
  generation/staged-generation, root and standalone Vacuum, API, lint,
  architecture, ordinary and race tests, vet, module verification and Linux
  cross-builds. Root storage race completed in `199.722s`; trash race completed
  in `65.311s`.

Passing generic gates do not close the deterministic production-SQLite and
fresh-process failures above.

## Acceptance disposition

- A-38: blocked for C-04. Configuration state rollback remains atomic, but
  exact-key retry after proven rollback is not durable.
- A-39: blocked for C-04. Restart visibility and ETag checks pass, while PATCH
  lost-response recovery cannot recognize the new revision.
- A-42: accepted for C-04's credential ownership/redaction contribution. R2b
  is closed; the open findings concern idempotency ownership.
- A-46: accepted for C-04. No application authentication or trusted forged
  actor was introduced.
- A-47: accepted for C-04. Conservative origin/CORS behavior remains green.
- A-56: blocked for C-04. Conflict and canonical-digest behavior pass, but
  durable release and route-wide recovery ownership remain incomplete.

## Integration recommendation

Do not mark C-04 complete. Preserve the product and this receipt. Correct the
missing release timestamp, classify rollback versus materialized PATCH state
using explicit pre/post evidence, make successful PATCH read-back independent
of equality to the stale request revision, and assemble a production terminal
owner for every durable mutation reservation. Then rerun the exact SQLite,
fresh-process and nonconfiguration probes plus the full offline gate.
