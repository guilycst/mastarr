# Implementation plan and agent lanes

Status: implementation in progress, authorized. Target v0.0.1 includes all
confirmed scope in [Spec 001](../specs/spec-001-media-reconciliation/spec-001-media-reconciliation.md).
Task completion is recorded in [state.json](../execution/state.json), while
[tasks.json](../execution/tasks.json) owns task definitions and dependencies.
The tables and task descriptions here are the readable projection of definitions.

## Dispatch contract

After explicit implementation authorization, coordinator records the user instruction,
current base SHA and phase. Read live Git/agents before choosing work. Default to two
concurrent implementation workers; a third needs independent file ownership and
review capacity recorded first. Reserve coordinator and independent reviewer slots.
Do not create user-visible tasks for internal subagents.

Each dispatch has task ID, base SHA, branch/worktree or shared checkout, owned paths,
acceptance IDs, exact expected checks, and handoff file. A worker is not alone and
must preserve others' changes. Only task-owned files plus that task's handoff are
writable. Agent model/role availability is discovered at dispatch, not frozen here.
Use the reviewer role when available; reviewer must not be the author of the batch.

Coordinator owns shared contracts, root/tools dependencies, migrations/sqlc integration,
process assembly, CI and execution state. D-01 owns SQL implementation until integrated;
subsequent query/migration needs go through coordinator or a bounded reopened D-01
assignment. UI module dependency changes belong to U-00 after C-02 completes. Workers
request contract changes and wait for integration before changing consumer assumptions.
Never assign overlapping writers even if dependency gates otherwise permit work.

Every task below includes its own focused tests in its owned package unless a separate
test path is listed. Coordinator assigns additional shared paths explicitly before
work starts. Broad path ownership is a ceiling, not permission for unrelated edits.

## Lanes and gates

| Lane | Responsibility | First dependency gate |
| --- | --- | --- |
| C | Coordinator-owned evidence, HTTP/domain contracts, modules and final wiring | Implementation authorization |
| D | SQLite/sqlc/migrations, credentials, effective config and recovery | Frozen C-03 domain/bootstrap contracts |
| F | Root-constrained files, discovery, scans and file operations | C-03; persistence where listed |
| X | Download/Arr/catalog adapters and native capability evidence | C-03; X-05 before write adapters |
| W | Plans, desired-state checks, durable actions, approvals, workflows and trash | Storage/ports; adapters for integration |
| U | Public Goshtoso selection, BFF pages and browser verification | Module baseline; API integration before runtime BFF wiring |
| V | Cross-system acceptance, portable containers, independent review and readiness | Integrated API/UI |

Suggested scheduling, not a replacement for task dependencies:

1. C-00 through C-03 serially establish evidence and shared contracts. U-00 can
   follow C-02 independently while coordinator finishes C-03.
2. Start D-01 and F-01. Rotate freed slots through X-01..X-04; these have distinct
   adapters. W-02 can proceed once D-01 lands, while config/discovery develops.
3. X-05 proves native write gates. F-04/F-05 and descriptor work can advance on
   independent ownership. Block only affected work if an upstream guarantee fails.
4. Integrate W-01..W-05, then C-04. No write path is enabled from an unreviewed
   read-adapter happy path.
5. U-01 establishes BFF; U-02/U-03 and U-04 have distinct page ownership after their
   prerequisites. U-05 verifies the full interaction ledger.
6. V-01 and V-02 may run independently, followed by V-03 review and V-04 readiness.

No gate closure can be inferred from a worker narrative. Need tested commit, command
outcomes and reviewer decision. Unsupported capability is visible scope remaining;
do not claim full release done while a required action remains gated.

## Task catalog

An acceptance ID can span several tasks. Early tasks prove only their owned
contribution, such as schema rejection, port behavior or source research. They do
not need a not-yet-built application to pass the final end-to-end scenario. Handoffs
state the proven slice and remaining integration check. V-01/V-02/U-05 close full
scenarios and V-03 verifies every case has complete evidence. A partial contribution
must never mark the whole acceptance case passed.

Spec inputs by lane: C uses all contract documents; D uses configuration and data/recovery;
F uses system/filesystem recovery; X uses connectors/research; W uses HTTP/data/recovery;
U uses dashboard/HTTP; V uses all acceptance cases. Read the linked acceptance case
before implementation. Relevant specification changes require coordinator integration.

### C-00: Freeze upstream evidence and capability gates

Lane C. Dependencies: implementation authorization.

