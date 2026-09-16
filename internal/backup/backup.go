// Package backup contains the versioned, database-neutral backup and isolated
// restore check for Mastarr's persistent state. The package copies the
// credential key and private descriptor/trash trees as opaque files; it does
// not define a second encryption or SQLite format.
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/guilycst/mastarr/internal/credentials"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/migrations"
	_ "modernc.org/sqlite"
)

const (
	// FormatVersion is the on-disk backup manifest format. A restore must
	// reject a future format rather than guessing its meaning.
	FormatVersion = 1

	manifestName      = "manifest.json"
	databaseName      = "database.sqlite"
	credentialKeyName = "keys/credentials.key"
	descriptorName    = "descriptors"
	trashName         = "trash"

	maxManifestBytes = 4 << 20
	maxTreeEntries   = 100_000
	maxArtifactBytes = int64(1) << 40
	maxTotalEntries  = 500_000
	maxTotalTrees    = 1_024
	maxTotalBytes    = int64(1) << 42
)

var (
	// ErrIncomplete means the source or backup cannot prove that all required
	// state was captured. Callers must not present such a backup as restorable.
	ErrIncomplete = errors.New("backup is incomplete")
	// ErrInvalidManifest identifies an unknown, malformed or contradictory
	// backup manifest.
	ErrInvalidManifest = errors.New("backup manifest is invalid")
	// ErrUnsafeArtifact identifies a symlink, special file or path escape in a
	// persistent artifact tree.
	ErrUnsafeArtifact = errors.New("backup artifact is unsafe")
	// ErrDestinationExists prevents a backup or restore from overwriting an
	// existing operator-selected directory.
	ErrDestinationExists = errors.New("backup destination already exists")
	// ErrNotQuiesced means the source was not explicitly made stable before the
	// SQLite snapshot and ancillary trees were captured.
	ErrNotQuiesced = errors.New("backup source is not quiescent")
	// ErrSchemaMismatch means the copied database is dirty, does not match its
	// manifest, or cannot be inspected without a migration downgrade.
	ErrSchemaMismatch = errors.New("backup schema is not restorable")
	// ErrCredentialVerification means an encrypted credential row could not be
	// authenticated with the restored key.
	ErrCredentialVerification = errors.New("backup credential verification failed")
	// ErrPublicationUncertain means the private tree was atomically renamed but
	// its containing directory could not be durably synchronized. The caller
	// must reconcile the requested destination with Verify before retrying.
	ErrPublicationUncertain = errors.New("backup publication outcome is uncertain")
	// ErrPublicationUnsupported means this platform cannot provide the
	// no-replace directory publication primitive required by this package.
	ErrPublicationUnsupported = errors.New("backup publication is unsupported")
)

// Limits bounds one backup capture. Zero fields use the package defaults. A
// caller may tighten these values for a deployment or a deterministic test;
// Restore and Verify use the package defaults because their public contract is
// already fixed and manifests carry their own exact contents.
type Limits struct {
	// MaxEntries is the maximum number of captured filesystem entries.
	MaxEntries int
	// MaxTrees is the maximum number of captured descriptor/trash trees.
	MaxTrees int
	// MaxBytes is the maximum aggregate size of captured regular files.
	MaxBytes int64
	// MaxManifestBytes is the maximum encoded manifest size.
	MaxManifestBytes int
}

func (limits Limits) withDefaults() Limits {
	if limits.MaxEntries <= 0 || limits.MaxEntries > maxTotalEntries {
		limits.MaxEntries = maxTotalEntries
	}
	if limits.MaxTrees <= 0 || limits.MaxTrees > maxTotalTrees {
		limits.MaxTrees = maxTotalTrees
	}
	if limits.MaxBytes <= 0 || limits.MaxBytes > maxTotalBytes {
		limits.MaxBytes = maxTotalBytes
	}
	if limits.MaxManifestBytes <= 0 || limits.MaxManifestBytes > maxManifestBytes {
		limits.MaxManifestBytes = maxManifestBytes
	}
	return limits
}

type copyBudget struct {
	limits  Limits
	entries int
	trees   int
	bytes   int64
}

func newCopyBudget(limits Limits) *copyBudget {
	return &copyBudget{limits: limits.withDefaults()}
}

func (budget *copyBudget) addTree() error {
	budget.trees++
	if budget.trees > budget.limits.MaxTrees {
		return ErrIncomplete
	}
	return nil
}

func (budget *copyBudget) addEntry(size int64) error {
	budget.entries++
	if budget.entries > budget.limits.MaxEntries {
		return ErrIncomplete
	}
	if size < 0 || budget.bytes > budget.limits.MaxBytes-size {
		return ErrIncomplete
	}
	budget.bytes += size
	return nil
}

func contextCheckpoint(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrIncomplete)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

type progressObserverKey struct{}

type progressObserver struct {
	CopyChunk func()
	WalkEntry func()
}

func observeCopyChunk(ctx context.Context) {
	if observer, ok := ctx.Value(progressObserverKey{}).(progressObserver); ok && observer.CopyChunk != nil {
		observer.CopyChunk()
	}
}

func observeWalkEntry(ctx context.Context) {
	if observer, ok := ctx.Value(progressObserverKey{}).(progressObserver); ok && observer.WalkEntry != nil {
		observer.WalkEntry()
	}
}

func withoutProgressObserver(ctx context.Context) context.Context {
	return context.WithValue(ctx, progressObserverKey{}, progressObserver{})
}

// Tree identifies one private directory that must be captured. Name becomes
// an archive-relative name, so it must be a stable non-secret identifier such
// as a configured storage-root ID. Path is read only from the source.
type Tree struct {
	Name string
	Path string
}

// Source describes one quiescent Mastarr installation. Store must be the
// already-open owner of the database; its SQLite handle is used for the
// consistent VACUUM INTO snapshot. DescriptorRoot and every TrashRoot are
// required, even when their directories are empty.
type Source struct {
	Store             *storage.Store
	CredentialKeyPath string
	DescriptorRoot    string
	TrashRoots        []Tree
	Quiesced          bool
	// Limits optionally tightens the capture resource ceilings.
	Limits Limits
}

// Artifact is one manifest entry. Paths are archive-relative and contain no
// source host paths. Directories have an empty SHA256 and zero Size.
type Artifact struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
	Mode   uint32 `json:"mode"`
}

// TreeManifest records the exact directory contents captured for one private
// tree. Files are sorted by Path for deterministic manifests.
type TreeManifest struct {
	Name  string     `json:"name"`
	Files []Artifact `json:"files"`
}

// Manifest is the versioned backup index. It deliberately contains digests,
// modes and schema metadata, never credential plaintext or source absolute
// paths.
type Manifest struct {
	FormatVersion int            `json:"format_version"`
	CreatedAt     time.Time      `json:"created_at"`
	SchemaVersion int64          `json:"schema_version"`
	Database      Artifact       `json:"database"`
	CredentialKey Artifact       `json:"credential_key"`
	Descriptors   TreeManifest   `json:"descriptors"`
	Trash         []TreeManifest `json:"trash"`
}

