# C-04 independent review, round two

## Decision

`changes_requested`.

R1, R4, R5 and R6 are closed for the correction scope. R2 and R3 remain open
with two P1 findings, and a new P2 is present in R3's request-digest boundary.
The happy-path restart tests and full offline guardrail pass, but failures after
an in-memory configuration mutation can expose state that SQLite rolled back,
and failure to persist an idempotency result permits a fresh process to
dispatch the same mutation again. Distinct valid large-integer request bodies
also collapse to one digest instead of producing the required changed-payload
conflict.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Dispatch/state checkpoint:
  `8fbbe1d09687f54b0d2015cde27134295e097d65`.
- Route-assembly product:
  `81e329a812c5b535a849db5250b89232c6b89ad3`, whose direct parent is the
  dispatch checkpoint.
- Final correction product:
  `b5b3e521618405d8b51deb10ff40de4d2d0de37a`, whose direct parent is the
  route-assembly product.
- Final product tree: `2dff94f00b9ac8902f9e00254226c0b7d1cc1a9d`.
- Correction handoff:
  `56005a180f57bea44fd3e55a4202b557728d5e32`, whose direct parent is the
  final product.
- Handoff tree: `e1c8f342035916428435542079c6f6db009c0337`.
- Exact review checkpoint:
  `3f523d39f3f211a9b9bfe7bf360703705bd3801b`, whose direct parent is the
  handoff.
- Review-checkpoint tree:
  `c542311116ede8931f1b53edeebf2b36b57955a9`.
- Scoped correction diff SHA-256:
  `d2da2c10757b5b1a7161f77e785c203454d83eba826b4f086922ec5e04c5b0b6`.
- Final product-scope archive SHA-256:
  `c709e7961c58c475cd2ebf23026361c1d6ae5d5e95abc48d967ab9538f75c2dd`.
- Reviewed product paths: `internal/transport/` and `cmd/mastarr/`. There is
  no product-path drift from the final product to the review checkpoint.
- Prior receipt: `33014ad84ea69f7a3f7960de2278001d63b6c3cb`.
- Acceptance reviewed: A-38, A-39, A-42, A-46, A-47 and A-56.

Review ran from a clean detached worktree at the exact checkpoint. Independent
probes used only temporary SQLite databases/listeners, synthetic configuration,
synthetic response bodies and in-memory fake dependencies. Their source hashes
are:

- configuration/idempotency failure probes archive:
  `32058fe7f365ca86c0554818337122dd7d9e1441835a46ea2970edcb88d175f6`;
- large-integer digest probe:
  `e565e47045abe3be3018e22feb2459586b5bfee0c264bc5d0a29827716eb1943`;
- ETag and nested-bound closure probes:
  `19a187362917d908fcf850a61df7f5c7f2a68ec33c32cd757209e30294b6bde3`.

The probe files were removed before this receipt. No product, state, schema,
generated contract, live service, private coordinate, real credential or media
data was changed.

## Findings

### R2a - P1 - A failed SQLite publication leaves rolled-back configuration visible and blocks retry

The persistence callbacks begin a SQLite transaction, create a provisional
row, and then invoke the configuration manager. The manager publishes the
candidate into its in-memory maps and snapshot before the callback performs
the final SQL update and transaction commit. If either of those later steps
fails, SQLite rolls back but the manager has no inverse operation and is not
rebuilt from durable state.

Independent reproduction used the production
`newSQLiteConfigurationPersistence` with a SQLite trigger that aborts the
final `storage_roots` update:

1. POST of a valid API-owned root returned sanitized 503
   `persistence_unavailable`.
2. SQLite contained zero rows for that ID because the transaction rolled back.
3. `configuration.Manager.GetStorageRoot` returned the supposedly failed root,
   so concurrent GETs can expose uncommitted data.
4. Retrying the same POST reached the manager's duplicate-ID check and returned
   409, even though no durable row existed.

The exact probe passed 20 ordinary and 10 race repetitions. The same ordering
exists in all create, update and retire callbacks; connection credential rows
share the transaction but do not make the already-published manager state
reversible. Revision invalidation can likewise run before durable commit.

Evidence:

- `cmd/mastarr/persistence.go:72-98` calls the connection manager before
  `persistConnection` and `Commit`.
- The same ordering appears at `101-117`, `120-140`, `143-176`, `179-218` and
  `221-289` for updates, retirement, roots and mappings.
- `internal/transport/server.go:719-826` passes the real manager mutation into
  those callbacks.
