# U-03 correction round one handoff

## Identity and scope

- Task: U-03 correction round one, exact review and workflow controls.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Dispatch/base checkpoint: `d4241bf0b1081892bd0874ff564813490b2914ca`.
- Prior product: `ac732775c0877932687148ddb1c77596c80d9be5`.
- Prior review receipt: `40d9c954d4db04271b84256289e56d1cd6e7f818`.
- Prior handoff: `a7536bad70b157d2db3fec6aed5b42a6ba135d50`.
- Branch/worktree: shared `main`; coordinator owns `docs/execution/state.json`.
- Owned product paths: `ui/internal/review/` and `ui/internal/workflows/`.
- Handoff path: `docs/execution/handoffs/U-03-correction-round1.md`.
- Product commit: `e5b6c6ee39b2b1bdd771ac4375661d2c0698f16f`.
- Acceptance contributions: A-12, A-13, A-15, A-20, A-35, A-37, A-48 and A-49.

## Findings closed

### R1 — joined action-run identity binding

The workflow HTTP reader now requires every linked action run to match the
requested run ID, referenced action-plan ID, workflow-run ID and step ID.
Missing or zero identities, foreign IDs, empty step IDs and unknown action-run
state/outcome values fail as sanitized protocol errors before normalization.
Referenced action plans are also required to have a nonzero response ID that
matches the requested plan. Deterministic HTTP fixtures cover foreign,
missing-run, missing-plan, missing-workflow/step and missing action-plan
identity; no foreign effect can reach the handler.

### R2 — per-step action-plan joins

Workflow normalization fetches every referenced action plan through the
generated client, binds its response ID to the workflow step, and stores the
plan revision, digest, approval gate, impacts, capabilities, estimate,
connection/mapping fences and exact manifest on that step. The normalized
action union keeps action kind, connection/provider/external/preview fields,
transfer or executor, client/torrent IDs, stopped-client prerequisites,
retention, monitoring and registration fields, trash/descriptor identities,
file/episode/subtitle/forced/SDH scope, and destructive flags. The handler
renders those per-step values, so composed workflows retain independent
bindings. HTTPReader-to-handler fixtures exercise a real workflow, plan and
action-run join.

The frozen generated workflow contract does not expose separate
source/configuration/desired digest scalars on an action plan; those normalized
fields remain explicitly unknown unless a future API projection supplies them.
Connection and mapping revision maps are retained and rendered per step.

### R3 — honest effect evidence

The workflow reader keeps observed and unresolved identities in their distinct
normalized fields and renders each identity once. It rejects empty, duplicate,
foreign-overlapping and contradictory effect sets, invalid states/outcomes,
complete applied/already-satisfied runs without any effect identity, and
aggregate or unresolved counts that disagree with step evidence. Cancellation,
partial, pending, unknown, failed and already-satisfied states remain visible
with their own evidence instead of being relabeled as success or retry-safe.
Focused synthetic regressions cover duplicate observed IDs, resolved/unresolved
overlap, empty complete outcomes and aggregate-count mismatch; the HTTP fixture
covers one observed plus one unresolved identity.

### R4 — authority-bearing review scope

Review detail now renders capabilities, estimated storage bytes, immutable
fences, exact client/torrent IDs, stopped-client prerequisites, trash
retention, retain-payload and permanent-delete values, irreversible and
unselected-payload scope, registration monitoring/season-folder/quality/root/
season fields, executor and transfer, descriptor/Jellyfin identity, and
subtitle language, forced and hearing-impaired flags. Monitoring appears only
for registration plans; transfer fallback appears only for hardlink or
Arr-import copy/hardlink plans and is described as a new plan. Workflow step
tables render the same exact identity and subtitle scope. Missing optional
authority values are shown as `unknown`. Every supported action kind has a
deterministic rendering regression.

### R5 — strict duplicate-key decoding

