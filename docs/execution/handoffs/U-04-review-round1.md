# U-04 independent review round one

## Decision

`changes_requested`.

Three P1 findings and two P2 findings remain. Baseline tests and repository
guardrails pass, but they do not cover unsafe trash draft binding, exact manifest
presentation, credential-bearing draft input, or contradictory source ownership.

## Review identity and scope

- Independent reviewer: `/root/c01_reviewer`; reviewer did not author product.
- Main product commit: `13ec09f9f54523788aa4ddee1efa9e7bd9165704`.
- Main product tree: `66b5d8d2f3f0a7c94d684d72f40d0207f1e2b1d9`.
- Product base: `171c6efcfa2ea7b6e4f5a813f47af4ebefe16cf9`.
- Coverage follow-up: `f63cedf1dc93c6def4fb851342a3da1c19fbbd9b`.
- Coverage follow-up tree: `9849bcca3ffa6442e45c71e34c9903fef394c4fc`.
- Implementer handoff: `b99fb7552be89994e870fa50b6352b2a58e9b533`.
- Handoff tree: `f5e585039ee94875079811cf501784fa1956feac`.
- Clean review checkpoint: `ebfb395927290668aa4e163f0de57ec204093c8d`.
- Checkpoint tree: `3e119268f9777345a62a7dcb4ac8ade9de9a5e31`.
- Handoff SHA-256:
  `aed675833f13bc3a7d3dbaaa8aadd6a854e80f2b02a8b58ea613f1546676040e`.
- Product-path diff SHA-256:
  `358df73cf6af1afc94351ea64773ebcd13efd5d0fed78d45b806f89fe7f06d73`.

Git ancestry binds both product commits, handoff and checkpoint. Reviewed product
changes are confined to seven files under `ui/internal/settings/` and
`ui/internal/trash/`; coordinator-only state and the frozen API/generated client
were not product edits. Review used synthetic `httptest` API services through
production `HTTPReader` and handler code. Temporary overlay probes lived under
`/tmp` and did not write generated or product files into repository.

## Findings

### P1-1 — trash confirmation carries a different or invalid action from visible choice

`ui/internal/trash/model.go:473-515` accepts any bounded `action` value.
`ui/internal/trash/model.go:389-410` always renders the visible select with
`restore` first and no selected state, while second form copies query action into
a hidden field.

Production-path probe for `?action=purge&confirm=yes` returned 200 with visible
selector showing restore and hidden `action=purge`. A second probe for
`?action=delete&confirm=yes` also returned 200 and preserved hidden
`action=delete`, despite only restore and purge being supported and permanent
delete having no UI shortcut. This breaks exact second confirmation for A-26 and
can turn visible restore context into destructive purge context when draft is
later submitted.

Required correction: reject action values outside `restore|purge`; bind selected
option and confirmation text to same accepted action; show exact action in second
confirmation; add production-reader-to-handler tests for restore, purge, missing,
duplicate and unrecognized action values.

### P1-2 — credential-bearing endpoint draft is copied into private HTML and URL

`ui/internal/settings/model.go:517-560` accepts arbitrary bounded endpoint draft
values. `ui/internal/settings/render.go:96-98` and `:292-314` prefer that query
value over redacted API endpoint and render it into input value.

Production-path probe sent synthetic
`https://operator:synthetic-secret@example.invalid/api?token=synthetic-token`.
Handler returned 200 and HTML contained both plaintext credential strings. API
response endpoint redaction works, but draft path bypasses it. This violates A-42
and dashboard rule forbidding secrets in HTML or browser storage.

Required correction: reject endpoint drafts containing userinfo, query, fragment
or credential-like material before retaining/rendering them; do not carry secret
replacement values in GET URLs; test response-derived and query-derived endpoint
redaction through production handler.

### P1-3 — exact trash manifest permits duplicate scope and drops returned identity evidence

`ui/internal/trash/http.go:483-520` and
`ui/internal/trash/model.go:633-673` validate entries individually but never reject
duplicate selected files or original targets. `ui/internal/trash/model.go:811-829`
renders identity, role, size and target, but drops required type plus returned
digest and observed time.

Production fixture with two identical manifest entries and two identical original
targets passed `GetTrash` and rendered as valid detail. Fixture supplied
`type=file`, `digest=sha256:synthetic-digest` and
`observedAt=2026-09-21T10:00:00Z`; type, digest and observation time were absent
from HTML. These values are present in frozen schema, so this is not a frozen
omission. A destructive restore/purge confirmation cannot establish one exact,
unique manifest as required by A-26/A-27.

Required correction: reject duplicate exact selected/original targets and any
ambiguous repeated file identity; render type and all returned identity evidence,
using explicit unknown for optional absent values; regression-test duplicated and
distinct multi-file manifests through list/detail readers and handler.

### P2-1 — YAML source can claim editable and receive edit draft

`ui/internal/settings/model.go:666-668` validates source enum and editability
independently. Production fixture with `source=yaml, editable=true` passed reader
and handler, rendered `Editable true`, then rendered connection edit draft through
`ui/internal/settings/render.go:76-99`. Configuration contract requires YAML
resources to return and render `editable=false` with file/restart instructions.

Required correction: bind YAML source to `editable=false` and fail contradictory
API evidence closed before rendering. Add active/retired API and YAML ownership
matrix tests for configuration, connection, storage root and path mapping.

### P2-2 — restore policy omits stopped/no-re-add behavior

