# X-19 independent correction review, round 3

## Decision and exact source

Decision: **approved** for the recorded X-19 read-adapter scope. R1, R2 and R2a
are closed. No remaining blocking finding was identified in this correction.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Product: `7bb07b5708335701b1884d2acf48e0d3391e4c1a`.
- Product tree: `23663fabc56341026c73dd37fe4e3d41a586f04f`.
- Handoff: `42d72767a614c99d9f7452cc3ed12f75b416ac3e`.
- Handoff tree: `92f19e8370a60e72004b38ee848a3dd1074a567d`.
- Prior receipt: `a8511095a181d029861463d74fcc94aee8331652`.
- Scope: `internal/adapters/arr/read/`, retained synthetic fixture behavior,
  root dependency/module boundaries and standalone Sonarr/Radarr checks, against
  A-07/A-08/A-09/A-10/A-16 and the recorded X-19 contribution.

Review ran in a detached isolated checkout at the handoff. Independent probes
used a separate archive of the exact product. Product paths, state and shared
main were untouched; this review commit changes only this receipt.

## Evidence for closure

`legacyPreviewShape` now calls product-specific candidate validation for every
row before allowing an old rejection alias to select compatibility decoding.
Sonarr requires positive candidate/series/episode IDs, a series title, matching
nested episode series IDs, nonnegative season/episode numbers and unique episode
IDs. Radarr requires a positive nested movie ID. Required candidate path/name/
relativePath/size fields are validated. Missing/null nested identity cannot be
authorized by an unrelated row's old rejection alias. The exact prior two-row
reviewer regression now passes its rejection assertion on all three race runs.
The included stronger tests supply otherwise complete series/title/rejection
fields and independently assert missing/null identity gives an unknown result,
zero accepted files and exactly one upstream request, for Sonarr and Radarr.

Valid complete accepted rows still coexist with a complete legacy rejected row.
The included positive control returns the selected file with two read requests;
the corresponding native `type` rejection control returns it with one request.
This validates useful compatibility without granting incomplete rows authority.

Original independent file-identity probes also pass three race repetitions:
same file ID with contradictory path/size remains incomplete, different IDs at
one mapped path cannot establish complete catalog/import evidence, and a valid
single file associated with multiple episodes remains accepted. The original
duplicate-key Radarr catalog probe passes. Existing native malformed/invalid
UTF-8 regressions remain green; strict root decoding and operation-specific
fallback selection are retained.

An additional exploratory assertion rejected reused candidate numeric IDs at
different paths. Source inspection showed the standalone preview's collection
identity is path plus relativePath (`clients/sonarr/client.go:766`), not numeric
ID alone. A direct native control confirmed the same reuse is accepted without
fallback. The experiment was therefore corrected into a native/legacy parity
positive control, which passed three times (one/two requests respectively).
It is not a remaining finding or a claimed stronger numeric-ID guarantee.

## Independent verification

| Reviewer command | Exact outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=180s ./internal/adapters/arr/read ./internal/domain ./internal/ports` | Exit 0; adapter 25.599s, domain 1.704s, ports 1.989s. Includes the new incomplete/complete compatibility tests and retained transport/query/collision coverage. |
| Root focused vet and `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Each standalone Sonarr/Radarr module: `GOWORK=off go test -mod=readonly -race -count=1 -timeout=120s ./...`, vet and module verification | Exit 0; Sonarr 1.695s, Radarr 1.450s; all modules verified. |
| Each standalone Sonarr/Radarr module: `GOWORK=off GOPROXY=off GOSUMDB=off ./check-generation.sh` | Exit 0; committed generated output reproduced offline with installed pinned tool dependencies. |
| Exact-product archive: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run TestReviewer -v ./internal/adapters/arr/read` | Final suite exit 0, 1.456s; exact mixed-row regression, original R1/R2 probes and native/legacy path-identity control pass all repetitions. No race detector report. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; final `guardrail checks passed (fast)`, generation/candidate generation, Vacuum, architecture, formatting and targeted tests. |
| `GOWORK=off ./scripts/check-lint.sh` | Exit 0; nine zero-issue module results and architecture checks passed. |
| `python3 scripts/check_planning.py` | Exit 0; 53 tasks, 60 acceptance cases; local links resolve. |

The correction changes only the handwritten adapter and its tests. Root
go.mod/go.sum and both standalone module trees are unchanged from round two.
The adapter continues to consume the exact public pseudo-versions
`v0.0.0-20260915230044-873e53ef6729`, with no replace or go.work dependency.
The fresh public bootstrap/origin/checksum evidence in round one remains
applicable to those unchanged bytes; a fresh public download was not repeated.
Generated upstream types remain private to standalone modules, and root ports
receive normalized domain observations only. Import-direction checks pass.

## Limits and handoff

Approval concerns this read-adapter correction, not live upstream compatibility,
remote CI, the full aggregate matrix or release/deployment readiness. Full root
tests/vet/cross-builds and `--ci` were not rerun by this review. Compatibility
shapes intentionally require complete identity; further shapes need a reviewed
contract and synthetic evidence. Lookup/history/reprocess remain on the
documented root read-only compatibility transport. No Arr write capability is
enabled and G-01 remains open.

Coordinator should record this receipt's exact commit SHA separately. No real
media, credentials, live service operation, publication or deployment occurred.
