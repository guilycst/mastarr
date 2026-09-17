# C-04 correction round two handoff

- Task: C-04 correction round two
- Base/state checkpoint: `4f8e3120650c4c31532bf72255e8489c08c8a7fc`
- Prior review receipt: `461b0c06b0a3094f5da28ca2920400b639e22a61` (integrated as
  `d593fd448a2f5ac29ebbca07284f5fa9ee8148ff`)
- Product commit: `9337475cceaee1c2401b8baaa05f45f40e0789ed`
- Handoff commit: this file, committed separately after the product commit
- Owned paths: `internal/transport/`, `cmd/mastarr/`, this handoff
- State ownership: coordinator; `docs/execution/state.json` was not edited

## Product

The transport now places a configuration read/write gate around the built-in
configuration service. Every built-in configuration reader holds the read side
through its manager snapshot, while every API mutation holds the write side
through the storage callback. A failed durable configuration mutation invokes a
bounded reload callback before releasing the gate. If the callback is absent or
cannot rebuild the effective manager, the server clears the manager and managed
credential metadata and marks readiness false. `cmd/mastarr` supplies the
reload callback, rebuilding API state from SQLite with the existing YAML,
credential store, encryption manager and key metadata. Managed credential IDs
are reconstructed from durable state and no credential values enter transport.

The idempotency seam now requires `Load`, `Reserve` and `Complete` whenever a
durable repository is configured. The SQLite implementation uses the existing
immutable `idempotency_records` table without a migration: it inserts a
reserved record before dispatch and an immutable completion companion only
after the handler response is known. A missing companion remains pending after
restart; a non-replayable completion remains pending as well. Fresh processes
return a stable `idempotency_pending` conflict and do not dispatch blindly.
Successful completion is read back before the local cache is populated, and
same-key concurrent servers are serialized by the durable reservation. The
legacy completed-only `Save` format remains readable for existing rows, while
new production startup supplies the reservation protocol.

Request digest canonicalization uses `json.Decoder.UseNumber`, retaining JSON
number text instead of converting through `float64`; object key ordering stays
canonical and values around 2^53 remain distinct.

## Evidence

- `TestConfigurationFailureReloadsDurableManagerBeforeUnlock` proves a manager
  candidate returned with a persistence error is not visible after the gate is
  released and that a retry can proceed from the reloaded durable manager.
- `TestDurableIdempotencyReservationSurvivesLostCompletion` proves a lost
  completion leaves a pending reservation, a fresh server refuses a second
  dispatch, and a recovered completion replays the original response.
- `TestDurableReservationSerializesConcurrentServers` runs two independent
  servers against one durable reservation store and proves one reservation and
  one handler dispatch.
- `TestSQLiteIdempotencyReservationAndCompletionSurviveRestart` exercises the
  actual SQLite implementation across close/reopen, preserving state, digest,
  status and response bytes.
- `TestRequestDigestPreservesJSONNumberText` covers the 2^53 boundary and
  equivalent object key order.

## Checks

All commands ran from the product commit with `GOWORK=off` unless stated:

- `go test ./internal/transport ./cmd/mastarr -count=1 -timeout=180s` passed.
- `go test -race ./internal/transport ./cmd/mastarr -count=1 -timeout=180s`
  passed.
- `go vet ./internal/transport ./cmd/mastarr` passed.
- `go test ./... -count=1 -timeout=180s` passed.
- `go vet ./...` passed.
- `go mod verify` passed (`all modules verified`).
- `python3 scripts/check-architecture.py` passed.
- `python3 scripts/check_planning.py` passed (`53 tasks, 60 acceptance
  cases`).
- The product commit pre-commit fast gate passed generation, staged
  generation, API validation, staged Vacuum for the root and five standalone
  OpenAPI contracts, architecture and targeted tests.
- `env GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci`
  passed in `103.68s` (`guardrail checks passed (ci)`). This included root,
  UI, tools and all six client modules; race, vet, module verification,
  generation, Vacuum, lint, architecture and Linux amd64/arm64 CGO-free
  cross-builds.

## Blockers and policy gates

No C-04 implementation blocker remains. The reservation/completion protocol
uses the existing immutable schema and required no migration or local replace.
Upstream/native write policies remain unchanged: G-01 Arr writes stay
fail-closed, F-05 unsupported filesystem capabilities stay fail-closed, and
Seerr remains read-only. No live services, credentials, private coordinates or
media data were used.
