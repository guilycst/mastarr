# U-03 independent review round three

## Decision

`approved`.

No P1 or P2 finding remains in the reviewed U-03 product. Correction round two
closes both round-two findings without reopening R1-R6: direct file targets now
remain distinct from Arr import and mapped-file shapes in both production
readers, and one workflow-wide identity set rejects repeated or cross-resolved
effects before aggregate counts can be accepted.

## Review identity and scope

- Independent reviewer: `/root/c01_reviewer`; reviewer did not author the
  product.
- Product commit:
  `75df6ac7bc5903bcdbfd9eae7a07862d033b6dc0`.
- Product tree: `ab2805d5b70a83ac3f2852aa9c63af17618a59dc`.
- Product base: `988f8ad988e45b09037d767061e94f7ae7e3046f`.
- Product base tree: `f2d53814234978828372aace9f6611019e2d4efb`.
- Correction handoff commit:
  `6bff56a18890c8939f3098423cb4b2ea7189d677`.
- Correction handoff tree: `128dd53270bd0e92a32c26519e292a1659642497`.
- Clean review checkpoint:
  `91e057fe72308bac848bd805498db54c60c49037`.
- Checkpoint tree: `0ccbf1e1798c4104d8b876b9c7da293b03e951df`.
- Prior independent review receipt:
  `7728d0118f334b837e7d4fc30b73b0adeedfded6`.
- Correction handoff SHA-256:
  `7c26a5b4c926fd5242e216f3583ec4585657010753c2d88c112a4dee3d22daa8`.
- Product diff SHA-256 for `ui/internal/review/` and
  `ui/internal/workflows/`:
  `58fd825dc26203b98394882cc10b5bac62f1f88950fce2df2e38ec4667849b72`.

Git ancestry binds product to handoff and handoff to checkpoint. The product
commit changes only seven files in the two owned U-03 packages. The state/API/
generated contracts were not part of the product commit. Review used synthetic
`httptest` services and the production generated-client readers. Temporary
reviewer probes were removed before this receipt, and the checkout was clean
before writing it.

## Round-two finding disposition

### RR2-1 — closed: direct target plans reach both production handlers

Both strict union validators now select explicit import, mapped and target file
shapes. `fs.trash` and `fs.delete` validate direct `FileTarget` objects with
only `rootId` and `relativePath`; Arr import continues to require `source` and
`movieOrEpisodeId`; mapped actions continue to require `source` and
`destination`. Cross-shape payloads fail closed.

Uncached generated-client fixtures for every supported action discriminator
passed through both readers and handlers. Trash retained the exact selected
target, 30-day retention and stopped client `torrent-exact-1`. Delete retained
the exact trash target, permanent and irreversible flags, and the rendered
unselected-payload boundary. An independent library-scope Jellyfin refresh
probe also passed without inventing an item ID.

### RR2-2 — closed: effect identity is unique across the workflow

`validateWorkflow` now supplies one shared effect set to every step. Local
duplicates, cross-step and cross-run duplicates, and observed/unresolved
crossovers all fail before count comparison. Aggregate and unresolved counts
are derived from the accepted unique entries and must match the API envelope.

Production fixtures with two generated action runs rejected both a repeated
observed ID and an ID observed in one step but unresolved in another. Existing
regressions continue to reject local duplication, overlap, empty completed
outcomes and contradictory aggregate counts while preserving distinct applied,
already-satisfied, pending, unknown, failed, cancelled and partial evidence.

## R1-R6 re-audit

| Item | Result | Evidence |
| --- | --- | --- |
| R1 run/plan/workflow/step identity | PASS | Foreign and missing run, plan, workflow and step identities fail closed before merge. |
| R2 per-step plan joins and composed registration/import | PASS | Independent production fixture rendered both plan digests, action kinds, impacts, preview binding, root-relative subtitle scope and SDH flag. Registration and exact import remain separately approved. |
| R3 honest effect identity and counts | PASS | Local/global duplicate and overlap cases fail; unique observed and unresolved counts match the envelope; cancellation and partial evidence remain visible. |
| R4 exact authority scope | PASS | Production readers and handlers cover every action discriminator, including client/torrent IDs, retention, stopped clients, permanent/irreversible scope, unselected payload, capabilities, estimates and subtitle flags. Draft controls remain scoped by action kind. |
| R5 duplicate/unknown JSON | PASS | Raw traversal rejects duplicate keys at every nesting depth, invalid UTF-8, trailing data and excessive nesting. Union validation rejects unknown and cross-shape action fields. |
| R6 query boundaries | PASS | Both handlers accept 512, 513 and 4096-byte reasons and reject 4097; other scalar values retain the 512-byte bound. |

