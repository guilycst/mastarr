# D-04 correction round three independent review

Decision: **changes_requested**.

The correction closes the prior false-success cases: mutations injected by the
existing publication seam are rejected by a new complete final check, and a
post-publication read-back prevents Create or Restore from returning success
for changed content. One P1 data-integrity requirement remains open. A child or
manifest can change after the final verification/read-back has consumed its
bytes but before native rename. The post-check detects this only after the
changed tree is visible at the requested destination and returns
`ErrPublicationUncertain`; it does not satisfy the required reject-and-publish-
nothing boundary.

## Review identity and scope

- Reviewer: `/root/x05_reviewer`, independent of correction author
  `/root/x05_implementer`.
- Exact correction product:
  `91e63363c9a2c15bce8964d19508a1888a97ba51`; tree
  `a9bc7951943ca6c6673c303999864b739c2b53d8`.
- Exact correction handoff:
  `dc743ca02c9925985294db26c0c5c5ff42805d40`; tree
  `99689054988e09f93af15712a5525b1233f0575c`.
- Exact review dispatch checkpoint:
  `4c649fd397d4e0bc1f882e7082a9b7a33762fcde`; tree
  `c76747bac87b281aa0ba1c562cfadc7c188130fe`.
- Prior independent receipt:
  `e5e51d9d7476c88b54732a0f4a5b42ccd8e8754d`.
- Correction-scope diff SHA-256:
  `9971c6b5023c3c623a94977520f44a94d4fb5e313b2020561d1df4e03cbf71d1`.
- Reviewed paths: `internal/backup/`,
  `docs/operations/backup-restore.md`, the correction handoff, and the prior
  receipt. Acceptance contributions: A-41, A-43 and A-44.
- Review receipt checkpoint: the commit containing this file; its exact SHA is
  reported after commit because a commit cannot contain its own SHA.

Review ran in a clean detached worktree at the exact dispatch checkpoint.
`git diff --exit-code dc743ca 4c649fd -- internal/backup
docs/operations/backup-restore.md` passed. Temporary adversarial probes were
removed after execution. Product, execution state, generated output and the
shared checkout were not edited.

## Finding

### P1: final child/read-back validation is still separated from publication

`publishDirectoryBoundContext` executes `stage.finalCheck`, verifies only the
stage root identity, and then calls the native name-based publication
(`backup.go:1435-1444`). The post-check happens after the rename has made the
destination visible (`backup.go:1446-1460`). Create's final check reruns Verify
and then compares only the manifest (`backup.go:446-454`). Restore similarly
finishes its final Verify and last staged-manifest comparison before returning
to the separate rename call (`backup.go:551-569`).

Two independent deterministic probes used the package's existing bounded
`progressObserver.CopyChunk` hook. Each probe first counted the exact copy
callbacks in a clean Verify, then mutated during the callback that had just
copied the final read-back bytes:

1. Public Create added an unexpected regular child after its final Verify and
   final manifest read had consumed their bytes, but before native rename.
   Create returned `ErrPublicationUncertain`, and the requested destination
   existed with the unverified child.
2. Public Restore changed the staged `manifest.json` `created_at` by 24 hours
   after the final staged-manifest read had consumed the original bytes, but
   before native rename. Restore returned `ErrPublicationUncertain`, and the
   requested target existed with the altered manifest.

Both behaviors reproduced in all five race-detector repetitions. The
post-publication checks correctly prevented a successful result and Restore
returned no stale successful report. They cannot undo the native rename, so
the final path still exposes content that did not match the last verified
snapshot. This is precisely a mutation after final Verify/read-back and before
publication. It violates the assigned requirement that Create reject the
added/changed child and publish nothing, and the broader child-content binding
required for Restore.

The new product regressions do not exercise this window. They mutate inside
`beforeStagingPublication` at `backup.go:1431`; the new `finalCheck` starts
afterward and therefore sees those mutations before rename.

Required change: make the verified child snapshot and native publication one
fail-closed boundary, or otherwise prevent child modification after the final
read-back until the no-replace publication completes. Detection after rename
remains valuable uncertainty handling but is not a substitute for the required
no-publication result. Add deterministic Create and Restore regressions that
mutate on the last final-check read, assert the operation returns an error, and
assert the requested destination remains absent.

Disposition: `current_blocker`.

## Prior finding disposition

- **Mutations injected before the final check: closed.** The product's public
  Create child-addition and Restore staged-manifest regressions pass repeatedly
  and leave their destinations absent.
- **Successful report/content disagreement: closed.** Complete destination
  read-back makes a post-check mismatch uncertain, and Restore does not return
  a stale successful report. The changed destination remains visible, which is
  the open P1 above.
- **Stage root substitution and cleanup deletion: closed.** Held root identity
  checks remain, and there is no pathname cleanup that can delete a replacement
  stage.
- **Restore source-manifest snapshot: closed.** The immutable source bytes,
  digest checks and same-inode mutation regression remain intact.
- **No-replace, schema, overlap, cancellation, aggregate bounds and private
  verification behavior:** no regression found in the reviewed correction or
  official suites.

The quiescence flag remains an operator-enforced source boundary. It does not
make the operation's private staging tree immutable.

## Independent checks

All Go checks used `GOWORK=off`; the aggregate check additionally used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact product/handoff/checkpoint identity, ancestry, trees, correction scope and unchanged post-handoff product | Passed. |
| Create last-read child-addition probe plus Restore last-read staged-manifest mutation probe, `go test -race -count=5` | Both returned `ErrPublicationUncertain` with the changed requested destination visible in every repetition; probe suite intentionally failed. |
| Product Create/Restore correction regressions, `go test -race -count=5` | Passed; package 19.688s. |
| `go test -mod=readonly -count=1 -timeout=300s ./internal/backup` | Passed; package 4.382s. |
| `go test -race -mod=readonly -count=1 -timeout=420s ./internal/backup` | Passed; package 52.279s. |
| `go vet -mod=readonly ./internal/backup` | Passed. |
| Offline `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed; generation and staged generation, Vacuum with zero warnings/errors, API, architecture, lint, all module tests/race/vet/mod verification and cross-build matrix completed; root storage race took 198.400s; final `guardrail checks passed (ci)`. |
| Live services, credentials, private coordinates, media mutation or deployment | Not used. |

## Acceptance and integration recommendation

- A-41 and A-43 retain the functional evidence accepted in prior reviews; no
  schema, key-verification or lock regression was found.
- A-44 is not accepted. Pre-publication child and manifest mutation can still
  become visible at the requested final path.

Do not close or integrate D-04 as approved from this correction. Route a
focused correction for the remaining final-check/publication boundary,
preserve the now-passing pre-final-check mutations, destination read-back,
root-substitution, cleanup, source-manifest, no-replace, cancellation and
resource-bound behavior, and submit exact new product and handoff SHAs for
independent review. This receipt does not authorize release, deployment, live
backup/restore, or any live media operation.
