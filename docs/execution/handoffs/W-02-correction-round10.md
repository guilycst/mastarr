# W-02 correction round ten handoff

## Assignment

- Task: W-02 correction round ten, retain dispatch identity through fresh
  reconciliation and merge opaque evidence markers.
- Owner: `/root/x05_implementer`; independent reviewer: `/root/x05_reviewer`.
- Source product/base: `89ebf83aa677a7d078a0c113c04b89c84c9d5c57`.
- Prior review receipt: `6ddce4bc1b242ffeb1942948537b4fcf265c8c93`.
- Coordinator dispatch checkpoint: `c87f07222b62e41235f00769e29b088141737b1a`.
- Product commit: `e46ab80265c6bfbcde7c51f29cfa04c0df421f19`.
- Branch/worktree: shared `main` checkout.
- Owned product paths: `internal/execution/`; this handoff is the only owned
  documentation path. No state, API, schema, module, adapter, client, UI or
  live-data paths were changed by this lane.
- Linked acceptance: A-14, A-32, A-33, A-34, A-35, A-36, A-59 and A-60.

## Corrections

### R10-1: preserve the exact dispatch identity for reconciliation

An uncertain dispatch can have a durable upstream command ID even when a fresh
worker has to create the first reconciliation attempt. The executor now derives
the identity from the current, newest dispatch attempt and copies it into a new
reconciliation attempt. An existing reconciling attempt with no identity is
bound to that ID through the same journal/claim fence; a contradictory existing
identity fails closed as an invalid journal rather than being overwritten.

The newest dispatch attempt is authoritative, including when it has no ID. This
prevents an old command ID from being attributed to a later dispatch attempt.
Post-dispatch validation and read-back failures now pass through the returned
dispatch identity, so a command accepted before a read-back outage remains
correlatable. An already-persisted attempt identity is never cleared by an
empty later result.

`TestExternalIDOnlyErroredDependencyForcesReconciliation` proves that an
ExternalID-only dependency error becomes uncertain, a fresh executor receives
`command-123`, and no second dispatch occurs. The SQL
`TestSQLExternalIDSurvivesRestartIntoReconciliation` fixture closes and reopens
the SQLite database before running a new executor, proving the command ID is
durable across restart and is passed to reconciliation. The
`TestReadBackFailureRetainsDispatchExternalIDForReconciliation` regression
covers the accepted-command/read-back-failure path, and
`TestLatestDispatchExternalIDUsesCurrentDispatchAttempt` prevents stale-ID
reuse.

### R10-2: merge marker fields without duplicate JSON keys

When an omitted planned effect already carries object evidence containing
`_mastarr_execution_markers`, `appendEvidence` now decodes the object into
raw-value fields, merges and de-duplicates the marker list, and re-encodes one
marker field. Existing opaque fields such as approval scope and digest retain
their JSON values. Invalid marker shapes remain inside the existing prior-value
envelope, preserving the original evidence without creating a duplicate marker
field.

`TestErroredDispatchPersistsReturnedPartialEffectsAndReconciles` supplies a
source marker, verifies the omitted effect retains its approved scope and both
markers, and asserts the marker field occurs exactly once. The later read-only
reconciliation still resolves both effects without another mutation dispatch.
Malformed, foreign or changed effect reports continue to use the existing
fail-closed uncertain path; marker evidence never authorizes a retry.

## Verification

| Check | Result |
| --- | --- |
| `GOWORK=off go test ./internal/execution -run 'Test(ExternalIDOnlyErroredDependencyForcesReconciliation\|ReadBackFailureRetainsDispatchExternalIDForReconciliation\|LatestDispatchExternalIDUsesCurrentDispatchAttempt\|ErroredDispatchPersistsReturnedPartialEffectsAndReconciles\|SQLExternalIDSurvivesRestartIntoReconciliation)$' -count=10 -timeout=240s` | Passed, exit 0. |
| `GOWORK=off go test ./internal/execution -count=3 -timeout=240s` | Passed, exit 0. |
| `GOWORK=off go test -race ./internal/execution -count=3 -timeout=300s` | Passed, exit 0. |
| `GOWORK=off go vet ./internal/execution` | Passed, exit 0. |
| `GOWORK=off go mod verify` | Passed: all modules verified. |
| `git diff --check` | Passed. |
| Versioned pre-commit fast hook during the product commit | Passed: generation, staged generation, Vacuum for root and standalone contracts, architecture, and targeted Go checks. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0: reproducible generation, staged generation, all root and standalone Vacuum checks with zero warnings/errors, architecture, lint, root and nested module tests, race/vet, module verification and Linux cross-build matrix. |
| Credentials, private coordinates, live services, real media or destructive writes | Not used; all evidence is synthetic. |

## Review and integration

- Product tree for independent review:
  `e46ab80265c6bfbcde7c51f29cfa04c0df421f19`.
- Handoff commit: recorded after this file is committed.
- Independent review: pending.
- Coordinator owns `docs/execution/state.json` and must record both exact
  commits. W-03 action-mapper findings remain outside this W-02 lane.
- G-01 upstream mutation evidence and all live write capabilities remain
  unchanged; this correction only preserves evidence returned by an already
  invoked handler and keeps uncertain actions read-only until reconciliation.

## Resume checkpoint

- Current state: product correction is committed and the complete offline
  guardrail matrix passes.
- Next safe action: coordinator records the product and handoff commits and
  dispatches independent review against the exact product tree.
- Blocker: independent review. No implementation blocker remains in this
  correction lane.
- No conflicting writes or unknown files were removed. Product changes are
  limited to `internal/execution/` and this handoff.
