package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// The health states. last_error always holds the current state when
// the state is not ok, and is empty when it is.
const (
	StateOK                 = "ok"
	StateSohoUnavailable    = "soho_unavailable"
	StateSohoLacksInventory = "soho_lacks_inventory"
	StateBadInventory       = "bad_inventory"
	StateHubUnreachable     = "hub_unreachable"
	StateRateLimited        = "rate_limited"
	StateRevoked            = "revoked"
)

// Health is the health line the agent writes atomically at StatePath on
// each change, and that the doctor of the next slice reads. The
// fields are the contract of the file: the current state, the last
// accepted and last attempted read in UTC milliseconds, the last error
// category (never free text) and the hub as scheme and host only. The
// machine key never appears in the file.
type Health struct {
	// State is the current state: one of the State* constants.
	State string `json:"state"`
	// LastOKMS is when the hub last accepted an inventory, in UTC
	// milliseconds, or 0 when it has not.
	LastOKMS int64 `json:"last_ok_ms"`
	// LastAttemptMS is when the last inventory read was attempted, in
	// UTC milliseconds, or 0 when there was none.
	LastAttemptMS int64 `json:"last_attempt_ms"`
	// LastError is the last error category: the current state when it
	// is not ok, empty when it is. Never free text, never the key.
	LastError string `json:"last_error"`
	// Hub is the hub URL reduced to its scheme and host.
	Hub string `json:"hub"`
}

// ReadHealth reads the health file at path.
func ReadHealth(path string) (Health, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Health{}, err
	}
	var h Health
	if err := json.Unmarshal(data, &h); err != nil {
		return Health{}, err
	}
	return h, nil
}

// errHealthDirMode marks the failure to set the health directory's
// mode: best effort on Unix, the file was still written, and the
// failure is recorded internally by writeHealth instead of stopping
// the agent.
var errHealthDirMode = errors.New("agent: the health directory mode could not be applied")

// writeHealth renders the current state and writes it atomically: a
// temp file in the health file's directory, a sync, and a rename. The
// directory is created 0700 and the file 0600. A failure to set the
// directory's mode is recorded internally and does not stop the agent.
func (st *state) writeHealth() error {
	h := Health{
		State:         st.state,
		LastOKMS:      st.lastOKMS,
		LastAttemptMS: st.lastAttemptMS,
		LastError:     st.lastError,
		Hub:           st.hub,
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if err := writeAtomic(st.statePath, append(data, '\n')); err != nil {
		if errors.Is(err, errHealthDirMode) {
			// Internal record: the directory mode is best effort, the
			// write succeeded and the loop goes on.
			st.dirModeErr = errHealthDirMode.Error()
			return nil
		}
		return err
	}
	return nil
}

// writeAtomic writes data to path through a temp file and a rename, so
// a reader never sees a partial file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll does not change the mode of a directory that already
	// exists; the dedicated health directory is private either way.
	// Unix only: Windows directories have no Unix mode.
	var modeErr error
	if runtime.GOOS != "windows" {
		modeErr = os.Chmod(dir, 0o700)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return modeErrIfNone(modeErr, err)
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return modeErrIfNone(modeErr, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return modeErrIfNone(modeErr, err)
	}
	if err := f.Close(); err != nil {
		return modeErrIfNone(modeErr, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return modeErrIfNone(modeErr, err)
	}
	if err := renameReplace(tmp, path); err != nil {
		return modeErrIfNone(modeErr, err)
	}
	tmp = ""
	return modeErrIfNone(modeErr, nil)
}

// modeErrIfNone returns the write error when it is not nil, and the
// recorded directory-mode failure wrapped as its internal category
// otherwise: the mode failure never hides a real write failure, and a
// write error is never masked by the best-effort mode.
func modeErrIfNone(modeErr, err error) error {
	if err != nil {
		return err
	}
	if modeErr != nil {
		return fmt.Errorf("%w: %v", errHealthDirMode, modeErr)
	}
	return nil
}

// The retries of renameReplace on Windows: a reader that has the
// health file open (nau doctor, a test) holds it for a moment, and the
// rename over it fails until the reader closes it.
var (
	renameRetries = 20
	renameWait    = 25 * time.Millisecond
)

// renameReplace renames tmp over path. On Windows a rename over a file
// another process has open fails (Go opens files without
// FILE_SHARE_DELETE), so the rename is tried again for a short while
// instead of failing the health write, which would stop the agent.
func renameReplace(tmp, path string) error {
	err := os.Rename(tmp, path)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	for i := 0; i < renameRetries && err != nil; i++ {
		time.Sleep(renameWait)
		err = os.Rename(tmp, path)
	}
	return err
}
