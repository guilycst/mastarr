# C-04 independent review, round three

## Decision

`changes_requested`.

R2a closes the prior SQLite rollback and manager-visibility defect, and R3b
closes the lossy large-number digest defect. R1, R4, R5 and R6 remain closed.
R3a still has one P1: a reservation whose completion cannot be inserted has no
production recovery owner. The same defect permanently holds requests whose
handler returned a known retryable, no-effect failure, including a configuration
write that SQLite rolled back and the new reload fence proved absent. One P2 is
also present in R2a's manager swap: reloaded managers reuse the credential
manager, but replaced/current managers are not closed through their documented
secret-release lifecycle.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of the product author.
- Product base/dispatch checkpoint:
  `4f8e3120650c4c31532bf72255e8489c08c8a7fc`.
- Product commit:
  `9337475cceaee1c2401b8baaa05f45f40e0789ed`, whose direct parent is the
  product base.
- Product tree: `075d1b9384bab9339edd7373423e6b5d2cba634f`.
- Correction handoff:
  `a08180a50df979c1f4ae6425ff4a02b42a4dd021`, whose direct parent is the
  product commit.
- Handoff tree: `b8bce63af2127e1e427a9a13e23ceeadd029f5d8`.
- Exact review checkpoint:
  `5628ad73ad29b06862b99b09a5c2c222d0aad8f9`, whose direct parent is the
  handoff.
- Review-checkpoint tree:
  `e3db02d446c89c1c0322517c0624d2e12bc6ae1f`.
- Scoped correction diff SHA-256:
  `a4a763600b0f7836a97c7955500068dc0beb6b01ed5c643338810fc880c20ed9`.
- Product-scope archive SHA-256:
  `7ff3fa713429f66ed6cda0587b5f0f5e1b09838800ecaea7f5a27e881350718d`.
- Reviewed product paths: `internal/transport/` and `cmd/mastarr/`. No product
  path changed between the product commit and review checkpoint.
- Prior receipt:
  `461b0c06b0a3094f5da28ca2920400b639e22a61`.
- Acceptance reviewed: A-38, A-39, A-42, A-46, A-47 and A-56.

Review ran from a clean detached worktree at the exact checkpoint. Independent
probes used one temporary SQLite journal, synthetic configuration and generated
HTTP handlers only. Probe source SHA-256 was
`287bec112eb884841430d4374a7d36834eb7f640dc32f864915b69b43a7caf6f`;
the file was removed before this receipt. No product, state, schema, generated
contract, live service, credential, private coordinate or media data changed.

## Findings

### R3c - P1 - Failed completion and known no-effect failures have no durable recovery path

The transport reserves every keyed mutation before generated dispatch. It then
calls `Complete` for every handler response, including retryable 408/429/5xx
responses. Such responses are written with `Replayable=false`; `Load` converts
both those completions and reservation-only records into
`ErrIdempotencyPending`. There is no production callback, worker, endpoint or
startup path that owns the captured handler result after `Complete` fails, and
immutable `idempotency_records` cannot be released. The producer test recovers
only by calling the fake store's test-only `completeLast` helper with an
in-memory copy of the lost response.

Independent production-SQLite probes established both unsafe liveness cases:

1. A trigger rejected the final `storage_roots` publication after the manager
   had produced a candidate. The first keyed HTTP request returned sanitized
   503, SQLite contained zero root rows, and the configuration reload restored
   an empty manager before unlock. After removing the fault and constructing a
   fresh server, the exact same key returned 409 `idempotency_pending`
   indefinitely. A different key immediately succeeded, proving the original
   request had a known safe no-effect result but could not follow the required
   same-key retry path.
2. A trigger allowed the 102 reservation row but rejected the completion
   companion after a synthetic 201 handler result. The client received 503 and
   one dispatch occurred. After removing the fault, two fresh-server retries
   both returned 409 `idempotency_pending`; dispatch count stayed one. This
   correctly prevents blind redispatch, but there is no production mechanism
   to read back/reconcile the effect and durably complete the exact reservation.

Both probes passed 20 ordinary repetitions and 5 race repetitions. Relevant
source is `internal/transport/server.go:1407-1488`, especially unconditional
completion at `1467-1473`, pending rejection at `2014-2015`, and completion
construction at `2054-2067`; SQLite retains reservation-only state at
`cmd/mastarr/persistence.go:449-478` and appends the companion at `561-582`.

Required correction: give every durable reservation an explicit terminal or
reconciliation owner. Known pre-effect/no-effect retryable failures must be
released or transitioned durably so the original key can retry. A lost
post-dispatch completion must retain enough durable route/resource evidence for
a fresh process to reconcile and write the exact terminal completion without
blind dispatch. A test-only retained response is not a restart recovery path.

### R2b - P2 - Configuration reload bypasses manager secret-release ownership

`configuration.Manager.Close` explicitly zeroes cached static credentials and
closes its credential manager. The startup defer closes only the original local
`manager`. Recovery builds a new manager with the same `crypt` pointer and
returns it to `Server.SetConfiguration`, but neither the replaced manager nor
the active reloaded manager is closed through the manager lifecycle. Closing a
replaced manager directly is also unsafe under the current ownership model
because it would close the shared `crypt` used by the replacement.

