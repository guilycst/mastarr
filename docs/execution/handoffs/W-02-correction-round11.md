# W-02 correction round eleven handoff

## Assignment

- Task: W-02 correction round eleven, preserve opaque JSON evidence while
  annotating effect outcomes.
- Owner: `/root/x05_implementer`; independent reviewer: `/root/x05_reviewer`.
- Source product/base: `e46ab80265c6bfbcde7c51f29cfa04c0df421f19`.
- Prior review receipt: `62668bedf67bfe25ceac528890d7e96c0a8b9d8e`.
- Coordinator dispatch checkpoint: `8ab9eee2a50c195dda9fadf5160358d2fb535b11`.
- Product commit: `6b85c3afde31cf2772f9dc5e84a7982de504635b`.
- Branch/worktree: shared `main` checkout.
- Owned product paths: `internal/execution/`; this handoff is the only owned
  documentation path. No state, API, schema, module, adapter, client, UI or
  live-data paths were changed by this lane.
- Linked acceptance: A-14, A-32, A-33, A-34, A-35, A-36, A-59 and A-60.

## Correction

Round ten merged an opaque object into a Go map before adding execution
markers. That was safe only for objects with unique keys and a compatible
marker field. JSON `null` was accepted as a nil `[]string`, and duplicate
marker or ordinary keys were collapsed to the last value, losing source
evidence.

The marker merge now first proves that the top-level object has unique keys
with a token walk, rather than relying on map decoding to detect that fact.
When the reserved marker field exists, it must be a non-null JSON array whose
members are strings. Compatible unique objects continue to merge source and
executor markers into one field. Null, non-array, invalid-member and duplicate
key objects take the existing `_mastarr_prior` envelope, which embeds the
original raw JSON bytes and adds one top-level execution marker field. This
keeps the evidence lossless while avoiding generated duplicate fields or
shadowing handler-owned data. Invalid evidence remains outside the merge path
and the existing journal validation still fails closed before annotation.

`TestAppendEvidencePreservesOpaqueJSONKeys` covers a compatible marker array,
null marker, non-array marker, duplicate marker keys and duplicate ordinary
keys. It verifies valid output, one unique top-level envelope, marker values,
and byte-preserving `_mastarr_prior` content for every fallback. The existing
`TestErroredDispatchPersistsReturnedPartialEffectsAndReconciles` integration
regression continues to prove source markers and approved scope survive an
uncertain partial dispatch and that read-only reconciliation resolves the
effects without a second dispatch.

All prior W-02 round-nine and round-ten external-ID, CAS/lease, cancellation,
restart, barrier, reservation, exact-effect and fail-closed behavior is
unchanged.

## Verification

| Check | Result |
| --- | --- |
| `GOWORK=off go test ./internal/execution -run 'TestAppendEvidencePreservesOpaqueJSONKeys\|TestErroredDispatchPersistsReturnedPartialEffectsAndReconciles' -count=20 -timeout=240s` | Passed, exit 0. |
| `GOWORK=off go test -race ./internal/execution -run 'TestAppendEvidencePreservesOpaqueJSONKeys\|TestErroredDispatchPersistsReturnedPartialEffectsAndReconciles' -count=10 -timeout=300s` | Passed, exit 0. |
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
  `6b85c3afde31cf2772f9dc5e84a7982de504635b`.
- Handoff commit: recorded after this file is committed.
- Independent review: pending.
- Coordinator owns `docs/execution/state.json` and must record both exact
  commits. W-03 findings remain outside this W-02 lane.
- G-01 upstream mutation evidence and all live write capabilities remain
  unchanged; this correction only annotates already-journaled evidence and
  keeps uncertain actions read-only until reconciliation.

## Resume checkpoint

- Current state: product correction is committed and the complete offline
  guardrail matrix passes.
- Next safe action: coordinator records the product and handoff commits and
  dispatches independent review against the exact product tree.
- Blocker: independent review. No implementation blocker remains in this
  correction lane.
- No conflicting writes or unknown files were removed. Product changes are
  limited to `internal/execution/` and this handoff.
