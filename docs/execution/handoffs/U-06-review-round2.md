# U-06 independent correction review round two

## Decision

`changes_requested`.

The prior configured-origin metadata, shared assets, single-heading and static
cache/Pragma defects are corrected. Two P2 regressions remain: composed pages
nest a second `<main>` landmark inside the shell's `<main>`, and Goshtoso static
assets lost `X-Content-Type-Options: nosniff` when private cache headers were
moved behind static dispatch. No P1 finding remains.

## Review identity and scope

- Independent reviewer: `/root/c01_reviewer`; reviewer did not author the correction.
- Prior receipt: `fb6a874861f1a38fc62ee3f4b7b0a5df5d34f41a`.
- Correction base: `154e0936f2f17df819959d0eeddd0932326d4d49`.
- Product commits: `46fb15c234678f010bf5ddeb351eca9b6ee3e532` and
  `a152214f06918ff8ad9528f898e3218df9fa25b9`.
- Final product tree: `ce2d22edba9d5c6a6c3d1d245ba210b06ae52bf1`.
- Correction product-path diff SHA-256:
  `cfbe070586040469056dbc3831ad58ba49a9496e40859d4b7fde8e4ffff976e1`.
- Handoff: `f530ad936c90328170416057fd0fc3413bd8ddb7`.
- Handoff tree: `f82f23d65ecc8f4a71ac7447de4f7c61b1765ab4`.
- Handoff SHA-256:
  `24429fdab45df2ade4b752a82815fdde72d42b9ba707f20a216bcb79f4bfa329`.
- Clean checkpoint: `8f383b9209b76aa01e156cc3e02453a497149246`.
- Checkpoint tree: `fcb11910cb27f989ec0157a345ee98e1cfa8bb78`.

Ancestry binds both product commits to the handoff and checkpoint. Correction
changes are confined to `ui/cmd/mastarr`, `ui/internal/router` and
`ui/internal/shell`; handoff and coordinator state are separate commits. Review
used synthetic production-router fakes and Go overlays under `/tmp`. The reviewer
edited no product, state, API or generated file.

## Prior finding closure

### P1-1 - substantially closed: metadata, assets, opaque IDs and one heading

The router now captures bounded normalized-handler output and passes it through
`shell.Render`. Successful and unavailable inventory, review, workflow, trash
and settings list/detail responses contain configured-origin canonical, Open
Graph/X metadata and console-shell assets. Forged Host and forwarded Host remain
absent. Rewritten settings routes preserve their public canonical path while
their internal handler path remains unchanged.

An independent success probe returned 200 for representative list/detail routes
across all five readers, including connection, storage, mapping and check
rewrites. Reader spies received the exact opaque IDs `media_opaque-1`,
`review_opaque-1`, `workflow_opaque-1`, `trash_opaque-1`,
`connection_opaque-1`, `root_opaque-1`, `mapping_opaque-1` and
`check_opaque-1`. Each response contained exactly one `<h1>`. Existing
unavailable/not-found probes retained sanitized status and content without
request-origin or transport-detail reflection.

### P2-1 - closed for cache isolation and Pragma

Preview, Goshtoso and console asset GETs now retain their public immutable or
revalidation `Cache-Control` policy and return no `Pragma`. POST reaches the
global method guard before every static delegate and returns 405 with
`Allow: GET, HEAD`, private no-store/no-cache and zero asset delegation.

## Findings

### P2-1 - composed pages contain nested main landmarks

`ui/internal/router/router.go:156-179` captures and forwards the complete
delegate `<main>...</main>` element. `ui/internal/shell/shell.go:247-257` writes
that element into the Goshtoso console shell content slot, which is already
inside the shell's authoritative `<main id="main-content">`. The correction
suppresses the shell heading, so the requested one-heading condition passes,
but the resulting document has two nested main landmarks.

The independent production-router probe counted `main=2, h1=1` for every
successful and unavailable/not-found route tested: media, reviews, workflows,
trash, settings connections, storage, mappings and checks, including list and
opaque-ID detail paths. Nested `<main>` elements violate semantic document
structure and weaken the A-50 accessibility contribution.

Required correction: compose the normalized handler's inner fragment into the
shell landmark, preserving its one heading and accessible label without copying
the delegate `<main>` wrapper. Add assertions for exactly one `<main>` and one
`<h1>` on successful, unavailable and not-found list/detail responses.

