# W-02 independent review, round eight

## Decision

`changes_requested`.

The correction safely persists valid partial effect reports and fails closed for
malformed or foreign reports, but one P1 still permits blind resubmission: an
external command ID is neither treated as evidence of possible dispatch nor
persisted on an errored attempt. One P2 also drops valid prior evidence from an
omitted target when that evidence uses the executor's supported opaque-object
JSON form.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Exact product: `f364a10301d68a670f945f57831dfa17c9909bef`.
- Product parent/base: `6450c47cfa0424c9b5ededd451b10eb1ddcf76c6`.
- Product tree: `8233521aa910f6668dac542d0fb096997809880f`.
- Exact handoff: `297b6fbc4f12ed6b3dffdba2164b74fb5a115147`;
  its direct parent is the product commit.
- Handoff tree: `81a6e3a1c1094746a0bf65ad932587786ba05c58`.
- Exact review-dispatch checkpoint:
  `8eeb4843c21b43cc850e9fe4fed11e148d8a85ea`.
- Dispatch tree: `d7b1cd4d529ef952f298195096fc1a35d5832b55`.
- Triggering W-03 receipt integrated on main:
  `d386489beb52cbb247d6326f34d201e2c596cd43`.
- Product-owned paths: `internal/execution/execution.go` and
  `internal/execution/execution_test.go` only.
- Acceptance reviewed: A-14, A-32, A-33, A-34, A-35, A-36, A-59, and A-60.

Review ran in a clean detached worktree at the exact state checkpoint.
`git diff --exit-code f364a10 8eeb484 -- internal/execution` passed. Product,
state, task, schema, generated output, and unrelated files were not edited.
Independent probes ran only in a temporary archive of the exact checkpoint.
They used synthetic in-memory journals and handlers; no live service,
credential, private coordinate, inventory, or media was used.

## Findings

### R8-1 — P1: an external command ID can still be blindly resubmitted

`dispatchResultHasEvidence` at `internal/execution/execution.go:2390-2392`
considers `Accepted`, `Outcome`, result evidence, and effects, but omits
`DispatchResult.ExternalID`. A handler may therefore return the upstream
command identity together with a dependency-classified error and still enter
the not-dispatched branch at lines 2373-2381. That branch marks the attempt
not dispatched, leaves the planned effect pending, and schedules the action for
retry.

The uncertain branch has the second half of the same defect: lines 2338-2344
copy result evidence but never assign `finished.ExternalID`. Even when another
signal correctly forces reconciliation, the durable attempt loses the upstream
identity needed to correlate later read-back or history.

Two independent probes reproduced this boundary under the race detector:

- `DispatchResult{ExternalID:"command-123"}` plus a retryable dependency error
  entered `waiting_dependency`, retained a pending effect, and a fresh executor
  dispatched the same action a second time after backoff. This occurred in
  10/10 repetitions.
- The same external ID plus ordinary result evidence and an explicit dispatched
  uncertainty correctly entered reconciliation, but the persisted dispatch
  attempt had an empty `ExternalID` in 10/10 repetitions.

This contradicts the handler contract and A-33: receipt of an upstream command
ID is affirmative evidence that a write may have been accepted, regardless of
how the accompanying error was classified. It also loses the correlation key
needed by A-34 recovery and breaks A-60 for a valid typed handler that reports
only its external command identity.

Required correction: treat a non-empty, validated `ExternalID` as dispatch
evidence; force uncertainty and all-target read-only reconciliation even when
the error is dependency-classified; and persist that exact ID on the uncertain
attempt before transition. Add regressions proving a fresh executor performs
zero second dispatches and retains the external ID both with and without other
result evidence.

### R8-2 — P2: omitted-target annotation destroys supported opaque evidence

For a valid partial report, each omitted target becomes unknown at
`internal/execution/execution.go:2848-2852`. The new `appendEvidence` helper at
lines 2861-2869 preserves prior evidence only when it decodes as `[]string`.
`Effect.Evidence` is an opaque `json.RawMessage`, and the executor's own normal
form is an object. Any valid object evidence is replaced by a new marker-only
object.

An independent two-target probe gave the omitted target
`{"approved_scope":"two.bin"}`. After the partial errored dispatch, its state
was correctly unknown, but its evidence became only
`{"evidence":["dispatch_result_unreported"]}`. The approved-scope evidence was
lost in 10/10 race repetitions.

This does not authorize a blind retry because the state remains unknown, but it
weakens the exact per-target audit and A-59/A-60 evidence contract. Required
correction: add the unreported marker without discarding any valid prior JSON
shape, or preserve the prior bytes and record the marker in a separate durable
field/envelope. Add array and object evidence regressions.

## Passing correction boundaries

- A valid one-of-two applied report is persisted with its handler evidence; the
  omitted target becomes unknown, the attempt becomes uncertain, and a fresh
  executor reconciles once with zero second dispatches.
- A repeated fail-closed matrix covered foreign target, foreign action,
  duplicate identity, changed ordinal, invalid state, and invalid JSON evidence.
  All cases entered reconciliation and journaled only the approved target as
  unknown in 10/10 race repetitions.
- Individual handler result evidence strings and accepted/outcome markers are
  bounded through the existing attempt-evidence encoder.
- Repeated lease, cancellation, returned-handler barrier, exact effect-set,
  reservation, restart, and no-blind-retry regressions passed. No generic DAG,
  duplicate outbox, schema change, generated DTO, filesystem capability, or
  upstream write capability was introduced.

## Independent checks

Go commands used `GOWORK=off`; offline commands also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Product/handoff/checkpoint identity, ancestry, scoped drift, and `git diff --check` | Passed. |
| Producer partial-effect regression, normal and race, `-count=10` | Passed. |
| Full `internal/execution` tests, `-count=3` | Passed. |
| Full `internal/execution` race tests, `-count=3` | Passed. |
| `go vet -mod=readonly ./internal/execution` and root `go mod verify` | Passed. |
| Nine lease/cancel/restart/barrier/effect/reservation regressions under race, `-count=10` | Passed. |
| Independent malformed/foreign six-case matrix under race, `-count=10` | Passed fail-closed. |
| Independent external-ID uncertainty and persistence probes under race, `-count=10` | Failed as expected 10/10 for each P1 boundary. |
| Independent opaque omitted-evidence probe under race, `-count=10` | Failed as expected 10/10; state was unknown but prior object evidence was lost. |
| No `go.work` or local `replace` directive | Passed. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0 in 321.68s. Reproducible and staged generation, all Vacuum contracts, architecture, lint, every module's tests/race/vet/mod verification, and Linux amd64/arm64 cross-builds passed; root storage race completed in 217.530s. |

## Disposition

Do not integrate this W-02 correction as complete. Correct R8-1 and R8-2, add
permanent regressions, and re-review exact correction SHAs. The separate W-03
manifest-action mapper finding remains open outside this lane. This receipt does
not authorize release, deployment, live upstream writes, or live
filesystem/media mutation.
