# U-06 correction round one handoff

## Identity and scope

- Task: U-06 correction round one, delegated shell composition and static cache policy.
- Owner: `/root/c01_implementer`.
- Independent reviewer: `/root/c01_reviewer`.
- Dispatch/base checkpoint: `154e0936f2f17df819959d0eeddd0932326d4d49`.
- Review receipt: `fb6a874861f1a38fc62ee3f4b7b0a5df5d34f41a`.
- Prior product commit: `d5b6484951dc0cda2611a3d3756c82634a0037d1`.
- Prior handoff: `2c297e7cc7965ce4deae67fb61636d0e2cf34c66`.
- Branch/worktree: shared `main`; coordinator owns `docs/execution/state.json`.
- Owned product paths: `ui/cmd/mastarr/`, `ui/internal/router/`, `ui/internal/shell/`.
- Handoff path: `docs/execution/handoffs/U-06-correction-round1.md`.
- Product commits: `46fb15c234678f010bf5ddeb351eca9b6ee3e532` and
  `a152214f06918ff8ad9528f898e3218df9fa25b9`.

## Findings closed

### P1-1 — delegated pages now use the configured shell

The production router captures each existing normalized inventory, review,
workflow, trash, and settings response, extracts the package-owned generated
`<main>` content, and renders that content inside the shared shell. The outer
document now consistently contains the configured public-origin canonical URL,
Open Graph metadata, Twitter metadata, local preview asset, console shell
assets, configuration guidance, and the normalized list/detail or sanitized
error content. The captured body is bounded at 1 MiB; an oversized delegate
response becomes a generic unavailable shell response rather than a partial
document.

Settings rewrites retain the original public path while the normalized handler
continues to receive its existing internal path. This preserves canonical
`/settings/connections`, `/settings/storage`, and `/settings/mappings` deep
links while keeping exact opaque IDs in delegated content. Shell canonical
allowlisting now covers downloads, descriptors, settings, and the native
settings aliases used by handler-generated links.

The composition does not reimplement normalized safety or upstream parsing;
delegated handlers still own their strict route/query validation, read bounds,
HTML escaping, status mapping, and sanitized errors. `ContentHTML` is private
to the shell/router boundary and is populated only from those package-owned
renderers after extracting their generated `<main>` element. When captured
content is present, the shell suppresses its generic heading so the normalized
document keeps one authoritative `<h1>` and its existing `aria-labelledby`
relationship; this avoids duplicate-heading and duplicate-identity markup.

### P2-1 — public static assets no longer inherit private cache headers

The router and the legacy startup handler classify static assets before adding
private response headers. Preview, Goshtoso, and console-shell assets therefore
retain their own public immutable or revalidation `Cache-Control` policy and
do not receive `Pragma: no-cache`. Mutation requests still receive the global
GET/HEAD rejection and private error policy before any asset delegate runs.

Focused tests cover preview, unversioned console CSS, versioned console CSS
discovered from the rendered shell, and Goshtoso CSS, including status, public
cache policy, and absence of `Pragma`.

## Verification

Commands ran against product commits
`46fb15c234678f010bf5ddeb351eca9b6ee3e532` and
`a152214f06918ff8ad9528f898e3218df9fa25b9`. All fixtures are synthetic; no
live service, credential, database, filesystem, media mount, or browser
automation was used.

| Check | Result |
| --- | --- |
| `gofmt -w cmd/mastarr/main.go internal/router/router.go internal/router/router_test.go internal/shell/shell.go` from `ui/` | PASS |
| `git diff --check` | PASS |
| `GOWORK=off go test ./...` from `ui/` | PASS |
| `GOWORK=off go test -race ./...` from `ui/` | PASS |
| `GOWORK=off go vet ./...` from `ui/` | PASS |
| `GOWORK=off go mod verify` from `ui/` | PASS; all modules verified |
| `GOWORK=off go mod tidy -diff` from `ui/` | PASS; no manifest diff |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` from `ui/` | PASS |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` from `ui/` | PASS |
| `GOWORK=off go test ./...` from repository root | PASS |
| `GOWORK=off go vet ./...` from repository root | PASS |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` from repository root | PASS |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` from repository root | PASS |
| `python3 scripts/check_planning.py` | PASS; planning links valid |
| `./scripts/check-guardrails.sh --ci` with correction product staged | PASS; generation, staged generation, API/Vacuum, architecture, lint, module tests/race/vet/verification, and Linux cross-build matrix passed; exit 0 |
| Product commit hook | PASS; staged generation/API/Vacuum, architecture, and fast guardrails passed |

## Boundaries and remaining limitation

Product changes stay within `ui/cmd/mastarr/`, `ui/internal/router/`, and
`ui/internal/shell/`. The router remains HTTP-only and read-only with no root,
database, storage, upstream, media-mount, credential, or mutation dependency.
The `/actions` route remains a shell overview because no normalized action
reader/handler exists in the assigned UI package set; no action behavior was
invented by this correction. Browser screenshots, live CUA behavior, and live
service verification remain outside U-06.

## Review and integration

- Product commits: `46fb15c234678f010bf5ddeb351eca9b6ee3e532` and
  `a152214f06918ff8ad9528f898e3218df9fa25b9`.
- Handoff commit: this file, committed separately after product.
- Independent review: pending `/root/c01_reviewer`.
- Coordinator state update: pending; this lane did not edit
  `docs/execution/state.json`.
- Blocker: none for the owned route composition and static policy. The
  action-specific handler limitation remains explicitly recorded above.
