# X-08 retained descriptor service, correction round four

## Assignment and scope

- Task: X-08 correction round four, capture publication versus delete fencing.
- Owner: `/root/x05_implementer`; independent reviewer: `/root/x05_reviewer`.
- Dispatch checkpoint: `d1bf30ccf4cb7ce2e520d4d6dd15bb356e23165a`.
- Source product under correction: `b4724473923d2b7059eca519252d119bff9a850e`.
- Prior independent review: `c29adbaccdd9b5d671fe72b00ba740c81269cc1f`.
- Integrated review/state checkpoint: `cedf380c236bbae96be31b37937e50769239446a`.
- Product commit: `cc7c530e4114112cceeaba28000ab59d893efc59`.
- Owned paths: `internal/descriptors/` and this handoff only. `docs/execution/state.json` remains coordinator-owned.
- Acceptance scope: A-06 and A-42, with review finding R4a.

## Correction

Capture recovery and acknowledged Delete now share a durable, append-only
`descriptor.capture.fence` transition in the existing audit journal. The fence
binds the exact pending capture intent event, descriptor resource ID and
operation. Acquisition is a database compare-and-swap: it requires the intent
to remain pending with no terminal capture event and refuses a second active
fence. A recovery executor may resume a pending fence for the same
`capture-recover` operation after restart; a `delete-reconcile` executor cannot
claim it, and vice versa. This makes the serialization boundary independent of
process-local descriptor locks and separate `Service` values.

`resolvePendingCapture` acquires the capture-recovery fence before observing
the final and stage paths and retains it through stage-to-final no-replace
publication, read-back, stage cleanup, metadata adoption/update and the
committed or aborted capture terminal transition. `reconcileCaptureBeforeDelete`
acquires the delete-reconciliation fence before its final/stage observations
and retains it through its decision and terminal transition. A concurrent
operation that owns the other fence returns `ErrCaptureUncertain` before it can
abort the intent or mark the descriptor deleted. Fence release is itself a
redacted durable transition; a release failure is surfaced as uncertainty so a
fresh executor can reuse the exact operation fence.

`finishCaptureIntent` now checks the existing terminal outcome. Repeating the
same committed or aborted transition remains idempotent, while requesting the
opposite outcome returns `ErrCaptureUncertain` and never reports success. This
prevents a stale delete-side abort from masking a capture-side commit.

The existing object identity, digest, size, no-replace link, read-back,
metadata/content split, exact deletion and fresh-service recovery safeguards
remain unchanged. No schema migration, upstream capability, credential, live
service, media path or public API was added.

## Regression evidence

`TestCaptureRecoveryFencePreventsDeleteAbortDuringPublication` creates an
unavailable descriptor and a verified operation-owned stage and intent, then
uses two separate `Service` values over the same SQLite database. The capture
service acquires the durable recovery fence and pauses immediately before
stage-to-final publication. The delete service attempts acknowledged Delete
while that fence is held and must return `ErrCaptureUncertain`; it cannot
record an aborted capture terminal or delete the unavailable row. Releasing
the publication seam lets capture complete, after which the descriptor is
available and the exact final object exists. The same regression also calls
the terminal helper with the opposite outcome and verifies the contradictory
request is rejected.

The prior descriptor recovery tests continue to cover metadata and cleanup
failure, fresh-service final adoption, stage recovery, replacement identity,
redacted journal metadata, exact deletion and idempotency. The test uses only
synthetic bytes, temporary roots and a disposable SQLite database.

## Verification

| Command or scenario | Result |
| --- | --- |
| `GOWORK=off go test -mod=readonly ./internal/descriptors -run 'TestCaptureRecoveryFencePreventsDeleteAbortDuringPublication' -count=1 -timeout=30s` | Exit 0. |
| `GOWORK=off go test -race -mod=readonly ./internal/descriptors -run 'TestCaptureRecoveryFencePreventsDeleteAbortDuringPublication' -count=5 -timeout=90s` | Exit 0; five repetitions passed. |
| `GOWORK=off go test -mod=readonly ./internal/descriptors -count=1 -timeout=90s` | Exit 0; full package passed. |
| `GOWORK=off go test -race -mod=readonly ./internal/descriptors -count=1 -timeout=120s` | Exit 0; full descriptor race suite passed in 30.895s. |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| Versioned pre-commit hook during product commit | Exit 0; generation, staged OpenAPI/Vacuum, architecture and fast guardrails passed. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Exit 0 in 107.54s; final marker `guardrail checks passed (ci)`. Generation, all six standalone Vacuum contracts, lint/architecture, root/UI/tools/client tests and race, vet, module verification and 18 Linux cross-builds passed. Aggregate log SHA-256: `4e85d1aabbb11d782e0f1624df34824af5143e726dec5874d47265ccba9e264f`. |
| `git diff --check`; `gofmt -l internal/descriptors/descriptors.go internal/descriptors/descriptors_test.go` | Exit 0; no whitespace or formatting findings. |

## Review and resume

- Product is ready for independent review at `cc7c530e4114112cceeaba28000ab59d893efc59`.
- R4a is addressed with a durable cross-service capture fence, exact terminal
  outcome matching, and a deterministic publication/Delete regression.
- No runtime capability, schema, upstream, credential or API surface was
  broadened. Unsupported platforms retain existing fail-closed behavior.
- Coordinator must record the product and this handoff commit in
  `docs/execution/state.json`; review remains a separate gate.
- No implementation blocker remains in this correction. The independent
  reviewer should verify the fence's restart reuse and both operation-order
  outcomes in the exact product tree.
