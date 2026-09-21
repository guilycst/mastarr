# U-04 correction round two handoff

## Identity and scope

- Task: U-04 correction round two, encoded endpoint credential rejection.
- Owner: `/root/c01_implementer`.
- Independent reviewer: `/root/c01_reviewer`.
- Dispatch/base checkpoint: `3342f17d5ce4615564108da2b3a4ce0574fa39b9`.
- Review receipt: `f7d7983284858f855b1c4600952ed7283c4b6a01`.
- Prior correction product: `067ebe5fbef03ae915bf31d22234d637a9147cb2`.
- Prior correction handoff: `51dedbcebb6a998c1302acb0544d1d0033c13e35`.
- Branch/worktree: shared `main`; coordinator owns `docs/execution/state.json`.
- Owned product paths: `ui/internal/settings/`.
- Handoff path: `docs/execution/handoffs/U-04-correction-round2.md`.
- Product commit: `f9bf91d9c4235196b7a50ec56e3d2039f233b1e9`.

## Finding closed

### P1-2 — bounded canonical and nested URL credential rejection

Endpoint drafts now inspect raw, parsed and URL-component representations, then
apply bounded repeated percent decoding to canonical candidates. Userinfo,
query, fragment, opaque URL forms, malformed escapes and credential markers are
rejected at every inspected layer. The bounded four-pass limit fails closed for
deeper nested escaping, and decoded candidates are size-checked before further
inspection. No rejected value reaches normalized state, the reader, or HTML.

Production handler fixtures cover literal userinfo/query/fragment, one-level
escaped markers and nested escaped markers. Each request returns `400`, calls
the wrapped production reader zero times and emits no raw, decoded or encoded
credential marker/value. A safe endpoint draft remains accepted and rendered.

## Verification

Commands ran against product commit `f9bf91d9c4235196b7a50ec56e3d2039f233b1e9`.
All fixtures use synthetic `httptest` services; no live service, credential,
database, filesystem or media operation was used.

| Check | Result |
| --- | --- |
| `gofmt -w ui/internal/settings/model.go ui/internal/settings/http_test.go` | PASS |
| `git diff --check` | PASS |
| `GOWORK=off go test ./internal/settings ./internal/trash -count=1` from `ui/` | PASS |
| `GOWORK=off go test ./... -count=1` from `ui/` | PASS |
| `GOWORK=off go test -race ./... -count=1` from `ui/` | PASS |
| `GOWORK=off go vet ./...` from `ui/` | PASS |
| `GOWORK=off go mod verify` from `ui/` | PASS; all modules verified |
| `GOWORK=off go mod tidy -diff` from `ui/` | PASS; no manifest diff |
| `GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` from `ui/` | PASS |
| `GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` from `ui/` | PASS |
| `GOWORK=off ./scripts/generate.sh --check` | PASS; generation and staged generation checks passed |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-api.sh` | PASS; API/Vacuum score 100/100 |
| `python3 scripts/check-architecture.py` | PASS |
| `GOWORK=off ./scripts/check-lint.sh` | PASS; lint and architecture boundaries passed |
| `python3 scripts/check_planning.py` | PASS; 53 tasks and 60 acceptance cases |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 and `guardrail checks passed (ci)` |
| Product commit hook | PASS; staged generation/API/Vacuum, architecture and fast guardrail checks |

## Boundaries and follow-up

Correction changes stay within `ui/internal/settings/` and this handoff. Trash
files, API/OpenAPI/generated files, router composition, root packages, state
files and other UI lanes were not edited. UI remains HTTP-only and read-only;
settings forms preserve safe GET draft context and never dispatch credential or
configuration mutations.

Frozen configuration and trash omissions remain explicitly unknown. Browser
integration, production router composition and independent correction review
remain separate gates.

## Review and integration

- Product commit: `f9bf91d9c4235196b7a50ec56e3d2039f233b1e9`.
- Handoff commit: this file, committed separately after product.
- Independent review: pending `/root/c01_reviewer`.
- Coordinator integration/state update: pending; this lane did not edit
  `docs/execution/state.json`.
- Blocker: none for owned settings reader, handler or synthetic verification.
