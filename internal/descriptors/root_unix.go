//go:build darwin || linux

package descriptors

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const descriptorOpenReadOnly = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW

func descriptorObjectIdentity(info fs.FileInfo) (string, bool) {
	if info == nil {
		return "", false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("unix:dev=%d:ino=%d", stat.Dev, stat.Ino), true
}

func createPrivateStage(root string) (*os.File, string, fs.FileInfo, error) {
	directory, err := openRootDirectory(root)
	if err != nil {
		return nil, "", nil, err
	}
	defer directory.Close()
	for range 8 {
		identifier, err := newID()
		if err != nil {
			return nil, "", nil, err
		}
		name := privateStagePrefix + strings.ReplaceAll(identifier, "-", "")
		fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return nil, "", nil, classifyPathError(err)
		}
		file := os.NewFile(uintptr(fd), name)
		if file == nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
			return nil, "", nil, ErrSpecialFile
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			_ = file.Close()
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
			if statErr != nil {
				return nil, "", nil, statErr
			}
			return nil, "", nil, ErrSpecialFile
		}
		return file, filepath.Join(root, name), info, nil
	}
	return nil, "", nil, os.ErrExist
}

func openConstrainedFile(root, relative string) (*os.File, fs.FileInfo, error) {
	parent, name, err := openConstrainedParent(root, relative)
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), name, descriptorOpenReadOnly, 0)
	if err != nil {
		return nil, nil, classifyPathError(err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, ErrSpecialFile
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, classifyPathError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		return nil, nil, ErrSymlink
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrSpecialFile
	}
	return file, info, nil
}

func openConstrainedParent(root, relative string) (*os.File, string, error) {
	if err := validateRelativePath(relative); err != nil {
		return nil, "", err
	}
	rootDirectory, err := openRootDirectory(root)
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(filepathSlash(relative), "/")
	current := rootDirectory
	for _, component := range parts[:len(parts)-1] {
		if component == "" || component == "." || component == ".." {
			_ = current.Close()
			return nil, "", ErrPathEscape
		}
		fd, openErr := unix.Openat(int(current.Fd()), component, descriptorOpenReadOnly|unix.O_DIRECTORY, 0)
		if openErr != nil {
			_ = current.Close()
			return nil, "", classifyPathError(openErr)
		}
		next := os.NewFile(uintptr(fd), component)
		if next == nil {
			_ = unix.Close(fd)
			_ = current.Close()
			return nil, "", ErrSpecialFile
		}
		info, statErr := next.Stat()
		if statErr != nil || !info.IsDir() {
			_ = next.Close()
			_ = current.Close()
			return nil, "", ErrSpecialFile
		}
		_ = current.Close()
		current = next
	}
	return current, parts[len(parts)-1], nil
}

func openRootDirectory(root string) (*os.File, error) {
	fd, err := unix.Open("/", descriptorOpenReadOnly|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, classifyPathError(err)
	}
	current := os.NewFile(uintptr(fd), "/")
	if current == nil {
		_ = unix.Close(fd)
		return nil, ErrSpecialFile
	}
	trimmed := strings.TrimPrefix(filepathSlash(root), "/")
	if trimmed == "" {
		return current, nil
	}
	for _, component := range strings.Split(trimmed, "/") {
		if component == "" || component == "." || component == ".." {
			_ = current.Close()
			return nil, ErrPathEscape
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), component, descriptorOpenReadOnly|unix.O_DIRECTORY, 0)
		if openErr != nil {
			_ = current.Close()
			return nil, classifyPathError(openErr)
		}
		next := os.NewFile(uintptr(nextFD), component)
		if next == nil {
			_ = unix.Close(nextFD)
			_ = current.Close()
			return nil, ErrSpecialFile
		}
		info, statErr := next.Stat()
		if statErr != nil || !info.IsDir() {
			_ = next.Close()
			_ = current.Close()
			return nil, ErrSpecialFile
		}
		_ = current.Close()
		current = next
	}
	return current, nil
}

func removeConstrainedFile(root, relative string, expected fs.FileInfo) error {
	parent, name, err := openConstrainedParent(root, relative)
	if err != nil {
		return err
	}
	defer parent.Close()
	file, info, err := openConstrainedFileFromParent(parent, name)
	if err != nil {
		return err
	}
	_ = file.Close()
	if expected == nil || !os.SameFile(expected, info) {
		return ErrDescriptorChanged
	}
	// The descriptor-relative unlink keeps parent resolution confined. The
	// object is private to the service root; callers still receive uncertainty
	// if identity/read-back checks cannot prove the selected object.
	if err := unix.Unlinkat(int(parent.Fd()), name, 0); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return ErrDescriptorChanged
		}
		return fmt.Errorf("%w: remove descriptor object", ErrDeleteUncertain)
	}
	if file, _, err := openConstrainedFileFromParent(parent, name); err == nil {
		_ = file.Close()
		return ErrDeleteUncertain
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ErrDeleteUncertain
	}
	return nil
}

func openConstrainedFileFromParent(parent *os.File, name string) (*os.File, fs.FileInfo, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return nil, nil, ErrPathEscape
	}
	fd, err := unix.Openat(int(parent.Fd()), name, descriptorOpenReadOnly, 0)
	if err != nil {
		return nil, nil, classifyPathError(err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, ErrSpecialFile
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, classifyPathError(err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrSpecialFile
	}
	return file, info, nil
}

func syncDirectoryPath(pathValue string) error {
	directory, err := openRootDirectory(pathValue)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func classifyPathError(err error) error {
	if errors.Is(err, unix.ELOOP) {
		return ErrSymlink
	}
	if errors.Is(err, unix.ENOENT) {
		return fs.ErrNotExist
	}
	if errors.Is(err, unix.ENOTDIR) {
		return ErrSpecialFile
	}
	return err
}

func filepathSlash(value string) string {
	return strings.ReplaceAll(value, string(os.PathSeparator), "/")
}
