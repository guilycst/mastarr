# X-06 independent review, round 4

## Decision and exact scope

Decision: **approved** for the reviewed blocked write-adapter product and its
corrections. All reported P1/P2 findings from rounds one through three are
closed within this scope. No new P1/P2 finding was observed. This approval does
not resolve G-01 or authorize native Arr writes, release, deployment or live
media operations.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Product: `0bbf8405859ac604d9d05237badef45dd0ee7fbc`.
- Product tree: `371f33d106e9842d1726b59696a62b3afb6825bb`.
- Correction handoff: `fa4f98b0e1f227eb072f3263d1973f6cab43c6de`.
- Handoff tree: `e3162dcde5c174e710358c74c0b07862b3e43a89`.
- Prior receipt: `2d366197a16517eab8abc8dcd975a167d0f8307e`.
- Reviewed scope: `internal/adapters/arr/write/`,
  `tests/fixtures/arr/write/`, root ports and the standalone-client boundary;
  A-12/A-13/A-16/A-17/A-33 contributions.

Review used an isolated detached checkout at the exact handoff. Independent
synthetic probes used a separate archive of the exact product. No product,
state, shared main or live services were changed. Only this receipt is
committed; coordinator records the exact receipt SHA separately.

## Independent finding closure

The new seenEpisodes index is checked and populated immediately after each
row's requested-series validation, before any nil-file branch. The exact
round-three fileless-plus-complete reproductions now return failures without
already-satisfied effects in both response orders, with zero writes on all
three race-enabled repetitions. The independent probe also checks distinct
complete file IDs/paths for one episode; that case remains rejected.
The standalone Sonarr client's global uniqueness policy remains consistent
with this implementation.

Round-two nested series identity probes for omitted, null, zero and foreign
values all fail closed through public New. The same-file changed path/size,
distinct files at one mapped path, and foreign Radarr title reproductions all
remain rejected. Sonarr MediaFile.MovieID is empty; Radarr retains its validated
movie identity. Strict aggregation is shared by initial observation and
post-command read-back.

An independent public complete season-pack control returns one complete file
801, path downloads:pack.mkv, size 20, associated with different episodes
301/302, with empty MovieID and already_satisfied. It performs zero writes on
all three race repetitions. The committed command/reconciliation season-pack
control also passes. Rejecting duplicate episode IDs does not reject legitimate
multiple distinct episodes sharing one file.

The independent forged-revision probe uses a fixed trusted server revision;
it makes zero commands. Code still refuses a missing resolver, mismatching
revision, omitted/duplicate/changed selections and selected native rejections.
Committed preview omission/rejection tests pass. Read-before-write idempotency
and lost import-command response reconciliation pass; incomplete per-file
read-back remains unknown. A materialized state needs no additional write.

Active-parent wrapped canceled/deadline transport and body probes preserve
errors.Is while excluding original endpoint/private wrapper details. Both
Arr payload builders preserve explicit monitoring true/false and default an
unspecified value to false, without search options. Existing registration
patch/default/idempotency tests pass.

Public New still installs blocked registration/import capabilities, with no
public capability-enablement configuration. The command-capable constructor
is unexported and exercised only by synthetic package tests. G-01 remains
explicit. No generated or standalone upstream DTO was added to root
ports/domain/public API, and no storage/execution import was introduced into
this adapter. Root and nested manifests contain no local replace directive;
fast isolation checks enforce no go.work.

## Evidence limits retained

The trusted root PreviewResolver must establish actual current native
observation and immutable revision authority, including connection, title and
transfer scope. The request-echo synthetic helper does not establish those
production guarantees. Nonzero ObservedAt alone is not a freshness proof.
Production composition and standalone write migration remain X-20 work while
G-01 blocks writes.

These synthetic source-path fixtures do not establish native library placement
and source preservation, registration lost-response handling, exact native
command/history provenance, native anime/subtitle compatibility, Jellyfin
availability or Seerr state. Command acceptance, file read-back and history
remain distinct evidence. The prior receipts' limits are not converted into
native acceptance by local test success. No live service was used.

## Independent checks

| Command | Outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./internal/adapters/arr/write ./tests/fixtures/arr/write ./internal/adapters/arr/read` | Exit 0; write 1.545s, fixtures 1.740s, read 36.583s. |
| Exact-product archive: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run TestReviewer -v ./internal/adapters/arr/write` | Exit 0, 1.502s; all former findings, both fileless row orders, nested-series variants, explicit monitoring, sanitized transport/body causes and public pack pass each repetition; no race report. |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=180s ./...` | Exit 0; full root suite, storage 12.926s. |
| `GOWORK=off go vet -mod=readonly ./...`; `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Each of ui, tools and all six clients: offline `GOWORK=off GOPROXY=off GOSUMDB=off go test -mod=readonly -count=1 -timeout=120s ./...`, then same with `-race`; module-local vet/mod verify | Exit 0 for all eight nested modules. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; deterministic generation/candidate generation, root and staged standalone API/Vacuum, formatting, isolation, architecture and targeted tests; final fast success. |
| `GOWORK=off ./scripts/check-lint.sh` | Exit 0; nine zero-issue module results and import boundaries. |
| `python3 scripts/check_planning.py` | Exit 0; 53 tasks, 60 acceptance cases; links resolve. |
| Linux amd64/arm64, `CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0 for both root builds. |

Receipt commit runs the versioned pre-commit hook without bypass. Full --ci was
not rerun here and remains C-06-owned. Remote CI and native write compatibility
are separate gates. Coordinator may integrate this reviewed blocked product;
preserve G-01 and the evidence limits above.
