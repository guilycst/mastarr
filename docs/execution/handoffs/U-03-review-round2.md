# U-03 independent review round two

## Decision

`changes_requested`.

Two P1 findings remain. The correction closes the foreign joined-run gap and
successfully joins a composed registration-then-import workflow through the
production generated client, but valid `fs.trash` and `fs.delete` plans are
rejected by both production readers. Workflow validation also permits the same
effect identity in multiple steps and counts it more than once.

## Review identity and scope

- Independent reviewer: `/root/c01_reviewer`; reviewer did not author U-03.
- Product commit:
  `e5b6c6ee39b2b1bdd771ac4375661d2c0698f16f`.
- Product tree: `b02eaad3d53169c5e70b8345d8cfc2c8f0e249ef`.
- Product base: `f9b6816db3092fdf1ccbb442a00e1e711294a2e9`.
- Product base tree: `99d9b98c2629fb2fd1f0323c46c1adae1514336f`.
- Correction handoff commit:
  `4047e726a9289d23db45d1b0f93c8dc03eca532a`.
- Correction handoff tree: `ebd3da2d149878a81efc4eaba88901d396ac9b7e`.
- Clean review checkpoint:
  `4ac842489dd8b30b8a6bc895fb734a7f1ef143f0`.
- Checkpoint tree: `1e9f3d4afdae1cb3e35517666e1b788fcb9c116b`.
- Correction handoff SHA-256:
  `dc13452fbd69c598598a85f2b11b3031c27efd7984581d1d3c8029a18d31d924`.
- Product diff SHA-256 for `ui/internal/review/` and
  `ui/internal/workflows/`:
  `c10812697a24a26efc55127e7d3b33fee8276fd888ca53b1a191cdf395fdcbc7`.

Git ancestry binds product to handoff and handoff to checkpoint. The product
commit changes only the ten recorded files in the two U-03 packages. Review
used synthetic `httptest` services and production `HTTPReader` adapters. All
temporary reviewer probes were removed before this receipt. The checkout was
clean before writing this file.

## Findings

### RR2-1 — P1: valid trash and hard-delete plans cannot reach either production page

Both strict union validators route `fs.trash` and `fs.delete` file arrays to
`validateRawFiles(..., false)` (`ui/internal/review/json.go:158-172` and
`ui/internal/workflows/json.go:158-172`). That branch expects Arr `ImportFile`
members named `source` and `movieOrEpisodeId` at lines 196-220. The OpenAPI
contract instead defines each trash/delete member as a direct `FileTarget` with
`rootId` and `relativePath`.

An independent generated-client fixture returned a valid `fs.trash` plan with
root `downloads`, relative path `Show/E01.mkv`, 30-day retention and stopped
client `torrent-1`. `review.HTTPReader.GetReview` returned `review response
invalid`; the same plan joined from a workflow caused
`workflows.HTTPReader.GetWorkflow` to return `workflow response invalid`.
`fs.delete` takes the identical broken branch. Thus production pages cannot
show the exact retention, stopped-client, permanent-delete, irreversible or
unselected-payload evidence that the normalized-model tests demonstrate.

Required correction: validate Arr import files, mapped files and direct file
targets with distinct shapes in both readers. Add production
generated-client-to-handler regressions for `fs.trash` and `fs.delete`, and
exercise every supported action discriminator through this path.

### RR2-2 — P1: duplicate effect identity across workflow steps is accepted and double-counted

`validateStepScope` creates its effect-identity set inside each step at
`ui/internal/workflows/model.go:591`, so duplicate and resolved/unresolved
checks end when that step ends. `validateWorkflow` then increments the aggregate
with raw slice lengths at lines 542-543. Two steps may therefore report the
same effect ID, pass an `aggregateEffectCount` of two, and render that one
identity twice as independently attributable evidence.

An independent handler fixture supplied two steps, each with observed effect
`effect:same`, and aggregate count two. The handler returned HTTP 200, rendered
the ID under both steps, and displayed two observed effects. This leaves R3's
one-honest-entry and count requirement incomplete for composed, partial and
post-cancellation workflows.

Required correction: enforce workflow-wide effect identity uniqueness across
observed and unresolved sets, and compare aggregate counts with the resulting
unique evidence. Add a production workflow fixture with separate action runs
that repeat or cross-resolve the same identity.

## Round-one finding disposition

