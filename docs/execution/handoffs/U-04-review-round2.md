# U-04 independent correction review round two

## Decision

`changes_requested`.

P1-1, P1-3, P2-1 and P2-2 are closed. P1-2 remains open because endpoint
credential-marker rejection examines only raw URL text. A percent-encoded marker
survives validation and is copied into HTML.

## Review identity and scope

- Independent reviewer: `/root/c01_reviewer`; reviewer did not author correction.
- Correction product: `067ebe5fbef03ae915bf31d22234d637a9147cb2`.
- Product tree: `11bb3c4d3dead8429d5560072c3e87c48bf72c63`.
- Product parent: `eefef5f1b71f0bee9d587887d99023889e751787`.
- Correction handoff: `51dedbcebb6a998c1302acb0544d1d0033c13e35`.
- Handoff tree: `82bd813628fb98c94032de59925bfc472484da8c`.
- Clean review checkpoint: `12435a6b25f89615dcea7286fde93a627cb490f0`.
- Checkpoint tree: `eeb41a0993f5616808b0a903ccace9e9898e7ff7`.
- Prior review: `26c1355f59c08cdf78c1eb0c426602b230cd8552`.
- Correction handoff SHA-256:
  `9e7969fc0c2f023412d329332dadea80cbae45605ffa258f19ed54b91add1b03`.
- Correction product-path diff SHA-256:
  `ce7c4f48cd9ffacfe186fcb7af90759b8cc1118432380f93b246c30aaf8f8e4b`.

Ancestry binds product to handoff and handoff to checkpoint. Product changes are
confined to settings/trash model and test files. Coordinator state changed outside
product commit. Review used synthetic `httptest` API services through production
generated-client `HTTPReader` and handlers. Overlay probes remained under `/tmp`;
no product, API, generated, router or state file was edited by reviewer.

## Remaining finding

### P1-2 — percent-encoded credential markers bypass endpoint draft rejection

`ui/internal/settings/model.go:674-692` parses endpoint, rejects userinfo/query/
fragment, then searches credential markers only in lowercased raw input. `url.Parse`
decodes escaped path content into `parsed.Path`, but validation never examines that
decoded form. `ui/internal/settings/render.go:292-314` then renders accepted draft.

Production probe requested:

`/connections/qbittorrent-main?endpoint=https%3A%2F%2Fexample.invalid%2Fapi%2Fs%2565cret%3Dsynthetic-value`

Decoded endpoint draft is
`https://example.invalid/api/s%65cret=synthetic-value`; decoded URL path contains
`secret=synthetic-value`. Handler returned 200, called production reader and copied
`s%65cret=synthetic-value` into HTML. Literal userinfo, query, fragment and literal
credential-marker cases correctly returned 400 before reader access and without
echo.

This leaves round-one P1-2 and A-42 open: percent encoding can hide the marker while
credential value remains in browser URL and response HTML.

Required correction: validate bounded canonical/decoded URL components as well as
raw input, reject encoded credential markers before reader access, and add single-
and nested-escape regression cases that assert 400, zero reader calls and no secret
echo. Preserve rejection of userinfo, query and fragment.

## Prior finding disposition

### P1-1 — closed: exact restore/purge action binding

Only restore and purge are accepted. Default/explicit restore and explicit purge
render matching selected option, hidden action and confirmation value. Missing
action with confirmation, empty, duplicate, unknown, mismatched and duplicate
confirmation inputs return 400 before reader access. Independent production matrix
confirmed all cases. No hard-delete action is retained.

### P1-3 — closed: exact unique manifest evidence

Duplicate root-relative selected files, duplicate original targets and repeated
nonempty file identities fail in both list and detail production readers. Distinct
video/subtitle manifests pass. Handler renders identity, type, role, size, digest,
observed timestamp and exact target; optional absent values remain unknown.

### P2-1 — closed: YAML ownership binding

YAML with `editable=true` fails validation for configuration, connection, storage
root and path mapping observations. Active API connection, retired API connection
and retired YAML root fixtures remain accepted, with source, retirement and
read-only metadata intact.

### P2-2 — closed: restore policy and unknown observations

