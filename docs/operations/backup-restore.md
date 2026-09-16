# Backup and isolated restore

Mastarr's recoverable state is a set of coupled artifacts. A usable backup
contains the SQLite journal, the credential encryption key, every retained
descriptor, and every configured trash tree. The database contains the
encrypted credential envelopes, action and effect journals, idempotency
records, descriptor references, and trash metadata; the key and private trees
must be restored with it.

The backup package is intentionally database-neutral. It uses the already
opened `internal/storage.Store` and the existing `internal/credentials`
format. It does not introduce another database, encryption scheme, or
migration path:

```go
manifest, err := backup.Create(ctx, backup.Source{
	Store:             store,
	CredentialKeyPath: "/data/keys/credentials.key",
	DescriptorRoot:    "/data/descriptors",
	TrashRoots: []backup.Tree{
		{Name: "library", Path: "/data/trash/library"},
	},
	Quiesced: true,
}, "/backups/mastarr-2026-09-16")
```

`Source.Limits` can tighten the built-in aggregate entry, tree, byte, and
manifest-size ceilings for a deployment. Zero fields use the package defaults;
larger values never raise the safety ceilings. Restore and Verify always apply
the built-in ceilings enforced by this binary.

The example paths are illustrative. A deployment must pass the actual
persistent paths for that instance and a stable, non-secret tree name for each
trash root. The destination must be a new directory below an existing secure
parent. Mastarr never overwrites an existing backup.

## Capture procedure

1. Stop or otherwise quiesce workers, request handling, migrations, janitor
   activity, and writers of the descriptor and trash trees. Set `Quiesced` only
   after that boundary is in force. A flag without an operator-enforced
   quiescence boundary is not a consistency proof.
2. Keep the SQLite database on persistent storage and let the already opened
   `storage.Store` retain its process lock. An in-memory database, a dirty
   migration, a missing key, a symlink, a special file, or a missing configured
   descriptor/trash tree makes the backup incomplete.
3. Call `backup.Create` with one `TrashRoots` entry for every configured trash
   root. The package takes a consistent SQLite `VACUUM INTO` snapshot, copies
   the key and private trees into a private staging directory, records SHA-256
   digests, sizes, and permission bits, writes a versioned `manifest.json`,
   syncs the staging directory, and runs the complete read-only `Verify` check
   while it is still private. Only a fully verified tree is published through
   the platform's atomic no-replace directory operation.
4. Treat the completed backup directory as sensitive. It contains the key
   that can decrypt the database's credential envelopes. Do not put it in a
   public artifact store or log its contents. A key supplied only through an
   environment variable must first be placed in a controlled 0600 file for
   this procedure; if the original key cannot be captured, stop and report an
   incomplete backup.

The resulting directory has this shape:

```text
manifest.json
database.sqlite
keys/credentials.key
descriptors/...
trash/<configured-root-name>/...
```

The manifest contains no source absolute paths or plaintext credentials. It
binds each file and retained directory to its type, mode, size, and digest and
records the schema version captured from `schema_migrations`.

If any precondition, snapshot, copy, digest, limit, verification, sync, or
publication step fails, `Create` returns an incomplete result and does not
publish the destination. A wrong key or a retained descriptor/trash reference
that is absent from the captured tree is discovered before publication. The
operation-owned private stage is retained for a janitor rather than removed by
an unchecked pathname cleanup. The caller must retain the prior backup and
investigate the failure; a partial directory is not a restore point.

The source, archive, destination parent, and every overlap check must use
canonical paths without application-controlled symlink ancestors or lexical
aliases. Restore refuses equal or overlapping archive and target paths before
creating staging, and verifies that the source archive identity is unchanged
before publication. This keeps a known-good archive usable after a refused
restore.

## Isolated restore check

Restore into a new directory that is isolated from the live instance and from
all live media mounts:

```go
report, err := backup.Restore(ctx,
	"/backups/mastarr-2026-09-16",
	"/restore-check/mastarr-2026-09-16",
)
```

`Restore` rejects an existing target, parses the strict versioned manifest,
rejects duplicate/unknown fields and unsafe paths, rejects archive/target
overlap, verifies every artifact, copies into a private staging directory, and
runs the read-only `backup.Verify` check before publishing the target. Restore
copies the exact preflight manifest bytes, checks their digest again after all
artifact work, and returns the manifest proven by the staged verification. It
never starts workers, contacts an upstream service, or runs a migration
downgrade. The database and manifest schema versions must not exceed the
embedded migration ceiling; supported older versions remain eligible for the
normal upward migration path after the isolated check.

Verification requires all of the following:

- the manifest and every copied file or directory still match its recorded
  mode, size, and digest;
- the restored key is present and valid, and every encrypted credential row
  decrypts with its original connection and field binding;
- the database has a clean schema version equal to the manifest version and
  contains the required recovery tables;
- retained descriptor rows resolve to captured descriptor files;
- non-purged trash items resolve to captured payloads and their trash
  manifests are valid;
- action runs, unresolved effects, idempotency records, and trash entries are
  present for the caller's recovery review.

`RestoreReport` is evidence of this check. `TrashRestoreReady` means the
captured trash metadata and selected payloads can be examined safely in the
isolated tree; it does not purge, restore, or delete anything.

After a successful isolated check, an operator may open the copied database
with the normal storage startup path. That path may apply supported upward
migrations. A backup made by a newer schema remains blocked until the running
binary supports it; Mastarr never silently downgrades or rewrites it to an
older schema. The normal database lock still applies, so a second executor for
the restored journal is rejected while the first owns its lock.

The isolated check must use synthetic or otherwise disposable upstream
endpoints and writable fixture roots. Before a live recovery, recreate the
same static YAML configuration, connection identity, path mapping revisions,
database/key mounts, descriptor location, and trash-root mounts. Static YAML,
Kubernetes ConfigMaps, Secrets, and mount wiring are deployment inputs and are
not inferred from a backup directory. The key remains mandatory even when
those inputs are supplied externally.

## Failure and tamper handling

Missing or wrong key material, changed ciphertext, changed credential binding,
edited descriptor/trash payloads, missing retained files, dirty schema state,
unexpected files, symlinks, path traversal, duplicate manifest fields,
schema-version disagreement, and future schemas all fail closed. The target is
not published and the key is never regenerated over an existing encrypted
database. Preserve
the failing archive for diagnosis and use a known-good backup after correcting
the source or restore environment.

File work is performed in bounded chunks with context checks and aggregate
entry/tree/byte limits. Cancellation leaves private staging for safe janitor
inspection and never publishes a destination. Publication uses a reviewed
no-replace primitive on
Linux and macOS. Other platforms return `ErrPublicationUnsupported` rather
than falling back to a replace-capable rename. If the containing-parent sync
fails after the atomic rename, the operation returns `ErrPublicationUncertain`;
the destination is a visible effect and must be reconciled with `Verify` before
retrying.

The backup procedure does not claim live service availability. After an
isolated restore has passed, upstream observations must be reacquired through
the normal read-only adapters before any action is resumed. Uncertain actions
remain available for reconciliation, and idempotency records remain part of
the restored journal so a retry can preserve the existing desired-state and
effect guarantees. Trash payloads remain in their captured trash roots until a
separately approved restore or janitor operation acts on them.
