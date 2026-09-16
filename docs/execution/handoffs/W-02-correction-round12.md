# W-02 correction round twelve handoff

## Assignment

- Task: W-02 correction round twelve, preserve ill-formed Unicode in opaque
  execution-marker evidence.
- Owner: `/root/x05_implementer`; independent reviewer: `/root/x05_reviewer`.
- Source product/base: `6b85c3afde31cf2772f9dc5e84a7982de504635b`.
- Prior review receipt: `5c7b7933f9d694e4ad768d4211a119dda06b511a`.
- Coordinator dispatch checkpoint: `de65cae07d376024ba44c01bf9426d9de8e51da3`.
- Product commit: `c3c15f149cd6b13630ef44696295f2eddb1a3ff5`.
- Branch/worktree: shared `main` checkout.
- Owned product paths: `internal/execution/`; this handoff is the only owned
  documentation path. No state, API, schema, module, adapter, client, UI or
  live-data paths were changed by this lane.
- Linked acceptance: A-14, A-32, A-33, A-34, A-35, A-36, A-59 and A-60.

## Correction

Round eleven correctly routed null, non-array, non-string and duplicate-key
objects through the raw `_mastarr_prior` envelope. One loss remained in the
compatible marker-array path: Go's JSON decoder accepts invalid UTF-8 and
escaped unpaired UTF-16 surrogates in string values, then replaces them with
U+FFFD when decoding into `string`. Re-encoding those values changed opaque
source evidence.

Marker arrays are now merge-compatible only when each raw member is a
lossless JSON string. The validator checks raw UTF-8 bytes and validates
surrogate escapes, requiring high surrogates to have an adjacent low-surrogate
pair and rejecting standalone low surrogates. Raw invalid UTF-8 and escaped
unpaired surrogates therefore use the existing `_mastarr_prior` envelope,
which embeds the original bytes and adds a single generated marker field.
Valid Unicode, valid escapes and valid surrogate pairs remain merge-compatible;
structurally malformed JSON and invalid marker shapes still fail closed through
the existing evidence validation and fallback boundaries.

`TestAppendEvidencePreservesOpaqueJSONKeys` now covers non-string members,
escaped unpaired surrogates and a raw `0xff` marker byte in addition to the
round-ten null, non-array and duplicate-key cases. It checks that fallback
`_mastarr_prior` bytes exactly match the input. The
`TestPartialDispatchPreservesIllFormedMarkerEvidence` integration regression
exercises both ill-formed forms as omitted targets in an uncertain partial
dispatch, verifies the unknown effect retains exact source bytes, and proves a
fresh executor completes read-only reconciliation with one dispatch and one
reconciliation.

All prior external-ID, CAS/lease, cancellation, restart, barrier,
reservation, exact-effect, duplicate-key and fail-closed behavior is
unchanged.

## Verification

| Check | Result |
| --- | --- |
| `GOWORK=off go test ./internal/execution -run 'Test(AppendEvidencePreservesOpaqueJSONKeys\|PartialDispatchPreservesIllFormedMarkerEvidence\|ErroredDispatchPersistsReturnedPartialEffectsAndReconciles)' -count=20 -timeout=240s` | Passed, exit 0. |
| `GOWORK=off go test -race ./internal/execution -run 'Test(AppendEvidencePreservesOpaqueJSONKeys\|PartialDispatchPreservesIllFormedMarkerEvidence\|ErroredDispatchPersistsReturnedPartialEffectsAndReconciles)' -count=10 -timeout=300s` | Passed, exit 0. |
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
  `c3c15f149cd6b13630ef44696295f2eddb1a3ff5`.
- Handoff commit: recorded after this file is committed.
- Independent review: pending.
- Coordinator owns `docs/execution/state.json` and must record both exact
  commits. W-03 findings remain outside this W-02 lane.
- G-01 upstream mutation evidence and all live write capabilities remain
  unchanged; this correction only preserves already-journaled evidence and
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
