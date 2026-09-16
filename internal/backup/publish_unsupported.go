//go:build !darwin && !linux

package backup

import "fmt"

func atomicPublishDirectory(_, _, _ string) error {
	return fmt.Errorf("%w: platform has no reviewed no-replace directory primitive", ErrPublicationUnsupported)
}
