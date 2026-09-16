//go:build !darwin && !linux

package backup

import "fmt"

func atomicPublishDirectoryBound(_ *stagingDirectory, _, _ string) error {
	return fmt.Errorf("%w: platform has no reviewed no-replace directory primitive", ErrPublicationUnsupported)
}

func rollbackPublishedDirectory(_ *stagingDirectory, _, _ string) error {
	return fmt.Errorf("%w: platform has no reviewed rollback primitive", ErrPublicationUnsupported)
}
