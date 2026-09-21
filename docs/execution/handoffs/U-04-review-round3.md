# U-04 independent correction review round three

## Decision

`approved`.

No P1 or P2 finding remains. Remaining P1-2 is closed: endpoint drafts now inspect
raw, parsed and component forms with bounded repeated percent decoding, reject
credential material before reader access and emit no submitted value in error HTML.

## Review identity and scope

- Independent reviewer: `/root/c01_reviewer`; reviewer did not author correction.
- Correction product: `f9bf91d9c4235196b7a50ec56e3d2039f233b1e9`.
- Product tree: `351e2aa71ce831f9feb09fc7ad2ddd47e4196fe8`.
- Product parent: `3342f17d5ce4615564108da2b3a4ce0574fa39b9`.
- Correction handoff: `8dd3171f13f254d5f7468b4d1a09d4bd6e6d7b7e`.
- Handoff tree: `fd5e7f81b9a0a64424126331035fc287f480a7dc`.
- Clean review checkpoint: `3d50a517e38d11916d00e01b333d3d12b3574f41`.
- Checkpoint tree: `98a0f981ceaeedaf5cb53130ddd19be52f7c986f`.
- Prior review receipt: `f7d7983284858f855b1c4600952ed7283c4b6a01`.
- Correction handoff SHA-256:
  `17303fca9cb396f58ac6133a387f9deedc550bf47012477a8b7dca9c8e79879c`.
- Correction product-path diff SHA-256:
  `f76f19fbb5b264cbb234609d5d93bea3309168cfcb48736a78d81c7e93e4dd48`.

Ancestry binds product to handoff and handoff to checkpoint. Product commit changes
only `ui/internal/settings/model.go` and its test. Coordinator state changed outside
product commit. Review used synthetic `httptest` API services through production
generated-client `HTTPReader` and handler. Overlay probes remained under `/tmp`;
reviewer changed no product, state, API, generated or router file.

## P1-2 closure

`ui/internal/settings/model.go:674-735` rejects unsafe endpoint drafts in three
stages:

1. Initial URL parsing rejects opaque forms, userinfo, query and fragment.
2. Raw URL, canonical string and parsed components are independently bounded and
   checked for credential markers.
3. Each candidate is repeatedly percent-decoded for four bounded passes. Every
   decoded value is size/UTF-8/control checked, checked for markers and reparsed for
   newly revealed userinfo/query/fragment. A changed value remaining after bound
   fails closed.

Independent production path matrix covered:

- literal userinfo, query, fragment and credential path;
- one through six levels of percent-encoded credential marker;
- encoded and nested encoded query delimiters/markers;
- encoded fragment and encoded full userinfo URL;
- encoded control byte and malformed escape;
- safe absolute endpoint;
- exact 512-byte accepted endpoint and 513-byte rejected endpoint.

Every unsafe case returned 400, made zero reader calls and emitted none of raw,
decoded or encoded marker/value in HTML. Safe and 512-byte endpoint drafts returned
200, called reader once and rendered escaped value. 513-byte endpoint returned 400
without reader call or echo.

## Earlier finding re-audit

- P1-1 remains closed: restore/purge selected, hidden and confirmation values bind;
  missing, duplicate, empty, unknown and mismatched action contexts fail before
  reader access. No hard-delete draft survives.
- P1-3 remains closed: duplicate selected targets, original targets and nonempty
  file identities fail; type, role, size, digest, observation time and exact target
  render for distinct manifest entries.
- P2-1 remains closed: YAML/editable contradictions fail for configuration,
  connections, roots and mappings while active/retired evidence remains visible.
- P2-2 remains closed: restore view states stopped/no-auto-re-add/no-auto-resume
  policy and labels absent association/effect evidence unknown.
- GET/HEAD-only handlers, strict JSON/identity/bounds validation, redirect refusal,
  cancellation/deadline classification, sanitized errors, escaped private HTML,
  pagination and frozen-schema unknown handling remain unchanged.
- Settings/trash remain HTTP-only with no root internals, DB, storage, upstream or
  media mount dependencies. Correction contains synthetic fixtures only.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact SHAs, trees, ancestry, two-file product diff, hashes, clean checkpoint and `git diff --check f7d7983..f9bf91d` | PASS. |
| Temporary production endpoint decoding matrix: literal/component/one-to-six-level nested encodings, malformed/control inputs, safe endpoint and no-echo checks | PASS. |
| Temporary production endpoint boundary probe: 512 accepted, 513 rejected, reader-call count and no echo | PASS. |
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
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 with `guardrail checks passed (ci)`. |

## Acceptance contribution assessment

- A-25 remains a scoped contribution: expiry and honest unknown retention render;
  frozen schema does not expose policy duration or janitor execution.
- A-26, A-27 and A-30 UI contributions pass for available schema: exact two-step
  action binding, unique manifest evidence, holds and restore client policy render.
- A-38 and A-39 UI contributions pass: YAML ownership, active/retired source,
  restart metadata and ETag draft context remain fail-closed.
- A-42 UI contribution passes: response and draft endpoint credentials are rejected
  or redacted; writeOnly values, key paths and check errors are not rendered.

## Integration recommendation

Approve U-04 correction product `f9bf91d9c4235196b7a50ec56e3d2039f233b1e9`
for coordinator integration. Coordinator retains execution state, API/generated and
router ownership. Approval covers reviewed UI package contribution only; it does
not claim browser integration, release, deployment or live-stack validation.
