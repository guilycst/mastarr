# C-04 independent review, round one

## Decision

`changes_requested`.

Three P1 and three P2 findings remain. The generated transport boundary and
startup lifecycle have useful, passing safety coverage, but this product does
not complete the C-04 deliverable. Thirty-seven documented operations still
return the fallback 503, API-owned configuration is not durable, managed
credential creation fails at the SQLite boundary, and transport idempotency
can permit a second mutation after cache eviction. The full offline guardrail
passes; that mechanical gate does not close these functional findings.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Dispatch base: `887e9ea2fa791720f9743d8a0ba89e1e49cb814b`.
- Dispatch/state commit: `390bb0e00d2a49903c7298ef22269e5af97e1629`,
  whose direct parent is the dispatch base.
- Exact product: `9f1607a5ff4ea901249a8d50f90848d35035a064`,
  whose direct parent is the dispatch/state commit.
- Product tree: `e9971b35d932a13937596330264d6ea97817d2d5`.
- Exact handoff: `c46b84183f0723d0cbb439eb539cd600c235acf0`,
  whose direct parent is the product.
- Handoff tree: `cbda498b3d96a18a9c32e515e08886396a975940`.
- Exact review checkpoint:
  `96f596a2eb5e743f850bdc81f430d17d1d253478`, whose direct parent is the
  handoff.
- Review-checkpoint tree:
  `b13f0a2471dfff6a10cf71c8de8bcef56bfbadce`.
- Scoped product diff SHA-256:
  `7bc83c736d75878ffdd5c48453838540f30f0341e005aa2caec8e8d624e24b7b`.
- Product-scope archive SHA-256:
  `ae1daa509be672110d74849554af43b96e0bd5ea493968c3939e959dfd7ecfbe`.
- Reviewed product paths: `internal/transport/` and `cmd/mastarr/`. There is
  no product-path drift from the exact product to the review checkpoint.
- Acceptance reviewed: A-38, A-39, A-42, A-46, A-47 and A-56.

Review ran from a clean detached worktree at the exact checkpoint. Independent
probes used only temporary SQLite databases and listeners, synthetic
configuration and synthetic credential values. Probe source was removed from
the worktree after execution; its archive SHA-256 is
`af5d095e66cbabd1e0d405c93394ef985b5eb9111d9f05adebf01f0886ffeeac`.
No live service, private coordinate, real credential or media data was used.

## Findings

### R1 - P1 - Thirty-seven documented operations are not assembled

The generated strict interface contains 55 operations. `Server` implements 18
of them: liveness/readiness, effective configuration, and connection, storage
root and path-mapping CRUD. Embedding `strictFallback` supplies all 55 methods
at compile time, but the remaining 37 always return `errRouteUnavailable`,
which the transport turns into a sanitized 503.

This includes action plans/runs/attempts/reconcile/retry, audit, connection
checks/options, descriptors, discoveries, downloads, media/candidates, review
decisions, scans, trash and workflows. The implementation is honest about
unknown state, but the C-04 deliverable requires wiring the strict generated
server to services for every documented resource. Compile-time route coverage
is not functional service assembly.

Evidence:

- `internal/transport/server.go:73-78` embeds the fallback.
- `internal/transport/fallback.go` defines all 55 generated methods as
  unconditional `errRouteUnavailable` returns.
- Counting generated interface methods gives 55; counting strict handler
  overrides in `server.go` gives 18. Direct requests to an unwired route return
  `503 service_unavailable` without invoking an application service.
- The product handoff records the same limitation under deliberate blockers.

Required correction: assemble every documented route through explicit service
dependencies, with typed translation and the existing fail-closed behavior
when a required service is actually unavailable. Keep the compile-time strict
interface assertion and sanitized problem mapping.

### R2 - P1 - Accepted API configuration is not durable, and managed credential creation cannot succeed

`Run` reconstructs an `APIState` from SQLite and passes it into a new
configuration manager, but no write-through repository or transaction is
connected to successful create, patch or retire operations. A connection
created through HTTP without credentials returns 201 and is visible during the
process; after a graceful stop and restart with the same data directory, GET
returns 404. The same applies to API-managed roots and mappings.