- The happy-path process restart fixture passes, but it does not inject a
  failure after the manager callback.

Required correction: make manager publication and durable publication one
recoverable state transition. A persistence or commit failure must leave the
manager at the exact pre-request snapshot, and no concurrent read may expose a
candidate before its durable commit. Lost commit responses need read-back and
an explicit uncertain/reconciliation result rather than an ordinary retryable
failure backed by divergent state.

### R3a - P1 - Failed idempotency persistence permits blind redispatch after restart

After the generated handler returns, the policy writes the successful response
to the process-local cache before calling the durable `Save`. If `Save` fails,
the client receives 503, but the local success remains. A same-process retry
replays that success from memory and never retries the durable save. A fresh
process finds no durable record and dispatches the mutation again.

Independent reproduction used two fresh `Server` instances, one shared fake
repository whose first save returned a lost-response error, and a counted
mutation handler:

1. The first handler dispatch occurred and produced 201; the failed save made
   the externally visible response 503.
2. Same-process retry returned cached 201, while the durable repository still
   contained no record and `Save` was not retried.
3. A fresh server loaded no record and invoked the mutation handler a second
   time before its save succeeded.

The exact probe passed 20 ordinary and 10 race repetitions. In production the
configuration commit and idempotency insert are separate transactions, so a
crash or lost save response in this window has the same shape. For action or
control dependencies, the second dispatch can repeat an external side effect;
there is no read-only reconciliation in this transport path.

Evidence:

- `internal/transport/server.go:1287-1301` calls
  `rememberIdempotency` before `persistIdempotency` and returns the persistence
  problem without removing or marking the local response uncertain.
- `internal/transport/server.go:1733-1755` replays that local entry before a new
  reservation can be created.
- `internal/transport/server.go:1790-1827` has no durable pending/uncertain
  intent or read-back protocol around dispatch.
- `cmd/mastarr/persistence.go:474-506` persists only the completed response
  after the application mutation has already committed.

Required correction: durably reserve the key and digest before dispatch, then
complete or reconcile that exact record after dispatch. A failed completion
write must remain discoverable as uncertain across restart. Same-process
replay must not suppress repair of the durable record, and a fresh process must
observe/read back before any second mutation.

### R3b - P2 - Distinct valid large integers collapse to one idempotency digest

`requestDigest` decodes JSON into `any`, so numbers become `float64`, then
re-encodes that value. Distinct valid integers above the exact float64 range can
therefore produce identical canonical bytes. The generated workflow schema
uses Go `int` for `deadlineSeconds` and sets no upper bound, so both values in
the reproduction are valid and distinct on the supported 64-bit targets.

Independent reproduction sent the same route and idempotency key with otherwise
identical workflow-create bodies whose deadlines were `9007199254740992` and
`9007199254740993`. The second request replayed the first 202 response instead
of returning 409 `idempotency_conflict`. The mutation handler was called only
once, proving the changed payload was treated as identical. This passed 50
ordinary and 20 race repetitions.

Evidence: `internal/transport/server.go:1684-1710`, especially the
`json.Unmarshal` into `any` at `1694-1699`. Required correction: canonicalize
JSON without lossy numeric conversion, for example by preserving `json.Number`
or exact numeric lexemes. Add changed-payload coverage around the float64
precision boundary.

## Prior finding disposition

### R1 - Closed for the correction scope

The generated strict interface, `RouteDependencies`, and `Server` each cover
the same 55 operation names. An independent source matrix found no missing,
extra or duplicate dependency field, no missing implementation and no method
without a matching delegation reference. Nil dependencies return the typed
sanitized 503, while an injected dependency receives the generated request and
its error is passed to sanitized response handling.

The runtime still supplies nil for action, discovery, media, descriptor, trash,
review, scan and workflow services. The handoff records that integration
blocker accurately. This review closes the former unconditional fallback
implementation; it does not claim those product services are operational.

### R4 - Closed

`strongETag` rejects missing, weak (case-insensitive `W/`), wildcard, unquoted,
unterminated, multiple and embedded-quote validators before the manager
mutation. The independent malformed matrix left the resource unchanged in 20
ordinary and 10 race repetitions. Stable 422/428/412 responses were observed,
depending on whether generated header binding or the strong validator rejected
the input.

### R5 - Closed for the requested boundary