// RestoreReport is read-only evidence from an isolated restored tree. Counts
// preserve the existence of uncertain actions, idempotency records and trash
// state without returning their private payloads.
type RestoreReport struct {
	Manifest                 Manifest
	SchemaVersion            int64
	EncryptedCredentialCount int
	DescriptorCount          int
	ActionRunCount           int
	UnresolvedActionCount    int
	IdempotencyRecordCount   int
	TrashEntryCount          int
	TrashItemCount           int
	TrashRestoreReady        bool
}

// Create captures a quiescent source into a new destination directory. The
// destination must not exist. Database consistency comes from SQLite's
// VACUUM INTO on the store-owned handle; descriptor and trash consistency also
// requires source.Quiesced=true because they are ordinary filesystem trees.
func Create(ctx context.Context, source Source, destination string) (Manifest, error) {
	if err := validContext(ctx); err != nil {
		return Manifest{}, err
	}
	if !source.Quiesced {
		return Manifest{}, ErrNotQuiesced
	}
	if err := validateSource(ctx, source); err != nil {
		return Manifest{}, err
	}
	destination, parent, err := prepareNewDirectory(destination, ".mastarr-backup-")
	if err != nil {
		return Manifest{}, err
	}
	if err := destinationOutsideSource(ctx, destination, source); err != nil {
		return Manifest{}, err
	}
	budget := newCopyBudget(source.Limits)
	staging, err := os.MkdirTemp(parent, ".mastarr-backup-stage-")
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: create staging directory", ErrIncomplete)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.RemoveAll(staging)
		return Manifest{}, fmt.Errorf("%w: secure staging directory", ErrIncomplete)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()

	version, err := migrationVersion(ctx, source.Store.DB())
	if err != nil {
		return Manifest{}, err
	}
	databasePath := filepath.Join(staging, databaseName)
	if err := vacuumInto(ctx, source.Store.DB(), databasePath); err != nil {
		return Manifest{}, incompleteError("consistent database snapshot", err)
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		return Manifest{}, incompleteError("secure database snapshot", err)
	}
	databaseArtifact, err := fileArtifactContext(ctx, databasePath, databaseName, budget)
	if err != nil {
		return Manifest{}, incompleteError("database snapshot", err)
	}

	keyDestination := filepath.Join(staging, filepath.FromSlash(credentialKeyName))
	if err := os.MkdirAll(filepath.Dir(keyDestination), 0o700); err != nil {
		return Manifest{}, incompleteError("create key directory", err)
	}
	keyArtifact, err := copyStableFileContext(ctx, source.CredentialKeyPath, keyDestination, credentialKeyName, true, budget)
	if err != nil {
		return Manifest{}, incompleteError("credential key", err)
	}

	descriptorDestination := filepath.Join(staging, descriptorName)
	if err := os.Mkdir(descriptorDestination, 0o700); err != nil {
		return Manifest{}, incompleteError("create descriptor tree", err)
	}
	descriptors, err := copyTreeContext(ctx, source.DescriptorRoot, descriptorDestination, descriptorName, budget)
	if err != nil {
		return Manifest{}, incompleteError("descriptor tree", err)
	}
	trashDestination := filepath.Join(staging, trashName)
	if err := os.Mkdir(trashDestination, 0o700); err != nil {
		return Manifest{}, incompleteError("create trash tree", err)
	}
	trash := make([]TreeManifest, 0, len(source.TrashRoots))
	for _, root := range source.TrashRoots {
		archiveRoot := filepath.Join(trashDestination, root.Name)
		if err := os.Mkdir(archiveRoot, 0o700); err != nil {
			return Manifest{}, incompleteError("create trash root", err)
		}
		captured, err := copyTreeContext(ctx, root.Path, archiveRoot, root.Name, budget)
		if err != nil {
			return Manifest{}, incompleteError("trash tree", err)
		}
		trash = append(trash, captured)
	}

	manifest := Manifest{
		FormatVersion: FormatVersion,
		CreatedAt:     time.Now().UTC(),
		SchemaVersion: version,
		Database:      databaseArtifact,
		CredentialKey: keyArtifact,
		Descriptors:   descriptors,
		Trash:         trash,
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, incompleteError("generated manifest", err)
	}
	if err := writeManifestContext(ctx, staging, manifest, budget.limits.MaxManifestBytes); err != nil {
		return Manifest{}, err
	}
	if err := syncTreeContext(ctx, staging); err != nil {
		return Manifest{}, incompleteError("sync backup", err)
	}
	// Verify the complete private tree before publication. This catches wrong
	// keys, missing retained references, schema drift and any copy that cannot
	// be opened as a recovery point while the destination is still private.
	if _, err := Verify(ctx, staging); err != nil {
		return Manifest{}, err
	}
	if err := validateSource(ctx, source); err != nil {
		return Manifest{}, err
	}
	if err := contextCheckpoint(ctx); err != nil {
		return Manifest{}, err
	}
	if err := publishDirectory(staging, destination, parent); err != nil {
		return Manifest{}, err
	}
	published = true
	return manifest, nil
}

