# U-05 correction round one handoff

## Identity and scope

- Task: U-05 browser/accessibility verification, correction round one.
- Owner: `/root/c01_implementer`.
- Independent reviewer: `/root/c01_reviewer`.
- Dispatch/base checkpoint: `d8eecc06414d1e7b02294128b5cdb7a7878a3205`.
- Review receipt: `aa258dc4a815d4df8a8c74064351612b775d36cd`.
- Prior product commit: `a753fe0b3164cab2beb456d961d1c679da518ad7`.
- Prior handoff correction: `cd710266c841980fc9191bb8e7350ec1c7fa8d48`.
- Branch/worktree: shared `main`; coordinator owns `docs/execution/state.json`.
- Owned product paths: `ui/tests/browser/`, `docs/verification/ui-ledger.md`.
- Handoff path: `docs/execution/handoffs/U-05-correction-round1.md`.
- Product correction commit: `4a93164b98c09b687598538de7dfa6be8309d4e1`.

## Findings closed

### P1-1 — acceptance mapping and unavailable evidence

The ledger and test names now follow the acceptance definitions instead of
grouping adjacent shell checks under the wrong IDs:

- A-47 explicitly records cross-origin, forwarded-origin, redirect, CSRF/CORS,
  and unauthenticated direct-client scenarios as **NOT RUN / BLOCKED**.
- A-48 contains only the runnable transport/sanitized-recovery evidence and
  explicitly records stale approval, same-key idempotency, reload/back-forward,
  and actual consequential-action effect scenarios as **NOT RUN / BLOCKED**.
- A-49 explicitly records exact media/episode/subtitle identity and deep-link
  switching as **NOT RUN / BLOCKED**.
- A-50 contains structural accessibility and shell configuration evidence only;
  browser viewport, theme, keyboard, dialog, contrast, zoom, focus, and
  announced-error scenarios are **NOT RUN / BLOCKED**.
- A-51 owns initial metadata, unknown-route structure, and preview asset
  evidence, while composed-route status, unknown-ID behavior, configured-origin
  asset loading, and screenshots remain **NOT RUN / BLOCKED**.

A-26 remains scoped production-handler evidence: four allowed reads and zero
upstream mutation effects. A-42 remains scoped production-handler redaction and
encoded endpoint rejection: unsafe drafts call the reader zero times and safe
draft calls it once. Neither is represented as browser completion.

The actual blocker is recorded: CUA reported `browsers: []`, and
`ui/cmd/mastarr/main.go` still does not compose review, workflow, trash, or
settings handlers into one router. No screenshot, keyboard trace, viewport
trace, browser-storage inspection, origin-policy trace, deep-link identity
trace, approval/idempotency trace, or browser effect count is claimed.

### P2-1 — unsupported claims and direct asset assertions

Unsupported ETag claims were removed from the ledger. The production asset
fixture now directly exercises `HEAD` and asserts `Content-Type`, immutable
`Cache-Control`, and `X-Content-Type-Options: nosniff`; `GET` asserts the same
headers plus the generic 1200x630 body. The ledger keeps composed-router and
browser-origin claims explicitly unavailable.

## Verification

Commands ran against product correction commit
`4a93164b98c09b687598538de7dfa6be8309d4e1`. All fixtures are synthetic;
no live service, credential, database, filesystem, media mount, or browser
account was used.

| Check | Result |
| --- | --- |
| `gofmt -w ui/tests/browser/browser_test.go` | PASS |
| `git diff --check` and `git diff --check HEAD^ HEAD` | PASS |
| `GOWORK=off go test ./tests/browser -count=1 -timeout=120s` from `ui/` | PASS |
| `GOWORK=off go test -mod=readonly ./... -count=1 -timeout=360s` from `ui/` | PASS |
| `GOWORK=off go test -mod=readonly -race ./... -count=1 -timeout=600s` from `ui/` | PASS |
| `GOWORK=off go vet -mod=readonly ./...` from `ui/` | PASS |
| `GOWORK=off go mod verify` from `ui/` | PASS; all modules verified |
| `GOWORK=off go mod tidy -diff` from `ui/` | PASS; no manifest diff |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go test -mod=readonly -run '^$' -c -o /tmp/u05-ui-amd64.test ./tests/browser` | PASS |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off go test -mod=readonly -run '^$' -c -o /tmp/u05-ui-arm64.test ./tests/browser` | PASS |
| `GOWORK=off ./scripts/check-lint.sh` | PASS; lint and architecture boundaries |
| `python3 scripts/check-architecture.py` | PASS |
| `python3 scripts/check_planning.py` | PASS; 53 tasks, 60 acceptance cases, local links resolve |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-api.sh` | PASS; generation and Vacuum 100/100 |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0, `guardrail checks passed (ci)` |
| Product commit hook | PASS; staged generation/API/Vacuum, architecture and fast guardrails |

## Boundaries and follow-up

Correction changes stay within `ui/tests/browser/` and
`docs/verification/ui-ledger.md` plus this handoff. No UI product package,
API/generated file, router, root package, client module, state file, or
unrelated lane was edited. The prior U-05 handoff's broad completion wording
is superseded by this correction's explicit **NOT RUN / BLOCKED** status.

U-05 must be rerun after router composition and browser availability. Required
follow-up includes A-47 origin/CSRF/CORS/direct-client checks; A-48 stale
approval, same-key recovery, reload/back-forward and actual effects; A-49 exact
media/episode/subtitle deep-link identity; A-50 390px/1440px light/dark and
keyboard/dialog/contrast/zoom/focus/error behavior; and A-51 composed status,
unknown-ID, configured-origin preview and screenshot evidence.

## Review and integration

- Product correction commit: `4a93164b98c09b687598538de7dfa6be8309d4e1`.
- Handoff commit: this file, committed separately after product.
- Independent review: pending `/root/c01_reviewer`.
- Coordinator state update: pending; this lane did not edit
  `docs/execution/state.json`.
- Blocker: browser surface unavailable (`browsers: []`) and production router
  uncomposed; no browser-level acceptance completion is claimed.
