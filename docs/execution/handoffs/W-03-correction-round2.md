# W-03 correction round two handoff

## Assignment

- Task: W-03, correction round two (R2a manifest filesystem effects)
- Owner: `/root/x05_implementer`
- Independent reviewer: `/root/x05_reviewer`
- Review source product: `0c120a1bf5e377510d332ebdadac61e40f77e652`
- Review receipt: `d11938398a0a7b2dbd4cc530212e35c751a73fb8`
- Dispatch checkpoint: `1130fbd75e0be84063642c72faf19f7a970ed723`
- Product commits: `533313c5d1f91e4573078bf5fa1d2939e21c17b0`, `4871bc4999f18f2d6aa119e366ca949b6886ac45`
- Final product tree: `4871bc4999f18f2d6aa119e366ca949b6886ac45`
- Owned product paths: `internal/actions/`
- Handoff path: `docs/execution/handoffs/W-03-correction-round2.md`
- Checkout: shared `main`; coordinator-owned execution state was not edited

## Correction

`FilesystemHandler` now derives its approved source identity set from the
exact action payload. Trash and permanent-delete intents use the recursively
flattened entries in `intent.Manifest`, while copy, hardlink, move, rename and
restore use the expanded concrete mappings from `intent.Files`. A returned
`FilesystemEffect.Affected` entry must match exactly one approved manifest or
mapping entry by root, relative path, type, size, identity and compatible
digest. Foreign, duplicate, ambiguous or structurally unapproved entries fail
closed and cannot produce an accepted result.

On an errored dispatch, the handler returns only the validated affected subset,
with its outcome-derived per-target state, port observation time and combined
handler evidence. Untouched targets are omitted from the handler result so the
durable executor can apply its reviewed contract: persist the returned subset
and mark unreported planned targets unknown. A directory manifest may return
both its parent and its approved children; these distinct source identities are
validated independently and map to the one corresponding top-level effect.

Successful dispatches still require the existing exact read-back path. If a
successful port includes affected entries, the same identity validation runs
before the result can be accepted. An invalid outcome or invalid affected set
is surfaced as dispatched uncertainty, preserving the no-blind-retry rule.

## Synthetic regressions

The action package covers repeated deterministic probes for:

- trash and permanent-delete manifest effects returned with a source-change
  error, preserving applied state, port observation time and evidence;
- a nested directory manifest returning both an approved child and parent;
- foreign, duplicate and foreign-without-error affected reports, all rejected
  without an accepted result;
- a two-file mapping with only the first source affected, returning only the
  first mapped effect and retaining its evidence;
- the existing copy partial-effect uncertainty regression; and
- a real `execution.Executor` over a temporary SQLite journal and the real
  `FilesystemHandler`, proving the affected mapping is journaled as applied,
  the omitted mapping is journaled as unknown with
  `dispatch_result_unreported`, and the action enters reconciliation with one
  unresolved effect.

All fixtures are synthetic in-memory ports or temporary SQLite state. No live
service, credential, private coordinate, inventory or media data was used.

## Verification

All commands used `GOWORK=off`; the aggregate additionally disabled network
module resolution with `GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| `gofmt -w internal/actions/filesystem.go internal/actions/actions_test.go internal/actions/executor_integration_test.go` | Passed |
| `git diff --check` | Passed |
| `GOWORK=off go test ./internal/actions -count=3 -timeout=180s` | Passed, exit 0 |
| `GOWORK=off go test -race ./internal/actions -count=3 -timeout=240s` | Passed, exit 0 |
| Focused manifest/partial-effect tests under `-race -count=10` | Passed, exit 0 |
| Executor integration under `-race -count=10` | Passed, exit 0 |
| `GOWORK=off go vet ./internal/actions` | Passed, exit 0 |
| `GOWORK=off go mod verify` | Passed, all modules verified |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0; final product tree aggregate completed in 102.34 seconds, including generation, staged generation, all bundled and standalone Vacuum checks, architecture, lint, root/UI/tools/client tests, race/vet/module verification and Linux amd64/arm64 CGO-free builds |
| Product pre-commit hook | Passed for both product commits; generation, staged generation, API/Vacuum, architecture and fast guardrails passed |

The W-02 executor R2b correction is the approved cross-lane dependency that
persists this handler result on returned errors. G-01 remains open: native Arr
registration/import writes stay fail-closed. F-05 remains capability-blocked
where its reviewed filesystem port reports unsupported. This lane adds no
native upstream writes, generated DTO exposure, direct transport or fallback
transfer behavior.

The final product checkpoint is ready for independent review. The coordinator
owns the execution-state update and integration decision.