// Restore copies a validated backup directory into a new isolated target and
// verifies it before publication. A failed verification removes the private
// staging target and leaves no partially restored destination.
func Restore(ctx context.Context, archive, destination string) (RestoreReport, error) {
	if err := validContext(ctx); err != nil {
		return RestoreReport{}, err
	}
	archive, err := existingDirectory(archive)
	if err != nil {
		return RestoreReport{}, err
	}
	archiveInfo, err := os.Stat(archive)
	if err != nil {
		return RestoreReport{}, incompleteError("archive identity", err)
	}
	destination, parent, err := prepareNewDirectory(destination, ".mastarr-restore-")
	if err != nil {
		return RestoreReport{}, err
	}
	overlaps, overlapErr := pathsOverlapResolved(archive, destination)
	if overlapErr != nil {
		return RestoreReport{}, incompleteError("archive and destination identity", overlapErr)
	}
	if overlaps {
		return RestoreReport{}, fmt.Errorf("%w: archive overlaps destination", ErrIncomplete)
	}
	preflightContext := withoutProgressObserver(ctx)
	manifest, err := readManifestContext(preflightContext, archive)
	if err != nil {
		return RestoreReport{}, err
	}
	if err := ensureSupportedSchema(manifest.SchemaVersion); err != nil {
		return RestoreReport{}, err
	}
	if err := verifyTreeContentsContext(preflightContext, archive, manifest, newCopyBudget(Limits{})); err != nil {
		return RestoreReport{}, err
	}
	staging, err := os.MkdirTemp(parent, ".mastarr-restore-stage-")
	if err != nil {
		return RestoreReport{}, fmt.Errorf("%w: create restore staging directory", ErrIncomplete)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.RemoveAll(staging)
		return RestoreReport{}, fmt.Errorf("%w: secure restore staging directory", ErrIncomplete)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := copyManifestTreeContext(ctx, archive, staging, manifest, newCopyBudget(Limits{})); err != nil {
		return RestoreReport{}, err
	}
	if err := syncTreeContext(ctx, staging); err != nil {
		return RestoreReport{}, incompleteError("sync restored tree", err)
	}
	report, err := Verify(ctx, staging)
	if err != nil {
		return RestoreReport{}, err
	}
	currentArchiveInfo, err := os.Stat(archive)
	if err != nil || !os.SameFile(archiveInfo, currentArchiveInfo) {
		if err == nil {
			err = ErrUnsafeArtifact
		}
		return RestoreReport{}, incompleteError("archive changed during restore", err)
	}
	if err := contextCheckpoint(ctx); err != nil {
		return RestoreReport{}, err
	}
	if err := publishDirectory(staging, destination, parent); err != nil {
		return RestoreReport{}, err
	}
	published = true
	report.Manifest = manifest
	return report, nil
}

// Verify performs the isolated, read-only restore check. It never runs an
// SQLite down migration or opens a live worker/store. A caller may open the
// verified target with storage.Open later, which can only apply supported up
// migrations according to normal startup policy.
func Verify(ctx context.Context, restored string) (RestoreReport, error) {
	if err := validContext(ctx); err != nil {
		return RestoreReport{}, err
	}
	restored, err := existingDirectory(restored)
	if err != nil {
		return RestoreReport{}, err
	}
	manifest, err := readManifestContext(ctx, restored)
	if err != nil {
		return RestoreReport{}, err
	}
	if err := ensureSupportedSchema(manifest.SchemaVersion); err != nil {
		return RestoreReport{}, err
	}
	if err := verifyTreeContentsContext(ctx, restored, manifest, newCopyBudget(Limits{})); err != nil {
		return RestoreReport{}, err
	}
	keyPath := filepath.Join(restored, filepath.FromSlash(credentialKeyName))
	databasePath := filepath.Join(restored, databaseName)
	manager, key, err := credentials.Open(credentials.KeyOptions{
		KeyFile:                keyPath,
		KeyFileProvided:        true,
		ExistingCredentialData: true,
	})
	if err != nil {
		return RestoreReport{}, fmt.Errorf("%w: %w", ErrCredentialVerification, err)
	}
	defer manager.Close()
	defer key.Close()

	db, err := openReadOnlyDatabase(databasePath)
	if err != nil {
		return RestoreReport{}, fmt.Errorf("%w: open database", ErrSchemaMismatch)
	}
	defer db.Close()
	version, err := migrationVersion(ctx, db)
	if err != nil {
		return RestoreReport{}, err
	}
	if version != manifest.SchemaVersion {
		return RestoreReport{}, fmt.Errorf("%w: manifest/database version differs", ErrSchemaMismatch)
	}
	if err := verifyEncryptedCredentials(ctx, db, manager); err != nil {
		return RestoreReport{}, err
	}
	report := RestoreReport{Manifest: manifest, SchemaVersion: version}
	if err := queryCounts(ctx, db, &report); err != nil {
		return RestoreReport{}, err
	}
	if err := verifyDescriptorReferences(ctx, db, manifest); err != nil {
		return RestoreReport{}, err
	}
	if err := verifyTrashReferences(ctx, db, manifest); err != nil {
		return RestoreReport{}, err
	}
	report.TrashRestoreReady = true
	return report, nil
}

func validContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrIncomplete)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func incompleteError(stage string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", ErrIncomplete, stage)
	}
	return fmt.Errorf("%w: %s: %w", ErrIncomplete, stage, err)
}

func validateSource(ctx context.Context, source Source) error {
	if source.Store == nil || source.Store.DB() == nil {
		return fmt.Errorf("%w: storage owner is unavailable", ErrIncomplete)
	}
	if _, err := databaseFilePath(ctx, source.Store.DB()); err != nil {
		return err
	}
	if err := validateAbsoluteRegular(source.CredentialKeyPath); err != nil {
		return fmt.Errorf("%w: key source", ErrIncomplete)
	}
	keyInfo, err := os.Stat(source.CredentialKeyPath)
	if err != nil || keyInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: key permissions", ErrIncomplete)
	}
	if err := validateAbsoluteDirectory(source.DescriptorRoot); err != nil {
		return fmt.Errorf("%w: descriptor source", ErrIncomplete)
	}
	if len(source.TrashRoots) == 0 {
		return fmt.Errorf("%w: no trash roots configured", ErrIncomplete)
	}
	seen := make(map[string]struct{}, len(source.TrashRoots))
	for _, root := range source.TrashRoots {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		if err := validateTreeName(root.Name); err != nil {
			return fmt.Errorf("%w: trash root name", ErrIncomplete)
		}
		if _, exists := seen[root.Name]; exists {
			return fmt.Errorf("%w: duplicate trash root", ErrIncomplete)
		}
		seen[root.Name] = struct{}{}
		if err := validateAbsoluteDirectory(root.Path); err != nil {
			return fmt.Errorf("%w: trash source", ErrIncomplete)
		}
	}
	if _, err := migrationVersion(ctx, source.Store.DB()); err != nil {
		return err
	}
	return nil
}

func validateAbsoluteRegular(path string) error {
	path, err := cleanAbsolutePath(path)
	if err != nil {
		return ErrUnsafeArtifact
	}
	if err := validatePathComponents(path, false); err != nil {
		return ErrUnsafeArtifact
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafeArtifact
	}
	return nil
}

func validateAbsoluteDirectory(path string) error {
	path, err := cleanAbsolutePath(path)
	if err != nil {
		return ErrUnsafeArtifact
	}
	if err := validatePathComponents(path, false); err != nil {
		return ErrUnsafeArtifact
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrUnsafeArtifact
	}
	return nil
}

func cleanAbsolutePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", ErrUnsafeArtifact
	}
	clean := filepath.Clean(path)
	if clean != path {
		return "", ErrUnsafeArtifact
	}
	return clean, nil
}

// validatePathComponents refuses symlink ancestors as well as a symlink leaf.
// macOS exposes the stable system temporary directory through /var (and /tmp)
// symlinks; those two OS-owned aliases are accepted so Go's own temp roots can
// be used, while all application-controlled aliases remain rejected.
func validatePathComponents(path string, allowMissingLeaf bool) error {
	abs, err := cleanAbsolutePath(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(abs)
	remainder := strings.TrimPrefix(abs, volume)
	current := volume
	if strings.HasPrefix(remainder, string(filepath.Separator)) {
		current += string(filepath.Separator)
		remainder = strings.TrimPrefix(remainder, string(filepath.Separator))
	}
	parts := strings.FieldsFunc(remainder, func(r rune) bool { return r == rune(filepath.Separator) })
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if allowMissingLeaf && index == len(parts)-1 && errors.Is(statErr, fs.ErrNotExist) {
				return nil
			}
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 && !trustedSystemAlias(current) {
			return ErrUnsafeArtifact
		}
	}
	return nil
}

func trustedSystemAlias(path string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	return path == string(filepath.Separator)+"var" || path == string(filepath.Separator)+"tmp"
}

