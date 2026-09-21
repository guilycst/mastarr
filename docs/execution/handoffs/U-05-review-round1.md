# U-05 independent review round one

## Decision

`changes_requested`.

The new fixtures pass, the product and handoff identities are valid, and the
ledger plainly discloses that browser and composed-router evidence is absent.
That disclosure is honest, but it also establishes that U-05's required browser
verification is not complete. The added rows additionally map several checks to
the wrong acceptance cases and overstate properties that the cited fixture does
not exercise.

## Review identity and scope

- Independent reviewer: `/root/c01_reviewer`; reviewer did not author the batch.
- Product commit: `a753fe0b3164cab2beb456d961d1c679da518ad7`.
- Product tree: `47d5b65901fd2b1f4018fb27c8fa05e73fb4248b`.
- Product parent/dispatch base:
  `1cc1c9465bc14cad860082bdc07ca8e4c3a704bb`.
- Corrected handoff commit: `cd710266c841980fc9191bb8e7350ec1c7fa8d48`.
- Corrected handoff tree: `45ffa79736c1e118d1a9d62f8d0c22cf10acc6ef`.
- Clean review checkpoint: `87176658f80a94d05bb1061908afecb0d8e316d4`.
- Checkpoint tree: `b2d75d171ee31be90d6120b2c9afafcdf2f23c77`.
- Corrected handoff SHA-256:
  `ea49463e83e37e3eefc35f4cf60db23f129746a549cb4d5b1fe6d8db0934560a`.
- Product-path diff SHA-256:
  `e2e64272b7c8283ff066133b8a3b8910cc7a53e6b2ecbe0ae61b610aaa5458ef`.

Ancestry binds product to handoff and handoff to checkpoint. The product commit
adds only `ui/tests/browser/browser_test.go` and
`docs/verification/ui-ledger.md`, matching assigned ownership. The handoff link
correction resolves both referenced files. Coordinator state changed outside the
product commit. Review used only synthetic fixtures and read-only inspection; no
product, state, API, generated or router file was edited.

## Findings

### P1-1 - required U-05 browser and consequential-action evidence is absent and acceptance IDs are misassigned

`docs/verification/ui-ledger.md:30-34` and
`ui/tests/browser/browser_test.go:193-320` assign adjacent shell checks to A-47
through A-50, but do not exercise the scenarios those acceptance cases define:

- A-47 requires cross-origin form/JSON mutation, forged Host/forwarded origin,
  redirect, conservative CSRF/CORS/origin policy and unauthenticated direct-client
  behavior. Its ledger row instead checks metadata and the preview asset, which
  belong to A-51. No cross-origin or CORS request is made.
- A-48 requires transport failure plus stale approval, repeated submission,
  reload and back/forward with the same idempotency key and an actual effect
  count. Its row checks semantic shell markup and repeated read-only trash drafts.
  Counting zero writes from a UI reader is not an executed consequential-action
  effect count, and no approval/idempotency/reload scenario is exercised.
- A-49 requires switching similar media deep links while preserving exact media,
  episode, subtitle, selection and executed-payload identity. Its row instead
  checks theme and navigation configuration, which is adjacent to A-50. No media
  identity or deep-link switch is present in the fixture.
- A-50 requires 390px/1440px, light/dark, keyboard, dialog escape, contrast, zoom,
  focus restoration and announced-error evidence. Its row instead checks API
  timeout/cancellation and sanitized error rendering, which contributes to A-48.

The ledger's evidence boundary at lines 13-19 and follow-up at lines 51-54
correctly classify screenshots, keyboard traversal, viewport/theme behavior and
actual router/effect traces as pending. Independent CUA inventory also returned
`browsers: []`, and `ui/cmd/mastarr/main.go` still does not compose review,
workflow, trash or settings handlers. Under the acceptance matrix, an unavailable
check is not a pass; under the U-05 task, screenshots, keyboard/focus, narrow/wide
themes, consequential-action allowed/denied/repeated cases and actual effects are
the deliverable. The handoff's `no blocker` statement at lines 86-87 therefore
cannot support approval of U-05.

Required correction: keep unavailable cases explicitly `blocked` or `not run`
under their correct acceptance IDs, then run U-05 after the production router and
browser surface exist. Exercise the generated-client/production-handler paths for
cross-origin policy, action recovery/idempotency/effect counts and exact media
deep-link identity, and collect the specified browser accessibility/theme evidence.
Do not substitute shell configuration or markup substring checks for those cases.

### P2-1 - the ledger attributes untested details to the new browser fixture

The ledger identifies `ui/tests/browser/browser_test.go` as its verification
artifact, but some exact-evidence statements exceed that fixture:

- `docs/verification/ui-ledger.md:31` says settings/trash drafts preserve
  operator/ETag context. The trash requests carry an operator but no ETag, while
  the settings request is plain `GET /settings`; no draft ETag is submitted or
  asserted.
- `docs/verification/ui-ledger.md:34` says the asset boundary is GET/HEAD-only
  with immutable cache and `nosniff`. The fixture sends GET, POST and PUT, but no
  HEAD, and asserts neither cache nor `X-Content-Type-Options` headers.