Pin candidate supported upstream versions and public schemas. Document native import race/rejection behavior, qBittorrent payload scope, descriptor export, Jellyfin refresh and subtitle/anime limits. Unknowns stay capability blockers; no live-stack mutation.

- Owned paths: `docs/research/compatibility-matrix.md`, `tests/compatibility/research/`.
- Acceptance contributions: A-16, A-31, A-54, A-55.
- Handoff: `docs/execution/handoffs/C-00.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### C-01: Freeze OpenAPI resource and action schemas

Lane C. Dependencies: C-00.

Define every route, discriminator, problem response, async resource, config-source rule, pagination and no-auth attribution. Include standalone actions and workflow approval gates. Validate schema before generated consumers begin.

- Owned paths: `api/openapi.yaml`, `api/oapi-codegen.yaml`, `ui/oapi-codegen.yaml`, `docs/specs/spec-001-media-reconciliation/http-api.md`.
- Acceptance contributions: A-12, A-15, A-45, A-46, A-56, A-60.
- Handoff: `docs/execution/handoffs/C-01.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### C-02: Establish independent modules and pinned tools

Lane C. Dependencies: C-01.

Create root, nested UI and tools modules; pin toolchain/generators, select and test SQLite/migrate build compatibility. Establish reproducible generation and GOWORK=off CI. No local replace or go.work.

- Owned paths: `go.mod`, `go.sum`, `tools/`, `ui/go.mod`, `ui/go.sum`, `scripts/generate.sh`, `.github/workflows/checks.yml`.
- Acceptance contributions: A-43, A-45.
- Handoff: `docs/execution/handoffs/C-02.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### C-03: Freeze domain ports and bootstrap contracts

Lane C. Dependencies: C-02.

Define typed identities, observations, capabilities, manifests, action lifecycle and errors. Parse env once with env/v11 and generate envdoc. Separate runtime UUIDs from stable config IDs and define worker/config bounds.

- Owned paths: `internal/domain/`, `internal/ports/`, `internal/bootstrap/`, `docs/generated/environment.md`.
- Acceptance contributions: A-09, A-38, A-40, A-45, A-60.
- Handoff: `docs/execution/handoffs/C-03.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### D-01: Create SQLite schema, migrations and sqlc repository

Lane D. Dependencies: C-03.

Implement schema constraints, source ownership, immutable plans, decisions, queue/attempt/effect records, idempotency, trash, coverage and audit. Generate sqlc queries and enforce one active process. Subsequent SQL changes return to this owner/coordinator.

- Owned paths: `internal/storage/`, `migrations/`, `sqlc.yaml`.
- Acceptance contributions: A-32, A-36, A-43, A-45.
- Handoff: `docs/execution/handoffs/D-01.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### D-02: Implement credential encryption and key lifecycle

Lane D. Dependencies: D-01.

Implement standard AEAD envelope, atomic persistent generation, env/file key selection, validation, key-check record, redaction and credential field binding. Prove missing/wrong key fails without regeneration.

- Owned paths: `internal/credentials/`.
- Acceptance contributions: A-40, A-41, A-42.
- Handoff: `docs/execution/handoffs/D-02.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### D-03: Implement effective configuration and source ownership

Lane D. Dependencies: D-02.

Load YAML/secret refs once, validate all ownership collisions and mappings before activation, retire removed config, expose editable/source/revision, handle managed credential updates and authority-bearing revision invalidation.

- Owned paths: `internal/configuration/`.
- Acceptance contributions: A-38, A-39, A-42.
- Handoff: `docs/execution/handoffs/D-03.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### D-04: Implement backup and restore procedure

Lane D. Dependencies: D-03.

Create consistent SQLite/key/descriptor/trash backup instructions and isolated restore check. No automatic live migration downgrade. Document required volumes and incomplete-backup failure behavior.

- Owned paths: `internal/backup/`, `docs/operations/backup-restore.md`.
- Acceptance contributions: A-41, A-43, A-44.
- Handoff: `docs/execution/handoffs/D-04.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### F-01: Implement root-constrained filesystem observations

Lane F. Dependencies: C-03.

Implement bounded enumeration, file identity/hash, no-follow confinement, component-aware mapping and permission/capability observations. Reject root targets, symlinks and special files; verify Linux race behavior.

