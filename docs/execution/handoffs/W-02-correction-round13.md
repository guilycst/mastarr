# W-02 correction round thirteen handoff

## Assignment

- Task: W-02 correction round thirteen, preserve ill-formed Unicode in
  top-level opaque execution-marker arrays.
- Owner: `/root/x05_implementer`; independent reviewer: `/root/x05_reviewer`.
- Source product/base: `c3c15f149cd6b13630ef44696295f2eddb1a3ff5`.
- Prior review receipt: `a6c49e88ad2f89ca7af65c806bdb426ba96ccdc5`.
- Coordinator dispatch checkpoint: `42c9eb0034f430ae139ee8e2a59bc914ab418d95`.
- Product commit: `f7ca4cd9e79d753b881d46075025c18f3ad3889e`.
- Branch/worktree: shared `main` checkout.
- Owned product paths: `internal/execution/`; this handoff is the only owned
  documentation path. No state, API, schema, module, adapter, client, UI or
  live-data paths were changed by this lane.
- Linked acceptance: A-14, A-32, A-33, A-34, A-35, A-36, A-59 and A-60.

## Correction

Round twelve made object marker fields lossless by validating each raw string
member before decoding it. The top-level array branch in `appendEvidence`
still decoded directly into `[]string`, so Go's JSON decoder could replace an
escaped unpaired surrogate or invalid UTF-8 with U+FFFD before the evidence was
journaled.

Top-level arrays now use the same raw-member `decodeExecutionMarkers` validator
as object marker fields. A lossless string array retains the existing compact
`{"evidence":[...]}` annotation. An array containing an escaped unpaired
surrogate or invalid UTF-8 cannot use that form; the valid-array fallback wraps
the exact original bytes in `_mastarr_prior` and emits one
`_mastarr_execution_markers` field. This keeps the evidence opaque and avoids
duplicate marker keys. Arrays with non-string members remain outside the
compact merge path and continue through the existing fail-closed boundaries.

`TestAppendEvidencePreservesIllFormedTopLevelArrayEvidence` covers an escaped
unpaired surrogate, a raw `0xff` byte in a JSON string, and a valid string array
that remains compact. It checks exact prior bytes, valid output and unique
top-level keys. `TestPartialDispatchPreservesIllFormedTopLevelArrayEvidence`
exercises both unsafe arrays as an omitted effect in an uncertain partial
dispatch, checks the exact raw prior and one generated marker after journal
read-back, and proves a fresh executor completes read-only reconciliation with
one dispatch and one reconciliation. The earlier object and duplicate-key
regressions remain unchanged.

All prior external-ID, CAS/lease, cancellation, restart, barrier, reservation,
exact-effect, opaque-object, duplicate-key and fail-closed behavior is
unchanged.

## Verification

| Check | Result |
| --- | --- |
| `gofmt -w internal/execution/execution.go internal/execution/execution_test.go` | Passed. |
| `git diff --check` | Passed. |
| `GOWORK=off go test ./internal/execution -run 'Test(AppendEvidencePreservesOpaqueJSONKeys\|AppendEvidencePreservesIllFormedTopLevelArrayEvidence\|PartialDispatchPreservesIllFormedMarkerEvidence\|PartialDispatchPreservesIllFormedTopLevelArrayEvidence)' -count=20 -timeout=240s` | Passed, exit 0. |
| `GOWORK=off go test -race ./internal/execution -run 'Test(AppendEvidencePreservesOpaqueJSONKeys\|AppendEvidencePreservesIllFormedTopLevelArrayEvidence\|PartialDispatchPreservesIllFormedMarkerEvidence\|PartialDispatchPreservesIllFormedTopLevelArrayEvidence)' -count=10 -timeout=300s` | Passed, exit 0. |
| `GOWORK=off go test ./internal/execution -count=3 -timeout=240s` | Passed, exit 0. |
| `GOWORK=off go test -race ./internal/execution -count=2 -timeout=300s` | Passed, exit 0. |
| `GOWORK=off go vet ./internal/execution` | Passed, exit 0. |
| `GOWORK=off go mod verify` | Passed: all modules verified. |
| Versioned pre-commit fast hook during the product commit | Passed: generation, staged generation, Vacuum for root and standalone contracts, architecture, and targeted Go checks. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0: reproducible generation, staged generation, zero-warning/error Vacuum for root and standalone contracts, architecture, lint, root and nested module tests including race/vet, module verification and Linux amd64/arm64 cross-build matrix. |
| Credentials, private coordinates, live services, real media or destructive writes | Not used; all evidence and execution fixtures are synthetic. |

## Review and integration

- Product tree for independent review:
  `f7ca4cd9e79d753b881d46075025c18f3ad3889e`.
- Handoff commit: recorded after this file is committed.
- Independent review: pending.
- Coordinator owns `docs/execution/state.json` and must record both exact
  commits. Other execution lanes remain outside this correction.
- G-01 upstream mutation evidence and all live write capabilities remain
  unchanged; this correction only preserves already-journaled opaque evidence
  and keeps uncertain actions read-only until reconciliation.

## Resume checkpoint

- Current state: product correction is committed and the complete offline
  guardrail matrix passes.
- Next safe action: coordinator records the product and handoff commits and
  dispatches independent review against the exact product tree.
- Blocker: independent review. No implementation blocker remains in this
  correction lane.
- No conflicting writes or unknown files were removed. Product changes are
  limited to `internal/execution/` and this handoff.