- The A-51 fixture calls `shell.Render` and `assets.Handler` directly. It does not
  verify initial HTTP status/metadata on every composed route, an unknown-ID HTTP
  404, or loading the absolute preview URL through the configured-origin router.
  The ledger appropriately marks part of this pending, so the exercised-result
  text should remain equally narrow.

Required correction: either add the missing requests/assertions through the
production HTTP boundary or remove the unsupported evidence claims. Each ledger
row should distinguish assertions made by this batch from earlier package tests
and from source inspection.

## Verified contributions and boundaries

- A-26 scoped UI contribution passes: restore and purge drafts retain distinct
  action/confirmation values; delete, mismatch and duplicate action drafts return
  400 before reader access; forged POST/DELETE return 405; exactly four allowed
  reads and zero mutation requests are observed.
- A-42 scoped UI contribution passes for the synthetic cases: YAML provenance is
  read-only, unsafe endpoint forms return 400 before `GetConnection`, the safe
  endpoint makes one reader call, and credential/error markers are absent from
  HTML. This is production `HTTPReader` to handler evidence, not browser-storage
  evidence.
- Shell semantic markup is useful partial A-50 structural evidence. Theme config
  is configuration-only partial A-50 evidence. Timeout/cancellation and sanitized
  outage rendering are partial A-48 transport evidence. Metadata and preview
  checks are partial A-51 structural evidence.
- Navigation hrefs correspond to the shell's known route set, and both Markdown
  links in the corrected handoff plus the ledger link resolve locally. The final
  composed route behavior remains pending as the ledger states.
- Product scope is confined to assigned files. No credential, private tracker URL,
  private hostname, real inventory or user runtime path was introduced; fixtures
  are synthetic.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact SHAs, trees, ancestry, product diff and SHA-256 identities | PASS. |
| Markdown link resolution for ledger and corrected handoff | PASS; all three local targets exist. |
| `git diff --check a753fe0^ a753fe0` | PASS. |
| CUA surface inventory | `browsers: []`; live screenshot/keyboard/viewport work unavailable. |
| Acceptance-to-fixture audit against `docs/verification/acceptance.md` and dashboard browser requirements | FAIL for U-05 completion; P1-1 reproduced from the cited rows and test cases. |
| Exact ledger-claim audit against `ui/tests/browser/browser_test.go` | FAIL for ETag and asset HEAD/header claims; P2-1 reproduced. |
| `GOWORK=off go test ./tests/browser -count=1 -timeout=120s` from `ui/` | PASS. |
| `GOWORK=off go test -mod=readonly ./... -count=1 -timeout=360s` from `ui/` | PASS. |
| `GOWORK=off go test -mod=readonly -race ./... -count=1 -timeout=600s` from `ui/` | PASS. |
| `GOWORK=off go vet -mod=readonly ./...` from `ui/` | PASS. |
| `GOWORK=off go mod verify` and `GOWORK=off go mod tidy -diff` from `ui/` | PASS; all modules verified and no manifest diff. |
| CGO-free Linux amd64/arm64 `go test -mod=readonly -run '^$' -c ./tests/browser` from `ui/`, outputs under `/tmp` | PASS. |
| `./scripts/generate.sh --check` | PASS; generation and staged generation checks passed. |
| `./scripts/check-api.sh` | PASS on sequential rerun; generation and Vacuum 100/100 passed. The first parallel reviewer invocation encountered a transient `.git/index.lock` created by the concurrent generation check, not a product failure. |
| `python3 scripts/check-architecture.py` | PASS. |
| `./scripts/check-lint.sh` | PASS; lint, architecture and standalone-client boundaries passed. |
| `python3 scripts/check_planning.py` | PASS: `Planning valid: 53 tasks, 60 acceptance cases; local links resolve.` |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 with `guardrail checks passed (ci)`, including generation, staged contracts/Vacuum, lint/architecture, all modules, race/vet/mod verification and Linux cross-builds. |

## Acceptance contribution assessment

- A-26: accepted as a scoped production-handler/read-only UI contribution; full
  direct API hard-delete acceptance remains outside this UI fixture.
- A-42: accepted as a scoped production-handler redaction and endpoint-draft
  contribution; browser URL/storage inspection remains not run.
- A-47: not accepted; required origin/CSRF/CORS/redirect scenarios are absent.
- A-48: not accepted; only transport and read-only draft fragments exist, without
  stale approval, same-key recovery or actual action-effect evidence.
- A-49: not accepted; no exact identity/deep-link switch scenario exists.
- A-50: not accepted; structural markup is partial evidence, while required live
  browser widths/themes/keyboard/dialog/contrast/zoom/focus checks are unavailable.
- A-51: partially accepted for direct shell/asset structure; composed-route initial
  HTTP, useful unknown-ID 404 and absolute preview loading remain not run, and some
  asset claims need direct assertions.

## Integration recommendation

Do not approve U-05 as complete. Preserve the current synthetic production-boundary
tests as partial evidence, correct the ledger mapping and unsupported statements,
then compose the production router and repeat this task with a browser surface.
Coordinator retains state, router, API/generated and product ownership. This
receipt makes no release, deployment or live-stack claim.