- Owned paths: `internal/filesystem/observe/`.
- Acceptance contributions: A-19, A-21, A-23.
- Handoff: `docs/execution/handoffs/F-01.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### F-02: Implement discovery groups and media readiness

Lane F. Dependencies: D-03, F-01.

Persist directory observations and grouping for video/subtitles/packs. Preserve orphan unknown provenance and distinguish stability from client completion. Exclude trash/staging and expose ambiguous companions for review.

- Owned paths: `internal/discovery/`.
- Acceptance contributions: A-01, A-02, A-10, A-11.
- Handoff: `docs/execution/handoffs/F-02.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### F-03: Implement durable scan scheduler and coverage

Lane F. Dependencies: F-02.

Schedule per-root intervals, manual coalescing, bounded concurrency/cancellation and restart progress. Record complete/partial/unknown source coverage; do not replace complete evidence with false absence.

- Owned paths: `internal/scanning/`.
- Acceptance contributions: A-02, A-03, A-53.
- Handoff: `docs/execution/handoffs/F-03.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### F-04: Implement copy and hardlink actions

Lane F. Dependencies: D-01, F-01.

Implement exact-plan content checks, exclusive staging, sync and atomic no-replace publication. Hardlink proves same file object and has no copy fallback. Journal/reconcile per-file effects through frozen ports.

- Owned paths: `internal/filesystem/placement/`.
- Acceptance contributions: A-18, A-19, A-20, A-22, A-59.
- Handoff: `docs/execution/handoffs/F-04.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### F-05: Implement move, rename and permanent filesystem delete

Lane F. Dependencies: F-04.

Implement same-filesystem no-replace move/rename and scoped delete with identity checks. Cross-device operations expose explicit copy/verify/delete composition. Never silently broaden directories or linked-client scope.

- Owned paths: `internal/filesystem/organize/`.
- Acceptance contributions: A-21, A-23, A-24, A-59.
- Handoff: `docs/execution/handoffs/F-05.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-01: Implement qBittorrent inventory adapter

Lane X. Dependencies: C-03.

Implement authenticated session handling, version/capability discovery, files/seeding/progress/completion and hash identity. Categories/tags are read hints. Sanitize errors and model uncertain/missing client evidence.

- Owned paths: `internal/adapters/qbittorrent/inventory/`, `tests/fixtures/qbittorrent/`.
- Acceptance contributions: A-04, A-09, A-28.
- Handoff: `docs/execution/handoffs/X-01.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-02: Implement NZBGet inventory adapter

Lane X. Dependencies: C-03.

Implement positional JSON-RPC queue/postprocessing/history, full-array bounds, FinalDir/DestDir mapping and correct drone/history/NZBID correlation. Prove partial coverage and original descriptor availability remain distinct.

- Owned paths: `internal/adapters/nzbget/`, `tests/fixtures/nzbget/`.
- Acceptance contributions: A-05, A-06, A-09.
- Handoff: `docs/execution/handoffs/X-02.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-03: Implement Arr inventory, lookup and native previews

Lane X. Dependencies: C-03.

Read all movies/series/files/options/history; support provider search and exact native preview/reprocessing DTOs per product. Distinguish preview endpoints from execution and retain rejection evidence.

- Owned paths: `internal/adapters/arr/read/`, `tests/fixtures/arr/read/`.
- Acceptance contributions: A-07, A-09, A-10, A-11, A-16.
- Handoff: `docs/execution/handoffs/X-03.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-04: Implement Jellyfin and Seerr observations

Lane X. Dependencies: C-03.

Implement complete item/media/request coverage, provider relationships, native status preservation and independent availability. Seerr connector exposes no writes. Version unsupported results stay explicit.

- Owned paths: `internal/adapters/jellyfin/read/`, `internal/adapters/seerr/`, `tests/fixtures/catalogs/`.
- Acceptance contributions: A-08, A-09, A-54, A-55.
- Handoff: `docs/execution/handoffs/X-04.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-10: Build standalone qBittorrent client module

Lane X. Dependencies: C-03.

Create the independent `clients/qbittorrent` Go module. Keep a narrow,
versioned Mastarr-owned OpenAPI compatibility document for application and
WebUI versions, torrent inventory, properties, files, categories and tags.
Generate typed code with the pinned oapi-codegen tool. Keep cookie login,
request authentication, deadlines and typed upstream errors inside the module.
Read methods only in this slice; mutations remain in the control lane.

- Owned paths: `clients/qbittorrent/`.
- Acceptance contributions: A-04, A-09, A-28, A-45.
- Handoff: `docs/execution/handoffs/X-10.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-11: Build standalone NZBGet client module

Lane X. Dependencies: C-03.

