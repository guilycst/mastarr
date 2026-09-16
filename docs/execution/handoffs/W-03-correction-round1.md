# W-03 correction round one handoff

## Assignment

- Task: W-03, correction round one (R1-R4 action-handler safety)
- Owner: `/root/x05_implementer`
- Independent reviewer: `/root/x05_reviewer`
- Review source product: `fc2bab90bd3db21c6801842babee6b473e2742da`
- Review receipt: `577a15a61947ac44ec7621d381b63a4a6cb87a1a`
- Dispatch checkpoint: `0d81a75477afd9825b126a90e2791b2c596d8ee7`
- Product commit: `0c120a1bf5e377510d332ebdadac61e40f77e652`
- Owned product paths: `internal/actions/`
- Handoff path: `docs/execution/handoffs/W-03-correction-round1.md`
- Checkout: shared `main`; no state or unrelated paths were edited

## Corrections

- **R1, partial reconciliation:** Arr import and filesystem reconciliation now
  preserve the exact per-target effect state and evidence returned by the
  read-back. `SafeToRetry` is set only when every exact target remains pending;
  mixed already-satisfied, applied, failed, cancelled or unknown effects stay
  unresolved and cannot re-dispatch the original batch blindly. Terminal and
  unknown effect helpers also retain existing per-target evidence.
- **R2, filesystem uncertainty:** filesystem dispatch retains an affected
  subset returned together with an error, maps each affected source identity
  back to its approved target, and returns a dispatched/uncertain failure when
  the port result or error permits that a mutation occurred. Returned effect
  evidence is retained for later reconciliation.
- **R3, native rename scope:** native rename rejects a source/destination root
  mismatch, containing-directory change, or invalid destination basename before
  linked-client observation or native dispatch. The positive path remains a
  same-root, same-directory file/folder operation.
- **R4, linked-client routing and serialization:** filesystem actions resolve
  the immutable `LinkedDownload` reference through a normalized resolver or
  exact per-reference map, verify the returned observation belongs to that
  reference, and reserve `client:<connection>:<external-id>` alongside path
  reservations. Client stop/remove actions use the same reservation key.

## Synthetic regressions

`internal/actions/actions_test.go` covers repeated deterministic probes for:

- mixed Arr import effects and mixed filesystem destination effects;
- an affected filesystem effect returned with `placement.ErrSourceChanged`,
  including preserved evidence and dispatched uncertainty;
- cross-root native rename rejection with zero linked-client observation or
  rename calls;
- per-reference linked-client resolution across two synthetic clients and the
  shared reservation key.

All fixtures are in-memory synthetic ports. No credentials, cookies, private
coordinates, live services, inventories or media were used.

## Verification

| Command | Result |
| --- | --- |
| `gofmt -w internal/actions/actions.go internal/actions/arr.go internal/actions/filesystem.go internal/actions/actions_test.go` | Passed |
| `git diff --check` | Passed |
| `GOWORK=off go test ./internal/actions -count=3 -timeout=180s` | Passed, exit 0 |
| `GOWORK=off go test -race ./internal/actions -count=3 -timeout=240s` | Passed, exit 0 |
| `GOWORK=off go vet ./internal/actions` | Passed, exit 0 |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0; generation, staged generation, bundled and standalone Vacuum, architecture, lint, root/UI/tools/client module tests, race/vet/module verification and Linux amd64/arm64 CGO-free builds completed |
| Product pre-commit hook | Passed; generation, staged generation, Vacuum, architecture and fast guardrails completed |

## Gates and next action

G-01 remains open: native Arr registration/import writes remain fail-closed and
were not enabled by this correction. F-05 remains capability-blocked wherever
its reviewed filesystem port reports unsupported. Native client writes remain
limited to the separately reviewed control contracts; this action package does
not add upstream transport or generated DTOs.

The product checkpoint is ready for independent review. The coordinator owns
the execution state update and any integration decision.