| Round-one item | Result | Independent evidence |
| --- | --- | --- |
| R1 joined run/plan/workflow/step identity | PASS | Existing foreign and missing identity regressions pass. Code checks requested run ID, plan ID, workflow ID and step ID before merge. |
| R2 per-step plan joins and composed registration/import | PASS behavior; regression gap remains | Independent production `HTTPReader`-to-handler fixture joined registration and import plans and rendered each digest, action kind, impact, preview, root-relative file identity and SDH flag. The committed suite still has only a standalone `fs.copy` production fixture. |
| R3 honest effect identities and counts | FAIL | Per-step duplicates, overlap, empty complete outcomes and count mismatch fail closed; applied, already-satisfied, pending, unknown, failed and cancelled evidence remains visible. Partial evidence is represented by observed plus unresolved identities. Workflow-wide duplicate identity remains accepted under RR2-2. |
| R4 exact authority scope and kind-scoped controls | FAIL in production | Normalized-model tests render client/torrent IDs, stopped IDs, retention, capabilities, subtitle SDH, irreversible/unselected payload and estimated bytes with kind-scoped controls. Valid trash/delete plans cannot reach those renderers under RR2-1. |
| R5 duplicate JSON keys at every depth | PASS | Independent probes rejected duplicate root keys, nested object keys and keys inside array objects in both packages. The raw walker precedes typed and union decoding. |
| R6 512/513/4096/4097 reason boundaries | PASS | Both handlers accept reason sizes 512, 513 and 4096 and reject 4097; other scalar fields retain the 512-byte bound. |

## Additional boundary review

- GET and HEAD are the only handler methods; POST, PUT and DELETE return 405
  without calling the reader. Forms preserve drafts through GET and do not
  dispatch approvals, retries, cancellations or other writes.
- Detail IDs, generated response identities and joined action identities fail
  closed. Redirects are refused, context cancellation/deadline identity is
  retained, bodies are bounded, transport/problem responses are sanitized, and
  HTML plus draft values are escaped under private/no-store response headers.
- Idempotency keys and deep-link draft fields round-trip. Registration approval
  remains separate from a later exact import approval.
- The UI packages import only HTTP/generated UI dependencies. Architecture and
  standalone-client checks found no root internals, database, filesystem mount
  or upstream client boundary violation.
- Jellyfin refresh remains a distinct action kind. No Seerr write path, live
  service operation, credential, private hostname, tracker data or real media
  fixture was found in scope.
- Production router/browser integration remains deferred in the correction
  handoff and was not claimed by this receipt; review exercised the production
  generated-client readers and handlers directly.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact commits, trees, ancestry, owned diff, SHA-256s, remote identity, clean checkpoint and `git diff --check` | PASS. Remote is `guilycst/mastarr`; product changes only the recorded U-03 files. |
| Temporary `TestReviewerRound2ComposedRegistrationImportProductionPath` | PASS. Both plans joined and rendered with separate immutable evidence and action scope. |
| Temporary `TestReviewerRound2ValidTrashPlanProductionReader` in both packages | FAIL as reproduction. A contract-valid trash plan was rejected by both readers, establishing RR2-1. |
| Temporary `TestReviewerRound2RejectsDuplicateEffectIdentityAcrossSteps` | FAIL as reproduction. Handler returned 200 and counted/rendered the same identity twice, establishing RR2-2. |
| Temporary duplicate-key probes at root, nested-object and array-object depths in both packages | PASS; all duplicate forms failed closed. |
| Temporary applied/already-satisfied/pending/unknown/failed/cancelled plus observed-and-unresolved evidence table | PASS. States remained distinct and count-consistent. |
| `GOWORK=off go test ./internal/review ./internal/workflows -count=1 -timeout=120s` | PASS. |
| `GOWORK=off go test -race ./internal/review ./internal/workflows -count=1 -timeout=180s` | PASS. |
| `GOWORK=off go test ./... -count=1 -timeout=240s` from `ui/` | PASS. |
| `GOWORK=off go test -race ./... -count=1 -timeout=300s` from `ui/` | PASS. |
| `GOWORK=off go vet ./...`, `GOWORK=off go mod verify`, and offline `go mod tidy -diff` from `ui/` | PASS; all modules verified and no manifest diff. |
| CGO-free `GOOS=linux go build ./...` from `ui/` for amd64 and arm64 | PASS. |
| Offline `./scripts/generate.sh --check` | PASS; generation and staged generation checks passed. |
| Offline `./scripts/check-api.sh` | PASS; Vacuum score 100/100 and API checks passed. |
| `python3 scripts/check-architecture.py` and `GOWORK=off ./scripts/check-lint.sh` | PASS; import boundaries and lint passed. |
| `python3 scripts/check_planning.py` | PASS: `Planning valid: 53 tasks, 60 acceptance cases; local links resolve.` |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 with `guardrail checks passed (ci)`. |

The green committed suite does not invalidate RR2-1 or RR2-2: neither rejected
production action shape nor cross-step duplicate identity is covered by it.

## Acceptance contribution assessment

- A-12: registration and exact import remain separately presented and bound;
  the independent composed production path passed.
- A-13, A-15, A-20 and A-49: normalized rendering has the required exact
  authority fields, but production coverage is incomplete and trash/delete
  scope is unreachable under RR2-1. These contributions are not cleared.
- A-35, A-37 and A-48: foreign joins, cancellation wording, retry evidence,
  reason boundaries and ordinary draft preservation pass. Honest composed
  effect identity/count remains blocked by RR2-2.

## Integration recommendation and resume checkpoint

Do not mark U-03 complete or integrated. Correct RR2-1 and RR2-2 in the owned
UI packages, add the production regressions described above, rerun focused and
aggregate offline gates, and request another independent review. Coordinator
retains `docs/execution/state.json`; this reviewer changed only this receipt and
removed no conflicting or unknown files.
