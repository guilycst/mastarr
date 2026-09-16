# W-02 correction round eight handoff

## Assignment

- Task: W-02 correction round eight, preserve handler-reported effects when a
  dispatch returns both a result and an error.
- Owner: `/root/x05_implementer`; independent reviewer: `/root/x05_reviewer`.
- Dispatch checkpoint: `6450c47cfa0424c9b5ededd451b10eb1ddcf76c6`.
- Prior review receipt: `d386489beb52cbb247d6326f34d201e2c596cd43` records the
  W-03 review that identified the durable W-02 R2b gap.
- Product commit: `f364a10301d68a670f945f57831dfa17c9909bef`.
- Branch/worktree: shared `main` checkout.
- Owned product paths: `internal/execution/`; this handoff is the only owned
  documentation path. No state, API, schema, module, adapter, client, UI or
  live-data paths were changed.
- Linked acceptance: A-14, A-32, A-33, A-34, A-35, A-36, A-59 and A-60.

## Correction

Before this correction, `Executor.processObserved` discarded the
`DispatchResult` whenever `Handler.Dispatch` also returned an error. The
uncertain path consequently rewrote every planned effect as unknown and lost
the handler's per-target state, affected identity and evidence.

The executor now passes the returned result into the durable dispatch-error
path. A non-empty result or effect report is treated as potentially dispatched
even when the wrapped failure did not explicitly carry a dispatched marker.
The attempt records the handler evidence, outcome and reported-effect count.

Returned effects are normalized and strictly checked against the exact
pre-created target set: action identity, target kind/ID, effect kind and
ordinal must match; duplicate, foreign, malformed or otherwise unapproved
reports fail closed. In one claimed transaction, each reported effect keeps
its state, observed time and evidence, while every planned target omitted by
the handler is marked `unknown` with `dispatch_result_unreported` evidence.
Journal or validation failure falls back to the existing all-unknown
uncertain path and never authorizes a safe mutation retry. The action remains
reconciling, so later read-only reconciliation can resolve the exact partial
dispatch before any retry.

The correction also keeps post-success validation/read-back failures on an
empty-result error path. An optimistic success result is therefore never
persisted as partial mutation evidence after the required final read-back
fails.

`TestErroredDispatchPersistsReturnedPartialEffectsAndReconciles` exercises a
two-target action whose dispatch returns one applied effect plus a dispatched
uncertain error. It proves the journal retains the applied effect/evidence,
marks the omitted target unknown, records the uncertain attempt, and performs
one read-only reconciliation on a fresh executor with no second dispatch.

## Verification

| Check | Result |
| --- | --- |
| `GOWORK=off go test ./internal/execution -run TestErroredDispatchPersistsReturnedPartialEffectsAndReconciles -count=10 -timeout=180s` | Passed, exit 0. |
| `GOWORK=off go test -race ./internal/execution -run TestErroredDispatchPersistsReturnedPartialEffectsAndReconciles -count=10 -timeout=240s` | Passed, exit 0. |
| `GOWORK=off go test ./internal/execution -count=3 -timeout=240s` | Passed, exit 0. |
| `GOWORK=off go test -race ./internal/execution -count=3 -timeout=300s` | Passed, exit 0. |
| `GOWORK=off go vet ./internal/execution` | Passed, exit 0. |
| `git diff --check` | Passed. |
| Versioned pre-commit fast hook during product commit | Passed: generation, staged generation, Vacuum for the root and standalone contracts, architecture, and targeted Go checks. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0: reproducible generation, staged generation, all root and standalone Vacuum checks with zero warnings/errors, architecture, lint, root and nested module tests, race/vet, module verification and Linux cross-build matrix. |
| Credentials, private coordinates, live services, real media or destructive writes | Not used; all evidence is synthetic. |

## Review and integration

- Product tree for independent review:
  `f364a10301d68a670f945f57831dfa17c9909bef`.
- Handoff commit: recorded after this file is committed.
- Independent review: pending.
- Coordinator owns `docs/execution/state.json` and must record both exact
  commits. The W-03 R2a action-mapper gap remains outside this W-02 lane.
- G-01 upstream mutation evidence and all live write capabilities remain
  unchanged; this correction only preserves durable evidence returned by an
  already-invoked handler.

## Resume checkpoint

- Current state: product correction is committed and the complete offline
  guardrail matrix passes.
- Next safe action: coordinator records the product and handoff commits and
  dispatches an independent review against the exact product tree.
- Blocker: independent review. No implementation blocker remains in this
  correction lane.
- No conflicting writes or unknown files were removed. Product changes are
  limited to `internal/execution/` and this handoff.
