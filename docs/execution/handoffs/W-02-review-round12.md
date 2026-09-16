# W-02 independent review, round twelve

## Decision

`changes_requested`.

The correction preserves ill-formed marker members when they occur in the
`_mastarr_execution_markers` field of an object. It does not close the same
loss in the older top-level string-array shortcut in `appendEvidence`. Direct
annotation and uncertain partial dispatch still normalize raw invalid UTF-8
and escaped unpaired surrogates to `U+FFFD`, discard the original bytes, and
emit no `_mastarr_prior` envelope. This is one remaining P2 against A-59/A-60.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Exact product: `c3c15f149cd6b13630ef44696295f2eddb1a3ff5`.
- Product parent: `e6ac914f157091b24aac4d5a7d073c3b8d59ae74`.
- Product tree: `27ddadbbca9114a19c1f1ecb4757cce553cdb2f5`.
- Exact handoff: `def4023ee075b1483b1f4c062010f19d0d295106`;
  its direct parent is the product commit.
- Handoff tree: `3f9cd1525231e6544582d05e9a472a2cc90cf24f`.
- Exact review-dispatch checkpoint:
  `4640d0d8a5fe3f7f9ab4f979b17634d9fe333dc9`.
- Checkpoint tree: `0fddac4d2f388a9edf1f4e8c5e2e31f58ac7c738`.
- Product-owned paths: `internal/execution/execution.go` and
  `internal/execution/execution_test.go` only.
- Acceptance reviewed: A-14, A-32, A-33, A-34, A-35, A-36, A-59, and A-60.

Review ran in a clean detached worktree at the exact checkpoint.
`git diff --exit-code c3c15f1 4640d0d -- internal/execution` passed. Product,
state, task, schema, generated output, and unrelated files were not edited.
Independent probes ran in a temporary archive of the exact checkpoint and used
only synthetic in-memory journals and handlers. No live service, credential,
private coordinate, inventory, or media was used.

## Finding

### R12-1 — P2: top-level marker arrays still rewrite ill-formed source bytes

`appendEvidence` at `internal/execution/execution.go:2934-2936` decodes any
top-level JSON string array directly into `[]string` before reaching the raw
non-object envelope at lines 2948-2959. That shortcut does not call the new
`losslessJSONString` validator.

Both of these valid-to-Go inputs enter the shortcut:

```text
["\ud800"]
["<raw byte 0xff>"]
```

For each input, direct `appendEvidence` returned the semantic equivalent of:

```json
{"evidence":["�","dispatch_result_unreported"]}
```

It did not return an object containing the byte-for-byte original value under
`_mastarr_prior`. The same result was persisted for an omitted effect during an
uncertain partial dispatch. The effect remained `unknown` and the action entered
read-only reconciliation, but its opaque recovery evidence was changed.

Independent race probes repeated both inputs through both paths ten times.
All 40 assertions failed deterministically: 20 direct cases lacked
`_mastarr_prior`, and 20 partial-dispatch cases lacked the original prior value.
The producer regression table covers the object-field shape only, so it cannot
detect this bypass.

Required correction: validate raw members in the top-level array before
unmarshalling to `[]string`, or remove the shortcut and route non-object values
through the raw `_mastarr_prior` envelope. Ill-formed top-level arrays must
retain exact source bytes in direct and partial-dispatch tests. Valid top-level
marker arrays may retain their existing behavior if every member passes the
lossless validator.

## Closed and preserved behavior

- Object `_mastarr_execution_markers` arrays containing raw invalid UTF-8 or an
  escaped unpaired surrogate now use `_mastarr_prior` and retain exact bytes.
- Object null, non-array, non-string-member, duplicate marker-key, and duplicate
  ordinary-key forms remain lossless with unique generated top-level keys.
- Compatible object marker arrays merge without duplicate marker keys.
- Malformed and foreign effect reports fail closed.
- External-ID-only dependency errors persist the current ID, enter
  reconciliation, deliver the ID to fresh executors, and do not blind
  redispatch. Later dispatch attempts do not reuse stale IDs.
- CAS, lease renewal and fencing, cancellation, restart recovery, dispatch
  barriers, reservation retention/release, exact effect identity, and unresolved
  counts passed repeated race checks.
- No schema, generic DAG, duplicate outbox, generated DTO, filesystem capability,
  or upstream mutation capability changed.

## Independent checks

All Go commands used `GOWORK=off`; the aggregate command also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Product/handoff/checkpoint identity, ancestry, scoped drift, `git diff --check`, no tracked `go.work`, and no local `replace` | Passed. |
| Producer focused correction tests, normal `-count=20` | Passed. |
| Producer correction, ExternalID, and partial-dispatch tests under race, `-count=10` | Passed. |
| CAS/lease/cancel/restart/barrier/reservation regressions under race, `-count=5` | Passed. |
| Malformed/foreign/exact-effect fail-closed matrix under race, `-count=3` | Passed. |
| Full `internal/execution` tests, normal and race, each `-count=3` | Passed. |
| `go vet ./internal/execution` and root `go mod verify` | Passed. |
| Independent top-level invalid-UTF-8 and unpaired-surrogate direct and partial-dispatch probes under race, `-count=10` | Failed as expected: all 40 cases lost the raw prior bytes. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible and staged generation, all Vacuum contracts, architecture, lint, nine-module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Disposition

Do not mark W-02 complete. Preserve the object-field correction and all prior
executor fixes, close R12-1 for top-level marker arrays, and re-review the exact
correction. This receipt does not authorize release, deployment, live upstream
writes, or live filesystem/media mutation.
