//go:build darwin

package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func atomicPublishDirectory(staging, destination, parent string) error {
	if filepath.Clean(filepath.Dir(staging)) != filepath.Clean(parent) || filepath.Clean(filepath.Dir(destination)) != filepath.Clean(parent) {
		return fmt.Errorf("%w: staging and destination must share a parent", ErrPublicationUnsupported)
	}
	if err := validateAbsoluteDirectory(staging); err != nil {
		return fmt.Errorf("%w: staging directory changed", ErrIncomplete)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("%w: inspect publication parent: %w", ErrIncomplete, err)
	}
	stagingParentInfo, err := os.Stat(filepath.Dir(staging))
	if err != nil || !os.SameFile(parentInfo, stagingParentInfo) {
		return fmt.Errorf("%w: publication parent changed", ErrIncomplete)
	}
	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: open publication parent: %w", ErrPublicationUnsupported, err)
	}
	defer unix.Close(parentFD)
	err = unix.RenameatxNp(parentFD, filepath.Base(staging), parentFD, filepath.Base(destination), unix.RENAME_EXCL)
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return ErrDestinationExists
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return fmt.Errorf("%w: renameatx_np unavailable: %w", ErrPublicationUnsupported, err)
	}
	return fmt.Errorf("%w: atomic directory publication: %w", ErrIncomplete, err)
}
