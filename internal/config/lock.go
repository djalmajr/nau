package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lockConfig uses the same exclusive file-lock mechanism as the outbox, on a
// stable sibling of the atomically replaced configuration, with a bounded wait.
func lockConfig(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "config.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ok, err := tryConfigLock(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if ok {
			return func() { releaseConfigLock(f); _ = f.Close() }, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("config: lock timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