func canonicalExistingPath(path string) (string, error) {
	abs, err := cleanAbsolutePath(path)
	if err != nil {
		return "", err
	}
	if err := validatePathComponents(abs, false); err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

func canonicalPathForOverlap(path string, allowMissing bool) (string, error) {
	abs, err := cleanAbsolutePath(path)
	if err != nil {
		return "", err
	}
	if err := validatePathComponents(abs, allowMissing); err != nil {
		return "", err
	}
	if _, statErr := os.Lstat(abs); statErr == nil {
		return canonicalExistingPath(abs)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", statErr
	}
	if !allowMissing {
		return "", fs.ErrNotExist
	}
	parent, err := canonicalExistingPath(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func validateTreeName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > 128 || strings.TrimSpace(name) != name || strings.ContainsAny(name, `/\\`) || strings.ContainsRune(name, 0) {
		return ErrInvalidManifest
	}
	for _, r := range name {
		if r > utf8.MaxRune || !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return ErrInvalidManifest
		}
	}
	return nil
}

func destinationOutsideSource(ctx context.Context, destination string, source Source) error {
	databasePath, err := databaseFilePath(ctx, source.Store.DB())
	if err != nil {
		return err
	}
	paths := append([]string{source.CredentialKeyPath, source.DescriptorRoot}, treePaths(source.TrashRoots)...)
	paths = append(paths, databasePath)
	for _, path := range paths {
		overlaps, overlapErr := pathsOverlapResolved(path, destination)
		if overlapErr != nil {
			return fmt.Errorf("%w: source path identity", ErrIncomplete)
		}
		if overlaps {
			return fmt.Errorf("%w: destination overlaps source", ErrIncomplete)
		}
	}
	return nil
}

func treePaths(trees []Tree) []string {
	paths := make([]string, 0, len(trees))
	for _, tree := range trees {
		paths = append(paths, tree.Path)
	}
	return paths
}

func pathWithin(root, candidate string) bool {
	rootAbs, rootErr := canonicalPathForOverlap(root, true)
	candidateAbs, candidateErr := canonicalPathForOverlap(candidate, true)
	if rootErr != nil || candidateErr != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func pathsOverlap(first, second string) bool {
	overlaps, err := pathsOverlapResolved(first, second)
	return err == nil && overlaps
}

func pathsOverlapResolved(first, second string) (bool, error) {
	firstPath, err := canonicalPathForOverlap(first, true)
	if err != nil {
		return false, err
	}
	secondPath, err := canonicalPathForOverlap(second, true)
	if err != nil {
		return false, err
	}
	return pathWithinCanonical(firstPath, secondPath) || pathWithinCanonical(secondPath, firstPath), nil
}

func pathWithinCanonical(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func prepareNewDirectory(destination, _ string) (string, string, error) {
	if strings.TrimSpace(destination) == "" || !filepath.IsAbs(destination) {
		return "", "", fmt.Errorf("%w: destination path", ErrIncomplete)
	}
	if filepath.Clean(destination) != destination {
		return "", "", fmt.Errorf("%w: destination lexical alias", ErrIncomplete)
	}
	destination = filepath.Clean(destination)
	parent := filepath.Dir(destination)
	if err := validateAbsoluteDirectory(parent); err != nil {
		return "", "", fmt.Errorf("%w: destination parent", ErrIncomplete)
	}
	if info, err := os.Lstat(destination); err == nil {
		_ = info
		return "", "", ErrDestinationExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", fmt.Errorf("%w: inspect destination", ErrIncomplete)
	}
	return destination, parent, nil
}

func existingDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: directory path", ErrInvalidManifest)
	}
	if filepath.Clean(path) != path {
		return "", fmt.Errorf("%w: directory lexical alias", ErrInvalidManifest)
	}
	path = filepath.Clean(path)
	if err := validateAbsoluteDirectory(path); err != nil {
		return "", fmt.Errorf("%w: directory path", ErrInvalidManifest)
	}
	return path, nil
}

func migrationVersion(ctx context.Context, db *sql.DB) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("%w: database unavailable", ErrIncomplete)
	}
	var version, dirty int64
	if err := db.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		return 0, fmt.Errorf("%w: migration state", ErrSchemaMismatch)
	}
	if version <= 0 || dirty != 0 {
		return 0, fmt.Errorf("%w: dirty or empty migration state", ErrSchemaMismatch)
	}
	ceiling, err := supportedMigrationCeiling()
	if err != nil {
		return 0, err
	}
	if version > ceiling {
		return 0, fmt.Errorf("%w: database migration %d exceeds supported ceiling %d", ErrSchemaMismatch, version, ceiling)
	}
	return version, nil
}

var (
	migrationCeilingOnce sync.Once
	migrationCeiling     int64
	migrationCeilingErr  error
)

func supportedMigrationCeiling() (int64, error) {
	migrationCeilingOnce.Do(func() {
		entries, err := fs.Glob(migrations.FS, "*.up.sql")
		if err != nil {
			migrationCeilingErr = fmt.Errorf("%w: inspect embedded migrations", ErrSchemaMismatch)
			return
		}
		for _, name := range entries {
			prefix := strings.SplitN(filepath.Base(name), "_", 2)[0]
			version, parseErr := strconv.ParseInt(prefix, 10, 64)
			if parseErr != nil || version <= migrationCeiling {
				continue
			}
			migrationCeiling = version
		}
		if migrationCeiling <= 0 {
			migrationCeilingErr = fmt.Errorf("%w: no embedded migrations", ErrSchemaMismatch)
		}
	})
	if migrationCeilingErr != nil {
		return 0, migrationCeilingErr
	}
	return migrationCeiling, nil
}

func ensureSupportedSchema(version int64) error {
	if version <= 0 {
		return fmt.Errorf("%w: invalid manifest schema version", ErrSchemaMismatch)
	}
	ceiling, err := supportedMigrationCeiling()
	if err != nil {
		return err
	}
	if version > ceiling {
		return fmt.Errorf("%w: manifest migration %d exceeds supported ceiling %d", ErrSchemaMismatch, version, ceiling)
	}
	return nil
}

func databaseFilePath(ctx context.Context, db *sql.DB) (string, error) {
	if db == nil {
		return "", fmt.Errorf("%w: database unavailable", ErrIncomplete)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("%w: database identity", ErrIncomplete)
	}
	defer rows.Close()
	for rows.Next() {
		if err := contextCheckpoint(ctx); err != nil {
			return "", err
		}
		var sequence int
		var name, path string
		if err := rows.Scan(&sequence, &name, &path); err != nil {
			return "", fmt.Errorf("%w: database identity row", ErrIncomplete)
		}
		if name != "main" {
			continue
		}
		if strings.TrimSpace(path) == "" {
			return "", fmt.Errorf("%w: persistent database is required", ErrIncomplete)
		}
		path, err = filepath.Abs(filepath.Clean(path))
		if err != nil {
			return "", fmt.Errorf("%w: database identity path", ErrIncomplete)
		}
		if err := validateAbsoluteRegular(path); err != nil {
			return "", fmt.Errorf("%w: database identity path", ErrIncomplete)
		}
		return path, nil
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("%w: database identity", ErrIncomplete)
	}
	return "", fmt.Errorf("%w: main database is unavailable", ErrIncomplete)
}

