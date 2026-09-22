# U-06 correction round two handoff

## Identity and scope

- Task: U-06 correction round two, semantic landmark composition and public static security headers.
- Owner: `/root/c01_implementer`.
- Independent reviewer: `/root/c01_reviewer`.
- Dispatch/base checkpoint: `ca14b9e11c918905aca5b5a6ad1a252569e95149`.
- Review receipt: `be2990cead8f60be8b7f74baef268d6a532f36d9`.
- Prior product commits: `46fb15c234678f010bf5ddeb351eca9b6ee3e532` and
  `a152214f06918ff8ad9528f898e3218df9fa25b9`.
- Prior handoff: `f530ad936c90328170416057fd0fc3413bd8ddb7`.
- Branch/worktree: shared `main`; coordinator owns `docs/execution/state.json`.
- Owned product paths: `ui/cmd/mastarr/`, `ui/internal/router/`, `ui/internal/shell/`.
- Handoff path: `docs/execution/handoffs/U-06-correction-round2.md`.
- Product commit: `964eae21abf58cb0c8c73f2e4ba4235e9f0e1c0f`.

## Findings closed

### P2-1 — delegated pages now have one main landmark and one heading

The production router now extracts the inner fragment of each package-owned
delegate `<main>` instead of copying the delegate landmark into the Goshtoso
shell. The shell keeps its single `main-content` landmark, transfers the
delegate `aria-labelledby` value to that landmark, and retains the delegate's
escaped heading and content IDs. Malformed or oversized captured content still
fails closed to the existing generic unavailable shell.

Synthetic router regressions cover a successful captured delegate, unavailable
inventory/review/workflow/trash/settings list pages, and not-found opaque detail
pages. Each asserts exactly one `<main>` and one `<h1>`, the expected
`aria-labelledby`/heading identity, escaped fragment content, configured-origin
metadata, and exact settings public canonical paths. Existing opaque IDs remain
in delegated reader requests and their escaped links/content.

### P2-2 — all public static responses retain nosniff and public caching

The composed router and legacy startup handler apply a cache-neutral
`X-Content-Type-Options: nosniff` header before preview, Goshtoso, and
console-shell static delegates. Their public immutable or revalidation
`Cache-Control` values remain owned by the asset packages, and public responses
do not receive private `Pragma: no-cache`. GET and HEAD regressions cover all
three asset families, including versioned console assets. Mutation requests are
still rejected before every static delegate with private no-store headers,
`Allow: GET, HEAD`, and no delegate/API call.

## Verification

All fixtures are synthetic. No live service, credential, database, filesystem,
media mount, browser session, or private coordinate was used.

| Check | Result |
| --- | --- |
| `gofmt -w internal/router/router.go internal/router/router_test.go internal/shell/shell.go cmd/mastarr/main.go cmd/mastarr/main_test.go` from `ui/` | PASS |
| `git diff --check` and staged product `git diff --cached --check` | PASS |
| `GOWORK=off go test ./internal/router ./internal/shell ./cmd/mastarr -count=1` from `ui/` | PASS |
| `GOWORK=off go test ./... -count=1` from `ui/` | PASS |
| `GOWORK=off go test -race ./... -count=1` from `ui/` | PASS |
| `GOWORK=off go vet ./...` from `ui/` | PASS |
| `GOWORK=off go mod verify` from `ui/` | PASS; all modules verified |
| `GOWORK=off go mod tidy -diff` from `ui/` | PASS; no manifest diff |
| `GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly -o /tmp/mastarr-u06-r2-linux-amd64 ./cmd/mastarr` from `ui/` | PASS |
| `GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly -o /tmp/mastarr-u06-r2-linux-arm64 ./cmd/mastarr` from `ui/` | PASS |
| `python3 scripts/check_planning.py` | PASS; 54 tasks, 60 acceptance cases, local links resolve |
| `./scripts/check-guardrails.sh --ci` with product staged | PASS; generation/staged generation, API/Vacuum, architecture/import boundaries, lint, root/UI/tools/client tests and race, vet, module verification, and Linux matrix passed; exit 0 |
| Product commit hook | PASS; staged generation/API/Vacuum, architecture, and fast guardrails passed |

## Boundaries and remaining limitation

Product changes stay within the assigned UI command, router, and shell paths.
The BFF remains HTTP-only and read-only with no root, database, storage,
upstream, media-mount, credential, or mutation dependency. `/actions` remains
a shell overview because no normalized action reader/handler exists in the
assigned UI package set; this correction invents no action behavior. Browser
screenshots, viewport/theme runs, live CUA behavior, and live service
verification remain outside U-06.

## Review and integration

- Product commit: `964eae21abf58cb0c8c73f2e4ba4235e9f0e1c0f`.
- Handoff commit: this file, committed separately after product.
- Independent review: pending `/root/c01_reviewer`.
- Coordinator state update: pending; this lane did not edit
  `docs/execution/state.json`.
- Blocker: none for the owned semantic composition and static-header
  correction. The action-specific handler and browser/live verification limits
  remain explicitly recorded above.
