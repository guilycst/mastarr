# C-04 correction round one handoff

## Assignment and exact identity

- Task: C-04 correction round one for independent review findings R1-R6.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Dispatch/checkpoint base: `8fbbe1d09687f54b0d2015cde27134295e097d65`.
- Prior independent review: `33014ad84ea69f7a3f7960de2278001d63b6c3cb`.
- Prior review integration: `a617151`.
- Product commits in this correction:
  - `81e329a812c5b535a849db5250b89232c6b89ad3` — explicit generated-route dependency assembly.
  - `b5b3e521618405d8b51deb10ff40de4d2d0de37a` — durable configuration, idempotency and boundary corrections.
- Owned product paths: `internal/transport/`, `cmd/mastarr/`.
- Handoff path: `docs/execution/handoffs/C-04-correction-round1.md`.
- No state, API contract, generated binding, adapter, client, storage schema,
  workflow or UI files were changed by this lane.

The repository and all fixtures remain public-safe. Checks use synthetic
configuration, temporary SQLite databases, temporary listeners and synthetic
credential values. No live service, private coordinate, real credential,
inventory or media data was used.

## Corrections

### R1 — explicit route assembly

The former unconditional generated fallback was removed. The 37 non-built-in
operations now have one typed field each on `transport.RouteDependencies` and
delegate to the injected service. A missing field returns the typed
`ErrRouteUnavailable`, which the policy maps to a sanitized retryable 503. The
18 built-in configuration and health operations retain their explicit service
implementations and also honor an injected dependency when one is assembled.
The strict generated interface assertion remains in place, and a focused test
proves both fail-closed absence and delegated service errors.

`cmd/mastarr` does not yet assemble application services that belong to later
action, discovery, media, descriptor, trash, review, scan and workflow lanes;
those routes therefore remain an honest sanitized unavailable response until
their owning services are passed through this constructor. No empty success is
fabricated.

### R2 — durable API configuration and credential ordering

`cmd/mastarr` now installs a SQLite configuration persistence boundary for
connection, storage-root and path-mapping create, update and retire operations.
Each mutation opens one transaction, inserts a parent row and pending immutable
snapshot, invokes the configuration manager with the same transaction context,
persists the returned revision snapshot, and commits before returning success.
Connection credential replacement uses that transaction context, so the
encrypted envelope rows are inserted after their parent connection row and
cannot fail on the parent foreign key. The startup path reloads API-owned
configuration and managed credential metadata from SQLite and verifies the
encrypted envelopes before readiness.

The persistence seam fails closed with a typed retryable persistence problem;
it never includes SQL, paths, endpoints or credential material in a response.
Successful create/restart/read-back coverage includes a managed API key,
storage root, path mapping, managed credential-state mapping and a no-op patch.

### R3 — durable idempotency and lost-response replay

Mutation keys are now checked against the durable `idempotency_records` table
before the process-local single-flight cache. Successful and client-error
responses retain their canonical request digest, sanitized replay headers and
generated response body in the table, which survives process restart and local
cache capacity eviction. A changed payload with the same method/path/key is a
stable conflict before the handler is called. Retryable server, timeout and
rate-limit responses are not remembered as permanent outcomes, allowing the
same key to be retried after the dependency recovers. The restart fixture
replays the exact original response and rejects a changed payload with the
same durable key.

The local bounded map remains only a single-process optimization; durable
records are the authority. A failure to save an idempotency record is returned
as a sanitized retryable persistence problem and is never silently treated as
durably acknowledged.

### R4 — strong `If-Match`

The configuration patch and retire paths now reject missing, weak (`W/`),
wildcard, unquoted or malformed validators before entering a mutation callback.
Only a strong quoted validator reaches the manager revision CAS. A focused
regression proves a weak validator performs zero mutation.

### R5 — typed query and recursive request bounds

Out-of-range `limit` values now return a typed `invalid_parameter` 422. The
pre-generated-decoder body policy recursively walks JSON and bounds every
contract collection field, including arrays nested below `action.files`, by
total entries and encoded array bytes. The check runs before generated decoding,
idempotency reservation or route dispatch. Focused regressions cover nested
entry and byte overflow.

### R6 — managed credential-state mapping

The transport tracks only API-owned connection IDs whose encrypted envelope
metadata was loaded and verified. Such connections map to the generated
`managed` credential state. YAML-owned credential references continue to map to
`static_reference`, while absent references remain `missing`; no credential
value or reference is exposed. Restart and HTTP response coverage assert the
managed state.

## Checks

The following checks passed against the final product commit
`b5b3e521618405d8b51deb10ff40de4d2d0de37a`:

- `GOWORK=off go test ./internal/transport ./cmd/mastarr -count=1 -timeout=180s`
- `GOWORK=off go test -race ./internal/transport ./cmd/mastarr -count=1 -timeout=180s`
- `GOWORK=off go vet ./internal/transport ./cmd/mastarr`
- `GOWORK=off go mod verify`
- `GOWORK=off ./scripts/check-lint.sh`
- `python3 scripts/check-architecture.py`
- `python3 scripts/check_planning.py`
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -o /tmp/mastarr-c04-corr-amd64 ./cmd/mastarr`
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off go build -o /tmp/mastarr-c04-corr-arm64 ./cmd/mastarr`
- The product pre-commit hook: generation, staged generation, API/Vacuum,
  architecture, targeted Go checks and staged diff checks all passed.

The full offline aggregate was also started from the final product with a
fresh temporary cache:

```text
tmpcache=$(mktemp -d "${TMPDIR:-/tmp}/mastarr-c04-cache.XXXXXX")
GOCACHE="$tmpcache" GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci
```

Before the execution was interrupted at the safe checkpoint, generation,
staged generation, API/Vacuum for the root and all standalone contracts,
architecture, lint, the root ordinary test matrix, and the nested module
ordinary/race checks through the Seerr race package had emitted success. The
process did not return an exit code or the final `guardrail checks passed (ci)`
marker, so this handoff does not claim the interrupted aggregate as a pass.
The coordinator or independent reviewer should rerun the unchanged command
from the clean handoff tree and record its exit code, duration and final
marker.

## Remaining gates

- The application-service dependency fields are assembled and fail closed, but
  `cmd/mastarr` still needs the owning action/discovery/media/descriptor/trash,
  review/scan/workflow services wired when those lanes are integrated.
- Full `check-guardrails.sh --ci` completion after this final product tree is
  a coordinator/reviewer gate because the fresh-cache run was interrupted
  after its successful phases.
- v0.0.1 remains unauthenticated. Existing G-01 Arr native registration/import,
  F-05 unsupported filesystem mutations, Seerr read-only behavior and
  qBittorrent safety gates remain unchanged.

No release, deployment, live upstream call or live media mutation is authorized
by this handoff.