func vacuumInto(ctx context.Context, db *sql.DB, destination string) error {
	if err := validContext(ctx); err != nil {
		return err
	}
	literal := strings.ReplaceAll(destination, "'", "''")
	_, err := db.ExecContext(ctx, "VACUUM INTO '"+literal+"'")
	return err
}

func fileArtifact(path, relative string) (Artifact, error) {
	return fileArtifactContext(context.Background(), path, relative, nil)
}

func fileArtifactContext(ctx context.Context, path, relative string, budget *copyBudget) (Artifact, error) {
	if err := contextCheckpoint(ctx); err != nil {
		return Artifact{}, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxArtifactBytes {
		return Artifact{}, ErrUnsafeArtifact
	}
	if budget != nil {
		if err := budget.addEntry(info.Size()); err != nil {
			return Artifact{}, err
		}
	}
	digest, size, err := digestFileContext(ctx, path, info.Size())
	if err != nil || size != info.Size() {
		if err != nil {
			return Artifact{}, err
		}
		return Artifact{}, ErrUnsafeArtifact
	}
	return Artifact{Path: filepath.ToSlash(relative), Kind: "file", Size: size, SHA256: digest, Mode: uint32(info.Mode().Perm())}, nil
}

func copyStableFile(source, destination, relative string, key bool) (Artifact, error) {
	return copyStableFileContext(context.Background(), source, destination, relative, key, nil)
}

func copyStableFileContext(ctx context.Context, source, destination, relative string, key bool, budget *copyBudget) (Artifact, error) {
	if err := contextCheckpoint(ctx); err != nil {
		return Artifact{}, err
	}
	if err := validateAbsoluteRegular(source); err != nil {
		return Artifact{}, err
	}
	info, err := os.Stat(source)
	if err != nil || info.Size() < 0 || info.Size() > maxArtifactBytes {
		return Artifact{}, ErrUnsafeArtifact
	}
	if key && info.Mode().Perm()&0o077 != 0 {
		return Artifact{}, ErrUnsafeArtifact
	}
	if budget != nil {
		if err := budget.addEntry(info.Size()); err != nil {
			return Artifact{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return Artifact{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".mastarr-artifact-")
	if err != nil {
		return Artifact{}, err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		return Artifact{}, err
	}
	input, err := os.Open(source)
	if err != nil {
		return Artifact{}, err
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return Artifact{}, ErrUnsafeArtifact
	}
	hash := sha256.New()
	written, err := copyReader(ctx, temporary, input, info.Size(), maxArtifactBytes, hash)
	if err != nil {
		return Artifact{}, err
	}
	if written != info.Size() {
		return Artifact{}, ErrUnsafeArtifact
	}
	if err := contextCheckpoint(ctx); err != nil {
		return Artifact{}, err
	}
	if err := temporary.Sync(); err != nil {
		return Artifact{}, err
	}
	if err := temporary.Close(); err != nil {
		return Artifact{}, err
	}
	latest, err := os.Stat(source)
	if err != nil || !os.SameFile(info, latest) || latest.Size() != info.Size() {
		return Artifact{}, ErrUnsafeArtifact
	}
	if err := os.Chmod(temporaryName, info.Mode().Perm()); err != nil {
		return Artifact{}, err
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return Artifact{}, err
	}
	removeTemporary = false
	return Artifact{Path: filepath.ToSlash(relative), Kind: "file", Size: written, SHA256: hex.EncodeToString(hash.Sum(nil)), Mode: uint32(info.Mode().Perm())}, nil
}

func copyTree(source, destination, name string) (TreeManifest, error) {
	return copyTreeContext(context.Background(), source, destination, name, nil)
}

func copyTreeContext(ctx context.Context, source, destination, name string, budget *copyBudget) (TreeManifest, error) {
	if err := contextCheckpoint(ctx); err != nil {
		return TreeManifest{}, err
	}
	if err := validateAbsoluteDirectory(source); err != nil {
		return TreeManifest{}, err
	}
	if err := validateTreeName(name); err != nil {
		return TreeManifest{}, err
	}
	if err := validateAbsoluteDirectory(destination); err != nil {
		return TreeManifest{}, err
	}
	if budget != nil {
		if err := budget.addTree(); err != nil {
			return TreeManifest{}, err
		}
	}
	manifest := TreeManifest{Name: name, Files: make([]Artifact, 0)}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		observeWalkEntry(ctx)
		if walkErr != nil {
			return walkErr
		}
		if len(manifest.Files) >= maxTreeEntries {
			return ErrIncomplete
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrUnsafeArtifact
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if err := validateRelativeArchivePath(rel); err != nil {
			return err
		}
		destinationPath := filepath.Join(destination, filepath.FromSlash(rel))
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if budget != nil {
				if err := budget.addEntry(0); err != nil {
					return err
				}
			}
			if err := os.Mkdir(destinationPath, info.Mode().Perm()); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if err := os.Chmod(destinationPath, info.Mode().Perm()); err != nil {
				return err
			}
			manifest.Files = append(manifest.Files, Artifact{Path: rel, Kind: "directory", Mode: uint32(info.Mode().Perm())})
			return nil
		}
		if !entry.Type().IsRegular() {
			return ErrUnsafeArtifact
		}
		artifact, err := copyStableFileContext(ctx, path, destinationPath, rel, false, budget)
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, artifact)
		return nil
	})
	if err != nil {
		return TreeManifest{}, err
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	return manifest, nil
}

func digestFile(path string, expected int64) (string, int64, error) {
	return digestFileContext(context.Background(), path, expected)
}

func digestFileContext(ctx context.Context, path string, expected int64) (string, int64, error) {
	if err := contextCheckpoint(ctx); err != nil {
		return "", 0, err
	}
	input, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer input.Close()
	hash := sha256.New()
	size, err := copyReader(ctx, io.Discard, input, expected, maxArtifactBytes, hash)
	if err != nil {
		return "", size, err
	}
	if size != expected {
		return "", size, ErrUnsafeArtifact
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func copyReader(ctx context.Context, destination io.Writer, source io.Reader, expected, maximum int64, hash io.Writer) (int64, error) {
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := contextCheckpoint(ctx); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read < 0 || int64(read) > maximum-total || (expected >= 0 && int64(read) > expected-total) {
			return total, ErrUnsafeArtifact
		}
		if read > 0 {
			if _, err := destination.Write(buffer[:read]); err != nil {
				return total, err
			}
			if hash != nil {
				if _, err := hash.Write(buffer[:read]); err != nil {
					return total, err
				}
			}
			total += int64(read)
			observeCopyChunk(ctx)
			if err := contextCheckpoint(ctx); err != nil {
				return total, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return total, readErr
		}
	}
	return total, nil
}

func writeManifest(directory string, manifest Manifest) error {
	return writeManifestContext(context.Background(), directory, manifest, maxManifestBytes)
}

func writeManifestContext(ctx context.Context, directory string, manifest Manifest, maximum int) error {
	if err := contextCheckpoint(ctx); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: encode manifest", ErrIncomplete)
	}
	data = append(data, '\n')
	if maximum <= 0 {
		maximum = maxManifestBytes
	}
	if len(data) > maximum {
		return fmt.Errorf("%w: manifest exceeds configured limit", ErrIncomplete)
	}
	if err := contextCheckpoint(ctx); err != nil {
		return err
	}
	path := filepath.Join(directory, manifestName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("%w: create manifest", ErrIncomplete)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return incompleteError("write manifest", err)
	}
	if err := contextCheckpoint(ctx); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return incompleteError("sync manifest", err)
	}
	if err := file.Close(); err != nil {
		return incompleteError("close manifest", err)
	}
	return nil
}

func publishDirectory(staging, destination, parent string) error {
	if err := validateAbsoluteDirectory(parent); err != nil {
		return fmt.Errorf("%w: publication parent", ErrIncomplete)
	}
	if err := atomicPublishDirectory(staging, destination, parent); err != nil {
		return err
	}
	if err := syncPublishedParent(parent); err != nil {
		return fmt.Errorf("%w: sync published directory: %w", ErrPublicationUncertain, err)
	}
	return nil
}

var syncPublishedParent = syncDirectory

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func syncTree(root string) error {
	return syncTreeContext(context.Background(), root)
}

func syncTreeContext(ctx context.Context, root string) error {
	if err := contextCheckpoint(ctx); err != nil {
		return err
	}
	directories := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		observeWalkEntry(ctx)
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrUnsafeArtifact
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i], string(filepath.Separator)) > strings.Count(directories[j], string(filepath.Separator))
	})
	for _, directory := range directories {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func readManifest(directory string) (Manifest, error) {
	return readManifestContext(context.Background(), directory)
}

func readManifestContext(ctx context.Context, directory string) (Manifest, error) {
	if err := contextCheckpoint(ctx); err != nil {
		return Manifest{}, err
	}
	path := filepath.Join(directory, manifestName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return Manifest{}, fmt.Errorf("%w: manifest is unavailable", ErrInvalidManifest)
	}
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest is unavailable", ErrInvalidManifest)
	}
	defer file.Close()
	data, err := readBoundedContext(ctx, file, maxManifestBytes)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Manifest{}, ctxErr
		}
		return Manifest{}, ErrInvalidManifest
	}
	if len(data) > maxManifestBytes || !utf8.Valid(data) {
		return Manifest{}, ErrInvalidManifest
	}
	if err := contextCheckpoint(ctx); err != nil {
		return Manifest{}, err
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Manifest{}, fmt.Errorf("%w: duplicate manifest field", ErrInvalidManifest)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Manifest{}, ErrInvalidManifest
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func readBoundedContext(ctx context.Context, source io.Reader, maximum int64) ([]byte, error) {
	if maximum < 0 {
		return nil, ErrInvalidManifest
	}
	var data bytes.Buffer
	buffer := make([]byte, 64*1024)
	for {
		if err := contextCheckpoint(ctx); err != nil {
			return nil, err
		}
		read, readErr := source.Read(buffer)
		if read < 0 || int64(data.Len()) > maximum-int64(read) {
			return nil, ErrInvalidManifest
		}
		if read > 0 {
			_, _ = data.Write(buffer[:read])
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return data.Bytes(), nil
			}
			return nil, readErr
		}
	}
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return ErrInvalidManifest
				}
				if _, exists := seen[name]; exists {
					return ErrInvalidManifest
				}
				seen[name] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return nil
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidManifest
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.FormatVersion != FormatVersion || manifest.CreatedAt.IsZero() || manifest.SchemaVersion <= 0 || len(manifest.Trash) == 0 || len(manifest.Trash)+1 > maxTotalTrees {
		return ErrInvalidManifest
	}
	if err := validateArtifact(manifest.Database); err != nil || manifest.Database.Path != databaseName || manifest.Database.Kind != "file" || manifest.Database.Mode != 0o600 {
		return ErrInvalidManifest
	}
	if err := validateArtifact(manifest.CredentialKey); err != nil || manifest.CredentialKey.Path != credentialKeyName || manifest.CredentialKey.Kind != "file" || manifest.CredentialKey.Mode&0o077 != 0 {
		return ErrInvalidManifest
	}
	if manifest.Descriptors.Name != descriptorName {
		return ErrInvalidManifest
	}
	if err := validateTreeManifest(manifest.Descriptors); err != nil {
		return err
	}
	seenTrees := map[string]struct{}{descriptorName: {}}
	entryCount := 2
	totalBytes := manifest.Database.Size + manifest.CredentialKey.Size
	if totalBytes < 0 || totalBytes > maxTotalBytes {
		return ErrInvalidManifest
	}
	entryCount += len(manifest.Descriptors.Files)
	for _, tree := range manifest.Trash {
		if err := validateTreeManifest(tree); err != nil {
			return err
		}
		if _, exists := seenTrees[tree.Name]; exists {
			return ErrInvalidManifest
		}
		seenTrees[tree.Name] = struct{}{}
		entryCount += len(tree.Files)
		for _, artifact := range tree.Files {
			if totalBytes > maxTotalBytes-artifact.Size {
				return ErrInvalidManifest
			}
			totalBytes += artifact.Size
		}
	}
	for _, artifact := range manifest.Descriptors.Files {
		if totalBytes > maxTotalBytes-artifact.Size {
			return ErrInvalidManifest
		}
		totalBytes += artifact.Size
	}
	if entryCount > maxTotalEntries {
		return ErrInvalidManifest
	}
	return nil
}