Create the independent `clients/nzbget` Go module with a Mastarr-owned OpenRPC
compatibility document using positional parameters for `version`, `listgroups`,
`listfiles` and `history`. Generate typed wrappers and DTOs with a deterministic
generator in `tools/internal/nzbgetgen`. Keep the JSON-RPC envelope, request IDs, Basic
authentication, deadlines, wire aliases and typed upstream errors inside the
module. Do not assume `rpc.discover` exists.

- Owned paths: `clients/nzbget/`, `tools/internal/nzbgetgen/`.
- Acceptance contributions: A-05, A-06, A-09, A-45.
- Handoff: `docs/execution/handoffs/X-11.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-12: Migrate qBittorrent adapter to standalone client

Lane X. Dependencies: X-10, C-05.

Translate standalone qBittorrent DTOs and typed errors into the existing
Mastarr download-inventory and capability ports without leaking generated types.
Keep the current adapter until the nested module is released and record any
bootstrap blocker instead of adding a local replace directive.

- Owned paths: `internal/adapters/qbittorrent/inventory/`, `tests/fixtures/qbittorrent/`.
- Acceptance contributions: A-04, A-09, A-28.
- Handoff: `docs/execution/handoffs/X-12.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-13: Migrate NZBGet adapter to standalone client

Lane X. Dependencies: X-11, C-05.

Translate standalone NZBGet DTOs and typed errors into the existing Mastarr
download-inventory and capability ports without leaking generated types. Keep
positional correlation and partial coverage semantics, and record any
unpublished-module bootstrap blocker instead of adding a local replace.

- Owned paths: `internal/adapters/nzbget/`, `tests/fixtures/nzbget/`.
- Acceptance contributions: A-05, A-06, A-09.
- Handoff: `docs/execution/handoffs/X-13.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-14: Extend standalone qBittorrent client for control operations

Lane X. Dependencies: X-10, X-05, X-07.

Extend the independent qBittorrent compatibility contract with the control
routes reached by X-07: stop, setLocation, renameFile, renameFolder and
metadata-only delete. Regenerate typed client code with the pinned oapi-codegen
tool. Keep cookie authentication, deadlines and typed upstream errors inside the
module. Cover request encoding, status normalization and synthetic httptest
behavior. Do not import Mastarr root packages or enable runtime writes without
versioned upstream evidence.

- Owned paths: `clients/qbittorrent/`.
- Acceptance contributions: A-28, A-29, A-30, A-31, A-33, A-45.
- Handoff: `docs/execution/handoffs/X-14.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-15: Build standalone Sonarr client module

Lane X. Dependencies: C-05, X-03.

Create the independent `clients/sonarr` Go module with a narrow, Mastarr-owned
OpenAPI compatibility document for the catalog, options, series/episode file
observations and native manual-import preview reads used by Mastarr. Generate
typed code with the pinned oapi-codegen tool. Keep API-key authentication,
deadlines, bounded decoding, typed upstream errors and synthetic httptest
fixtures inside the module. Do not add write methods in this initial slice;
registration/import writes require a later reviewed contract extension.

- Owned paths: `clients/sonarr/`.
- Acceptance contributions: A-07, A-09, A-10, A-45.
- Handoff: `docs/execution/handoffs/X-15.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-16: Build standalone Radarr client module

Lane X. Dependencies: C-05, X-03.

Create the independent `clients/radarr` Go module with a narrow, Mastarr-owned
OpenAPI compatibility document for the catalog, options, movie/file
observations and native manual-import preview reads used by Mastarr. Generate
typed code with the pinned oapi-codegen tool. Keep API-key authentication,
deadlines, bounded decoding, typed upstream errors and synthetic httptest
fixtures inside the module. Do not add write methods in this initial slice;
registration/import writes require a later reviewed contract extension.

- Owned paths: `clients/radarr/`.
- Acceptance contributions: A-07, A-09, A-10, A-45.
- Handoff: `docs/execution/handoffs/X-16.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-17: Build standalone Jellyfin client module

Lane X. Dependencies: C-05, X-04.

Create the independent `clients/jellyfin` Go module with a narrow,
Mastarr-owned OpenAPI compatibility document for system information, libraries,
items/provider IDs and tested refresh scopes. Generate typed code with the
pinned oapi-codegen tool. Keep token authentication, deadlines, bounded
decoding, refresh response observation, typed upstream errors and synthetic
httptest fixtures inside the module. A refresh request is accepted separately
from eventual library availability.

