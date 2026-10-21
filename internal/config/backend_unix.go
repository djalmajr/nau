//go:build unix

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func backendExecutable(path string, info os.FileInfo) bool {
	return info.Mode().Perm()&0111 != 0 && unix.Access(path, unix.X_OK) == nil
}

func trustedBackendChain(path string) error {
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("cannot inspect backend path")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Getuid())) {
			return fmt.Errorf("backend path has an untrusted owner")
		}
		if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("backend path has an unsupported file type")
		}
		if info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0022 != 0 {
			// Sticky directories protect the next entry only when it has a
			// trusted owner; that entry was checked in the previous iteration.
			if !info.IsDir() || info.Mode()&os.ModeSticky == 0 {
				return fmt.Errorf("backend path is writable by group or others")
			}
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}