Trash detail states associated clients stay stopped and restore performs no
automatic re-add or automatic resume. Empty client associations and frozen-schema
stop/remove evidence render unknown rather than inferred success.

## Re-audited boundaries

- GET/HEAD remain only allowed methods; invalid draft queries and mutation methods
  fail before reader calls. No restore, purge, configuration or credential mutation
  is dispatched.
- Redirect refusal, body/depth/query bounds, cancellation/deadline classification,
  strict duplicate/unknown/null JSON checks, response identity checks and sanitized
  errors remain intact.
- HTML remains escaped and private with no-store, noindex/nofollow, nosniff and
  no-referrer headers.
- Pagination, lifecycle enums, exact root ID plus relative path, expiry, holds,
  capabilities, source/revision/startup/restart metadata, ETags, retired records and
  credential-state-only observations remain intact.
- Frozen retention, stop/remove, conflict and per-item outcome omissions remain
  explicit unknowns.
- Settings/trash packages remain HTTP-only and import no root internals, database,
  storage mount, upstream client or media filesystem dependency.
- Reviewed correction contains only synthetic fixture values; no credential,
  private tracker data, private hostname, inventory or user runtime path appears.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact SHAs, trees, ancestry, four-file product diff, hashes, clean checkpoint and `git diff --check 26c1355..067ebe5` | PASS. |
| Temporary action-binding production matrix, including default/explicit restore, purge, missing/empty/duplicate/unknown/mismatch | PASS; invalid requests made zero reader calls. |
| Temporary distinct/duplicate manifest production matrix | PASS; valid evidence rendered and duplicate file/target/identity failed list and detail. |
| Temporary active/retired API/YAML source matrix | PASS; YAML/editable contradictions failed closed. |
| Temporary literal endpoint userinfo/query/fragment/credential-marker matrix | PASS; 400, zero reader calls, no echo. |
| Temporary encoded marker `s%65cret=synthetic-value` production probe | REPRODUCED P1-2; 200 and value copied into HTML. Probe process passed because it asserted current behavior. |
| `GOWORK=off go test ./internal/settings ./internal/trash -count=1` from `ui/` | PASS. |
| `GOWORK=off go test ./... -count=1` from `ui/` | PASS. |
| `GOWORK=off go test -race ./... -count=1` from `ui/` | PASS. |
| `GOWORK=off go vet ./...` from `ui/` | PASS. |
| `GOWORK=off go mod verify` and `GOWORK=off go mod tidy -diff` from `ui/` | PASS; modules verified and no diff. |
| CGO-free Linux amd64 and arm64 `go build ./...` from `ui/` | PASS. |
| `GOWORK=off ./scripts/generate.sh --check` | PASS; generation and staged generation checks passed. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-api.sh` | PASS; Vacuum 100/100 and API checks passed. |
| `python3 scripts/check-architecture.py` and `GOWORK=off ./scripts/check-lint.sh` | PASS; architecture, standalone-client boundaries and lint passed. |
| `python3 scripts/check_planning.py` | PASS: `Planning valid: 53 tasks, 60 acceptance cases; local links resolve.` |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; `guardrail checks passed (ci)`. |

## Acceptance contribution assessment

- A-25: unchanged partial contribution; expiry and honest unknown retention render.
- A-26: correction accepted; restore/purge binding and two-step UI boundary pass.
- A-27: correction accepted for available schema; unique exact manifest and holds
  pass, while frozen missing conflict/outcome fields remain unknown.
- A-30: correction accepted; stopped/no-re-add/no-resume policy and unknown client
  evidence render together.
- A-38: correction accepted; YAML ownership contradictions fail and active/retired
  source evidence remains.
- A-39: correction contribution remains accepted for metadata and ETag drafts.
- A-42: not accepted because encoded credential-like endpoint material can reach
  URL and HTML.

## Integration recommendation

Do not approve U-04 yet. Close remaining P1-2 with decoded/canonical endpoint draft
validation and focused regression coverage, then repeat independent correction
review. Coordinator retains state, API/generated and router ownership. This receipt
claims no browser integration, release, deployment or live-stack validation.