The managed-credential path fails earlier. `configuration.Manager` calls
`sqlCredentialStore.Replace` before the new connection is present in the
`connections` table. The encrypted-credential insert is protected by that
foreign key, so POST with credentials returns the sanitized 500
`internal_error` and creates nothing. API-owned configuration therefore cannot
meet A-38/A-39 restart behavior, and the promised encrypted managed-credential
save in A-42 is unavailable through the assembled API.

Evidence:

- `cmd/mastarr/main.go:166-186` supplies loaded `APIState` and the credential
  store to an in-memory manager, with no durable configuration mutation seam.
- `internal/configuration/configuration.go:1563-1665` writes the managed
  credential set before publishing the in-memory connection.
- `cmd/mastarr/main.go:502-526` inserts the credential rows while the parent
  connection row does not exist.
- Independent process-level probes reproduced both boundaries: no-credential
  create was accepted then disappeared after restart; credential-bearing
  create returned 500 with no secret in the problem response or logs.

Required correction: commit the nonsecret resource row and encrypted
credential set under a storage-owned atomic protocol before returning success,
including lost-response read-back. Startup must reconstruct the accepted state,
and create/patch/retire must preserve the configuration manager's source and
revision rules.

### R3 - P1 - Process-local idempotency eviction permits a second mutation

The policy stores completed mutation responses in a process-local map capped at
1,024 entries. At capacity, it removes the first key returned by Go map
iteration. The specification requires durable `idempotency_records` and keeps
their tombstones until an explicit deletion policy exists. Restart discards
every current entry, and capacity eviction discards an arbitrary live key.

An independent probe created a connection, filled the cache until its original
key was evicted, then sent the same route/key with a changed PATCH body and the
new current ETag. The request returned 200 and changed the label. It should have
returned the stable idempotency conflict with zero second mutation. This is the
exact A-56 changed-payload boundary.

The cache also records every captured response. A POST made before the
configuration service was installed returned retryable 503; after installing
the service and readiness, replaying the same key and body returned the cached
503 indefinitely. This is fail-closed but makes recovery depend on choosing a
new key and is inconsistent with a durable service-aware idempotency record.

Evidence:

- `internal/transport/server.go:89-100` stores only process memory.
- `internal/transport/server.go:874-933` records every captured mutation
  response.
- `internal/transport/server.go:1351-1365` evicts one arbitrary map entry at
  the 1,024-entry bound.
- `docs/specs/spec-001-media-reconciliation/data-and-recovery.md:29-34`
  requires durable records and retained tombstones.
- Both eviction and retryable-startup-response probes passed ten ordinary and
  ten race repetitions.

Required correction: bind transport keys to the durable idempotency repository,
retain the canonical request digest and original outcome under the specified
retention policy, and define retryable pre-dispatch failures so service recovery
does not leave an unusable permanent record. The same-key changed-payload path
must remain conflict-only after restart and under capacity pressure.

### R4 - P2 - Weak `If-Match` values are accepted as strong preconditions

`normalizeETag` removes a leading `W/` before passing the revision to the
configuration CAS. An independent probe patched a connection with
`If-Match: W/"<current revision>"`; the server returned 200 and mutated the
resource. `If-Match` uses strong comparison, so a weak validator cannot
authorize a state change. This weakens A-39 optimistic concurrency and A-56
precondition enforcement.

Evidence: `internal/transport/server.go:550-555`, with the value then used by
the patch/retire handlers. Required correction: reject weak validators with a
stable 4xx problem before any service mutation.

### R5 - P2 - Request bounds and invalid query values do not produce the contracted boundary

There are two independent boundary gaps:

- `boundedLimit` returns a plain error for values outside 1..1000. That error is
  not one of the typed validation errors recognized by `problemFor/statusFor`.
  `GET /api/v1/connections?limit=1001` therefore returns 500
  `internal_error`, rather than a stable client-validation problem.
- `validateArrayBounds` examines only arrays at the top-level request object.
  Action-plan file targets are nested under `action.files`; a synthetic
  `fs.delete` request with 10,001 small file targets and a body below the
  default 4 MiB limit passed the entry bound and reached the fallback 503. It
  should have failed with 413 before strict decoding/service dispatch.

Evidence: `internal/transport/server.go:536-543`, `789-815` and `1232-1253`.
Required correction: return typed parameter validation and apply entry/byte
bounds at every contract location that can carry a manifest or target set.

