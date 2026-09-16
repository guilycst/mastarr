//go:build linux

package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func atomicPublishDirectoryBound(stage *stagingDirectory, destination, parent string) error {
	if stage == nil || stage.file == nil {
		return fmt.Errorf("%w: missing staging descriptor", ErrStagingChanged)
	}
	if filepath.Clean(filepath.Dir(stage.path)) != filepath.Clean(parent) || filepath.Clean(filepath.Dir(destination)) != filepath.Clean(parent) {
		return fmt.Errorf("%w: staging and destination must share a parent", ErrPublicationUnsupported)
	}
	if err := stage.verifyPathIdentity(); err != nil {
		return fmt.Errorf("%w: %w", ErrIncomplete, err)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("%w: inspect publication parent: %w", ErrIncomplete, err)
	}
	stagingParentInfo, err := os.Stat(filepath.Dir(stage.path))
	if err != nil || !os.SameFile(parentInfo, stagingParentInfo) {
		return fmt.Errorf("%w: publication parent changed", ErrIncomplete)
	}
	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: open publication parent: %w", ErrPublicationUnsupported, err)
	}
	defer unix.Close(parentFD)
	err = unix.Renameat2(parentFD, filepath.Base(stage.path), parentFD, filepath.Base(destination), unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return ErrDestinationExists
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return fmt.Errorf("%w: renameat2 unavailable: %w", ErrPublicationUnsupported, err)
	}
	return fmt.Errorf("%w: atomic directory publication: %w", ErrIncomplete, err)
}

// rollbackPublishedDirectory moves the operation-owned directory back to its
// private stage name after a post-publication content check fails. The
// no-replace flag prevents an already-created stage replacement from being
// overwritten. If the destination or stage identity changes, the caller
// keeps the visible effect for reconciliation instead of deleting by name.
func rollbackPublishedDirectory(stage *stagingDirectory, destination, parent string) error {
	if stage == nil || stage.file == nil {
		return fmt.Errorf("%w: missing staging descriptor", ErrStagingChanged)
	}
	if filepath.Clean(filepath.Dir(stage.path)) != filepath.Clean(parent) || filepath.Clean(filepath.Dir(destination)) != filepath.Clean(parent) {
		return fmt.Errorf("%w: staging and destination must share a parent", ErrPublicationUnsupported)
	}
	if err := stage.verifyPublishedIdentity(destination); err != nil {
		return fmt.Errorf("%w: published identity changed: %w", ErrStagingChanged, err)
	}
	if _, err := os.Lstat(stage.path); err == nil {
		return fmt.Errorf("%w: rollback stage path is occupied", ErrDestinationExists)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: inspect rollback stage path: %w", ErrIncomplete, err)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("%w: inspect rollback parent: %w", ErrIncomplete, err)
	}
	stagingParentInfo, err := os.Stat(filepath.Dir(stage.path))
	if err != nil || !os.SameFile(parentInfo, stagingParentInfo) {
		return fmt.Errorf("%w: rollback parent changed", ErrIncomplete)
	}
	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: open rollback parent: %w", ErrPublicationUnsupported, err)
	}
	defer unix.Close(parentFD)
	if err := unix.Renameat2(parentFD, filepath.Base(destination), parentFD, filepath.Base(stage.path), unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: rollback stage path appeared", ErrDestinationExists)
		}
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
			return fmt.Errorf("%w: rollback rename unavailable: %w", ErrPublicationUnsupported, err)
		}
		return fmt.Errorf("%w: rollback publication: %w", ErrIncomplete, err)
	}
	if err := stage.verifyPathIdentity(); err != nil {
		return fmt.Errorf("%w: rollback identity: %w", ErrPublicationUncertain, err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("%w: rollback destination remains", ErrPublicationUncertain)
		}
		return fmt.Errorf("%w: inspect rollback destination: %w", ErrPublicationUncertain, err)
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("%w: sync rollback parent: %w", ErrPublicationUncertain, err)
	}
	return nil
}