- Owned paths: `clients/jellyfin/`.
- Acceptance contributions: A-08, A-09, A-45, A-55.
- Handoff: `docs/execution/handoffs/X-17.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-18: Build standalone Seerr client module

Lane X. Dependencies: C-05, X-04.

Create the independent `clients/seerr` Go module with a narrow, Mastarr-owned
OpenAPI compatibility document for paginated media and request reads. Generate
typed code with the pinned oapi-codegen tool. Keep configured API-key or bearer
authentication, deadlines, bounded decoding, pagination termination, typed
upstream errors and synthetic httptest fixtures inside the module. Seerr has no
write methods in v0.0.1 and must not assume hidden or discovery-only APIs.

- Owned paths: `clients/seerr/`.
- Acceptance contributions: A-08, A-09, A-45, A-54.
- Handoff: `docs/execution/handoffs/X-18.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-19: Migrate Arr read adapters to standalone clients

Lane X. Dependencies: X-15, X-16, X-03.

Translate Sonarr and Radarr module DTOs and typed errors into the existing Arr
read ports without leaking generated types. Preserve pagination, native status,
instance-scoped identities and unknown coverage. If a module release is not
available, keep the current adapter and record the bootstrap blocker.

- Owned paths: `internal/adapters/arr/read/`, `tests/fixtures/arr/read/`.
- Acceptance contributions: A-07, A-08, A-09, A-10, A-16.
- Handoff: `docs/execution/handoffs/X-19.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-20: Migrate Arr write adapters to standalone clients

Lane X. Dependencies: X-06, X-15, X-16.

Move Arr registration and manual-import transport behind the standalone Sonarr
and Radarr modules while retaining root-owned validation, preview binding,
read-back and capability gates. Do not enable native writes while G-01 remains
unresolved; an unpublished module remains a recorded bootstrap blocker.

- Owned paths: `internal/adapters/arr/write/`, `tests/fixtures/arr/write/`.
- Acceptance contributions: A-12, A-13, A-16, A-17, A-33.
- Handoff: `docs/execution/handoffs/X-20.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-21: Migrate Jellyfin adapters to the standalone client

Lane X. Dependencies: X-09, X-17, X-04.

Translate the standalone Jellyfin DTOs and typed errors into read and refresh
ports. Preserve provider IDs, mapping evidence, accepted refresh responses and
separate eventual availability. No generated types or direct upstream calls
cross the root adapter boundary.

- Owned paths: `internal/adapters/jellyfin/read/`, `internal/adapters/jellyfin/write/`, `tests/fixtures/jellyfin/`.
- Acceptance contributions: A-08, A-09, A-33, A-55.
- Handoff: `docs/execution/handoffs/X-21.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-22: Migrate Seerr adapters to the standalone client

Lane X. Dependencies: X-04, X-18.

Translate Seerr media/request DTOs and typed errors into the read-only request
catalog port. Preserve native status, pagination coverage, instance topology
and unavailable states. Do not add request creation, approval or other Seerr
writes.

- Owned paths: `internal/adapters/seerr/`, `tests/fixtures/seerr/`.
- Acceptance contributions: A-08, A-09, A-54.
- Handoff: `docs/execution/handoffs/X-22.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-05: Prove upstream write safety in disposable fixtures

Lane X. Dependencies: C-00, X-12, X-13, X-03, X-04, F-01.

Exercise pinned native APIs with synthetic files, collision/rejection/race/lost-response scenarios and subtitle/episode mapping. Freeze enabled write capabilities only with evidence. Record unresolved G-01 as blocker, not a weaker hidden guarantee.

- Owned paths: `tests/compatibility/writes/`, `docs/research/write-capabilities.md`.
- Acceptance contributions: A-16, A-17, A-28, A-29, A-31, A-55.
- Handoff: `docs/execution/handoffs/X-05.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-06: Implement Arr upsert and manual import adapters

Lane X. Dependencies: X-05.

Implement explicit patch fields, unmonitored/no-search add and validated ManualImport command. Read back identity, command/history and exact file associations; preserve source and reconcile ambiguous results.

- Owned paths: `internal/adapters/arr/write/`, `tests/fixtures/arr/write/`.
- Acceptance contributions: A-12, A-13, A-16, A-17, A-33.
- Handoff: `docs/execution/handoffs/X-06.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-07: Implement qBittorrent control adapter

Lane X. Dependencies: X-05.

Implement stop/read-back, supported setLocation/renameFile/renameFolder and metadata-only removal. Model whole-torrent versus selected-file impacts, no deleteFiles=true and no automatic re-add/resume.

