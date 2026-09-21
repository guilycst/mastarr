# U-06 independent review round one

## Decision

`changes_requested`.

One P1 and one P2 finding remain in the production route composition. The
HTTP-only startup wiring, route vocabulary, global method boundary, unknown-route
handling, sanitized errors and architecture boundary are otherwise sound.

## Review identity and scope

- Independent reviewer: `/root/c01_reviewer`; reviewer did not author the batch.
- Product commit: `d5b6484951dc0cda2611a3d3756c82634a0037d1`.
- Product tree: `cc6d4459146e1437a26c2dcea7d181cf8a4a9f97`.
- Product parent/base: `b0f6d550472f91048301590baf49fa77db38731b`.
- Product-path diff SHA-256:
  `7459005c2dbfb06127c9f8862f7ed4b431bf1f8c2183d4c071333dbd46a65fb9`.
- Handoff commit: `2c297e7cc7965ce4deae67fb61636d0e2cf34c66`.
- Handoff tree: `d82a04357be709995f079fcd0b3a624eb3acdf9a`.
- Handoff SHA-256:
  `0f50019bd2040dad97197a7ee4add5dd74370de62328af0829eb481c24b0051e`.
- Clean coordinator checkpoint: `646a857202e2c8d071044b2b513b62c8763fe876`.
- Checkpoint tree: `da734fdb91b997c40e4448647ecd655876a0e97b`.

Ancestry binds product to handoff and handoff to checkpoint. The product commit
changes only `ui/cmd/mastarr/main.go` and `ui/internal/router/`; the handoff is a
separate commit. Review used synthetic fixtures and a Go overlay under `/tmp`.
No product, state, API, generated or router file was edited by the reviewer.

## Findings

### P1-1 - delegated initial pages bypass the configured-origin shell and metadata

`ui/internal/router/router.go:93-106` sends inventory, review, workflow, trash
and settings requests straight to their package handlers through
`servePrivate`; only `/`, `/actions` and router-owned 404s reach `shell.Render`
at `ui/internal/router/router.go:108-118,151-184`. The configured public origin
is therefore absent from every composed reader route, including direct detail
deep links. Those handlers each emit an independent minimal HTML document (for
example `ui/internal/inventory/inventory.go:542-562`,
`ui/internal/review/model.go:730-734`, `ui/internal/workflows/model.go:849-853`,
`ui/internal/trash/model.go:444-456` and `ui/internal/settings/model.go:466-478`)
without the shared console shell, canonical URL, Open Graph/X metadata or shell
assets.

The production-router overlay requested `/media`, `/reviews`, `/workflows`,
`/trash` and `/settings/connections` with configured origin
`https://ui.example.test`. All five responses reported
`canonical=false`, `og=false`, `configuredOrigin=false` and
`shellAsset=false`. This occurred on sanitized unavailable responses, and source
inspection shows the same document writer is used for successful list/detail
responses. Request Host and forwarded Host are not reflected, but omitting all
configured-origin metadata does not satisfy the U-06 deliverable or the initial
HTML requirements in `dashboard.md:95-108`. It also breaks the stable outer
shell required at `dashboard.md:73-79` and leaves the composed A-50/A-51
contribution unproven.

Required correction: make every initial inventory/review/workflow/trash/settings
list and detail response render through the configured shell (with a clear
fragment path for HTMX if used), or give the delegated renderer equivalent
configured-origin canonical/social metadata and shared-shell structure. Add
production-router assertions for successful and unavailable list/detail deep
links, including forged Host/forwarded headers.

Regression: the existing router test checks configured-origin metadata only on
`/`; its delegated-route tests assert status/body/cache policy without checking
initial metadata or shared-shell structure.

### P2-1 - public static responses retain `Pragma: no-cache`

`ui/internal/router/router.go:70-91` calls `setPrivateHeaders` before dispatching
all requests. That function sets `Pragma: no-cache` at
`ui/internal/router/router.go:229-234`. Static delegates overwrite
`Cache-Control`, but they do not clear `Pragma`, so public immutable responses
carry contradictory legacy cache instructions.

The production-router overlay discovered assets from the actual root document
and observed `Pragma="no-cache"` on every response. `/preview.svg`, the
versioned console JS/CSS and versioned Goshtoso runtimes simultaneously returned
`Cache-Control="public, max-age=31536000, immutable"`; unversioned CSS/runtime
returned their public revalidation policy with the same Pragma. This contradicts
the U-06 static asset policy and the handoff claim that static assets retain a
clean immutable public policy.

