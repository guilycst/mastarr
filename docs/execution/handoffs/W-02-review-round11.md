# W-02 independent review, round eleven

## Decision

`changes_requested`.

Round eleven closes the null, non-array, non-string member, and duplicate-key
cases from round ten. It also preserves all prior external-ID and durable
executor behavior. One P2 remains: the compatible marker-array path decodes
opaque string members through `encoding/json`, which normalizes accepted
ill-formed Unicode to `U+FFFD`. The resulting journal evidence no longer
contains the original bytes.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Exact product: `6b85c3afde31cf2772f9dc5e84a7982de504635b`.
- Product parent: `883aaa5f9b193c31d72542fc762e6f330e1c7944`.
- Product tree: `ba37ad70ef7dd2e75e751e8bfe7cdae18d2c347f`.
- Exact handoff: `a9609d0affcc2ec811ca860f22576771acf9a88a`;
  its direct parent is the product commit.
- Handoff tree: `7fcc6000b72103cafbc9b9e2c3a1b1ddb7effa1d`.
- Exact review-dispatch checkpoint:
  `a7fa687f5a7cb621a5610b6a9779e421e5d22c4e`.
- Checkpoint tree: `f3a2deeb42c7288418278fd05080a6cdf7432e30`.
- Product-owned paths: `internal/execution/execution.go` and
  `internal/execution/execution_test.go` only.
- Acceptance reviewed: A-14, A-32, A-33, A-34, A-35, A-36, A-59, and A-60.

Review ran in a clean detached worktree at the exact checkpoint.
`git diff --exit-code 6b85c3a a7fa687 -- internal/execution` passed. Product,
state, task, schema, generated output, and unrelated files were not edited.
Independent probes ran only in a temporary archive of the exact product tree.
They used synthetic in-memory and SQLite journals and handlers; no live
service, credential, private coordinate, inventory, or media was used.

## Finding

### R11-1 — P2: compatible marker decoding can rewrite opaque source bytes

`decodeExecutionMarkers` at `internal/execution/execution.go:3048-3068`
accepts any member that `json.Unmarshal` can decode into a Go string. Go's JSON
decoder replaces invalid UTF-8 and escaped unpaired UTF-16 surrogates with the
Unicode replacement character rather than returning an error. Both inputs are
accepted by the current `json.Valid` and effect-validation boundary.

The decoded strings are later marshaled into a replacement marker array at
lines 2995-3004. This makes the merge appear compatible while changing opaque
source evidence.

Independent ASCII/UTF-8 fixture:

```json
{"_mastarr_execution_markers":["\ud800"],"approved_scope":"two.bin"}
```

Observed journal evidence:

```json
{"_mastarr_execution_markers":["�","dispatch_result_unreported"],"approved_scope":"two.bin"}
```

The original `\ud800` bytes were absent, and no `_mastarr_prior` envelope was
created. A second fixture containing raw byte `0xff` inside the marker string
was also accepted by `json.Valid` and normalized to `�`. Direct helper and
full partial-dispatch integration probes reproduced the loss in 10/10 race
repetitions.

The omitted target remains `unknown`, so this does not permit blind mutation.
It still violates the lane's lossless opaque-evidence requirement and weakens
A-59/A-60 recovery audit data.

Required correction: treat a marker array as merge-compatible only when every
raw string member can be retained losslessly. Preserve compatible source
members as raw JSON while appending the executor marker, or route invalid UTF-8
and unpaired-surrogate members through `_mastarr_prior`. Add permanent direct
and partial-dispatch regressions for both forms. Also add the claimed
invalid-member fixture to `TestAppendEvidencePreservesOpaqueJSONKeys`; current
runtime behavior passes independent invalid-member tests, but the permanent
table does not contain that case.

## Closed round-ten finding

- `null` marker fields use `_mastarr_prior`; original bytes and generated marker
  remain separate.
- Non-array marker fields use the same lossless envelope.
- Arrays containing `null`, number, boolean, object, or array members use the
  lossless envelope in independent race probes.
- Duplicate marker keys and duplicate ordinary keys are detected before map
  decoding. Their complete raw source object is retained under
  `_mastarr_prior`, while the generated top-level envelope has unique keys.
- Compatible string arrays merge into one marker field. Repeated annotation
  adds `dispatch_result_unreported` at most once and does not shadow other
  source fields.
- Invalid JSON reports remain fail-closed before annotation.

## Preserved external-ID and executor behavior

- External-ID-only dependency errors enter reconciliation and deliver the
  exact current ID to fresh and restarted handlers with zero blind second
  dispatch.
- Accepted-command read-back failures retain the external ID.
- Existing blank reconciliation attempts bind the newest dispatch identity.
- A later dispatch with no ID does not reuse an earlier command ID.
- Malformed/foreign reports, CAS, lease renewal, cancellation, restart
  recovery, dispatch barriers, reservations, exact effect sets, and unresolved
  counts passed repeated race checks.
- No schema, generated DTO, generic DAG, duplicate outbox, filesystem
  capability, or upstream write capability changed.

## Independent checks

Go commands used `GOWORK=off`; offline commands also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Product/handoff/checkpoint identity, ancestry, scoped drift, and `git diff --check` | Passed. |
| Producer round-eleven focused tests, normal `-count=20` and race `-count=10` | Passed. |
| Full `internal/execution` tests, normal and race, each `-count=3` | Passed. |
| `go vet ./internal/execution` and root `go mod verify` | Passed. |
| Nine CAS/lease/cancel/restart/barrier/effect/reservation regressions under race, `-count=10` | Passed. |
| Independent null/non-array/invalid-member/duplicate-key/repeated-marker probes under race, `-count=10` | Passed losslessly with unique generated envelope keys. |
| Independent malformed/foreign fail-closed matrix and external-ID lifecycle probes under race, `-count=10` | Passed. |
| Independent invalid-UTF-8 and unpaired-surrogate direct probes under race, `-count=10` | Failed as expected 10/10: source bytes became `U+FFFD`. |
| Independent unpaired-surrogate partial-dispatch integration probe under race, `-count=10` | Failed as expected 10/10 with target unknown but source evidence rewritten. |
| No tracked `go.work` or local `replace` directive | Passed. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible and staged generation, all Vacuum contracts, architecture, lint, nine-module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Disposition

Do not integrate W-02 round eleven as complete. Preserve all closed cases,
correct R11-1, add the required permanent fixtures, and re-review exact correction
SHAs. Separate W-03 findings remain outside this lane. This receipt does not
authorize release, deployment, live upstream writes, or live filesystem/media
mutation.
