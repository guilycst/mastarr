# U-05 independent correction review round two

## Decision

`approved`.

No P1 or P2 finding remains in the correction. The ledger now records only the
synthetic production-boundary evidence that actually ran, maps A-47 through A-51
to their defined scenarios, and marks every unavailable browser or composed-router
scenario **NOT RUN / BLOCKED**. Approval covers this corrected evidence artifact;
browser-level U-05 acceptance remains blocked and is not claimed complete.

## Review identity and scope

- Independent reviewer: `/root/c01_reviewer`; reviewer did not author correction.
- Prior review receipt: `aa258dc4a815d4df8a8c74064351612b775d36cd`.
- Correction product: `4a93164b98c09b687598538de7dfa6be8309d4e1`.
- Product tree: `ab9bc123166263891f0fcba30f728388f3400467`.
- Product parent: `d8eecc06414d1e7b02294128b5cdb7a7878a3205`.
- Correction handoff: `37aefb6156829a1db64239901fe4105596907f37`.
- Handoff tree: `8e4c56dc75ecf89b56232a9202470b711b9a44e1`.
- Clean review checkpoint: `65543221f1e6560b23620ca9b020e4e069c9c123`.
- Checkpoint tree: `e76853a245a49f7ae7b8e9a45d2e2f05c0f2c274`.
- Correction handoff SHA-256:
  `83f5ebeb65680a693abc3e5bda2adf709beaa5b9fe9e4a97dcf59045e7190229`.
- Correction product-path diff SHA-256:
  `9f350700f0f4514c09c479eab208e7fdf8fae7ea5734f8a0d9d5f4499c4340ed`.

Ancestry binds the prior review to correction product, product to handoff and
handoff to checkpoint. Product changes are confined to
`docs/verification/ui-ledger.md` and `ui/tests/browser/browser_test.go`; the
handoff is separate. Coordinator state changed outside the product commit. Review
used synthetic fixtures and read-only inspection. No state, product package, API,
generated or router file was edited by the reviewer.

## Prior finding closure

### P1-1 - closed: acceptance mapping and unavailable evidence

- A-47 now records cross-origin form/JSON, forged Host/forwarded origin,
  redirect, CSRF/CORS and direct-client-origin scenarios as **NOT RUN / BLOCKED**.
- A-48 contains only the exercised transport, cancellation and sanitized-recovery
  evidence. Stale approval, same-key recovery, reload/back-forward and actual
  action-effect counts are explicitly **NOT RUN / BLOCKED**.
- A-49 records exact media/episode/subtitle, selection, executed payload and
  deep-link switching as **NOT RUN / BLOCKED** with no invented identity count.
- A-50 contains only structural accessibility and shell theme-configuration
  evidence. Viewport, visual theme, keyboard, dialog, contrast, zoom, focus and
  announced-error browser runs are explicitly unavailable.
- A-51 owns the shell metadata, structural unknown route and preview asset
  evidence. Composed initial HTTP status, unknown-ID routing, configured-origin
  asset loading and screenshot evidence remain **NOT RUN / BLOCKED**.

The ledger also keeps A-26 and A-42 narrow: exact read/mutation and reader-call
counts are production `HTTPReader` to handler evidence, while browser action,
URL and storage checks remain blocked or not run. Test names now reflect only
their runnable acceptance portions.

Independent CUA inventory returned `browsers: []`; native app inventory was also
unavailable because the Mac was locked. Source inspection confirms
`ui/cmd/mastarr/main.go` still serves the shell/readiness/assets and does not
compose the review, workflow, trash or settings handlers. The correction treats
both conditions as an integration blocker, supersedes the earlier `no blocker`
wording and requires a later U-05 rerun. No browser evidence was fabricated.

### P2-1 - closed: evidence claims match direct assertions

The unsupported ETag statement is gone. The correction directly sends preview
`HEAD`, verifies status, content type, immutable cache policy and `nosniff`, and
asserts the same headers plus dimensions and query non-reflection on `GET`.
Mutation methods remain rejected. The ledger separately identifies composed
router and browser-origin behavior as unavailable. Metadata and safe canonical
claims match the rendered shell and existing shell/asset focused checks.