Required correction: apply private no-cache headers after route classification,
or explicitly remove private-only cache headers before serving public assets.
Add assertions for the complete static response policy, including absence of
contradictory headers.

Regression: `TestProductionRouterShellMetadataAssetsAndActionOverview` checks
only preview status and content type; it does not inspect cache headers on the
preview, console-shell or Goshtoso assets.

## Re-audited behavior and boundaries

- Settings rewrites are exact: configuration, connection, storage-root,
  path-mapping and connection-check list/detail aliases map to the handlers'
  native paths while query values remain on the cloned URL.
- Inventory, review, workflow, trash and settings prefixes use segment-aware
  matching. Existing focused tests exercise unknown opaque detail IDs, and the
  delegated handlers retain their strict opaque-ID validation.
- GET/HEAD rejection occurs before static or API delegates. The response exposes
  `Allow: GET, HEAD`; no mutation reader/writer is composed.
- Unknown routes render the generic shell 404 without a readiness call.
- Production startup constructs all normalized HTTP readers from the validated
  API origin and passes them to the composed router. Readiness and route-reader
  failures remain generic; request Host, forwarded Host and transport detail are
  not reflected.
- Router imports are confined to the nested UI module and public Goshtoso asset
  packages. No root internals, database, storage, upstream, media-mount,
  credential or direct-write dependency entered the batch.
- `/actions` is exactly shell-only and does not accept detail paths. The handoff
  explicitly records the absent normalized action handler as a remaining
  limitation; this narrow limitation is honest.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| SHA/tree/ancestry, product scope, handoff SHA-256 and `git diff --check d5b6484^ d5b6484` | PASS. |
| `GOWORK=off go test -overlay=/tmp/u06_review_overlay.json ./internal/router -run 'TestU06Review(StaticHeaders\|SettingsRewrites\|DelegatedMetadata)$' -count=1 -v` from `ui/` | Probe ran successfully; exact rewrites passed and reproduced both findings above. Overlay and test source stayed under `/tmp`. |
| `GOWORK=off go test ./internal/router ./cmd/mastarr -count=1` from `ui/` | PASS. |
| `GOWORK=off go test ./... -count=1` from `ui/` | PASS. |
| `GOWORK=off go test -race ./... -count=1` from `ui/` | PASS. |
| `GOWORK=off go vet ./...` from `ui/` | PASS. |
| `GOWORK=off go mod verify` and `GOWORK=off go mod tidy -diff` from `ui/` | PASS; modules verified and no manifest diff. |
| `GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly -o /tmp/mastarr-u06-linux-amd64 ./cmd/mastarr` from `ui/` | PASS. |
| `GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly -o /tmp/mastarr-u06-linux-arm64 ./cmd/mastarr` from `ui/` | PASS. |
| `python3 scripts/check_planning.py` | PASS: `Planning valid: 54 tasks, 60 acceptance cases; local links resolve.` |
| `scripts/check-guardrails.sh --ci` | PASS; exit 0 with generation/staged generation, API/Vacuum 100/100, architecture/import boundaries, lint, module tests/race/vet/verify and Linux matrix passing. |

An initial reviewer cross-build omitted `-o` and created `ui/mastarr`; that sole
generated artifact was removed immediately, the clean `/tmp` cross-builds above
were rerun, and the checkout was clean before this receipt was written. An
initial planning invocation used the nonexistent legacy name
`scripts/validate_planning.py`; the repository's actual
`scripts/check_planning.py` then passed as recorded.

## Acceptance contribution assessment

- A-47 and A-48 retain the scoped method-boundary, HTTP-only startup and
  sanitized readiness/transport contributions.
- A-49 retains exact production routing into the normalized list/detail
  handlers, but browser/deep-link state recovery remains outside this local run.
- A-50 and A-51 are not clear: delegated initial documents bypass the shared
  shell and configured-origin metadata, and static cache headers conflict.
- Browser screenshots, viewport/theme runs and live accessibility traces remain
  separate U-05/browser evidence and are not claimed here.

## Integration recommendation

Do not integrate U-06 as complete. Correct P1-1 and P2-1, add production-router
regressions for delegated initial/deep-link metadata and full static headers,
then rerun focused and guardrail checks through a new independent review round.
This decision does not claim release, deployment, live-stack or browser
verification.
