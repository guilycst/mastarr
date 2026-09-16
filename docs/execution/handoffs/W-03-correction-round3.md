# W-03 correction round three handoff

## Assignment

- Task: W-03, correction round three (preserve mixed filesystem effect states)
- Owner: `/root/x05_implementer`
- Independent reviewer: `/root/x05_reviewer`
- Source product: `4871bc4999f18f2d6aa119e366ca949b6886ac45`
- Review receipt: `5c3068edccd8e6b22f6fd5a107f939468c9f2375`
- Dispatch checkpoint: `db3317674c592bc49761b0d653153d8bb5bc72e6`
- Product commit: `a4ad5a0fcfee724491513224f6b082a25be82e0a`
- Handoff path: `docs/execution/handoffs/W-03-correction-round3.md`
- Owned product paths: `internal/actions/`
- Checkout: shared `main`; execution state remains coordinator-owned

## Correction

`mapFilesystemAffected` now treats the handler's read observation as
authoritative for an effect that was already satisfied. The aggregate
`FilesystemEffect.Outcome` is applied only to mapped effects that are still
`EffectPending`. Therefore, a mixed manifest containing an absent source and a
source changed or removed by the dispatch retains `EffectAlreadySatisfied` for
the absent entry while recording the returned terminal state for the affected
entry. Returned error, dispatch, observation-time and evidence handling remains
unchanged, so the executor can persist the partial result and reconcile the
uncertain operation without downgrading the read-proven state.

## Synthetic regressions

- `TestDeleteDispatchPreservesReadSatisfiedEffectInMixedManifest` exercises the
  direct delete handler with an absent manifest entry, a present entry, an
  aggregate applied effect, and `organize.ErrSourceChanged`. It verifies the
  returned effects are `EffectAlreadySatisfied` and `EffectApplied` in the
  approved order. The probe is repeated ten times.
- `TestExecutorPersistsMixedDeleteStatesWithoutDowngrade` exercises the real
  `execution.Executor`, the real `FilesystemHandler`, and a temporary SQLite
  journal with the same mixed manifest and returned error. It verifies the
  action enters reconciliation and that journal read-back retains the two
  distinct effect states rather than persisting `EffectApplied` for both.
- The focused pair was repeated under the race detector ten times; the package
  test and race suites were also repeated three times. All fixtures use
  synthetic in-memory ports and temporary SQLite state. No live services,
  credentials, private coordinates, inventories or media data were used.

## Verification

All commands were run from the shared checkout with `GOWORK=off`.

| Check | Result |
| --- | --- |
| `gofmt -w internal/actions/filesystem.go internal/actions/actions_test.go internal/actions/executor_integration_test.go` | Passed |
| `git diff --check` | Passed before product commit |
| `GOWORK=off go test ./internal/actions -run 'Test(DeleteDispatchPreservesReadSatisfiedEffectInMixedManifest\|ExecutorPersistsMixedDeleteStatesWithoutDowngrade)$' -count=1 -timeout=120s` | Passed, exit 0 |
| `GOWORK=off go test -race ./internal/actions -run 'Test(DeleteDispatchPreservesReadSatisfiedEffectInMixedManifest\|ExecutorPersistsMixedDeleteStatesWithoutDowngrade)$' -count=10 -timeout=240s` | Passed, exit 0; 16.373s |
| `GOWORK=off go test ./internal/actions -count=3 -timeout=180s` | Passed, exit 0; 0.615s |
| `GOWORK=off go test -race ./internal/actions -count=3 -timeout=240s` | Passed, exit 0; 10.547s |
| `GOWORK=off go vet ./internal/actions` | Passed, exit 0 |
| `GOWORK=off go mod verify` | Passed; all modules verified |
| Product pre-commit hook | Passed; generation, staged generation, API/Vacuum, architecture and fast guardrails passed |
| `/usr/bin/time -p env GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0; `real 102.87s`, `user 117.76s`, `sys 87.97s` |

The aggregate CI-equivalent gate covered deterministic generation and staged
generation, bundled and standalone OpenAPI Vacuum validation, architecture and
lint checks, root/UI/tools/client tests and race tests, module verification,
and Linux amd64/arm64 CGO-free cross-builds.

## Gates and next step

The W-02 executor R2b correction is the approved dependency that persists
handler-provided partial effects on returned errors. G-01 remains open, so Arr
registration/import writes stay fail-closed. F-05 remains capability-blocked
where its reviewed filesystem port reports unsupported. This correction adds no
native upstream writes, generated DTO exposure, direct transport, or fallback
transfer behavior. The product checkpoint is ready for independent review; the
coordinator records the execution-state update and integration decision.
