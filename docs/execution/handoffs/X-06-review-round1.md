# X-06 independent review, round 1

## Decision and exact scope

Decision: **changes_requested**. Two P1 and two P2 findings remain open.
The public mutation gates remain fail-closed, but they do not prevent false
already-satisfied observations from the public adapter. The synthetic write
protocol also lacks its claimed exact preview binding.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Product: `d131422a7319be22ccc6fba17a54a047c89e25d8`.
- Product tree: `1925d1d1266ec8d7bdc1574ca4dae282a554671e`.
- Handoff: `e7047e143fae06d2871afc56337709fca339a311`.
- Handoff tree: `fc4eb6cb0ef2a9d1167bdc7563a5ede81c9c4ed9`.
- Reviewed paths: `internal/adapters/arr/write/`, `tests/fixtures/arr/write/`
  and existing root ports/specifications; A-12/A-13/A-16/A-17/A-33.

Review ran in an isolated detached checkout at the handoff. Independent synthetic
probes ran in a separate archive of the exact product. Product, state, shared
main and live services were untouched. This commit contains only this receipt.

## R1 — P1: nonempty text substitutes for exact preview authorization

Locations: `client.go:280-324`, `client.go:1048-1050`, and `commandPayload`.

PreviewRevision is used only in a nonempty/length check. No saved immutable
preview is resolved, no request fields are bound to that revision, and no native
preview/rejection check precedes the command. The POST body is built entirely
from caller-supplied source paths/associations/attributes. This does not implement
the handoff's "preview-bound" claim or the A-12/A-16 refusal boundary.

Deterministic probe `TestReviewerForgedPreviewMustNeverDispatch` uses the
existing synthetic constructor and its separately supported import capability.
Serve GET `/api/v3/episode` with episode 301, series 201, no file; serve history
as an empty array. A manualimport route is available to return an ExistingFile
rejection for `/synthetic/downloads/one.mkv`. Call Import with registered ID 201,
transfer copy, source `downloads:one.mkv`, episode 301, and preview revision
`arbitrary-forged-preview`. The adapter never reads manualimport and POSTs one
ManualImport command. It later returns unknown because the fixture did not
materialize a file. That later failure does not repair the unauthorized dispatch.
All three race-enabled repetitions reproduced one command.

Public New still blocks actual changed-state dispatch via G-01, so this probe is
a synthetic protocol defect, not a claim that production writes are enabled.
Keep that gate blocked throughout correction.

Required correction: resolve/bind exact trusted preview evidence (including
connection, registered identity, file/episode/subtitle attributes and transfer)
and enforce current native rejections before a write. Missing/forged/stale or
changed revisions must make zero command requests. Preserve the immutable
workflow approval boundary separately from native preview validation. If the
existing ports cannot supply that evidence, record a contract blocker instead
of treating arbitrary text as authorization. Add valid-binding positive and
forged/changed/rejected negative synthetic tests.

The current success predicate also compares library read-back to request Source
paths (`importMatchesExpected`), rather than deriving approved mapped destination
evidence. Existing happy-path fixtures report imported files in the download
paths. This is not native library-placement/provenance proof; correction must
bind expected materialization rather than generalize those in-place fixtures.

## R2 — P1: untrusted file/title read-back produces false public success

Locations: `client.go:334-404`, `client.go:535-550`, and `client.go:785-813`.

Sonarr aggregation deduplicates by file ID and appends episode IDs without
comparing repeated file path/size. Different file IDs at one mapped path are
also accepted. Radarr readTitle checks only that returned ID is positive; it
does not require the requested ID. A missing file movieId is accepted and the
adapter substitutes the requested ID. These inputs can satisfy the initial
Import predicate before G-01's dispatch gate, producing already_satisfied from
contradictory or foreign evidence.

Probe `TestReviewerConflictingReadbackNeverAlreadySatisfied` uses public New,
RootPaths mapping `/synthetic/downloads` to root downloads, and empty history.
Its cases are:

1. GET episode returns outer episodes 301/302 with seriesId 201, each referring
   to file 801. First file: seriesId 201, path `/synthetic/downloads/one.mkv`,
   size 10. Second file: same ID/series, path `/synthetic/downloads/two.mkv`, size
   20. Request both episodes against source `downloads:one.mkv`. The adapter
   returns nil error with one file at one.mkv, episode IDs 301/302 and a success
   effect, despite the conflicting second association.
2. Change the second file ID/episodeFileId to 802 and path/size to one.mkv/10.
   Both distinct files at the same mapped target are accepted as successful
   already-satisfied evidence.
3. For Radarr, GET `/api/v3/movie/101` returns title ID 999 and nested file ID
   501, path `/synthetic/downloads/one.mkv`, size 10, with movieId absent.
   Request movie 101 at downloads:one.mkv. The public adapter returns nil error,
   ExternalID 101 and a file whose MovieID was fabricated as 101.