- Owned paths: `internal/adapters/qbittorrent/control/`.
- Acceptance contributions: A-28, A-29, A-30, A-31, A-33.
- Handoff: `docs/execution/handoffs/X-07.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-08: Implement retained descriptor service

Lane X. Dependencies: D-03, X-01, X-02.

Capture exact original bytes only from verified export or configured mounted directory. Restricted storage, digests/provenance, unavailable states and independent reviewed deletion. No raw descriptor ordinary response/log.

- Owned paths: `internal/descriptors/`.
- Acceptance contributions: A-06, A-42.
- Handoff: `docs/execution/handoffs/X-08.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### X-09: Implement Jellyfin refresh adapter

Lane X. Dependencies: X-05.

Implement only tested refresh scopes and response observation. Report accepted refresh separately from eventual library visibility; no Seerr status mutation.

- Owned paths: `internal/adapters/jellyfin/write/`.
- Acceptance contributions: A-33, A-55.
- Handoff: `docs/execution/handoffs/X-09.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### W-01: Implement tracking aggregation and immutable plans

Lane W. Dependencies: F-03, X-12, X-13, X-03, X-04.

Aggregate per-instance evidence without universal tracked boolean. Create bounded exact manifests, semantic desired predicates, conflicts and immutable revisions bound to meaningful config/source identity.

- Owned paths: `internal/reconciliation/`, `internal/planning/`.
- Acceptance contributions: A-08, A-09, A-10, A-11, A-15, A-19.
- Handoff: `docs/execution/handoffs/W-01.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### W-02: Implement durable action executor and cancellation

Lane W. Dependencies: D-01, C-03.

Implement transaction/CAS work claiming, ordered attempt journal, observe-before-write, uncertain reconciliation, persisted backoff/deadlines and cancellation dispatch boundary. No generic DAG or duplicate outbox.

- Owned paths: `internal/execution/`.
- Acceptance contributions: A-14, A-32, A-33, A-34, A-35, A-36, A-59, A-60.
- Handoff: `docs/execution/handoffs/W-02.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### W-03: Wire typed standalone action handlers

Lane W. Dependencies: W-01, W-02, F-04, F-05, X-06, X-07, X-08, X-09.

Implement independent registration/import/placement/organize/client/descriptor/refresh handlers with shared safety and recovery contracts. Enforce linked-client prerequisites even when called outside a composition.

- Owned paths: `internal/actions/`.
- Acceptance contributions: A-13, A-14, A-17, A-18, A-20, A-24, A-33, A-60.
- Handoff: `docs/execution/handoffs/W-03.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### W-04: Implement ordered workflows and review decisions

Lane W. Dependencies: W-03.

Atomically approve immutable plans and enqueue actions. Compose explicit recipes/gates, generate later import review after registration, preserve failures/effects, and reject blanket approval or steps added to cancelled workflows.

- Owned paths: `internal/workflows/`, `internal/reviews/`.
- Acceptance contributions: A-12, A-15, A-32, A-35, A-37, A-56.
- Handoff: `docs/execution/handoffs/W-04.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### W-05: Implement trash, restore and janitor workflows

Lane W. Dependencies: W-04.

Coordinate exact per-volume manifests, retention, stop prerequisites, claim against restore, expiry authority and metadata-only torrent removal. Hold uncertain entries, preserve partial state, leave restore stopped.

- Owned paths: `internal/trash/`.
- Acceptance contributions: A-25, A-26, A-27, A-29, A-30, A-57.
- Handoff: `docs/execution/handoffs/W-05.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### C-04: Integrate generated REST transport and process startup

Lane C. Dependencies: D-04, F-03, X-08, W-05.

Wire strict generated server to services for every documented resource, validation/problem responses/ETags/idempotency, startup locking/migrations/key readiness and graceful shutdown. Coordinator integrates shared registrations and schema drift.

- Owned paths: `internal/transport/`, `cmd/mastarr/`.
- Acceptance contributions: A-38, A-39, A-42, A-46, A-47, A-56.
- Handoff: `docs/execution/handoffs/C-04.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### C-05: Extend generation and CI across standalone client modules

Lane C. Dependencies: X-10, X-11.

Wire the deterministic NZBGet OpenRPC generator owned by X-11 into generation.
Make formatting, tests, vet, lint, module verification and architecture checks
discover every nested client module with `GOWORK=off`. Generated output stays
committed and reproducible without `go.work` or local replace directives.

- Owned paths: `scripts/generate.sh`, `scripts/check-guardrails.sh`, `scripts/check-lint.sh`, `.github/workflows/checks.yml`.
- Acceptance contributions: A-43, A-45.
- Handoff: `docs/execution/handoffs/C-05.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### C-06: Extend generation and CI across every client module

