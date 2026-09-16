//go:build !darwin && !linux

package descriptors

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func descriptorObjectIdentity(_ fs.FileInfo) (string, bool) {
	// The fallback platforms do not have a reviewed, serializable object
	// identity primitive for restart-safe deletion. Callers fail closed before
	// attempting a physical delete when this proof is unavailable.
	return "", false
}

// Platforms without the reviewed Unix no-follow primitives still use a
// canonical, no-symlink path check. Deletion remains fail closed because a
// descriptor-bound unlink is unavailable.
func createPrivateStage(root string) (*os.File, string, fs.FileInfo, error) {
	file, err := os.CreateTemp(root, privateStagePrefix+"*")
	if err != nil {
		return nil, "", nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		_ = os.Remove(file.Name())
		if err != nil {
			return nil, "", nil, err
		}
		return nil, "", nil, ErrSpecialFile
	}
	return file, file.Name(), info, nil
}

func openConstrainedFile(root, relative string) (*os.File, fs.FileInfo, error) {
	if err := validateRelativePath(relative); err != nil {
		return nil, nil, err
	}
	if err := rejectSymlinkComponents(root, relative); err != nil {
		return nil, nil, err
	}
	pathValue := filepath.Join(root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(pathValue)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fs.ErrNotExist
		}
		return nil, nil, err
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil || !withinRoot(rootResolved, resolved) {
		return nil, nil, ErrPathEscape
	}
	info, err := os.Lstat(pathValue)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, ErrSymlink
	}
	if !info.Mode().IsRegular() {
		return nil, nil, ErrSpecialFile
	}
	file, err := os.Open(pathValue)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, nil, ErrDescriptorChanged
	}
	return file, opened, nil
}

func rejectSymlinkComponents(root, relative string) error {
	current := root
	target := filepath.Join(root, filepath.FromSlash(relative))
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.ErrNotExist
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
		if !info.IsDir() && current != target {
			return ErrSpecialFile
		}
	}
	return nil
}

func removeConstrainedFile(_, _ string, _ fs.FileInfo) error {
	return ErrDeleteUncertain
}

func removeConstrainedFileWithGuard(_, _ string, _ fs.FileInfo, _ func() error) error {
	return ErrDeleteUncertain
}

func syncDirectoryPath(pathValue string) error {
	directory, err := os.Open(pathValue)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func withinRoot(root, candidate string) bool {
	if root == candidate {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return false
	}
	return true
}