All three cases failed their rejection assertions on all three race repetitions;
no mutation route was needed. These are semantic failures, not race reports.

Required correction: validate requested title and parent/file identity presence,
cross-row same-ID physical details, unique identity per mapped path, complete
episode associations and configured mapping ambiguity before any successful
observation. Do not fabricate absent upstream parent IDs. Use identical strict
aggregation for preflight and post-write reconciliation. Contradiction must
remain unknown/conflicted without success effects. Add native complete-field
fixtures and retain valid multi-episode file reuse as a positive control.

## R3 — P2: wrapped context errors expose endpoint and transport text

Location: `client.go:890-891`.

When the parent context is still active but transport returns an error wrapping
canceled/deadline, request returns the original http.Client error. This is a
url.Error containing the endpoint and arbitrary transport text. Preserving
errors.Is is correct, but returning that wrapper violates sanitized connector
errors and public-repository/runtime coordinate boundaries.

Probe `TestReviewerWrappedContextMustBeSanitized` constructs public New at
`http://fixture.invalid/private-prefix` with a synthetic RoundTripper returning
`fmt.Errorf("synthetic private transport detail: %w", context.Canceled)`.
Register's catalog read returns:

```text
Get "http://fixture.invalid/private-prefix/api/v3/movie": synthetic private transport detail: context canceled
```

The leakage assertion failed three times. The same branch handles a wrapped
deadline error. The committed deadline test cancels the parent context itself,
so it misses this distinct branch. Read-body errors likewise need context
identity checked before generic malformed/bound classification.

Required correction: canonicalize context causes or wrap them in a sanitized
typed boundary. Preserve errors.Is without original URL/transport/body detail;
test wrapped canceled/deadline transport and body failures under active parents.

## R4 — P2: explicit new-title monitoring opt-in is silently overwritten

Location: `client.go:629-632`.

registrationPayload applies Fields.Monitored and then unconditionally replaces
it with false for a new title. A-13 and connectors.md specify default unmonitored
with explicit monitoring opt-in, not ignoring an explicit true value. Register
subsequently compares read-back with the original true request and reports
unknown after creating the differently configured title.

Probe `TestReviewerExplicitNewMonitoringMustBeHonored` calls the payload builder
with new movie provider 4242, root `/synthetic/library`, quality profile 7 and
Monitored pointing to true. It returns Monitored false. All three race-enabled
repetitions failed the opt-in assertion.

Required correction: apply false only when monitoring is unspecified; retain
explicit true/false without enabling search. Verify payload, read-back and
already-satisfied retry behavior for both Arr products. Existing tests prove
only the unspecified default and a monitored update of an existing Radarr title.

## Independent checks and evidence limits

| Reviewer command | Exact outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Exit 0; adapter 1.502s, fixture package 1.723s. |
| Focused vet and `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Exact-product archive: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run TestReviewer -v ./internal/adapters/arr/write` | Exit 1; R1/R2/R3/R4 assertions fail on all three repetitions; no race detector report. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; generation/candidate generation, API/Vacuum, architecture, formatting and targeted tests; final `guardrail checks passed (fast)`. |
| `GOWORK=off ./scripts/check-lint.sh` | Exit 0; nine zero-issue module results and import boundaries pass. |
| `python3 scripts/check_planning.py` | Exit 0; 53 tasks, 60 acceptance cases; local links resolve. |
| Linux amd64/arm64, `CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./internal/adapters/arr/write` | Exit 0 for both architectures. |

Public New installs separate unknown registration/import capabilities and has no
public configuration to enable either. Read-before-write and no-search defaults
are present; native mutations stay blocked. No standalone generated types or
root storage/workflow imports were added. Standalone write migration is X-20,
not evidence from this handwritten protocol model. No live Arr service was used.

Registration loss returns immediately before title read-back; existing tests do
not cover a lost registration response. Import drops command response identity
and history is a first-page, title-level boolean. One old title history event is
not exact command or per-file provenance; history alone does not establish
success in the code. New-title Sonarr registration, schema/options validation,
unselected native fields outside this DTO subset, actual mapped library
destination/source-preservation and companion/native anime/subtitle behavior
are not proven by these tests. Do not claim native compatibility or complete
cross-system A-33 from the synthetic fixtures. The two JSON files are descriptive
documents: their fixture test asserts labels only; inline httptest cases supply
the actual protocol evidence.

Full root tests/vet/cross-builds, full --ci, remote CI and live compatibility were
not rerun or approved here. C-06 owns the full aggregate. Keep G-01 open and
capabilities blocked; correction/re-review must not enable native writes.

## Handoff

Coordinator should record this receipt's exact commit SHA separately. Keep X-06
open for the four findings and independent re-review against exact corrected
product/handoff SHAs. Reviewer changed no product, state or live media.