### R6 - P2 - API-managed credentials are reported as static references

`mapConnection` maps any nonempty credential-reference map to
`static_reference`. That state is correct for startup YAML references but not
for encrypted credentials entered through the API, whose generated enum has a
distinct `managed` value. An independent mapper probe confirms the wrong public
state. Once R2 enables the write path, this would mislead operators about who
owns the secret and how it can be changed.

Evidence: `internal/transport/server.go:587-597`; generated
`ConnectionCredentialState` includes `managed`, `static_reference`, `missing`
and `invalid`. Required correction: map credential state from source/managed
credential metadata, preserving missing and invalid states without exposing a
value or reference.

## Passing boundaries retained

- The generated strict `net/http` handler owns route parsing, and transport
  errors are sanitized `application/problem+json` responses with request IDs.
- Duplicate and unknown nested JSON fields, invalid UTF-8, body size, nesting
  depth, origin/forwarded-header and CORS checks fail closed in covered shapes.
- Liveness and readiness are distinct. Readiness remains 503 until SQLite,
  migrations, key loading, configuration loading and managed-credential
  verification complete.
- Startup holds the process lock, generates or loads the stable credential key,
  verifies active managed envelopes, and performs bounded graceful shutdown.
  Startup failures are sanitized; the credential-failure probe exposed no
  secret or private path.
- v0.0.1 remains unauthenticated. The transport rejects forwarded mutation
  authority and does not invent a trusted actor.
- No Arr, Seerr, filesystem, download-client or live media write was enabled by
  this product.

## Independent checks

All Go and aggregate commands used `GOWORK=off`; the aggregate additionally
used `GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact ancestry, trees, clean worktree, product-to-checkpoint no-drift, scoped hashes, `git diff --check`, no tracked `go.work`, no local `replace` | Passed. |
| Independent transport/startup probes, `-count=10` | Passed reproductions; transport `0.563s`, startup `2.499s`. |
| Same probes, `-race -count=10` | Passed reproductions; transport `5.531s`, startup `42.252s`; elapsed `43.88s`. |
| `go test ./cmd/mastarr ./internal/transport -count=10` | Passed; startup `2.418s`, transport `0.452s`; elapsed `3.39s`. |
| `go test -race ./cmd/mastarr ./internal/transport -count=10` | Passed; startup `30.472s`, transport `1.489s`; elapsed `31.48s`. |
| `go test ./... -count=1` | Passed; elapsed `20.92s`. |
| `go vet ./cmd/mastarr ./internal/transport` and root `go mod verify` | Passed; all modules verified. |
| `python3 scripts/check-architecture.py` | Passed. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases and local links resolved. |
| Linux amd64 and arm64 CGO-free builds of `./cmd/mastarr` | Passed. |
| Full offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0 in `296.03s`; final marker `guardrail checks passed (ci)`. Generation, staged generation, Vacuum, lint, architecture, root/UI/tools/client tests, race, vet/module verification and Linux amd64/arm64 cross-builds passed. Root storage race was `200.630s`; trash race was `66.652s`. |

## Acceptance disposition

- A-38: blocked for C-04. Static ownership/read-only behavior is retained, but
  an accepted API-owned resource disappears after restart and
  credential-bearing connection creation fails.
- A-39: blocked for C-04. Static startup snapshots and revision CAS exist, but
  accepted API changes are not durable and weak ETags can authorize writes.
- A-42: blocked for C-04. Encryption/key readiness and response redaction pass,
  but the assembled managed-credential save path fails and its public state is
  misclassified.
- A-46: accepted for C-04's boundary contribution. No login is required, no
  forwarded identity is trusted and no authenticated actor is fabricated.
- A-47: accepted for C-04's boundary contribution. Configured-origin CORS,
  conservative mutation-origin handling, forwarded-header refusal and relative
  locations retain passing evidence.
- A-56: blocked for C-04. Duplicate/unknown JSON, body/depth and basic
  precondition checks pass, but idempotency eviction allows a changed-payload
  second mutation, weak ETags are accepted, nested target bounds are bypassed,
  and an invalid limit becomes 500.

This receipt does not approve C-04 for integration. It does not authorize
release, deployment, live media mutation or live upstream control.