func validateTreeManifest(tree TreeManifest) error {
	if err := validateTreeName(tree.Name); err != nil || len(tree.Files) > maxTreeEntries {
		return ErrInvalidManifest
	}
	seen := make(map[string]struct{}, len(tree.Files))
	for _, artifact := range tree.Files {
		if err := validateArtifact(artifact); err != nil {
			return err
		}
		if _, exists := seen[artifact.Path]; exists {
			return ErrInvalidManifest
		}
		seen[artifact.Path] = struct{}{}
	}
	return nil
}

func validateArtifact(artifact Artifact) error {
	if err := validateRelativeArchivePath(artifact.Path); err != nil || artifact.Size < 0 || artifact.Size > maxArtifactBytes {
		return ErrInvalidManifest
	}
	if artifact.Kind == "directory" {
		if artifact.Size != 0 || artifact.SHA256 != "" {
			return ErrInvalidManifest
		}
		return nil
	}
	if artifact.Kind != "file" || len(artifact.SHA256) != sha256.Size*2 {
		return ErrInvalidManifest
	}
	if _, err := hex.DecodeString(artifact.SHA256); err != nil {
		return ErrInvalidManifest
	}
	return nil
}

func validateRelativeArchivePath(value string) error {
	if value == "" || value == "." || value == ".." || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "//") || strings.ContainsRune(value, 0) {
		return ErrInvalidManifest
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean != value || strings.HasPrefix(clean, "../") {
		return ErrInvalidManifest
	}
	return nil
}

