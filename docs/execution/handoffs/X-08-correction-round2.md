# X-08 retained descriptor service, correction round two

## Assignment and scope

- Task: X-08 correction round two, durable capture/delete serialization.
- Owner: `/root/x05_implementer`; independent review: `/root/x05_reviewer`.
- Dispatch checkpoint: `64a916e65ed10d156c1ae6c396cf45accf0d4f48`.
- Source product under correction: `b766ea8712fd18f6b02dd6c8aa4c304745304298`.
- Prior independent review: `eb996380766dad776c7915f0b27dfecbe23a7724`.
- Integrated review/state checkpoint supplied by coordinator: `b879cf5ae9cebf156812d87f8ec078d6604c4299`.
- Product commit: `b8f648f055d922699061264877e9acb81d3ad075`.
- Owned paths: `internal/descriptors/` and this handoff only. `docs/execution/state.json` remains coordinator-owned.
- Acceptance scope: A-06 and A-42, with the round-two R3 concurrency correction.

## Correction

Round two closes the race in which an unavailable descriptor could be
terminally deleted while another service instance captured bytes for the same
download and descriptor type. The local transition key is now one
unambiguous `download_id` plus `descriptor_type` identity for capture,
unavailable recording, and delete. Delete still reloads its row after taking
that key; this orders calls sharing one service while the durable database
fence covers separate service values and processes.

The append-only `descriptor.delete.intent` event is now an exact-row compare
and swap. A new intent is inserted only when the descriptor ID, download ID,
type, storage path, digest presence/value, capture source, capture time,
retention, unavailable reason and non-deleted state still match the reviewed
snapshot. A changed row returns a conflict and cannot proceed to deletion.
Existing pending intents remain strictly decoded and identity-validated.

The terminal deletion update uses the same exact-row compare and swap and also
requires the pending intent. It cannot mark a stale unavailable snapshot as
deleted after capture has made that row available. Capture and unavailable
updates require that no pending delete intent exists. Capture checks this
before materialization and its SQL update checks it again, so an intent-first
interleaving fails closed without publishing retained bytes. An operation that
loses the transition returns a conflict or pending result; it does not claim a
successful deletion with an orphaned object.

The append-only audit model is unchanged: the pending intent is retained as
redacted recovery evidence, and one terminal `descriptor.delete` event is
appended only after the exact row transition succeeds. No schema or migration
was added, and no upstream, credential, live media or private path data was
used.

## Regression evidence

`TestDeleteCaptureAcrossServiceValuesUsesDurableDescriptorFence` uses two
descriptor service values over the same synthetic SQLite database. Its
intent-first case pauses after the durable unavailable-delete intent; capture
observes the pending fence, returns `ErrDescriptorDeletePending`, publishes no
object, and deletion completes with a deleted metadata record. Its
stale-delete-first case pauses before intent creation; a separate service
captures bytes and updates the unavailable row, then the stale delete's exact
CAS returns `ErrDescriptorConflict`. The captured bytes, retained metadata,
and absence of pending/terminal delete audit events are verified. The test
passed ten race repetitions.

The existing descriptor tests continue to cover exact export evidence,
unavailable state, idempotent capture, no-replace private staging,
post-unlink restart reconciliation, replacement identity checks, redacted
audit metadata and explicit deletion acknowledgement.

## Checks

| Command or scenario | Result |
| --- | --- |
| `env GOWORK=off go test -mod=readonly ./internal/descriptors -count=1` | Exit 0; package passed. |
| `env GOWORK=off go test -race -mod=readonly ./internal/descriptors -count=1` | Exit 0; package passed. |
| `env GOWORK=off go test -race -mod=readonly ./internal/descriptors -run '^TestDeleteCaptureAcrossServiceValuesUsesDurableDescriptorFence$' -count=10` | Exit 0; ten repetitions passed. |
| `env GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `env GOWORK=off go mod verify` | Exit 0; all modules verified. |
| `env GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| `env GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| `env GOWORK=off GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| `env GOWORK=off GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| Versioned pre-commit hook during product commit | Exit 0; generation, staged contract/Vacuum, architecture and fast guardrails passed. |
| `env GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Exit 0; generation, all Vacuum checks, lint/architecture, root/UI/tools/client tests and race, vet, module verification and 18 Linux cross-builds passed; final marker `guardrail checks passed (ci)`. |

## Review status and resume

- Product is ready for independent review at `b8f648f055d922699061264877e9acb81d3ad075`.
- No runtime capability, schema, upstream or API surface was broadened.
- No known implementation blocker remains in this correction. Unsupported
  platforms retain the existing fail-closed object-identity behavior.
- Coordinator must record the product and this handoff commit in
  `docs/execution/state.json`; review remains a separate gate.