Lane C. Dependencies: C-05, X-15, X-16, X-17 and X-18.

Add Sonarr, Radarr, Jellyfin and Seerr to deterministic generation, lint,
architecture, module verification, cross-build and CI matrices. Generation must
run from each module's committed contract and leave generated output unchanged.
Every module is checked independently with `GOWORK=off`; no local `go.work` or
replace directive is allowed.

- Owned paths: `scripts/generate.sh`, `scripts/check-guardrails.sh`, `scripts/check-lint.sh`, `.github/workflows/checks.yml`.
- Acceptance contributions: A-43, A-45.
- Handoff: `docs/execution/handoffs/C-06.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### U-00: Select public Goshtoso components and compatible runtime

Lane U. Dependencies: C-02.

Recheck latest release, choose compatible App Shell/runtime pair and explicit pins, record reuse/compose/gap ledger. Freeze public assets/templ conventions before dashboard markup; request tools-version edits from coordinator.

- Owned paths: `docs/research/ui-components.md`, `ui/go.mod`, `ui/go.sum`.
- Acceptance contributions: A-45, A-50, A-51.
- Handoff: `docs/execution/handoffs/U-00.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### U-01: Build BFF shell and generated-client boundary

Lane U. Dependencies: U-00, C-03, C-04.

Generate HTTP client from frozen OpenAPI, add server-rendered shell/routes/errors, bootstrap origin and API URL, private-safe initial metadata and actual static preview asset. Prove no DB/upstream/root-internal imports.

- Owned paths: `ui/internal/client/`, `ui/internal/shell/`, `ui/internal/config/`, `ui/cmd/`, `ui/internal/assets/`.
- Acceptance contributions: A-45, A-46, A-47, A-51.
- Handoff: `docs/execution/handoffs/U-01.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### U-02: Build discovery, inventory and matching views

Lane U. Dependencies: U-01.

Render paginated discoveries/catalogs/detail with provenance, unknown states, descriptors, seeding, per-instance tracking, editable identity/episode/subtitle associations. Preserve deep-link identity and input state.

- Owned paths: `ui/internal/inventory/`.
- Acceptance contributions: A-01, A-07, A-08, A-09, A-10, A-11, A-49.
- Handoff: `docs/execution/handoffs/U-02.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### U-03: Build exact review and workflow controls

Lane U. Dependencies: U-02, W-04.

Render both approval phases, standalone/composed effects, monitoring opt-in, copy fallback review, retries and cancellation. Preserve idempotency keys across failed submissions; show late/partial effects honestly.

- Owned paths: `ui/internal/review/`, `ui/internal/workflows/`.
- Acceptance contributions: A-12, A-13, A-15, A-20, A-35, A-37, A-48, A-49.
- Handoff: `docs/execution/handoffs/U-03.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### U-04: Build trash and configuration views

Lane U. Dependencies: U-01, W-05.

Implement UI trash-then-purge, restore/conflict/expiry/client-state views and multi-instance config CRUD with immutable YAML provenance. Credentials replace-only and no plaintext rendering.

- Owned paths: `ui/internal/trash/`, `ui/internal/settings/`.
- Acceptance contributions: A-25, A-26, A-27, A-30, A-38, A-39, A-42.
- Handoff: `docs/execution/handoffs/U-04.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### U-05: Verify browser interaction and accessibility

Lane U. Dependencies: U-03, U-04.

Run consequential-action allowed/denied/repeated ledger, direct forged requests, real transport failures, keyboard/focus, narrow/wide themes and initial metadata/assets. Record actual effect counts and screenshots with synthetic data.