Both HTTP readers now walk and validate the raw JSON tree before typed decode,
rejecting duplicate object keys at every nesting depth, invalid UTF-8,
trailing data and excessive nesting. Generated action-union decoding is
supplemented with package-local strict validation for every action discriminator,
nested action fields, file/target objects and refresh scope, closing the
generated raw-union unknown-field gap while retaining
`encoding/json.DisallowUnknownFields` for the rest of each response.
Review and workflow fixtures cover nested duplicate and unknown fields.

### R6 — reason draft boundary

Draft parsing accepts UTF-8 `reason` values through 4096 bytes while retaining
the 512-byte bound for other scalar fields. Both forms preserve the submitted
reason and advertise the same `maxlength="4096"`. Boundary regressions cover
512, 513, 4096 and 4097 bytes in review and workflow handlers.

## Verification

Commands ran against product commit
`e5b6c6ee39b2b1bdd771ac4375661d2c0698f16f`, with `GOWORK=off` where shown.
No live service, credentials, database, filesystem or media operation was
used.

| Check | Result |
| --- | --- |
| `gofmt -w ui/internal/review/*.go ui/internal/workflows/*.go` | PASS |
| `GOWORK=off go test ./internal/review ./internal/workflows -count=1 -timeout=120s` from `ui/` | PASS |
| `GOWORK=off go test -race ./internal/review ./internal/workflows -count=1 -timeout=180s` from `ui/` | PASS |
| `GOWORK=off go vet ./internal/review ./internal/workflows` from `ui/` | PASS |
| `GOWORK=off go test ./... -count=1 -timeout=240s` from `ui/` | PASS |
| `GOWORK=off go test -race ./... -count=1 -timeout=300s` from `ui/` | PASS |
| `GOWORK=off go vet ./...` from `ui/` | PASS |
| `GOWORK=off go mod verify` from `ui/` | PASS; all modules verified |
| `GOWORK=off go mod tidy -diff` from `ui/` | PASS; no manifest diff |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOWORK=off go build ./...` from `ui/` | PASS |
| `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOWORK=off go build ./...` from `ui/` | PASS |
| `CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 GOWORK=off go build ./...` from `ui/` | PASS |
| `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 GOWORK=off go build ./...` from `ui/` | PASS |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-api.sh` | PASS; generation and API/Vacuum score 100/100 |
| `python3 scripts/check-architecture.py` | PASS |
| `python3 scripts/check_planning.py` | PASS; 53 tasks and 60 acceptance cases |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` under a Python process-group bound of 360 seconds | PASS; exit 0 and final `guardrail checks passed (ci)` marker; log SHA-256 `70d1365ec6e9d3dd4bc4c323dd7260a853fa7a27a587c51001bd6f14281ed57d` |
| Product commit hook | PASS; staged generation/API/Vacuum, architecture, root/UI smoke and fast guardrail checks |

## Boundaries and follow-up

The correction stays within the assigned review/workflow packages and their
tests. Generated API files, API contracts, router composition, root packages,
state files and other UI lanes were not edited. The UI remains HTTP-only and
read-only: approvals, retries, cancellation, reconciliation and all native
writes remain API-owned. G-01 Arr native registration/import writes, F-05
filesystem capability restrictions and Seerr read-only policy remain unchanged.

The standalone generated contract does not yet provide independent
source/configuration/desired scalar revisions for each action plan; the
available connection/mapping maps are retained and absent fields stay
unknown. Browser-led evidence, production router composition and independent
round-two review remain separate gates.

## Review and integration

- Product commit: `e5b6c6ee39b2b1bdd771ac4375661d2c0698f16f`.
- Handoff commit: this file, committed separately after the product.
- Independent review: pending round-two review by `/root/x05_reviewer`.
- Coordinator integration/state update: pending; this lane did not edit
  `docs/execution/state.json`.
- Blocker: none for the owned normalized readers, handlers or synthetic
  verification.
