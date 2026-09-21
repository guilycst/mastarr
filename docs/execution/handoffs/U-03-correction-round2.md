# U-03 correction round two implementer handoff

## Identity and scope

- Task: U-03 correction round two, strict action unions and workflow effect
  identity.
- Owner: `/root/c01_implementer`.
- Independent reviewer: `/root/c01_reviewer`.
- Clean dispatch checkpoint: `988f8ad`.
- Independent review receipt: `7728d0118f334b837e7d4fc30b73b0adeedfd6`.
- Prior product: `e5b6c6ee39b2b1bdd771ac4375661d2c0698f16f`.
- Prior correction handoff: `4047e726a9289d23db45d1b0f93c8dc03eca532a`.
- Product commit: `75df6ac7bc5903bcdbfd9eae7a07862d033b6dc0`.
- Owned product paths:
  - `ui/internal/review/`
  - `ui/internal/workflows/`
- Handoff path: `docs/execution/handoffs/U-03-correction-round2.md`.
- Shared checkout: `main`; coordinator owns `docs/execution/state.json`.
- The state ledger, API contract and generated API files were not edited.

## RR2 findings closed

The two strict generated-union readers now select an explicit file shape for
each action: Arr import records retain `source` plus `movieOrEpisodeId`, mapped
filesystem actions retain `source` plus `destination`, and `fs.trash` plus
`fs.delete` validate direct `FileTarget` members containing only `rootId` and
`relativePath`. The existing raw JSON walk still rejects duplicate keys,
unknown fields, invalid UTF-8, trailing data and excessive nesting before typed
decoding.

Synthetic generated-client-to-handler fixtures now cover every supported action
discriminator in both production readers. The trash fixture preserves 30-day
retention and the exact stopped torrent ID. The delete fixture preserves
permanent and irreversible acknowledgement flags, the exact selected target,
and the rendered statement that unselected payload remains outside the plan.
The Jellyfin refresh union is projected through its nested library/item union
so that this discriminator also reaches the normalized handler path.

Workflow validation now owns one effect identity set for the complete workflow.
Observed and unresolved IDs are rejected when repeated within a step, across
steps, across action runs, or across resolved/unresolved sets. Aggregate and
unresolved counts are accumulated from the unique workflow-wide evidence.
Production generated workflow fixtures use two separate action runs to prove
both repeated observed IDs and cross-resolved IDs fail closed while ordinary
queued, partial, cancelled and already-satisfied semantics remain available.

## Verification

All commands below completed with exit code 0. UI-module commands ran from
`ui/`; repository commands ran from the repository root. Fixtures are synthetic
and no live service, credential, database, filesystem or media operation was
used.

| Check | Result |
| --- | --- |
| `gofmt -w ui/internal/review/http.go ui/internal/review/http_test.go ui/internal/review/json.go ui/internal/workflows/http.go ui/internal/workflows/http_test.go ui/internal/workflows/json.go ui/internal/workflows/model.go` | PASS |
| `git diff --check` | PASS |
| `GOWORK=off go test ./internal/review ./internal/workflows -count=1 -timeout=120s` | PASS, exit 0 |
| `GOWORK=off go test -race ./internal/review ./internal/workflows -count=1 -timeout=180s` | PASS, exit 0 |
| `GOWORK=off go vet ./internal/review ./internal/workflows` | PASS, exit 0 |
| `GOWORK=off go mod verify` | PASS, exit 0; all modules verified |
| `GOWORK=off go mod tidy -diff` | PASS, exit 0; no manifest diff |
| `GOWORK=off go test ./... -count=1 -timeout=240s` | PASS, exit 0 |
| `GOWORK=off go test -race ./... -count=1 -timeout=300s` | PASS, exit 0 |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOWORK=off go build ./...` | PASS, exit 0 |
| `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOWORK=off go build ./...` | PASS, exit 0 |
| `GOWORK=off ./scripts/generate.sh --check` | PASS, exit 0; generation and staged generation checks passed |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-api.sh` | PASS, exit 0; API/Vacuum score 100/100 |
| `python3 scripts/check-architecture.py` | PASS, exit 0 |
| `python3 scripts/check_planning.py` | PASS, exit 0; 53 tasks and 60 acceptance cases |
| `GOWORK=off ./scripts/check-lint.sh` | PASS, exit 0 |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS, exit 0; final marker `guardrail checks passed (ci)` |
| Product commit hook for `75df6ac7bc5903bcdbfd9eae7a07862d033b6dc0` | PASS, exit 0; fast guardrail completed |

Focused production regressions run uncached with exit 0:

- `GOWORK=off go test -count=1 -run 'GeneratedActionFixtures|CrossShape|RejectsCrossShape' ./internal/review`
- `GOWORK=off go test -count=1 -run 'GeneratedActionFixtures|CrossStepEffect' ./internal/workflows`

## Acceptance contributions and remaining review

This correction closes the production-path portions of A-13, A-15, A-20,
A-35, A-37, A-48 and A-49 while preserving the separate registration/import
presentation and exact approval scope in A-12. No UI mutation path was added:
GET and HEAD remain the only handler methods, and approvals, retries,
cancellation, reconciliation and native writes remain API-owned.

Independent round-two review and coordinator state integration remain pending.
Browser/router composition is outside this package correction and was not
claimed. No product blocker remains in the owned paths.
