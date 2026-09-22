# U-06 independent correction review round three

## Decision

`approved`.

No P1 or P2 finding remains in the correction. Delegated documents now retain
one labelled main landmark and one authoritative heading across success and
sanitized error states. Preview, Goshtoso and console assets retain their public
cache policy without `Pragma`, carry `nosniff` on GET and HEAD, and reject
mutations through the private global boundary.

## Review identity and scope

- Independent reviewer: `/root/c01_reviewer`; reviewer did not author the correction.
- Prior receipt: `be2990cead8f60be8b7f74baef268d6a532f36d9`.
- Correction base: `ca14b9e11c918905aca5b5a6ad1a252569e95149`.
- Product commit: `964eae21abf58cb0c8c73f2e4ba4235e9f0e1c0f`.
- Product tree: `f36df0747208b63aef2700bdadebc50abe43a4f2`.
- Product-path diff SHA-256:
  `6c434a5dde6ef779dc53afd87fce93f358d9aa27ad2d06346e4b90575e4956dd`.
- Handoff: `4c61478dba6b1eead614ddbecbb371c49f750778`.
- Handoff tree: `78864bef73791fcdaf5f6c91abdd15684bc357a7`.
- Handoff SHA-256:
  `7b69f8f36146907f1a617048bdd0f0bd5da85012e377f3a4464042b596a84f1c`.
- Clean checkpoint: `cb09214baffa067cd9d31b92d461e08037cea198`.
- Checkpoint tree: `449517b46326c1549084237b57f9efb1f9991584`.

Ancestry binds product to handoff and handoff to checkpoint. Product changes are
confined to the assigned UI command, router and shell paths; handoff and
coordinator state are separate commits. Review used synthetic production-router
fakes and Go overlays under `/tmp`. The reviewer edited no product, state, API
or generated file.

## Finding closure

### P2-1 - closed: one labelled main landmark and one heading

The router now extracts the contents and `aria-labelledby` identity from the
normalized delegate's main element instead of copying that landmark. The shell
keeps its single `main-content` landmark, transfers the bounded validated label,
and retains the delegate's escaped heading and content IDs.

Independent success probes returned 200 for representative inventory, review,
workflow, trash and settings list/detail routes. Settings coverage included the
rewritten public connections, storage, mappings and checks paths. Every response
contained exactly one `<main>`, exactly one `<h1>`, the expected transferred
`aria-labelledby` value and matching heading ID, configured-origin metadata and
shared console assets.

Reader spies received the exact opaque IDs `media_opaque-1`,
`review_opaque-1`, `workflow_opaque-1`, `trash_opaque-1`,
`connection_opaque-1`, `root_opaque-1`, `mapping_opaque-1` and
`check_opaque-1`. Unavailable list and not-found detail probes across the same
families also produced one labelled main and one heading while retaining their
sanitized status/content. Forged Host and forwarded Host values were absent.

Malformed or oversized captured content continues to fail closed to the generic
bounded shell. The added label parser accepts only bounded HTML ID-reference
tokens before adding the attribute to the trusted shell document.

### P2-2 - closed: complete public static header policy

Independent GET and HEAD probes covered `/preview.svg`, unversioned and
versioned Goshtoso CSS/JS, and unversioned and versioned console assets. Each
returned 200, its handler-owned public immutable or revalidation
`Cache-Control`, no `Pragma`, and `X-Content-Type-Options: nosniff`.

POST to every probed asset returned 405 with `Allow: GET, HEAD`,
`Cache-Control: no-store, private`, `Pragma: no-cache` and `nosniff` before a
static delegate could run. The same cache-neutral static security header is
applied by the composed router and the legacy startup handler.

## Re-audited boundaries

- Configured-origin canonical, Open Graph/X and preview metadata remain isolated
  from request Host, forwarded headers, query filters and opaque resource IDs.
- Normalized handlers retain strict query/identity validation, escaped private
  content, bounded reads and sanitized transport errors. Capture remains capped
  at 1 MiB and never forwards delegate headers into the shell response.
- GET/HEAD rejection precedes every API/static delegate. Unknown routes avoid
  readiness reads. Production startup remains wired to normalized HTTP readers.
- The nested UI module contains no root, database, storage, upstream,
  media-mount, credential or direct mutation dependency.
- `/actions` remains an explicitly recorded shell-only limitation pending a
  separately owned normalized action handler.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| SHA/tree/ancestry/scope identities and `git diff --check 964eae2^ 964eae2` | PASS. |
| Success production-router overlay across five readers and rewritten settings list/detail routes | PASS; 16 routes returned 200 with one main, one heading, matching transferred label, configured metadata/assets and exact opaque reader IDs. |
| Unavailable/not-found production-router overlay | PASS; one main/heading, canonical metadata, shared assets, sanitized status/content and no forged-origin reflection. |
| Preview/Goshtoso/console GET+HEAD+POST overlay | PASS; public cache/no-Pragma/nosniff on reads and private 405 policy on mutations. |
| `GOWORK=off go test ./internal/router ./internal/shell ./cmd/mastarr -count=1` from `ui/` | PASS. |
| `GOWORK=off go test ./... -count=1` from `ui/` | PASS. |
| `GOWORK=off go test -race ./... -count=1` from `ui/` | PASS. |
| `GOWORK=off go vet ./...` from `ui/` | PASS. |
| `GOWORK=off go mod verify` and `GOWORK=off go mod tidy -diff` from `ui/` | PASS; modules verified and no manifest diff. |
| CGO-free Linux amd64/arm64 `go build -mod=readonly -o /tmp/... ./cmd/mastarr` from `ui/` | PASS. |
| `python3 scripts/check_planning.py` | PASS: `Planning valid: 54 tasks, 60 acceptance cases; local links resolve.` |
| `scripts/check-guardrails.sh --ci` | PASS; generation/staged generation, API/Vacuum 100/100, architecture/import boundaries, lint, root/UI/tools/client tests and race, vet, module verification and Linux matrix passed. |

## Acceptance contribution assessment

- A-47 through A-49 retain their scoped HTTP-only method, routing, exact
  identity and sanitized transport contributions.
- A-50 clears the local semantic composition gate: composed success and error
  documents have one labelled main landmark and one heading.
- A-51 clears the local configured-origin metadata and static delivery gate,
  including cache, Pragma, nosniff and mutation behavior.
- Browser screenshots, viewport/theme runs, live accessibility traces and live
  service verification remain separate U-05/deployment evidence and are not
  claimed here.

## Integration recommendation

Approve product `964eae21abf58cb0c8c73f2e4ba4235e9f0e1c0f` and handoff
`4c61478dba6b1eead614ddbecbb371c49f750778` for U-06 integration. This approval
does not claim release, deployment, live-stack or browser verification.
