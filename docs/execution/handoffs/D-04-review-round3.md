# D-04 correction round two independent review

Decision: **changes_requested**.

The correction closes pathname cleanup of a replacement stage, rejects the
tested whole-directory substitution, and binds the restore source manifest
snapshot through copy and private verification. One P1 data-integrity finding
remains: the held staging-directory identity does not bind its children after
the final full verification. Both Create and Restore can therefore publish a
tree changed after verification and report success.

## Review identity and scope

- Reviewer: `/root/x05_reviewer`, independent of correction author
  `/root/x05_implementer`.
- Exact correction product:
  `6adb5a5cf2ce7d9effd5d4ba6c2a8737d9e28b3a`; tree
  `d51fb59760944ccecaa7e3712d11980a44112e39`.
- Exact correction handoff:
  `fe0b2b3e7299e5ea7dac8be6bec9d819d81fa115`; tree
  `292646e93d5a6d38256be4d8e174965920d85336`.
- Exact review dispatch checkpoint:
  `032c08ff1827f4690b823e8fc10fcace98710768`; tree
  `b725c6b49eedbb1c1224a962b6fd9718a1d0a81e`.
- Prior independent receipt:
  `ec768b048cb46121c8bd09ea87d79dc79de2308e`.
- Correction-scope diff SHA-256:
  `b5e9adf4a6d24e9e70e7cee9b115007eb007d05576f940244dec1e8694b18de7`.
- Reviewed paths: `internal/backup/`,
  `docs/operations/backup-restore.md`, the correction handoff, and the prior
  receipt. Acceptance contributions: A-41, A-43 and A-44.
- Review receipt checkpoint: the commit containing this file; its exact SHA is
  reported after commit because a commit cannot contain its own SHA.

Review ran in a clean detached worktree at the exact dispatch checkpoint.
`git diff --exit-code fe0b2b3 032c08f -- internal/backup
docs/operations/backup-restore.md` passed. Temporary adversarial probes were
removed after execution. Product, execution state, generated output and the
shared checkout were not edited.

## Finding

### P1: verified staging contents can change before publication without detection

Create completes its full private `Verify` at `backup.go:430-434`; Restore
completes `Verify`, the second source-snapshot comparison, and the exact staged
manifest read-back at `backup.go:511-525`. Both then call
`publishDirectoryBound`. That function invokes the package test seam and only
rechecks the staging directory's device/inode identity afterward
(`backup.go:1342-1356`). The platform implementations repeat the same root
directory identity check before their name-based rename. No artifact, child
set, manifest bytes, or verified report is checked after the seam or bound to
the native publication.

Two deterministic package-local probes demonstrate the consequence:

1. During public `Create`, `beforeStagingPublication` added an unexpected
   regular file inside the retained stage without replacing the stage
   directory. `Create` returned success in all five race-detector repetitions.
   The published destination then failed `Verify` with `ErrInvalidManifest`.
2. During public `Restore`, the same seam rewrote only the staged
   `manifest.json` `created_at` value after Restore had verified and read back
   that manifest. The stage directory inode remained unchanged. Restore
   returned success in all five race-detector repetitions, and the published
   target itself passed `Verify`; however, `RestoreReport.Manifest.CreatedAt`
   described the earlier manifest while the published target contained a
   value exactly 24 hours later.

The first outcome publishes an invalid backup while claiming success. The
second publishes valid but different recovery evidence from the successful
report. This contradicts the documented guarantees that only the fully
verified tree is published and that Restore returns the manifest proven by
staged verification (`docs/operations/backup-restore.md:52,106-107`). A held
root descriptor proves which directory moved; it does not prove that the
directory still contains the verified children.

Required change: bind the complete verified staged contents through the
publication boundary, or otherwise prevent and fail closed on child mutation
between final verification/read-back and publication. The final successful
Restore report must be derived from the exact manifest content actually
published. Add public Create and Restore regressions that mutate a child while
preserving the stage root identity and assert no successful publication or
report/content disagreement.

Disposition: `current_blocker`.

## Prior finding disposition

- **Whole-directory substitution and cleanup deletion: closed for the tested
  paths.** The stage descriptor and `SameFile` checks reject the injected root
  pathname exchange. Failed and uncertain paths no longer use pathname
  `RemoveAll`, so the operation does not delete another actor's replacement
  stage. Post-rename mismatch remains an explicit uncertain effect.
- **Restore source-manifest snapshot: closed before the final publication
  window.** Restore writes the exact preflight bytes, verifies the source
  snapshot twice, checks the staged bytes/digest, and returns the staged parsed
  value. The same-inode multi-chunk source mutation fixture rejects publication
  repeatedly. The P1 above shows that the staged copy can still change after
  those checks.
- **No-replace, schema, overlap, cancellation, aggregate bounds and prior
  private verification behavior:** no regression found in the reviewed
  correction or official suites.

The quiescence flag remains an operator-enforced source boundary. It does not
make the operation's private stage immutable and does not resolve this finding.

## Independent checks

All Go checks used `GOWORK=off`; the aggregate check additionally used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact product/handoff/checkpoint identity, ancestry, trees, correction scope and unchanged post-handoff product | Passed. |
| Public Create post-Verify child-addition probe, `go test -race -count=5` | Unsafe success reproduced in every repetition; each published destination subsequently failed `Verify` with `ErrInvalidManifest`. Probe suite intentionally failed. |
| Public Restore post-Verify manifest-rewrite probe, `go test -race -count=5` | Report/published-manifest disagreement reproduced in every repetition while the published target still passed `Verify`. Probe suite intentionally failed. |
| `GOWORK=off go test -count=1 ./internal/backup` | Passed; package 3.755s. |
| `GOWORK=off go test -race -count=1 ./internal/backup` | Passed; package 46.378s. |
| `GOWORK=off go vet ./internal/backup` | Passed. |
| Three correction regressions, `go test -count=10` | Passed; root substitution, uncertainty replacement retention and same-inode source-manifest mutation all passed in 1.937s. |
| Offline `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed; generation and staged generation, Vacuum with zero warnings/errors, API, architecture, lint, all module tests/race/vet/mod verification and cross-build matrix completed; root storage race took 199.086s; final `guardrail checks passed (ci)`. |
| Live services, credentials, private coordinates, media mutation or deployment | Not used. |

## Acceptance and integration recommendation

- A-41 and A-43 retain the functional evidence accepted in the prior review;
  no schema, key-verification or lock regression was found.
- A-44 is not accepted. Successful backup and restore publication is not bound
  to the exact staged child set that passed private verification.

Do not close or integrate D-04 as approved from this correction. Route one
focused correction for the remaining P1, preserve the passing root-substitution,
cleanup, source-manifest, no-replace, cancellation and resource-bound behavior,
and submit exact new product and handoff SHAs for independent review. This
receipt does not authorize release, deployment, live backup/restore, or any
live media operation.