- Owned paths: `ui/tests/browser/`, `docs/verification/ui-ledger.md`.
- Acceptance contributions: A-26, A-42, A-47, A-48, A-49, A-50, A-51.
- Handoff: `docs/execution/handoffs/U-05.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### U-06: Compose production BFF routes

Lane U. Dependencies: U-03 and U-04.

Compose the HTTP-only inventory, review, workflow, trash and settings readers
behind the running UI BFF. Keep the global GET/HEAD boundary, configured-origin
metadata, sanitized readiness and transport errors, exact deep-link routing and
static asset policy. Do not add direct writes, authentication or root-module
imports. Add synthetic end-to-end HTTP coverage so U-05 can rerun against real
routes when a browser surface is available.

- Owned paths: `ui/cmd/mastarr/`, `ui/internal/router/`.
- Acceptance contributions: A-47, A-48, A-49, A-50, A-51.
- Handoff: `docs/execution/handoffs/U-06.md`.
- Exit gate: focused checks pass, exact route/effect evidence recorded, independent review clears findings, coordinator integrates.

### V-01: Run cross-system fault and recovery acceptance

Lane V. Dependencies: C-04, U-05.

Run end-to-end synthetic Arr/library-only/cleanup flows with crash injection at each durable boundary, outage and partial pack effects. Verify same desired state across restarts/new keys and no data-loss scope expansion.

- Owned paths: `tests/integration/`, `docs/verification/integration-evidence.md`.
- Acceptance contributions: A-06, A-12, A-16, A-17, A-24, A-27, A-29, A-32, A-33, A-34, A-35, A-37, A-44, A-59.
- Handoff: `docs/execution/handoffs/V-01.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### V-02: Package portable containers and measure operating limits

Lane V. Dependencies: C-04, U-05.

Build nonroot amd64/arm64 API/BFF OCI targets, generic/Kubernetes examples, key/DB/volume documentation and smoke/restore tests. Measure bounded synthetic inventory behavior; no live deploy or image publication implied.

- Owned paths: `deploy/`, `build/`, `docs/operations/deployment.md`, `tests/containers/`.
- Acceptance contributions: A-43, A-44, A-45, A-52, A-53.
- Handoff: `docs/execution/handoffs/V-02.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

### V-03: Independently review completed release scope

Lane V. Dependencies: V-01, V-02.

Assign reviewer who authored none of reviewed implementation batch. Audit invariant-to-evidence coverage, contracts, no-auth boundaries, upstream uncertainty, filesystem loss risks, packaging and outstanding gates. Findings reopen owning tasks.

- Owned paths: `docs/verification/review.md`.
- Acceptance contributions: A-58.
- Handoff: `docs/execution/handoffs/V-03.md`.
- Exit gate: reviewer report records exact findings and evidence; coordinator resolves ownership and integrates the report. No recursive review of the review task is required.

### V-04: Prepare release readiness and user acceptance report

Lane V. Dependencies: V-03.

Coordinator gathers exact commit/CI/review evidence and known limitations, reruns only invalidated checks, and prepares v0.0.1 readiness. Do not tag, publish images, release or deploy without release-specific authorization.

- Owned paths: `docs/verification/release-readiness.md`, `CHANGELOG.md`.
- Acceptance contributions: A-45, A-58.
- Handoff: `docs/execution/handoffs/V-04.md`.
- Exit gate: focused checks pass, exact commit/effect evidence recorded, independent review clears findings, coordinator integrates.

## Common checks and handoff

Run checks appropriate to changed modules/packages. Each module must build/test
independently with GOWORK=off. Root concurrency/storage changes require race checks
on supported environments; filesystem/adapter behavior requires the relevant native
fixture tests. Run generators through the tools module and require no unexplained
generated diff. The planning validator also remains green.

Do not run a growing full suite repeatedly without a new change or unresolved risk.
Record exactly what ran and what did not. A mock-only test cannot close a native
capability gate. Browser evidence cannot replace API forged-request tests.

Use [handoff template](../execution/handoffs/TEMPLATE.md). Coordinator updates state
at dispatch, checkpoint, review, integration, blocker and context boundary. Completed
state requires exact tested/integrated commits, commands, evidence and independent
review. If interrupted, leave changed paths and next action in the handoff before
stopping. Never mark done to make a status table look tidy.

## Contract change and review protocol

1. Worker reports mismatched assumption or missing field with affected task IDs.
2. Coordinator records blocker and owns contract/schema/dependency edit.
3. Independent review checks impact, regenerates affected clients/queries and updates
   acceptance cases. Freeze a new base before consumers continue.
4. Reopen only tasks invalidated by that change; preserve proven independent work.

Critical findings cover unauthorized scope, overwrite/data loss, lost/duplicated
writes, false availability, key loss, and untrusted identity. These block integration.
Reviewers report exact reproduction, affected contract and needed correction. Authors
fix within assigned ownership; reviewers recheck the fix and relevant regression.

Release readiness is separate from permission to tag/release/publish images/deploy.
Live-stack testing also needs explicit scope and runtime coordinates kept private.
Basic auth/OIDC remains a separate v0.0.2 design, not a v0.0.1 completion criterion.
