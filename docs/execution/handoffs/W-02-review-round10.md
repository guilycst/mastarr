# W-02 independent review, round ten

## Decision

`changes_requested`.

Round ten closes the external-command identity defect. Fresh and restarted
executors now deliver the exact current dispatch `ExternalID` to
`Handler.Reconcile`, and a later dispatch without an ID does not inherit an old
one. One P2 remains in opaque evidence handling: map-based marker merging
silently discards accepted source JSON when the object contains duplicate keys,
and treats a `null` marker value as an empty marker list instead of preserving
the incompatible source under the prior-value envelope.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Exact product: `e46ab80265c6bfbcde7c51f29cfa04c0df421f19`.
- Product parent: `c87f07222b62e41235f00769e29b088141737b1a`.
- Product tree: `35e8d6f220d5de5a57465755b78de938450234de`.
- Exact handoff: `8ef12618496f623421cbfb638fdda3d8b93c7ba0`;
  its direct parent is the product commit.
- Handoff tree: `063e172d6952045d4f03df1008b9a3e288d66618`.
- Exact review-dispatch checkpoint:
  `092d19e6d6182d3219ac546c4c490e2a607774da`.
- Checkpoint tree: `7958e4de07b74c5372bb891b7ddaa1c66e4956d9`.
- Product-owned paths: `internal/execution/execution.go` and
  `internal/execution/execution_test.go` only.
- Acceptance reviewed: A-14, A-32, A-33, A-34, A-35, A-36, A-59, and A-60.

Review ran in a clean detached worktree at the exact checkpoint.
`git diff --exit-code e46ab80 092d19e -- internal/execution` passed. Product,
state, task, schema, generated output, and unrelated files were not edited.
Independent probes ran only in a temporary archive of the exact product tree.
They used synthetic in-memory and SQLite journals and handlers; no live
service, credential, private coordinate, inventory, or media was used.

## Finding

### R10-1 — P2: map merge still discards valid opaque source evidence

`appendEvidence` accepts any syntactically valid JSON object at
`internal/execution/execution.go:2927-2944` and passes it to
`mergeObjectEvidenceMarkers`. That helper decodes the opaque object into
`map[string]json.RawMessage` at lines 2963-2967 and re-encodes the map at lines
2993-2998.

This has two independently reproduced loss cases:

1. Input
   `{"_mastarr_execution_markers":null,"approved_scope":"two.bin"}` is
   accepted by `json.Unmarshal` into `[]string`: JSON `null` becomes a nil
   slice without error. The original `null` evidence is overwritten with
   `["dispatch_result_unreported"]` instead of using the documented
   `_mastarr_prior` envelope.
2. Duplicate JSON keys are accepted by `json.Valid` and by existing effect
   validation, but decoding into a Go map keeps only the last value. Both
   `{"_mastarr_execution_markers":["first"],
   "_mastarr_execution_markers":["second"]}` and an ordinary opaque object
   with `{"approved_scope":"one","approved_scope":"two"}` lose the first
   source value during annotation.

All three probes failed identically in 10/10 race repetitions. Example output
for the duplicate-marker case was:

```json
{"_mastarr_execution_markers":["second","dispatch_result_unreported"],"approved_scope":"two.bin"}
```

The target remains `unknown`, so this defect does not authorize another
mutation. It still violates the opaque evidence contract and the round-nine
review requirement to cover non-array and duplicate-key inputs. It also
contradicts the round-ten handoff statement that invalid marker shapes stay in
the prior-value envelope. Lost source values weaken A-59/A-60 recovery audit
evidence.

Required correction: merge only an object proven to contain unique keys and,
when present, exactly one non-null string-array marker field. Route `null`,
non-array, and duplicate-key objects through the lossless `_mastarr_prior`
envelope, or reject them before journaling without replacing existing evidence.
Add permanent regressions for null marker, duplicate marker key, and duplicate
ordinary opaque key inputs.

## Closed round-nine findings

### R9-1 external command identity — closed

- External-ID-only dependency errors persist the ID, enter reconciliation, and
  deliver the ID to a fresh handler with one dispatch and one reconciliation.
- SQLite close/reopen coverage proves the same identity survives process-style
  restart and is copied to the new reconciliation attempt.
- Accepted-command read-back failure retains the returned external ID for later
  reconciliation.
- An existing blank reconciliation attempt is bound to the exact newest
  dispatch ID before handler entry.
- Independent two-cycle probe observed reconciliation IDs
  `["old-command", ""]`: a later uncertain dispatch without an ID did not reuse
  the earlier command identity.
- Conflicting non-empty reconciliation and dispatch identities fail closed as
  `ErrInvalidJournal` by inspection; neither is overwritten.

### R9-2 compatible marker merge — partially closed

- A single existing string-array marker field merges source and executor
  markers into one JSON key.
- Repeated annotation does not add the same new marker again.
- An incompatible non-array object marker uses `_mastarr_prior` and preserves
  its source object.
- Null and duplicate-key cases remain open as R10-1.

## Other passing boundaries

- Repeated malformed/foreign result matrix covered foreign target, foreign
  action, duplicate identity, changed ordinal, invalid state, and invalid JSON
  evidence. Every case remained uncertain with approved targets unknown.
- Prior CAS, lease renewal, cancellation, restart recovery, dispatch barrier,
  reservation, exact effect-set, and unresolved-count regressions passed under
  normal and race repetition.
- No schema, generated DTO, generic DAG, duplicate outbox, filesystem
  capability, or upstream write capability changed.

## Independent checks

Go commands used `GOWORK=off`; offline commands also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Product/handoff/checkpoint identity, ancestry, scoped drift, and `git diff --check` | Passed. |
| Producer round-ten focused tests, normal `-count=20` and race `-count=10` | Passed. |
| Full `internal/execution` tests, normal and race, each `-count=3` | Passed. |
| `go vet ./internal/execution` and root `go mod verify` | Passed. |
| Nine CAS/lease/cancel/restart/barrier/effect/reservation regressions, normal `-count=20` and race `-count=10` | Passed. |
| Independent external-ID, stale-ID, existing-attempt, malformed/foreign, compatible-marker, and non-array-marker probes under race, `-count=10` | Passed. |
| Independent null-marker, duplicate-marker-key, and duplicate-opaque-key probes under race, `-count=10` | Failed as expected 10/10: accepted source evidence was discarded. |
| No tracked `go.work` or local `replace` directive | Passed. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible and staged generation, all Vacuum contracts, architecture, lint, nine-module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Disposition

Do not integrate W-02 round ten as complete. Preserve the now-correct external
identity behavior, correct R10-1, add the three evidence regressions, and
re-review exact correction SHAs. Separate W-03 findings remain outside this
lane. This receipt does not authorize release, deployment, live upstream
writes, or live filesystem/media mutation.