Evidence: `cmd/mastarr/main.go:200-215` owns the original manager and shared
credential manager, while `215-242` constructs replacements without updating
the deferred owner. `internal/transport/server.go:211-219` replaces the pointer
without closing or transferring ownership. `internal/configuration/configuration.go:379-396`
defines the omitted secret-release work.

Required correction: define one ownership-safe swap/close protocol. Replaced
managers must zero their cached static credential bytes without invalidating
services still owned by the replacement, and shutdown must close the active
manager exactly once.

## Prior finding disposition

### R2a - Closed for rollback visibility; retry remains blocked by R3c

Every built-in configuration read now holds `configurationGate.RLock`, and all
nine built-in create/update/retire methods hold the write lock through the
SQLite callback and recovery. A persistence failure reloads committed API state
and managed credential IDs before unlock; reload failure clears manager and IDs
and marks readiness false. The independent SQLite rollback probe observed zero
durable rows and no candidate through the reloaded manager. No migration changed.

The required exact-key retry is not complete because R3c turns the known 503
into a permanent pending idempotency record.

### R3a - Partially closed

Reservation acquisition is durable and atomic. Same-process and cross-server
source/tests bind one digest; a loser reads the reservation and never dispatches.
Successful completion is read back before local caching. Reservation/completion
rows survive SQLite reopen. Lost completion safely prevents blind redispatch.
R3c remains the missing recovery half.

### R3b - Closed

`canonicalJSON` uses `json.Decoder.UseNumber`. Independent repeated ordinary and
race checks kept `9007199254740992` and `9007199254740993` distinct while
canonicalizing equivalent object key order.

### R1, R4, R5 and R6 - Remain closed

An independent source matrix found 55 generated operations, 55 dependency
fields, 55 server methods and 55 matching delegation references, with no
missing or extra name. Nil dependencies remain sanitized 503s. Strong ETag,
recursive bounds, managed/static/missing credential classification, readiness,
origin/CORS and error sanitization regressions stayed green. No migration,
tracked `go.work`, local `replace`, generated DTO boundary change or native
write enablement was introduced.

## Independent checks

All Go and aggregate commands used
`GOWORK=off GOPROXY=off GOSUMDB=off`.

- Exact ancestry, tree identity, clean detached worktree, scoped no-drift,
  `git diff --check`, migration no-drift, no tracked `go.work` and no local
  `replace`: passed.
- Generated/interface/dependency/implementation/delegation source matrix:
  passed `55/55/55/55`, no missing or extra names.
- Independent SQLite rollback and lost-completion probes:
  `go test ./cmd/mastarr -run 'TestReviewer(KnownRollback|SQLiteLostCompletion)' -count=20 -timeout=240s`
  passed in package time `2.828s`.
- Same independent probes under race, `-count=5 -timeout=240s`: passed in
  package time `15.475s`.
- Large-number/object-order digest test, `-count=100`: passed in `0.294s`.
- Same digest test under race, `-count=50`: passed in `1.450s`.
- Focused ordinary tests, `go test ./internal/transport ./cmd/mastarr -count=10 -timeout=240s`:
  passed; package times `0.475s` and `9.202s`.
- Focused race tests, `go test -race ./internal/transport ./cmd/mastarr -count=5 -timeout=420s`:
  passed; package times `1.319s` and `36.549s`.
- Focused and root `go vet`: passed. Root `go mod verify`: `all modules verified`.
- Lint and architecture: passed with zero issues; standalone client boundaries
  passed.
- Planning: passed, `53 tasks, 60 acceptance cases; local links resolve`.
- Linux CGO-free amd64 and arm64 `./cmd/mastarr` builds: passed.
- Full offline `./scripts/check-guardrails.sh --ci`: exit 0 with final marker
  `guardrail checks passed (ci)`. Generation and staged generation, root and
  standalone Vacuum, lint, architecture, all module ordinary/race/vet/module
  checks and Linux cross-build matrix passed. Root storage race completed in
  `197.880s`; trash race completed in `65.519s`.

## Acceptance disposition

- A-38: blocked for C-04. Durable config rollback/reload now passes, but the
  original idempotency key cannot retry a proven rolled-back write.
- A-39: blocked for C-04 for the same exact-key retry/restart boundary; ETag and
  manager snapshot behavior otherwise pass.
- A-42: blocked for C-04 until active/replaced configuration managers have an
  ownership-safe secret-release lifecycle.
- A-46: accepted for C-04. No authentication or trusted forged identity was
  introduced.
- A-47: accepted for C-04. Conservative origin/CORS behavior remains green.
- A-56: blocked for C-04. Changed-payload and numeric digest behavior pass, but
  retryable/lost-completion idempotency records lack a durable recovery path.

## Integration recommendation

Do not integrate C-04 as complete. Preserve product commit and this receipt,
correct R3c and R2b in a new atomic product commit, then rerun independent
SQLite lost-response/restart probes and the full offline gate.