## Re-audited evidence and boundaries

- A-26 still observes exactly four allowed reads, zero mutation requests and zero
  reader calls for invalid action drafts; forged POST/DELETE remain 405.
- A-42 still observes zero `GetConnection` calls for five unsafe endpoint drafts,
  one call for the safe draft, and no credential/error marker in HTML.
- Transport failures preserve unavailable/deadline/cancellation identity without
  copying private response details into normalized errors or shell HTML.
- Structural semantics include a single heading, labelled main, skip link,
  focusable target, labelled navigation, current item and status/alert roles.
- Preview GET/HEAD, metadata, unknown-route structure and safe canonical behavior
  use synthetic data only. No private hostname, real inventory, tracker URL,
  credential or user runtime path was added.
- Product scope and local ledger link are correct. No unrelated lane, root import,
  API/generated contract, router or state file entered the product commit.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact SHAs, trees, ancestry, two-file product diff and SHA-256 identities | PASS. |
| Prior P1/P2 line-by-line audit against acceptance matrix, dashboard requirements, ledger and fixture | PASS; no remaining finding. |
| CUA inventory | `browsers: []`; Mac locked for native app inventory. Browser scenarios remain honestly blocked. |
| `git diff --check 4a93164^ 4a93164` | PASS. |
| Ledger local-link resolution | PASS. |
| `GOWORK=off go test ./tests/browser -count=1 -timeout=120s` from `ui/` | PASS. |
| `GOWORK=off go test ./internal/shell ./internal/assets -count=1 -timeout=120s` from `ui/` | PASS. |
| `GOWORK=off go test -mod=readonly ./... -count=1 -timeout=360s` from `ui/` | PASS. |
| `GOWORK=off go test -mod=readonly -race ./... -count=1 -timeout=600s` from `ui/` | PASS. |
| `GOWORK=off go vet -mod=readonly ./...` from `ui/` | PASS. |
| `GOWORK=off go mod verify` and `GOWORK=off go mod tidy -diff` from `ui/` | PASS; modules verified and no manifest diff. |
| CGO-free Linux amd64/arm64 `go test -mod=readonly -run '^$' -c ./tests/browser` from `ui/`, outputs under `/tmp` | PASS. |
| `./scripts/generate.sh --check` | PASS; generation and staged generation passed. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-api.sh` | PASS; generation and Vacuum 100/100 passed. |
| `python3 scripts/check-architecture.py` | PASS. |
| `./scripts/check-lint.sh` | PASS; lint, architecture and standalone-client boundaries passed. |
| `python3 scripts/check_planning.py` | PASS: `Planning valid: 53 tasks, 60 acceptance cases; local links resolve.` |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 with `guardrail checks passed (ci)`, including staged generation/contracts, Vacuum, lint/architecture, module tests/race/vet/verification and Linux cross-builds. |

One auxiliary focused shell/assets invocation was initially issued from the
repository root and failed package discovery; the identical command from `ui/`
passed. This reviewer working-directory error does not affect product evidence.

## Acceptance contribution assessment

- A-26 and A-42 retain accepted scoped production-handler contributions; their
  browser portions remain blocked/not run.
- A-47 and A-49 have no runnable contribution in this batch and are correctly
  blocked/not run.
- A-48 retains accepted partial transport/sanitization evidence; action recovery,
  idempotency and effect evidence remain blocked/not run.
- A-50 retains accepted partial structural accessibility/configuration evidence;
  all browser accessibility and visual checks remain blocked/not run.
- A-51 retains accepted partial shell/asset evidence, now including direct
  GET/HEAD header assertions; composed route and browser loading remain blocked.

## Integration recommendation

Approve correction product `4a93164b98c09b687598538de7dfa6be8309d4e1`
and handoff `37aefb6156829a1db64239901fe4105596907f37` as the accurate U-05 evidence record.
Keep browser-level U-05 acceptance open until router composition and browser
availability permit the explicitly listed rerun. This approval does not claim
release, deployment, live-stack or completed browser verification.
