# W-02 independent review, round nine

## Decision

`changes_requested`.

Round-nine code correctly treats a non-empty external command ID as uncertain
dispatch evidence and persists it on the dispatch attempt. It does not carry
that identity into the later reconciliation attempt passed to the handler.
An ID-dependent read-back therefore cannot correlate the accepted upstream
command. A second defect emits duplicate JSON keys when opaque handler evidence
already contains the executor marker namespace, shadowing valid prior evidence.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Exact product: `89ebf83aa677a7d078a0c113c04b89c84c9d5c57`.
- Product parent/base: `44f0cf8c63aa349fae72f0cf2d2592bd3a635438`.
- Product tree: `0002fba90892c20c0cb10fdbcf92efce17f2a84b`.
- Exact handoff: `161edd5956f0633d8db6191c076934175141263a`;
  its direct parent is the product commit.
- Handoff tree: `8e11c0f944cf09e4667b4923ab9b6fe1fd9ce17d`.
- Exact review-dispatch checkpoint:
  `6d022eebef5583aaf9959d17b64891fffcd739f3`.
- Checkpoint tree: `4f14da835ba7b5910f50533ff5ce49ff52e2e005`.
- Product-owned paths: `internal/execution/execution.go` and
  `internal/execution/execution_test.go` only.
- Acceptance reviewed: A-14, A-32, A-33, A-34, A-35, A-36, A-59, and A-60.

Review ran in a clean detached worktree at the exact checkpoint.
`git diff --exit-code 89ebf83 6d022ee -- internal/execution` passed. Product,
state, task, schema, generated output, and unrelated files were not edited.
Independent probes ran only in a temporary archive of the exact product tree.
They used synthetic in-memory journals and handlers; no live service,
credential, private coordinate, inventory, or media was used.

## Findings

### R9-1 — P1: persisted external identity is absent from handler reconciliation

`finishDispatchError` at `internal/execution/execution.go:2335-2373` now marks an
external-ID-only error uncertain and stores `DispatchResult.ExternalID` on the
dispatch attempt. On the next run, however, `reconciliationAttemptOwned` at
lines 2545-2555 searches only for an existing reconcile-phase attempt. Because
the uncertain row is a dispatch-phase attempt, the method creates a new
reconcile attempt with an empty `ExternalID`. `processReconciliation` passes
that empty attempt to `Handler.Reconcile` at line 2235.

An independent three-run probe reproduced the consequence under the race
detector in 10/10 repetitions:

1. Dispatch returned `ExternalID: "command-123"` plus a dependency error.
   Executor entered `reconciling`, persisted the command ID and recorded the
   target unknown.
2. Fresh executor invoked `Reconcile` with `Attempt.ExternalID == ""` rather
   than `command-123`. The synthetic ID-driven reader could not correlate the
   command and reported its no-observed-effect retry result.
3. Executor requeued the action; the next fresh executor dispatched again.
   Observed counters were `dispatch=2`, `reconcile=1`.

The permanent regression at
`internal/execution/execution_test.go:1683-1722` does not inspect its reconcile
attempt. Its callback returns a fabricated applied read-back regardless of the
argument, so the test proves journal persistence and state transition but not
that a fresh handler can use the persisted identity.

`Attempt` is the handler-visible operation record and explicitly contains
`ExternalID`. Losing that field at the dispatch-to-reconcile boundary defeats
the round-nine correlation purpose and leaves A-33 read-back dependent on
evidence unavailable through the handler contract. It can also permit blind
resubmission after an ID-dependent reader concludes safe retry without seeing
the ID that the executor retained.

Required correction: bind reconciliation to the exact uncertain dispatch
identity. The `Attempt` delivered to `Handler.Reconcile` must retain
`command-123` across a fresh executor without rewriting or guessing it. Add a
permanent regression whose reconcile callback asserts the ID, performs the
ID-scoped read-back, and proves zero second dispatches across fresh executors.

### R9-2 — P2: marker append can shadow opaque handler evidence

For an omitted reported target, `appendEvidence` at
`internal/execution/execution.go:2863-2890` splices a new
`_mastarr_execution_markers` property into any valid JSON object. It does not
check whether the opaque handler-owned object already has that property.

Independent probe input:

```json
{"_mastarr_execution_markers":["handler-owned"],"approved_scope":"two.bin"}
```

Produced journal evidence:

```json
{"_mastarr_execution_markers":["handler-owned"],"approved_scope":"two.bin","_mastarr_execution_markers":["dispatch_result_unreported"]}
```

This failed in 10/10 race repetitions. JSON remains syntactically valid, but
standard decoding keeps the last duplicate key, so the original marker value
is hidden. The effect state still fails closed as unknown, but the round-nine
claim that no handler field is discarded is false and the A-59/A-60 audit
record is ambiguous.

Required correction: never emit duplicate keys. If the namespace is reserved,
reject a collision before journaling and retain a safe complete record. If it
is composable, merge compatible marker arrays while preserving original
values; otherwise wrap the complete prior JSON under a separate envelope.
Add collision regressions for array, non-array, and duplicate-key inputs.

## Passing correction boundaries

- External-ID-only dependency errors enter reconciliation, persist the exact ID
  on the uncertain dispatch attempt, and mark approved targets unknown.
- Valid object evidence without a reserved-name collision retains its original
  fields and gains `dispatch_result_unreported`.
- String-array and ordinary non-object evidence retain their prior values
  through the established envelopes.
- Repeated fail-closed matrix covered foreign target, foreign action, duplicate
  identity, changed ordinal, invalid state, and invalid JSON evidence. All
  cases entered reconciliation and journaled only approved targets as unknown.
- Prior CAS, lease renewal, cancellation, restart recovery, dispatch barrier,
  reservation, exact effect-set, and unresolved-count regressions passed under
  repetition and race.
- No schema, generated DTO, generic DAG, duplicate outbox, filesystem
  capability, or upstream write capability changed.

## Independent checks

Go commands used `GOWORK=off`; offline commands also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Product/handoff/checkpoint identity, ancestry, scoped drift, and `git diff --check` | Passed. |
| Producer round-nine focused tests, normal `-count=20` and race `-count=10` | Passed. |
| Full `internal/execution` tests, normal and race, each `-count=3` | Passed. |
| `go vet ./internal/execution` and root `go mod verify` | Passed. |
| Nine CAS/lease/cancel/restart/barrier/effect/reservation regressions, normal `-count=20` and race `-count=10` | Passed. |
| Independent malformed/foreign six-case matrix, normal `-count=20` and race `-count=10` | Passed fail-closed. |
| Independent external-ID correlation/fresh-executor probe under race, `-count=10` | Failed as expected 10/10: reconcile received empty ID and next run dispatched again. |
| Independent reserved-marker collision probe under race, `-count=10` | Failed as expected 10/10: duplicate key shadowed handler evidence. |
| Linux amd64 and arm64 CGO-free `internal/execution` test-binary compilation | Passed. |
| No tracked `go.work` or local `replace` directive | Passed. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible and staged generation, all Vacuum contracts, architecture, lint, nine-module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Disposition

Do not integrate W-02 round nine as complete. Correct R9-1 and R9-2, add
permanent regressions, and re-review exact correction SHAs. The separate W-03
action-mapper issue remains outside this lane. This receipt does not authorize
release, deployment, live upstream writes, or live filesystem/media mutation.
