# D-04 correction round four independent review

Decision: **approved**.

No P1 or P2 findings remain in the reviewed correction. Create and Restore now
detect final-read content drift, move the same operation-owned directory back
to its private stage name with a no-replace native operation, return a certain
incomplete result, and leave the requested destination absent. Rollback refuses
an occupied stage name and a foreign destination without moving or replacing
either unrelated object.

## Review identity and scope

- Reviewer: `/root/x05_reviewer`, independent of correction author
  `/root/x05_implementer`.
- Exact correction product:
  `036288114bcf303f98e0ee02e54ddce18384e504`; tree
  `60cf6b592922ff3ba1bf795fd98db074a44bcc33`.
- Exact correction handoff:
  `49b7873dc851948099580094b9b0017b1646a879`; tree
  `1e39ba86a271d615fcfad921deac1711b11cd352`.
- Exact review dispatch checkpoint:
  `32a0172b3bad0e089a31d743b67019d18addc8d1`; tree
  `d0d0af5140bb0977b3fd050a8aaa5c8f139400af`.
- Prior independent receipt:
  `766903206caba0c59e4febed252b2a19e41ccd5b`.
- Correction-scope diff SHA-256:
  `7778daf8f7168e8bc0f88b0bbf5506f2c2d110a236ce14a75e9428f0b411dd04`.
- Reviewed paths: `internal/backup/`,
  `docs/operations/backup-restore.md`, the correction handoff, and the prior
  receipt. Acceptance contributions: A-41, A-43 and A-44.
- Review receipt checkpoint: the commit containing this file; its exact SHA is
  reported after commit because a commit cannot contain its own SHA.

Review ran in a clean detached worktree at the exact dispatch checkpoint.
`git diff --exit-code 49b7873 32a0172 -- internal/backup
docs/operations/backup-restore.md` passed. Temporary independent probes were
removed after execution. Product, execution state, generated output and the
shared checkout were not edited.

## Boundary verification

The correction adds an explicit seam after the final private read. Both public
entrypoints preserve their complete final verification and destination
read-back. When the read-back detects drift, rollback:

- requires the visible destination to match the retained stage descriptor;
- refuses an occupied private-stage name;
- uses `RENAME_NOREPLACE` on Linux or `RENAME_EXCL` on macOS relative to an
  opened no-follow parent descriptor;
- rechecks the returned private-stage identity and destination absence; and
- syncs the containing parent before reporting a proven rollback.

Independent probes did not reuse the new mutation seam. They used the existing
bounded `progressObserver.CopyChunk` hook to mutate immediately after the final
read-back copied its last bytes:

1. Create gained an unexpected child. It returned `ErrIncomplete` without
   `ErrPublicationUncertain`, the requested destination remained absent, and
   the changed operation-owned tree returned to its private stage name.
2. Restore's staged manifest changed `created_at` by 24 hours. It returned the
   same certain incomplete result, returned no successful report, and left the
   requested target absent.

Both probes passed in five race-detector repetitions. Three direct rollback
cases also passed in five repetitions:

- normal rollback restored the exact descriptor-bound directory to its stage
  name and removed the destination name;
- an occupied stage name returned `ErrDestinationExists`, preserving both the
  operation-owned destination and the occupying directory; and
- a foreign destination returned `ErrStagingChanged`, preserving the foreign
  destination and the operation-owned directory at its alternate name.

The package retains conservative uncertainty when ownership, no-replace rename
or parent-sync proof fails. It performs no pathname deletion or replacement in
those cases. That behavior is consistent with the documented reconciliation
boundary and does not weaken the proven rollback path.

## Prior finding disposition

- **Last-read Create child mutation: closed.** The requested destination is
  absent after a certain rollback.
- **Last-read Restore manifest mutation: closed.** The requested target is
  absent and no successful report/content disagreement is returned.
- **Rollback identity and no-replace behavior: closed.** Normal, collision and
  foreign-identity probes preserve the expected objects.
- **Pre-final-check mutations, destination read-back, root substitution,
  cleanup deletion, source-manifest snapshot, no-replace publication, schema,
  overlap, cancellation and aggregate bounds:** no regression found.

The quiescence flag remains an operator-enforced source boundary. Unsupported
platforms still fail closed with `ErrPublicationUnsupported`; this approval
does not broaden the documented Linux/macOS publication support.

## Independent checks

All Go checks used `GOWORK=off`; the aggregate check additionally used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact product/handoff/checkpoint identity, ancestry, trees, correction scope and unchanged post-handoff product | Passed. |
| Independent last-read Create/Restore probes plus direct rollback success/collision/foreign-identity probes, `go test -race -count=5` | Passed; package 20.438s. |
| Product last-read rollback regressions, `go test -race -count=10` | Passed; package 39.289s. |
| `go test -mod=readonly -count=1 -timeout=300s ./internal/backup` | Passed; package 4.879s. |
| `go test -race -mod=readonly -count=1 -timeout=420s ./internal/backup` | Passed; package 55.832s. |
| `go vet -mod=readonly ./internal/backup` | Passed. |
| Offline `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed; generation and staged generation, Vacuum with zero warnings/errors, API, architecture, lint, all module tests/race/vet/mod verification and cross-build matrix completed; backup race took 62.727s, root storage race took 198.567s, and the command ended with `guardrail checks passed (ci)`. |
| Live services, credentials, private coordinates, media mutation or deployment | Not used. |

## Acceptance and integration recommendation

- A-41 and A-43 retain their previously accepted recovery, schema and lock
  evidence.
- A-44 is accepted for D-04. Tampered or drifted publication now either rolls
  back with a proven absent destination or remains explicitly uncertain when
  rollback safety cannot be proven.

D-04 is approved for coordinator integration at the exact product and handoff
SHAs above. This receipt does not authorize release, deployment, live
backup/restore, or any live media operation.
