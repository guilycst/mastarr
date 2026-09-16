# W-02 correction round nine handoff

## Assignment

- Task: W-02 correction round nine, preserve external dispatch identity and
  opaque per-effect evidence.
- Owner: `/root/x05_implementer`; independent reviewer: `/root/x05_reviewer`.
- Source product/base: `f364a10301d68a670f945f57831dfa17c9909bef`.
- Prior review receipt: `eb514ccfe90e334d7e697b0eaac2363590c8ebd1`.
- Coordinator dispatch checkpoint: `44f0cf8c63aa349fae72f0cf2d2592bd3a635438`.
- Product commit: `89ebf83aa677a7d078a0c113c04b89c84c9d5c57`.
- Branch/worktree: shared `main` checkout.
- Owned product paths: `internal/execution/`; this handoff is the only owned
  documentation path. No state, API, schema, module, adapter, client, UI or
  live-data paths were changed by this lane.
- Linked acceptance: A-14, A-32, A-33, A-34, A-35, A-36, A-59 and A-60.

## Corrections

### R8-1: external command identity is dispatch evidence

An errored dispatch can return an upstream command identity even when its
failure is classified as a dependency outage. A non-empty, trimmed
`DispatchResult.ExternalID` now forces the same uncertain, read-only
reconciliation path as other evidence. The uncertain attempt persists the
exact external ID before the action transition, allowing subsequent history
or read-back reconciliation to correlate the upstream command. This prevents
the previous waiting-dependency path from blindly dispatching the action a
second time.

The external-ID-only regression uses a dependency error with no accepted flag,
outcome, evidence list or effects. It proves the first result is reconciling,
the dispatch attempt retains `command-123` and uncertain certainty, and a
fresh executor reconciles once with zero second dispatches. The existing
partial-effect regression also verifies an external ID is retained alongside
other result evidence.

### R8-2: opaque omitted-effect evidence is retained

`Effect.Evidence` remains opaque JSON. When an omitted planned target is moved
to `unknown`, the executor now retains valid object bytes and appends a
namespaced `_mastarr_execution_markers` field containing
`dispatch_result_unreported`. String-array evidence keeps the established
executor envelope; other valid JSON values are preserved inside an explicit
prior-value envelope. No handler-provided scope, digest or other audit field
is discarded.

The partial-effect regression supplies object evidence for the omitted target,
then decodes the journal value to prove both the original `approved_scope` and
the unreported marker survive. It also proves later read-only reconciliation
resolves both effects without another mutation dispatch.

Malformed, duplicate, foreign or changed-ordinal effect reports still fail
closed through the existing all-unknown uncertain path. External-ID evidence
never authorizes retry by itself; only read-only reconciliation can resolve an
uncertain action.

## Verification

| Check | Result |
| --- | --- |
| `GOWORK=off go test ./internal/execution -run 'Test(ErroredDispatchPersistsReturnedPartialEffectsAndReconciles\|ExternalIDOnlyErroredDependencyForcesReconciliation)$' -count=20 -timeout=180s` | Passed, exit 0. |
| `GOWORK=off go test -race ./internal/execution -run 'Test(ErroredDispatchPersistsReturnedPartialEffectsAndReconciles\|ExternalIDOnlyErroredDependencyForcesReconciliation)$' -count=10 -timeout=240s` | Passed, exit 0. |
| `GOWORK=off go test ./internal/execution -count=3 -timeout=240s` | Passed, exit 0. |
| `GOWORK=off go test -race ./internal/execution -count=3 -timeout=300s` | Passed, exit 0. |
| `GOWORK=off go vet ./internal/execution` | Passed, exit 0. |
| `git diff --check` | Passed. |
| Versioned pre-commit fast hook during product and handoff commits | Passed: generation, staged generation, Vacuum for root and standalone contracts, architecture, and targeted Go checks. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0: reproducible generation, staged generation, all root and standalone Vacuum checks with zero warnings/errors, architecture, lint, root and nested module tests, race/vet, module verification and Linux cross-build matrix. |
| Credentials, private coordinates, live services, real media or destructive writes | Not used; all evidence is synthetic. |

## Review and integration

- Product tree for independent review:
  `89ebf83aa677a7d078a0c113c04b89c84c9d5c57`.
- Handoff commit: recorded after this file is committed.
- Independent review: pending.
- Coordinator owns `docs/execution/state.json` and must record both exact
  commits. The W-03 R2a action-mapper gap remains outside this W-02 lane.
- G-01 upstream mutation evidence and all live write capabilities remain
  unchanged; this correction only preserves evidence returned by an already
  invoked handler and keeps uncertain actions read-only until reconciliation.

## Resume checkpoint

- Current state: product correction is committed and the complete offline
  guardrail matrix passes.
- Next safe action: coordinator records the product and handoff commits and
  dispatches an independent review against the exact product tree.
- Blocker: independent review. No implementation blocker remains in this
  correction lane.
- No conflicting writes or unknown files were removed. Product changes are
  limited to `internal/execution/` and this handoff.