func verifyTreeContents(directory string, manifest Manifest) error {
	return verifyTreeContentsContext(context.Background(), directory, manifest, newCopyBudget(Limits{}))
}

func verifyTreeContentsContext(ctx context.Context, directory string, manifest Manifest, budget *copyBudget) error {
	if err := contextCheckpoint(ctx); err != nil {
		return err
	}
	if budget != nil {
		if err := budget.addTree(); err != nil {
			return err
		}
		for range manifest.Trash {
			if err := budget.addTree(); err != nil {
				return err
			}
		}
	}
	expected := map[string]Artifact{manifest.Database.Path: manifest.Database, manifest.CredentialKey.Path: manifest.CredentialKey}
	expectedDirectories := map[string]uint32{
		"keys":         0o700,
		descriptorName: 0o700,
		trashName:      0o700,
	}
	for _, artifact := range manifest.Descriptors.Files {
		path := filepath.ToSlash(filepath.Join(descriptorName, artifact.Path))
		if artifact.Kind == "directory" {
			expectedDirectories[path] = artifact.Mode
		} else {
			expected[path] = artifact
		}
	}
	for _, tree := range manifest.Trash {
		expectedDirectories[filepath.ToSlash(filepath.Join(trashName, tree.Name))] = 0o700
		for _, artifact := range tree.Files {
			path := filepath.ToSlash(filepath.Join(trashName, tree.Name, artifact.Path))
			if artifact.Kind == "directory" {
				expectedDirectories[path] = artifact.Mode
			} else {
				expected[path] = artifact
			}
		}
	}
	seen := map[string]struct{}{}
	seenDirectories := map[string]struct{}{}
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		observeWalkEntry(ctx)
		if walkErr != nil {
			return walkErr
		}
		if path == directory {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrUnsafeArtifact
		}
		if budget != nil {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			size := int64(0)
			if !entry.IsDir() {
				size = info.Size()
			}
			if err := budget.addEntry(size); err != nil {
				return err
			}
		}
		rel, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			mode, ok := expectedDirectories[rel]
			if !ok {
				return ErrInvalidManifest
			}
			info, err := entry.Info()
			if err != nil || uint32(info.Mode().Perm()) != mode {
				return ErrIncomplete
			}
			seenDirectories[rel] = struct{}{}
			return nil
		}
		if rel == manifestName {
			return nil
		}
		artifact, ok := expected[rel]
		if !ok || !entry.Type().IsRegular() {
			return ErrInvalidManifest
		}
		if err := verifyArtifactContext(ctx, path, artifact); err != nil {
			return err
		}
		seen[rel] = struct{}{}
		return nil
	})
	if err != nil {
		return incompleteError("artifact verification", err)
	}
	for path := range expected {
		if _, ok := seen[path]; !ok {
			return fmt.Errorf("%w: required artifact missing", ErrIncomplete)
		}
	}
	for path := range expectedDirectories {
		if _, ok := seenDirectories[path]; !ok {
			return fmt.Errorf("%w: required directory missing", ErrIncomplete)
		}
	}
	return nil
}

func verifyArtifact(path string, artifact Artifact) error {
	return verifyArtifactContext(context.Background(), path, artifact)
}

func verifyArtifactContext(ctx context.Context, path string, artifact Artifact) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Size || uint32(info.Mode().Perm()) != artifact.Mode {
		return ErrIncomplete
	}
	digest, size, err := digestFileContext(ctx, path, artifact.Size)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return ErrIncomplete
	}
	if size != artifact.Size || digest != artifact.SHA256 {
		return ErrIncomplete
	}
	return nil
}

func copyManifestTree(archive, destination string, manifest Manifest) error {
	return copyManifestTreeContext(context.Background(), archive, destination, manifest, nil)
}

func copyManifestTreeContext(ctx context.Context, archive, destination string, manifest Manifest, budget *copyBudget) error {
	if err := contextCheckpoint(ctx); err != nil {
		return err
	}
	if budget != nil {
		if err := budget.addTree(); err != nil {
			return err
		}
		for range manifest.Trash {
			if err := budget.addTree(); err != nil {
				return err
			}
		}
	}
	manifestPath := filepath.Join(archive, manifestName)
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil || manifestInfo.Size() > maxManifestBytes {
		return fmt.Errorf("%w: manifest restore", ErrIncomplete)
	}
	manifestArtifact, err := copyStableFileContext(
		ctx,
		manifestPath,
		filepath.Join(destination, manifestName),
		manifestName,
		false,
		budget,
	)
	if err != nil || manifestArtifact.Kind != "file" {
		return incompleteError("manifest restore", err)
	}
	if err := makeRestoreDirectoriesContext(ctx, destination, manifest, budget); err != nil {
		return incompleteError("restore directory tree", err)
	}
	if err := copyManifestArtifactContext(ctx, archive, destination, manifest.Database, budget); err != nil {
		return incompleteError("database restore", err)
	}
	if err := copyManifestArtifactContext(ctx, archive, destination, manifest.CredentialKey, budget); err != nil {
		return incompleteError("key restore", err)
	}
	for _, artifact := range manifest.Descriptors.Files {
		artifact.Path = filepath.ToSlash(filepath.Join(descriptorName, artifact.Path))
		if err := copyManifestArtifactContext(ctx, archive, destination, artifact, budget); err != nil {
			return incompleteError("descriptor restore", err)
		}
	}
	for _, tree := range manifest.Trash {
		for _, artifact := range tree.Files {
			artifact.Path = filepath.ToSlash(filepath.Join(trashName, tree.Name, artifact.Path))
			if err := copyManifestArtifactContext(ctx, archive, destination, artifact, budget); err != nil {
				return incompleteError("trash restore", err)
			}
		}
	}
	return nil
}

func makeRestoreDirectories(destination string, manifest Manifest) error {
	return makeRestoreDirectoriesContext(context.Background(), destination, manifest, nil)
}

func makeRestoreDirectoriesContext(ctx context.Context, destination string, manifest Manifest, budget *copyBudget) error {
	for _, path := range []string{"keys", descriptorName, trashName} {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(destination, filepath.FromSlash(path)), 0o700); err != nil {
			return err
		}
		if budget != nil {
			if err := budget.addEntry(0); err != nil {
				return err
			}
		}
	}
	for _, tree := range manifest.Trash {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(destination, trashName, tree.Name), 0o700); err != nil {
			return err
		}
		if budget != nil {
			if err := budget.addEntry(0); err != nil {
				return err
			}
		}
	}
	type directorySpec struct {
		path string
		mode os.FileMode
	}
	directories := make([]directorySpec, 0)
	for _, tree := range append([]TreeManifest{manifest.Descriptors}, manifest.Trash...) {
		root := descriptorName
		if tree.Name != descriptorName {
			root = filepath.ToSlash(filepath.Join(trashName, tree.Name))
		}
		for _, artifact := range tree.Files {
			if artifact.Kind != "directory" {
				continue
			}
			directories = append(directories, directorySpec{
				path: filepath.Join(destination, filepath.FromSlash(filepath.Join(root, artifact.Path))),
				mode: os.FileMode(artifact.Mode),
			})
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		leftDepth := strings.Count(directories[i].path, string(filepath.Separator))
		rightDepth := strings.Count(directories[j].path, string(filepath.Separator))
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return directories[i].path < directories[j].path
	})
	for _, directory := range directories {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		if err := os.Mkdir(directory.path, directory.mode); err != nil {
			return err
		}
		if budget != nil {
			if err := budget.addEntry(0); err != nil {
				return err
			}
		}
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			return err
		}
	}
	return nil
}