Frozen `TrashEntry` lacks stop/remove/conflict/outcome members; renderer correctly
shows those sections as unknown instead of inferring success. However,
`ui/internal/trash/model.go:374-421` never explains that restore leaves client
seeding stopped and cannot automatically re-add or resume a removed torrent.
Dashboard contract requires that operator-facing statement for A-30 even when
observed client state remains unknown.

Required correction: state stopped/no-auto-re-add/no-auto-resume policy next to
restore draft, while retaining unknown labels for absent observations. Add removed
torrent and unknown association presentation tests.

## Passed boundary review

- Both handlers allow only GET and HEAD; mutation methods return 405 before reader
  calls. Forms are GET-only and no direct API mutation method is invoked.
- Readers use generated HTTP clients, bounded bodies, deadlines and redirect
  refusal. Cancellation/timeout classes and rendered failures are sanitized.
- Raw JSON duplicate traversal covers every nesting depth; unknown members,
  trailing JSON, invalid UTF-8, excessive depth, missing/null required fields,
  invalid detail IDs, response-ID mismatch and duplicate page IDs fail closed.
- Pagination cursor/limit bounds and page envelope validation are present. Coverage
  completeness is rendered; frozen detail responses have no coverage envelope and
  are labelled unknown.
- Trash lifecycle enum accepts held, ready, restoring, purging, purged and blocked.
  Exact root ID plus relative path boundaries, expiry, capabilities, associations
  and holds survive production reader conversion. Frozen retention-policy and
  effect omissions remain unknown rather than fabricated.
- Settings readers retain YAML/API source, document/revision/startup/restart
  metadata, retired timestamps and ETags. API-returned endpoint userinfo/query are
  stripped; key path and writeOnly credential fields are discarded; raw sanitized
  connection-check error text is reduced to presence only.
- HTML values are escaped. Responses carry private no-store, noindex/nofollow,
  nosniff and no-referrer headers.
- UI packages import only nested generated/client dependencies; no root internals,
  DB, storage mount, upstream client or media filesystem dependency appears.
- Reviewed diff contains synthetic credential strings only in fixtures and no real
  credentials, private tracker data, private hostname, inventory or user runtime
  path.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact commits, trees, ancestry, owned seven-file diff, hashes, clean checkpoint and `git diff --check 171c6ef..ebfb395` | PASS. |
| Temporary production settings probe: YAML/editable contradiction plus credential-bearing endpoint draft | REPRODUCED P1-2 and P2-1; test process passed because it asserted current unsafe behavior. |
| Temporary production trash probe: duplicate manifest/targets, purge visible/hidden state and omitted digest/time | REPRODUCED P1-1 and P1-3; test process passed because it asserted current unsafe behavior. |
| Temporary production trash probe: unrecognized `action=delete` | REPRODUCED P1-1; returned 200 with hidden delete draft. |
| `GOWORK=off go test ./internal/settings ./internal/trash -count=1` from `ui/` | PASS. |
| `GOWORK=off go test ./... -count=1` from `ui/` | PASS. |
| `GOWORK=off go test -race ./... -count=1` from `ui/` | PASS. |
| `GOWORK=off go vet ./...` from `ui/` | PASS. |
| `GOWORK=off go mod verify` from `ui/` | PASS; all modules verified. |
| `GOWORK=off go mod tidy -diff` from `ui/` | PASS; no manifest diff. |
| `GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` from `ui/` | PASS. |
| `GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` from `ui/` | PASS. |
| `GOWORK=off ./scripts/generate.sh --check` | PASS; generation and staged generation checks passed. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-api.sh` | PASS; Vacuum 100/100 and API checks passed. |
| `python3 scripts/check-architecture.py` | PASS; architecture import boundaries passed. |
| `GOWORK=off ./scripts/check-lint.sh` | PASS; lint and architecture checks passed. |
| `python3 scripts/check_planning.py` | PASS: `Planning valid: 53 tasks, 60 acceptance cases; local links resolve.` |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 with `guardrail checks passed (ci)`. |

## Acceptance contribution assessment

- A-25: partial. Expiry and honest unknown retention policy render, but frozen
  schema cannot prove default/custom duration or early-janitor behavior.
- A-26: not accepted. Two forms exist and no live hard-delete call exists, but
  visible/hidden action mismatch and arbitrary action draft break exact second
  confirmation.
- A-27: not accepted. Lifecycle/holds render, but duplicate scope passes and exact
  per-item/conflict evidence is either omitted from HTML or absent from schema.
- A-30: not accepted. Association evidence remains visible/unknown, but required
  stopped/no-re-add/no-resume restore behavior is absent.
- A-38: not accepted. Retired/source metadata render and duplicate aggregate/page
  IDs fail, but contradictory YAML editability is trusted.
- A-39: contributed. Startup/restart metadata and API ETag/If-Match draft context
  render without claiming hot reload; actual stale-write rejection remains API and
  later integration scope.
- A-42: not accepted. API response credential redaction passes, but endpoint query
  drafts leak synthetic plaintext credentials into HTML/browser URL.

## Integration recommendation

Do not integrate U-04 as accepted. Correct P1 findings before another independent
review; close P2 findings in same correction because they affect assigned
acceptance behavior. Coordinator retains `docs/execution/state.json`, frozen API,
generated client and router ownership. This receipt claims no browser integration,
release, deployment or live-stack validation.