## Additional boundary review

- GET and HEAD remain the only handler methods. Mutation methods return 405
  without calling the reader; the forms preserve drafts without dispatching an
  approval, retry, cancellation or other write.
- Route IDs, response IDs and joined action IDs fail closed. Redirect refusal,
  bounded bodies, timeout/cancellation identity, sanitized transport/problem
  errors, HTML escaping and private no-store/noindex headers remain intact.
- Idempotency keys and deep-link draft fields round-trip. Registration approval
  never authorizes the later exact import.
- The UI packages remain HTTP-only and import no root internals, database,
  filesystem mount or direct upstream client. Jellyfin refresh remains distinct
  from availability, and no Seerr write path was introduced.
- No credential, private hostname, tracker data, user runtime path, live media
  inventory or real service operation appears in the reviewed product or
  handoff.
- Browser/router composition remains outside this package correction and was
  not claimed; review exercised the production generated-client readers and
  handlers directly.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact commits, trees, ancestry, seven-file owned product diff, SHA-256s, remote identity, clean checkpoint and `git diff --check` | PASS. |
| Uncached focused review tests: `GeneratedActionFixtures`, `CrossShape`, nested duplicate/unknown fields and 512/513/4096/4097 reason boundaries | PASS. Every action discriminator reached the handler; invalid shapes failed closed. |
| Uncached focused workflow tests: generated action matrix, cross-step effects, foreign/missing joins, nested duplicate/unknown fields and reason boundaries | PASS. |
| Temporary composed registration/import production `HTTPReader`-to-handler probe | PASS. Separate per-step binding, kinds, impacts, preview, subtitle and SDH evidence rendered. |
| Temporary Jellyfin library-refresh production probe | PASS. Library scope retained connection/capability evidence with no item identity. |
| `GOWORK=off go test ./internal/review ./internal/workflows -count=1 -timeout=120s` | PASS. |
| `GOWORK=off go test -race ./internal/review ./internal/workflows -count=1 -timeout=180s` | PASS. |
| `GOWORK=off go test ./... -count=1 -timeout=240s` from `ui/` | PASS. |
| `GOWORK=off go test -race ./... -count=1 -timeout=300s` from `ui/` | PASS. |
| `GOWORK=off go vet ./...`, `GOWORK=off go mod verify`, and offline `go mod tidy -diff` from `ui/` | PASS; all modules verified and no manifest diff. |
| CGO-free `GOOS=linux go build ./...` from `ui/` for amd64 and arm64 | PASS. |
| Offline `./scripts/generate.sh --check` | PASS; generation and staged generation checks passed. |
| Offline `./scripts/check-api.sh` | PASS on isolated rerun; Vacuum score 100/100 and API checks passed. An initial parallel invocation collided with the simultaneous generation check's temporary Git index lock and was discarded. |
| `python3 scripts/check-architecture.py` and `GOWORK=off ./scripts/check-lint.sh` | PASS; architecture, standalone-client boundaries and lint passed. |
| `python3 scripts/check_planning.py` | PASS: `Planning valid: 53 tasks, 60 acceptance cases; local links resolve.` |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 with `guardrail checks passed (ci)`. |

## Acceptance contribution assessment

- A-12 and A-13: separate registration/import presentation, tri-state
  monitoring and exact action-specific review scope pass.
- A-15 and A-20: immutable per-step plan evidence, exact manifests,
  capabilities, estimates and no-silent-fallback presentation pass.
- A-35 and A-37: cancellation, retry, uncertainty and workflow-wide effect
  attribution/counting pass.
- A-48 and A-49: idempotency/deep-link preservation, reason bounds, route and
  joined identity binding, and exact client/file/subtitle scope pass.

## Integration recommendation and resume checkpoint

U-03 correction round two is approved for coordinator integration at product
`75df6ac7bc5903bcdbfd9eae7a07862d033b6dc0`. Coordinator retains
`docs/execution/state.json` and must record integration separately. This review
does not claim release, deployment, live-stack operation or browser/router
acceptance. The reviewer changed only this receipt and removed no conflicting
or unknown files.
