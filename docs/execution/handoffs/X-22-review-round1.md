# X-22 independent review, round 1

## Decision and source

Decision: **changes_requested**. One P2 timestamp regression remains open (R1).
No blocking transport, identity, generated-type or read-only defect was found
in the other reviewed paths.

- Reviewer: `/root/x05_reviewer`; independent of the product author.
- Exact product: `f998ae5d68fbcc8853c8b5180835a9008c60a68b`.
- Product tree: `b114b58cd179d9764d14f3ce42aa483438acc3bc`.
- Exact handoff: `4fd1f38c76c00abf59e89edd79a5d2d9a7cb2da6`.
- Handoff tree: `f1b9993cbe2d20098df0665a18df1febe9e58aa4`.
- Scope: `internal/adapters/seerr/`, `tests/fixtures/seerr/`, root go.mod/go.sum
  and the standalone `clients/seerr/` boundary; A-08/A-09/A-54.

The review checkout is isolated and detached at the handoff. Additional reviewer
probes used an archive of the exact product. Product paths, execution state,
shared main and live services were untouched. This commit contains this receipt
only.

## R1 — P2: fresh request observation uses source update/create time

Location: `internal/adapters/seerr/client.go:589-603`.

`mapRequest` sets the common port record's `ObservedAt` from the upstream
`SourceUpdatedAt`, or `SourceCreatedAt` when no update time is present. Those are
Seerr object lifecycle timestamps, not when Mastarr read the current request
state. The previous root implementation used read time for this record while
retaining source times separately. Media observations and page coverage still
use fresh read time, so the migration now reports inconsistent freshness for
request status versus its nested media.

A-08 requires separate dimensions and timestamps for current request state,
Arr import and availability. An unchanged request may be read successfully
today but appear stale because its last edit was days ago; a future source
timestamp can instead be misrepresented as future observation evidence.

Deterministic reproduction uses the committed fixture and existing helper:

```go
handler := &seerrFixtureHandler{fixture: loadSeerrFixture(t)}
client, server := newSeerrFixtureClient(t, handler, "reviewer-seerr")
defer server.Close()
before := time.Now().UTC()
page, err := client.ListRequestsDetailed(context.Background(), "reviewer-seerr", "", 2)
after := time.Now().UTC()
// Assert err == nil and the first record's ObservedAt lies within before..after.
```

At the exact product, request 9001 has
`Record.ObservedAt = 2026-09-10T09:30:00Z`, identical to SourceUpdatedAt. The
independent read occurred at `2026-09-16T01:00:25Z`; coverage observed time and
nested media availability are current. The read-time assertion failed on every
one of three race-enabled repetitions. There was no race detector report.
The existing fixture tests inspect native status/relationships but do not
assert this observation timestamp distinction.

Required correction: use a read-time observation timestamp, preferably the
page's normalized coverage observation time, for every common request record.
Keep SourceCreatedAt and SourceUpdatedAt in their existing separate fields.
Cover old/future update timestamps, created-only requests, and requests with
missing media/timestamps; their observation time must still represent this read.

## Other independently inspected behavior

The adapter owns only translation from normalized standalone observations into
Mastarr types. The standalone client is private; generated DTOs do not appear in
public adapter or domain types. Scope validation precedes reads. Common record
IDs stay paired with the page's connection, and detailed scoped identities
retain the configured instance prefix. Provider/service/base/4K topology and
season/request evidence are copied without direct upstream writes.

An independent synthetic request page preserved known zero ServerID, ProfileID,
LanguageProfileID, base/4K service IDs and service-error ID as string `"0"`.
Native request status 999 remained unknown; media status UNKNOWN retained
Availability.Known=false. This positive probe passed all three race repetitions.
A wrapped canceled/deadline transport error preserved errors.Is and exposed
only the canonical context error text, with no synthetic transport detail;
that probe also passed all three repetitions. The first version of the topology
probe incorrectly supplied serviceErrors as an array; using its specified
native object shape fixed the test data. That invalid-array rejection was not
an adapter finding.

Pagination delegates to the bounded standalone traversal. Fixture tests prove
take/skip, overlap deduplication, changing page evidence and termination; offset
traversals retain partial `pagination_snapshot_unverified` coverage. Missing or
incomplete pageInfo cannot claim complete absence. Root coverage maps the
standalone completeness/count/reasons/times and connection; it has no source ID,
start time or snapshot revision from this standalone public coverage type. No
immutable upstream snapshot or additional absence authority is claimed here.

Auth token aliases remain API-key aliases as documented by the root adapter.
The nested client enforces the configured 30-second request bound alongside
earlier parent/client deadlines. Version metadata does not enable unpinned read
capabilities; Seerr writes remain unsupported. Fixture reads use GET and the
synthetic API key only. No live service or private inventory was used.

## Independent verification

| Reviewer command | Exact outcome |
| --- | --- |
| Root `GOWORK=off go test -mod=readonly -race -count=3 -timeout=180s ./internal/adapters/seerr` | Exit 0; 1.542s. |
| Root focused vet and `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Standalone `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./...`, vet and module verification | Exit 0; 1.667s; all modules verified. |
| Standalone `GOWORK=off GOPROXY=off GOSUMDB=off ./check-generation.sh` | Exit 0; committed output reproduced offline with installed pinned tool dependencies. |
| Exact-product reviewer probes: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run TestReviewer -v ./internal/adapters/seerr` | Final suite exit 1; read-time assertion fails all three runs; topology/status/context positive controls pass. |
| Root adapter Linux amd64/arm64 builds, `CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./internal/adapters/seerr` | Exit 0 for both architectures. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; generation/candidate generation, API/Vacuum, architecture, formatting and targeted tests; final `guardrail checks passed (fast)`. |
| `GOWORK=off ./scripts/check-lint.sh` | Exit 0; nine zero-issue module results and import boundaries pass. |
| `python3 scripts/check_planning.py` | Exit 0; 53 tasks, 60 acceptance cases; local links resolve. |

Fresh public bootstrap was independently checked using a new module cache with
`GOWORK=off GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org` and
`go mod download -json` for the pinned Seerr pseudo-version. Exit 0, public Git
origin `873e53ef67292549bcd3e42f8246d10c81496132`, subdirectory `clients/seerr`,
sum `h1:NhasfCbWK9+Omoy2+iKHuDkB625MRA0216saEYP2FUw=` and go.mod sum
`h1:sR+1FJpZDb3HyEpMUo9UyIXXM60YEPDtFtR+grqG5K4=` match root go.sum.
Local standalone files match the downloaded module byte-for-byte. No replace,
local go.work or unpublished-module bootstrap is required by this slice.

## Handoff and limits

Keep X-22 open for R1 correction and independent re-review against exact corrected
source/handoff SHAs. Coordinator should record this receipt's exact commit SHA
separately. Full root tests/vet/cross-builds, full --ci, remote CI and live service
compatibility were not rerun or approved here. C-06 owns the full aggregate.
G-01 remains open; this review enables no mutation, release or deployment.