An out-of-range list limit now maps to stable 422 `invalid_parameter`.
Recursive entry and encoded-byte accounting sees nested `action.files`; the
independent HTTP probe exceeded the configured nested entry bound, received 413
and observed zero calls to the injected action-plan dependency. Ordinary and
race repetitions passed.

### R6 - Closed

Startup supplies the IDs whose managed encrypted envelopes were loaded and
verified, and successful create/credential replacement updates that metadata.
API-owned managed credentials render as `managed`; YAML references remain
`static_reference`, and missing credentials remain `missing`. The successful
managed-create/restart fixture passes without exposing credential values.
R2a must still close the failure-path visibility window before this boundary is
safe under persistence errors.

## Other retained boundaries

- Retryable 408/429/5xx responses are not permanently cached on the ordinary
  path; dependency recovery with the same key succeeds in existing tests.
- Generated parsing, invalid UTF-8, duplicate/unknown fields, request bytes and
  depth, origin/forwarded-header and CORS checks retain passing evidence.
- Liveness/readiness, instance lock, migrations, stable key loading, encrypted
  envelope verification, sanitized startup errors and graceful shutdown retain
  passing evidence.
- No generated DTO crosses into domain/storage packages. No tracked `go.work`
  or local `replace` exists.
- No native Arr/Seerr/filesystem/download-client write or live media operation
  was enabled by this correction.

## Independent checks

All Go and aggregate commands used `GOWORK=off GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact ancestry, trees, clean worktree, product-to-checkpoint no-drift, scoped hashes, `git diff --check`, no tracked `go.work`, no local `replace` | Passed. |
| Generated/interface/dependency/implementation/delegation 55-operation source matrix | Passed: `55/55/55`, with no missing, extra or duplicate names. |
| R2a and R3a independent failure probes, `-count=20` | Passed reproductions; transport `0.463s` (`1.50s` elapsed), startup `2.166s` (`3.21s` elapsed). |
| Same failure probes, `-race -count=10` | Passed reproductions; transport `1.427s` (`3.14s` elapsed), startup `15.947s` (`17.72s` elapsed). |
| R3b large-integer digest probe, `-count=50` and `-race -count=20` | Passed reproduction; package times `0.431s` and `1.708s`. |
| Independent ETag/nested-bound closure probes, `-count=20` and `-race -count=10` | Passed; package times `0.475s` and `1.435s`. |
| `go test ./internal/transport ./cmd/mastarr -count=10 -timeout=240s` | Passed; transport `0.413s`, startup `4.379s`; elapsed `6.13s`. |
| `go test -race ./internal/transport ./cmd/mastarr -count=10 -timeout=420s` | Passed; transport `1.467s`, startup `62.925s`; elapsed `63.98s`. |
| `go test ./... -count=1 -timeout=240s` | Passed; elapsed `25.61s`. |
| Focused `go vet` and root `go mod verify` | Passed; all modules verified. |
| Lint, architecture and planning | Passed; planning resolved 53 tasks and 60 acceptance cases. |
| Linux amd64 and arm64 CGO-free `./cmd/mastarr` builds | Passed. |
| Full offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0 in `312.43s`; final marker `guardrail checks passed (ci)`. Generation, staged generation, root/client Vacuum, lint, architecture, root/UI/tools/client ordinary/race/vet/module checks and Linux cross-builds passed. Root storage race was `203.411s`; trash race was `67.011s`. |

## Acceptance disposition

- A-38: blocked for C-04. The successful create/restart path is durable, but a
  failed final SQL publication leaves the manager serving configuration that
  does not exist in SQLite.
- A-39: blocked for C-04. Strong ETags and successful restart application pass,
  but a persistence failure can leave the in-memory revision ahead of durable
  state and make exact retry conflict.
- A-42: blocked for C-04's assembled failure boundary. Managed happy-path
  encryption, restart verification, state classification and redaction pass;
  the shared manager-before-commit ordering still prevents an atomic failure
  result across parent configuration and credential metadata.
- A-46: accepted for C-04. No login is required, forwarded identity is not
  trusted and no authenticated actor is fabricated.
- A-47: accepted for C-04. Configured-origin CORS, conservative mutation-origin
  handling, forwarded-header refusal and relative locations retain passing
  evidence.
- A-56: blocked for C-04. Original eviction, retryable-response, weak ETag,
  invalid-limit and nested-bound findings are corrected, but failed durable
  completion permits blind redispatch and lossy numeric canonicalization does
  not reject a changed payload.

This receipt does not approve C-04 for integration. It does not authorize
release, deployment, live media mutation or live upstream control.