func copyManifestArtifact(archive, destination string, artifact Artifact) error {
	return copyManifestArtifactContext(context.Background(), archive, destination, artifact, nil)
}

func copyManifestArtifactContext(ctx context.Context, archive, destination string, artifact Artifact, budget *copyBudget) error {
	if err := contextCheckpoint(ctx); err != nil {
		return err
	}
	if artifact.Kind == "directory" {
		path := filepath.Join(destination, filepath.FromSlash(artifact.Path))
		return os.Chmod(path, os.FileMode(artifact.Mode))
	}
	if artifact.Kind != "file" {
		return nil
	}
	source := filepath.Join(archive, filepath.FromSlash(artifact.Path))
	target := filepath.Join(destination, filepath.FromSlash(artifact.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	copied, err := copyStableFileContext(ctx, source, target, artifact.Path, false, budget)
	if err != nil {
		return err
	}
	if copied.Size != artifact.Size || copied.SHA256 != artifact.SHA256 || copied.Mode != artifact.Mode {
		return ErrIncomplete
	}
	return nil
}

func openReadOnlyDatabase(path string) (*sql.DB, error) {
	if err := validateAbsoluteRegular(path); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func verifyEncryptedCredentials(ctx context.Context, db *sql.DB, manager *credentials.Manager) error {
	rows, err := db.QueryContext(ctx, `SELECT connection_id, name, envelope_version, nonce, ciphertext, key_fingerprint FROM encrypted_credentials`)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: encrypted credential table", ErrSchemaMismatch)
	}
	defer rows.Close()
	for rows.Next() {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		var connectionID, name, fingerprint string
		var version int64
		var nonce, ciphertext []byte
		if err := rows.Scan(&connectionID, &name, &version, &nonce, &ciphertext, &fingerprint); err != nil {
			return fmt.Errorf("%w: encrypted credential row", ErrCredentialVerification)
		}
		plaintext, err := manager.Open(credentials.Envelope{Version: version, Nonce: nonce, Ciphertext: ciphertext, KeyFingerprint: fingerprint}, connectionID, name)
		if err != nil {
			return fmt.Errorf("%w: authenticated credential row", ErrCredentialVerification)
		}
		for index := range plaintext {
			plaintext[index] = 0
		}
	}
	if err := rows.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: encrypted credential scan", ErrCredentialVerification)
	}
	return nil
}

func queryCounts(ctx context.Context, db *sql.DB, report *RestoreReport) error {
	queries := []struct {
		query string
		out   *int
	}{
		{`SELECT count(*) FROM encrypted_credentials`, &report.EncryptedCredentialCount},
		{`SELECT count(*) FROM descriptors`, &report.DescriptorCount},
		{`SELECT count(*) FROM action_runs`, &report.ActionRunCount},
		{`SELECT count(*) FROM action_runs WHERE state IN ('running', 'reconciling') OR unresolved_count > 0 OR outcome_json LIKE '%uncertain%' OR EXISTS (SELECT 1 FROM action_effects WHERE action_effects.action_run_id = action_runs.id AND action_effects.state = 'unknown')`, &report.UnresolvedActionCount},
		{`SELECT count(*) FROM idempotency_records`, &report.IdempotencyRecordCount},
		{`SELECT count(*) FROM trash_entries`, &report.TrashEntryCount},
		{`SELECT count(*) FROM trash_items`, &report.TrashItemCount},
	}
	for _, item := range queries {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, item.query).Scan(item.out); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("%w: required recovery table", ErrSchemaMismatch)
		}
	}
	return nil
}

func verifyDescriptorReferences(ctx context.Context, db *sql.DB, manifest Manifest) error {
	available := make(map[string]struct{}, len(manifest.Descriptors.Files))
	for _, artifact := range manifest.Descriptors.Files {
		if artifact.Kind == "file" {
			available[artifact.Path] = struct{}{}
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT storage_path, deleted_at, unavailable_reason FROM descriptors`)
	if err != nil {
		return fmt.Errorf("%w: descriptor references", ErrSchemaMismatch)
	}
	defer rows.Close()
	for rows.Next() {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		var path string
		var deleted, unavailable sql.NullString
		if err := rows.Scan(&path, &deleted, &unavailable); err != nil {
			return fmt.Errorf("%w: descriptor reference row", ErrIncomplete)
		}
		if deleted.Valid || unavailable.Valid {
			continue
		}
		path = filepath.ToSlash(path)
		if err := validateRelativeArchivePath(path); err != nil {
			return fmt.Errorf("%w: unsafe retained descriptor path", ErrIncomplete)
		}
		if _, ok := available[path]; !ok {
			return fmt.Errorf("%w: retained descriptor is absent", ErrIncomplete)
		}
	}
	if err := rows.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: descriptor references", ErrIncomplete)
	}
	return nil
}

func verifyTrashReferences(ctx context.Context, db *sql.DB, manifest Manifest) error {
	available := make(map[string]struct{})
	for _, tree := range manifest.Trash {
		for _, artifact := range tree.Files {
			if artifact.Kind == "file" {
				available[filepath.ToSlash(filepath.Join(tree.Name, artifact.Path))] = struct{}{}
			}
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT te.state, te.manifest_json, ti.root_id, ti.trash_relative_path FROM trash_entries AS te JOIN trash_items AS ti ON ti.entry_id = te.id`)
	if err != nil {
		return fmt.Errorf("%w: trash references", ErrSchemaMismatch)
	}
	defer rows.Close()
	for rows.Next() {
		if err := contextCheckpoint(ctx); err != nil {
			return err
		}
		var state, manifestJSON, rootID, relative string
		if err := rows.Scan(&state, &manifestJSON, &rootID, &relative); err != nil {
			return fmt.Errorf("%w: trash reference row", ErrIncomplete)
		}
		if !json.Valid([]byte(manifestJSON)) {
			return fmt.Errorf("%w: trash manifest is malformed", ErrIncomplete)
		}
		if state == "restored" || state == "purged" {
			continue
		}
		relative = filepath.ToSlash(relative)
		if err := validateRelativeArchivePath(relative); err != nil {
			return fmt.Errorf("%w: unsafe selected trash path", ErrIncomplete)
		}
		if _, ok := available[filepath.ToSlash(filepath.Join(rootID, relative))]; !ok {
			return fmt.Errorf("%w: selected trash payload is absent", ErrIncomplete)
		}
	}
	if err := rows.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: trash references", ErrIncomplete)
	}
	return nil
}
