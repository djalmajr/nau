package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// writeHealth renders the current state and writes it atomically: a
// temp file in the health file's directory, a sync, and a rename. The
// directory is created 0700 and the file 0600.
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
	return writeAtomic(st.statePath, append(data, '\n'))
}

// writeAtomic writes data to path through a temp file and a rename, so
// a reader never sees a partial file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	tmp = ""
	return nil
}
