# W-02 independent review, round thirteen

## Decision

`approved`.

Round thirteen closes R12-1. Top-level string arrays now use the same raw-member
validation as object marker fields. Invalid UTF-8 and unpaired surrogate members
retain their exact source bytes under `_mastarr_prior` through direct annotation
and uncertain partial dispatch. Lossless arrays keep the existing compact form.
No P1 or P2 finding remains in the reviewed W-02 scope.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Exact product: `f7ca4cd9e79d753b881d46075025c18f3ad3889e`.
- Product parent: `e11ecd1a7c37d2efaaafee32521694912dc8dd67`.
- Product tree: `b12f543a06364d1a8362bf48abdd5a6df9bf340a`.
- Exact handoff: `77f30e73c51881e38ba2f1920fce190db61ec560`;
  its direct parent is the product commit.
- Handoff tree: `e9179b1254df60621948345d36a6963a98de809e`.
- Exact review-dispatch checkpoint:
  `b44c9aaccc8f438ceb242e3c92f25260c275fadb`.
- Checkpoint tree: `82d0e5b2e78cf05f348fac7b40b9e21af54b769d`.
- Product-owned paths: `internal/execution/execution.go` and
  `internal/execution/execution_test.go` only.
- Acceptance reviewed: A-14, A-32, A-33, A-34, A-35, A-36, A-59, and A-60.

Review ran in a clean detached worktree at the exact checkpoint.
`git diff --exit-code f7ca4cd b44c9aa -- internal/execution` passed. Product,
state, task, schema, generated output, and unrelated files were not edited.
Independent probes ran in a temporary archive of the exact checkpoint and used
only synthetic in-memory journals and handlers. No live service, credential,
private coordinate, inventory, or media was used.

## R12-1 closure

`appendEvidence` now sends a top-level array through `decodeExecutionMarkers`
before compacting it. That decoder first retains each member as
`json.RawMessage`, applies `losslessJSONString`, and only then converts valid
members to Go strings. Ill-formed arrays therefore bypass the compact path and
reach the exact-byte `_mastarr_prior` envelope.

Independent probes verified:

- raw invalid UTF-8 and escaped unpaired high-surrogate arrays retain their
  byte-for-byte source under `_mastarr_prior`;
- the same exact bytes survive journal write/read during an omitted effect in
  an uncertain partial dispatch;
- the omitted effect remains `unknown`, the action enters reconciliation, and a
  fresh executor reconciles without a second dispatch;
- unpaired low surrogates and high surrogates followed by text also use the raw
  prior envelope;
- valid Unicode, valid surrogate pairs, escaped literal surrogate text, and
  empty string arrays remain compact;
- arrays with non-string members use the raw prior envelope;
- repeated annotation keeps one top-level marker key, retains the original raw
  prior, and does not duplicate the generated marker.

The prior failing direct and partial-dispatch probes passed 10/10 under race.
Additional edge and repeated-annotation probes also passed 10/10 under race.

## Preserved behavior

- Object marker arrays containing invalid UTF-8 or unpaired surrogates remain
  lossless.
- Object null, non-array, non-string-member, duplicate marker-key, and duplicate
  ordinary-key forms retain their raw input with unique generated top-level
  keys.
- Compatible object and top-level marker arrays retain compact behavior.
- Malformed and foreign effect reports fail closed.
- External-ID-only dependency errors persist the current ID, enter
  reconciliation, deliver the ID to fresh executors, and never blind
  redispatch. Later dispatch attempts do not reuse stale IDs.
- CAS claims, lease renewal and fencing, cancellation, restart recovery,
  dispatch barriers, reservation retention/release, exact effect identity, and
  unresolved counts passed repeated race checks.
- No schema, generic DAG, duplicate outbox, generated DTO, filesystem
  capability, or upstream mutation capability changed.

## Independent checks

All Go commands used `GOWORK=off`; the aggregate command also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Product/handoff/checkpoint identity, ancestry, scoped drift, `git diff --check`, no tracked `go.work`, and no local `replace` | Passed. |
| Producer marker/object/top-level/partial-dispatch tests, normal `-count=20` | Passed. |
| Producer marker, partial-dispatch, and ExternalID tests under race, `-count=10` | Passed. |
| Independent prior-failing top-level direct and partial-dispatch probes under race, `-count=10` | Passed. Exact invalid bytes retained. |
| Independent low/high surrogate, valid-pair, escaped-literal, non-string, compact-array, repeated-marker, and unique-key probes under race, `-count=10` | Passed. |
| CAS/lease/cancel/restart/barrier/reservation regressions under race, `-count=5` | Passed. |
| Malformed/foreign/exact-effect fail-closed matrix under race, `-count=5` | Passed. |
| Full `internal/execution` tests, normal and race, each `-count=3` | Passed. |
| `go vet ./internal/execution` and root `go mod verify` | Passed. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible and staged generation, all Vacuum contracts, architecture, lint, nine-module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Disposition

W-02 round thirteen is approved for coordinator integration and execution-state
recording. This approval covers the reviewed local product tree only. It does
not authorize release, deployment, live upstream writes, or live
filesystem/media mutation. Separate lane findings remain outside this receipt.
