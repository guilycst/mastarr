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
