// Package config manages the nau user configuration file: a key=value
// file under the user configuration directory, with defaults for every
// known key. The configuration carries the two keys machine_label and
// herdr_soho_bin. A key the configuration does not define is ignored on
// load (an old file with a removed key still reads) and is refused on set.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config is the machine configuration with effective values (defaults
// applied for absent keys). HerdrSohoBin is effective: the auto value is
// resolved (see ResolveBin) by Load and by Set.
type Config struct {
	MachineLabel       string `json:"machine_label"`
	HerdrSohoBin       string `json:"herdr_soho_bin"`
	herdrSohoBinStored string
}

// ErrUnknownKey marks an attempt to set or read a key the configuration does
// not define.
var ErrUnknownKey = fmt.Errorf("unknown configuration key")

// defaultConfig returns the defaults: machine_label and herdr_soho_bin
// empty (the empty herdr_soho_bin is the auto form, resolved on load).
func defaultConfig() Config {
	return Config{}
}

// Keys lists every known configuration key in canonical order.
func Keys() []string {
	return []string{"machine_label", "herdr_soho_bin"}
}

// IsAutoBin reports whether the stored backend selection is automatic (empty).
func IsAutoBin(stored string) bool {
	return stored == ""
}

// ResolveBin resolves a stored herdr_soho_bin value to the effective
// executable: the auto value resolves to soho found on the PATH,
// else to the bare name soho
// (the not-found reporting is left to the caller, which looks the value
// up again); an explicit value (a bare name, a relative or an absolute
// path) passes through unchanged.
func ResolveBin(stored string) string {
	if !IsAutoBin(stored) {
		return stored
	}
	if p, err := exec.LookPath("soho"); err == nil {
		return p
	}
	return "soho"
}

// Load reads <dir>/config and returns the effective configuration. A missing
// file or directory yields the defaults without error and without creating
// anything, so read-only commands never create the config directory. A key
// the configuration does not define is ignored: an old file with a removed
// key still reads. The herdr_soho_bin value is resolved: the field holds the
// effective executable, the stored form is kept for the next Set rewrite.
func Load(dir string) (Config, error) {
	data, err := readReplacedFile(filepath.Join(dir, "config"))
	if os.IsNotExist(err) {
		data, err = nil, nil
	}
	if err != nil {
		return defaultConfig(), err
	}
	return decodeConfig(data)
}

func decodeConfig(data []byte) (Config, error) {
	cfg := defaultConfig()
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return cfg, fmt.Errorf("config line %d: missing '='", i+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "machine_label":
			cfg.MachineLabel = value
		case "herdr_soho_bin":
			cfg.HerdrSohoBin = value
			cfg.herdrSohoBinStored = value
		default:
			// A key removed from the configuration is ignored on read:
			// an old file with it still reads.
		}
	}
	cfg.HerdrSohoBin = ResolveBin(cfg.HerdrSohoBin)
	return cfg, nil
}

// Set writes one key of the configuration file atomically. Unknown keys and
// invalid values are refused with an error before anything is read or
// written. The auto value of herdr_soho_bin (the empty value) is stored as
// the empty value.
func Set(dir, key, value string) error {
	stored, err := storedValue(key, value)
	if err != nil {
		return err
	}
	_, err = setLocked(dir, key, stored, false)
	return err
}

// SetIfAutomatic pins only an automatic backend selection. A nonempty warning
// means no configuration bytes were written; unsafe candidates are not errors.
func SetIfAutomatic(dir, key, value string) (string, error) {
	if key != "herdr_soho_bin" {
		return "", fmt.Errorf("--if-automatic is only valid for herdr_soho_bin")
	}
	return setLocked(dir, key, value, true)
}

func setLocked(dir, key, stored string, automatic bool) (string, error) {
	unlock, err := lockConfig(dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	data, err := readReplacedFile(filepath.Join(dir, "config"))
	if os.IsNotExist(err) {
		data, err = nil, nil
	}
	if err != nil {
		return "", err
	}
	cfg, err := decodeConfig(data)
	if err != nil {
		return "", err
	}
	if automatic {
		if !IsAutoBin(cfg.HerdrSohoBinStored()) {
			return "herdr_soho_bin is set explicitly; keeping it", nil
		}
		if err := safeBackend(stored); err != nil {
			return "warning: unsafe backend; herdr_soho_bin unchanged: " + err.Error(), nil
		}
	}
	return "", writeFileAtomic(filepath.Join(dir, "config"), replaceRecord(data, key, stored))
}

// replaceRecord changes the last matching record (the one Load uses), retaining
// all other bytes, including duplicate records and each line's terminator.
func replaceRecord(data []byte, key, value string) []byte {
	text := string(data)
	lines := strings.SplitAfter(text, "\n")
	last := -1
	eol := "\n"
	if i := strings.IndexByte(text, '\n'); i > 0 && text[i-1] == '\r' {
		eol = "\r\n"
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		k, _, ok := strings.Cut(trimmed, "=")
		if ok && strings.TrimSpace(k) == key {
			last = i
		}
	}
	if last >= 0 {
		ending := ""
		if strings.HasSuffix(lines[last], "\r\n") {
			ending = "\r\n"
		} else if strings.HasSuffix(lines[last], "\n") {
			ending = "\n"
		}
		lines[last] = key + "=" + value + ending
		return []byte(strings.Join(lines, ""))
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += eol
	}
	return []byte(text + key + "=" + value + eol)
}

// Value returns the effective value of a known key as a string, or
// ErrUnknownKey for an unknown key. For herdr_soho_bin the effective
// value is the resolved executable (Load and Set resolve it).
func (c Config) Value(key string) (string, error) {
	switch key {
	case "machine_label":
		return c.MachineLabel, nil
	case "herdr_soho_bin":
		return c.HerdrSohoBin, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownKey, key)
}

// HerdrSohoBinStored returns the raw herdr_soho_bin value from the
// configuration file. An empty result means the automatic form, including
// when the key or the file is absent.
func (c Config) HerdrSohoBinStored() string {
	return c.herdrSohoBinStored
}

// storedValue validates the value of key and returns the form that is stored
// in the configuration file.
func storedValue(key, value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("%s: value must not contain CR, LF or NUL", key)
	}
	switch key {
	case "machine_label", "herdr_soho_bin":
		// The auto form of herdr_soho_bin (the empty value) stores empty;
		// an explicit selection stores unchanged.
		return value, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
}

// atomicWriteSync is the sync step of writeFileAtomic; tests replace it to
// count syncs without changing the exported signature.
var atomicWriteSync = func(f *os.File) error { return f.Sync() }

// writeFileAtomic writes data to path atomically: a temp file in the same
// directory, fsync, then rename. The final file has mode 0600. The rename
// goes through replaceFile, which on Windows asks for a POSIX-semantics
// replace so a reader holding the file with FILE_SHARE_DELETE does not
// block it; on the other systems it is os.Rename.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
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
	if err := atomicWriteSync(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := replaceFile(tmp, path); err != nil {
		return err
	}
	tmp = ""
	return nil
}
