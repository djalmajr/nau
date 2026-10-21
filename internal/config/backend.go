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
	// Load trims the stored value: a path with outer spaces would select
	// another file than the one checked here.
	if strings.TrimSpace(path) != path {
		return fmt.Errorf("path must not start or end with spaces")
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
	var parts []string
	for _, part := range strings.Split(strings.TrimPrefix(path, prefix), string(os.PathSeparator)) {
		if part != "" {
			parts = append(parts, part)
		}
	}
	for i, part := range parts {
		prefix += string(os.PathSeparator) + part
		// The backend and the directory that holds it get the full check;
		// the ancestors above only need to keep their entries in place.
		near := 0
		switch len(parts) - 1 - i {
		case 0:
			near = 2
		case 1:
			near = 1
		}
		if err := trustedBackendChain(prefix, near); err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(prefix)
		if err != nil {
			return fmt.Errorf("cannot resolve backend path component")
		}
		if err := trustedBackendChain(resolved, near); err != nil {
			return err
		}
	}
	return nil
}