Regression: `TestProductionRouterComposesCompleteShellForDeepLinks` asserts only
that a `<main>` exists and that one `<h1>` exists; it does not reject duplicate
or nested main landmarks.

### P2-2 - Goshtoso assets lost the required nosniff header

`ui/internal/router/router.go:83-94` and the parallel legacy startup handler now
dispatch all static routes before applying `setPrivateHeaders`. This correctly
isolates `Pragma` and private cache policy, but the Goshtoso `/assets/` handler
does not set `X-Content-Type-Options` itself. Independent GET probes observed:

- `/preview.svg`: `nosniff` present;
- `/consoleshell/assets/shell.css` and versioned shell JS: `nosniff` present;
- `/assets/styles.css` and versioned HTMX JS: `nosniff` absent.

The global U-06 boundary previously supplied `nosniff` to these CSS/JS
responses. Dropping it is a security-header regression and leaves the static
policy inconsistent across the three asset families.

Required correction: apply cache-neutral security headers such as
`X-Content-Type-Options: nosniff` to public static responses while keeping
private `Cache-Control` and `Pragma` isolated. Add preview, Goshtoso and console
GET/HEAD assertions for both cache and security headers, plus mutation rejection.

Regression: the new asset checks verify status, cache policy and absent Pragma,
but do not assert `nosniff` on Goshtoso assets.

## Re-audited boundaries

- Configured-origin canonical paths cover inventory, review, workflow, trash,
  public settings rewrites and native settings aliases without reflecting Host,
  forwarded headers, filters or opaque IDs.
- Bounded capture fails closed to a generic 503 shell after 1 MiB. Normalized
  handlers retain strict query/ID validation, escaping and sanitized error text.
- GET/HEAD rejection precedes all API/static delegates. Unknown routes still
  avoid readiness reads. Production startup remains wired to normalized HTTP
  readers only.
- No root, database, storage, upstream, media-mount, credential or mutation
  dependency entered the nested UI module. `/actions` remains explicitly
  shell-only pending a separately owned normalized action handler.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| SHA/tree/ancestry/scope identities and `git diff --check 46fb15c^ a152214` | PASS. |
| Synthetic success production-router overlay for five readers and rewritten settings list/detail routes | PASS for 200, configured-origin metadata/assets, one heading and exact IDs; reproduced two main landmarks on every route. |
| Synthetic unavailable/not-found/static production-router overlay | PASS for sanitization, canonical metadata, one heading, cache/Pragma isolation and mutation rejection; reproduced two main landmarks and missing Goshtoso `nosniff`. |
| `GOWORK=off go test ./internal/router ./internal/shell ./cmd/mastarr -count=1` from `ui/` | PASS. |
| `GOWORK=off go test ./... -count=1` from `ui/` | PASS. |
| `GOWORK=off go test -race ./... -count=1` from `ui/` | PASS. |
| `GOWORK=off go vet ./...` from `ui/` | PASS. |
| `GOWORK=off go mod verify` and `GOWORK=off go mod tidy -diff` from `ui/` | PASS; modules verified and no manifest diff. |
| CGO-free Linux amd64/arm64 `go build -mod=readonly -o /tmp/... ./cmd/mastarr` from `ui/` | PASS. |
| `python3 scripts/check_planning.py` | PASS: `Planning valid: 54 tasks, 60 acceptance cases; local links resolve.` |
| `scripts/check-guardrails.sh --ci` | PASS; generation/staged generation, API/Vacuum 100/100, architecture/import boundaries, lint, module tests/race/vet/verify and Linux matrix passed. |

## Acceptance contribution assessment

- A-47 through A-49 retain the correction's scoped HTTP-only method, routing,
  identity and sanitized transport contributions.
- A-51 now has configured-origin metadata/assets and correct public cache/Pragma
  separation, but its static security-header policy needs P2-2 correction.
- A-50 is not clear because every delegated page has nested main landmarks even
  though the one-heading condition now passes.
- Browser screenshots, viewport/theme runs and live accessibility traces remain
  separate U-05 evidence and are not claimed here.

## Integration recommendation

Do not mark U-06 complete. Correct P2-1 and P2-2, add exact semantic-landmark and
static security-header regressions, and submit a bounded independent re-review.
This decision does not claim release, deployment, live-stack or browser
verification.
