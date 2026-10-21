package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func safeBackend(path string) error {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return fmt.Errorf("path must be absolute without CR, LF or NUL")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("cannot resolve backend")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || !backendExecutable(resolved, info) {
		return fmt.Errorf("backend must be a regular executable file")
	}
	if err := trustedBackendPath(path); err != nil {
		return err
	}
	return trustedBackendPath(resolved)
}

func trustedBackendPath(path string) error {
	// Resolve each prefix too: an intermediate symlink may traverse a
	// writable directory that the final resolved path no longer contains.
	path = filepath.FromSlash(path)
	prefix := filepath.VolumeName(path)
	for _, part := range strings.Split(strings.TrimPrefix(path, prefix), string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		prefix += string(os.PathSeparator) + part
		if err := trustedBackendChain(prefix); err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(prefix)
		if err != nil {
			return fmt.Errorf("cannot resolve backend path component")
		}
		if err := trustedBackendChain(resolved); err != nil {
			return err
		}
	}
	return nil
}
