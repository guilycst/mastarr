# X-08 correction round one — retained descriptor safety

## Assignment

- Task ID and title: X-08 correction round one — mounted pathname identity and restart-safe deletion journal.
- Owner/agent and independent reviewer: `/root/x05_implementer`; independent review by `/root/x05_reviewer`.
- Base commit: `53d0f33062494931a5da6ed876c08b5b4d800a75`.
- Branch/worktree or shared-checkout ownership: shared `main` checkout in the public Mastarr repository.
- Owned files and generated outputs: `internal/descriptors/`; this correction handoff; no generated output.
- Prior review receipt: `docs/execution/handoffs/X-08-review-round1.md`, receipt commit `cedb49af4ee4dfb99bba0d4abacfff46aca1f0a3`.
- Required acceptance IDs and exact planned commands: A-06 and A-42; focused descriptor tests, race, vet, module verification, generation/API/Vacuum/architecture/lint/planning/guardrail checks, and Linux/Darwin CGO-free compilation.

## Contract and work

- Linked specification sections and invariants: `docs/specs/spec-001-media-reconciliation/data-and-recovery.md` descriptor persistence and filesystem safety; `docs/specs/spec-001-media-reconciliation/http-api.md` descriptor metadata/content and exact deletion; I-13 private-data boundary. Missing or changed evidence remains incomplete, and an irreversible filesystem effect must have a durable reconciliation path.
- Intended result and capability limits: keep mounted capture bound to the pathname selected by review, and make an acknowledged exact descriptor deletion recoverable after a database failure following unlink. The service remains synthetic/read-only with respect to upstream systems; no migration, HTTP wiring, live service, credential, or media data was added.
- Changes completed:
  - `CaptureMounted` performs a constrained reopen after the opened-file stability check. It requires the configured root-relative pathname to name the same file object as the reviewed descriptor and rechecks bounded digest/size before `capture`; a pathname replacement, disappearance, symlink, or changed bytes returns `ErrDescriptorChanged` and creates no descriptor row.
  - `Delete` commits a redacted append-only `descriptor.delete.intent` audit event before unlink. The intent records descriptor type, digest, and the Unix device/inode identity of the verified private object without recording a pathname or content bytes. The existing terminal `descriptor.delete` event is appended only after the filesystem effect and metadata transition; audit immutability is preserved.
  - A fresh service recognizes a pending intent. If the exact object is absent, it completes the metadata/audit transition without another filesystem operation. If an object is present, its persisted device/inode identity must match before digest verification or removal; a same-content replacement is rejected and preserved. The path remains root-confined and no-replace.
  - Unsupported platforms without a serializable reviewed object identity continue to fail closed for physical deletion. Unavailable descriptors use the same durable intent/terminal sequence without inventing object identity.
- Shared contract changes requested from coordinator: none. The existing append-only `audit_events` schema is sufficient; no migration or generated storage change is required.
- Changed paths and tested product commit: `internal/descriptors/descriptors.go`, `internal/descriptors/descriptors_test.go`, `internal/descriptors/root_unix.go`, and `internal/descriptors/root_other.go` at `b766ea8712fd18f6b02dd6c8aa4c304745304298`.

## Verification

| Command or scenario | Commit / fixture version | Result / exit status | Evidence path |
| --- | --- | --- | --- |
| `GOWORK=off go test ./internal/descriptors -count=1` | `b766ea87` / temporary SQLite and mounted roots | Passed, exit 0 | `internal/descriptors/descriptors_test.go` |
| `GOWORK=off go test -race ./internal/descriptors -run 'TestCaptureMountedRejectsAtomicPathReplacement\|TestDeleteIntentReconcilesAfterDatabaseFailure\|TestDeleteFailsClosedWhenSelectedObjectIsReplaced' -count=3` | `b766ea87` / rename replacement, database-close seam, same-content replacement | Passed, exit 0 | `internal/descriptors/descriptors_test.go` |
| `GOWORK=off go vet ./internal/descriptors` | `b766ea87` | Passed, exit 0 | package vet output |
| `GOWORK=off go mod verify` | `b766ea87` | Passed, exit 0; all modules verified | module verification output |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./internal/descriptors` and arm64 equivalent | `b766ea87` | Both passed, exit 0 | cross-build output |
| `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./internal/descriptors` and arm64 equivalent | `b766ea87` | Both passed, exit 0 | cross-build output |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | exact clean product `b766ea8712fd18f6b02dd6c8aa4c304745304298` | Passed, exit 0; generation, staged generation, OpenAPI/Vacuum, architecture, lint, root/UI/tools/client tests and race, vet, module verification, and Linux cross-build matrix passed; final marker `guardrail checks passed (ci)` | aggregate guardrail output |
| `TestCaptureMountedRejectsAtomicPathReplacement` | `b766ea87` / synthetic `afterSourceRead` atomically renames a different regular file over the selected path, repeated four times | Every capture returned `ErrDescriptorChanged`; no descriptor row was created; replacement bytes remained | `internal/descriptors/descriptors_test.go` |
| `TestDeleteIntentReconcilesAfterDatabaseFailure` | `b766ea87` / synthetic post-unlink hook closes the first SQLite service | First call returned `ErrDeleteUncertain`; object was absent; one pending redacted intent survived restart; a same-content replacement was rejected by object identity; after removal, a fresh service completed deletion with one terminal audit event | `internal/descriptors/descriptors_test.go` |
| Versioned pre-commit hook during product commit | `b766ea87` | Passed; fast generation, staged contract, API, architecture, and targeted checks passed | `.githooks/pre-commit` output |

No check was skipped or blocked. All fixtures use disposable temporary roots, synthetic bytes, and synthetic SQLite rows. No credentials, private coordinates, live services, tracker data, raw descriptor bytes, or private storage paths were added to the handoff or audit metadata.

## Review and integration

- Reviewer identity/role and reviewed commit: pending independent `/root/x05_reviewer` review of product `b766ea8712fd18f6b02dd6c8aa4c304745304298`.
- Findings addressed: R1 mounted capture now rechecks the selected pathname identity and bytes after the source-read boundary. R2 deletion now has append-only pre-unlink intent, persisted object identity, and fresh-service reconciliation after journal failure. The deterministic regressions cover both findings and a same-content replacement boundary.
- Fix commit and regression evidence: product `b766ea8712fd18f6b02dd6c8aa4c304745304298`; tests and commands above.
- Final reviewer decision: pending.
- Integrated commit, recorded by coordinator: pending; coordinator owns `docs/execution/state.json`.

## Resume checkpoint

- Current state and outstanding uncertainty: product is complete at `b766ea8712fd18f6b02dd6c8aa4c304745304298`; the correction handoff is the remaining owned commit. Independent correction review remains open. Unsupported non-Unix physical deletion remains deliberately fail-closed pending a platform-specific identity primitive.
- Active process or agent ownership, sanitized: no active descriptor test process; shared `main` may advance under coordinator ownership.
- Next safe action: commit this handoff, then run independent correction review against the exact product SHA.
- Blocker and exact input/evidence needed: no implementation blocker. Coordinator must record product and handoff SHAs in `docs/execution/state.json` after independent review.
- No conflicting writes or unknown files removed: only `internal/descriptors/` and this correction handoff were changed; no state, review receipt, client, adapter, migration, or unrelated files were staged or removed.
