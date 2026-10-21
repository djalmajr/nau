// Package config manages the herdr-hermes user configuration file: a
// key=value file under the user configuration directory, with defaults for
// every known key. The configuration also carries the five keys of the
// dispatcher-side machine router (herdr_bin, route_machines, route_disabled,
// route_orchestrator_name and route_probe_timeout_s), which are validated on
// load and on set. The dispatcher API key never lives in this file.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/djalmajr/nau/internal/outbox"
	"github.com/djalmajr/nau/internal/router"
)

// Config is the machine configuration with effective values (defaults
// applied for absent keys). HerdrSohoBin is effective: the auto value is
// resolved (see ResolveBin) by Load and by Set.
type Config struct {
	MachineLabel          string `json:"machine_label"`
	DispatcherURL         string `json:"dispatcher_url"`
	HerdrSohoBin          string `json:"herdr_soho_bin"`
	PushTimeoutS          int    `json:"push_timeout_s"`
	HerdrBin              string `json:"herdr_bin"`
	RouteMachines         string `json:"route_machines"`
	RouteDisabled         string `json:"route_disabled"`
	RouteOrchestratorName string `json:"route_orchestrator_name"`
	RouteProbeTimeoutS    int    `json:"route_probe_timeout_s"`

	// herdrSohoBinStored is the raw value the configuration file holds
	// for herdr_soho_bin (the empty value is the auto form), never the
	// resolved path. Updating other keys preserves this record verbatim.
	herdrSohoBinStored string
}

// ErrUnknownKey marks an attempt to set or read a key the configuration does
// not define.
var ErrUnknownKey = fmt.Errorf("unknown configuration key")

// Limits of the router keys: at most maxRouteListItems labels in one route
// list and a route probe timeout of routeProbeTimeoutMin to
// routeProbeTimeoutMax seconds.
const (
	maxRouteListItems    = 32
	routeProbeTimeoutMin = 1
	routeProbeTimeoutMax = 120
)

// defaultConfig returns the defaults: machine_label and dispatcher_url
// empty, herdr_soho_bin the empty auto form (resolved on load),
// push_timeout_s 15, herdr_bin "herdr", route_machines and route_disabled
// empty, route_orchestrator_name "orchestrator", route_probe_timeout_s 20.
func defaultConfig() Config {
	return Config{
		PushTimeoutS:          15,
		HerdrBin:              "herdr",
		RouteOrchestratorName: router.DefaultOrchestratorName,
		RouteProbeTimeoutS:    20,
	}
}

// Keys lists every known configuration key in canonical order.
func Keys() []string {
	return []string{"machine_label", "dispatcher_url", "herdr_soho_bin", "push_timeout_s", "herdr_bin", "route_machines", "route_disabled", "route_orchestrator_name", "route_probe_timeout_s"}
}

// IsAutoBin reports whether a stored herdr_soho_bin value is the auto
// value: the empty value or exactly herdr-soho. The legacy name counts
// as auto because the old `config set` wrote every key's default into
// the file, so that stored value is never a user selection; a missing
// key stores the empty value and is auto too. Any other value (a path
// or another name) is an explicit selection and passes through
// unchanged.
func IsAutoBin(stored string) bool {
	return stored == "" || stored == "herdr-soho"
}

// ResolveBin resolves a stored herdr_soho_bin value to the effective
// executable: the auto value resolves to the first of soho and
// herdr-soho found on the machine's PATH, else to the bare name soho
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
	if p, err := exec.LookPath("herdr-soho"); err == nil {
		return p
	}
	return "soho"
}

// Load reads <dir>/config and returns the effective configuration. A missing
// file or directory yields the defaults without error and without creating
// anything, so read-only commands never create the config directory. The
// herdr_soho_bin value is resolved: the field holds the effective
// executable, the stored form is kept for the next Set rewrite.
func Load(dir string) (Config, error) {
	data, err := outbox.ReadReplacedFile(filepath.Join(dir, "config"))
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
		case "dispatcher_url":
			cfg.DispatcherURL = value
		case "herdr_soho_bin":
			cfg.HerdrSohoBin = value
			cfg.herdrSohoBinStored = value
		case "push_timeout_s":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return cfg, fmt.Errorf("config: push_timeout_s must be a positive integer")
			}
			cfg.PushTimeoutS = n
		case "herdr_bin":
			if strings.TrimSpace(value) == "" {
				return cfg, fmt.Errorf("config: herdr_bin must be a non-empty string")
			}
			cfg.HerdrBin = value
		case "route_machines", "route_disabled":
			list, err := parseLabelList(key, value)
			if err != nil {
				return cfg, fmt.Errorf("config: %w", err)
			}
			if key == "route_machines" {
				cfg.RouteMachines = strings.Join(list, ",")
			} else {
				cfg.RouteDisabled = strings.Join(list, ",")
			}
		case "route_orchestrator_name":
			if !router.OrchestratorNamePattern.MatchString(value) {
				return cfg, fmt.Errorf("config: route_orchestrator_name: invalid orchestrator name %q", value)
			}
			cfg.RouteOrchestratorName = value
		case "route_probe_timeout_s":
			n, err := parseRouteProbeTimeout(value)
			if err != nil {
				return cfg, fmt.Errorf("config: %w", err)
			}
			cfg.RouteProbeTimeoutS = n
		default:
			return cfg, fmt.Errorf("%w: %q", ErrUnknownKey, key)
		}
	}
	cfg.HerdrSohoBin = ResolveBin(cfg.HerdrSohoBin)
	return cfg, nil
}

// Set writes one key of the configuration file atomically. Unknown keys and
// invalid values are refused with an error before anything is read or
// written. The stored form is normalized: herdr_bin is trimmed, the
// route lists are joined with "," without spaces, and the auto value of
// herdr_soho_bin (the empty value or a legacy name) is stored as the
// empty value.
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
	data, err := outbox.ReadReplacedFile(filepath.Join(dir, "config"))
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
	return "", outbox.WriteFileAtomic(filepath.Join(dir, "config"), replaceRecord(data, key, stored))
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
	case "dispatcher_url":
		return c.DispatcherURL, nil
	case "herdr_soho_bin":
		return c.HerdrSohoBin, nil
	case "push_timeout_s":
		return strconv.Itoa(c.PushTimeoutS), nil
	case "herdr_bin":
		return c.HerdrBin, nil
	case "route_machines":
		return c.RouteMachines, nil
	case "route_disabled":
		return c.RouteDisabled, nil
	case "route_orchestrator_name":
		return c.RouteOrchestratorName, nil
	case "route_probe_timeout_s":
		return strconv.Itoa(c.RouteProbeTimeoutS), nil
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
// in the configuration file (the trimmed herdr_bin, the normalized route
// lists).
func storedValue(key, value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("%s: value must not contain CR, LF or NUL", key)
	}
	switch key {
	case "machine_label", "dispatcher_url":
		return value, nil
	case "herdr_soho_bin":
		// The auto form (the empty value or a legacy name) stores empty;
		// an explicit selection stores unchanged.
		if IsAutoBin(value) {
			return "", nil
		}
		return value, nil
	case "push_timeout_s":
		if n, err := strconv.Atoi(value); err != nil || n <= 0 {
			return "", fmt.Errorf("push_timeout_s must be a positive integer")
		}
		return value, nil
	case "herdr_bin":
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("herdr_bin must be a non-empty string")
		}
		return strings.TrimSpace(value), nil
	case "route_machines", "route_disabled":
		list, err := parseLabelList(key, value)
		if err != nil {
			return "", err
		}
		return strings.Join(list, ","), nil
	case "route_orchestrator_name":
		if !router.OrchestratorNamePattern.MatchString(value) {
			return "", fmt.Errorf("route_orchestrator_name: invalid orchestrator name %q", value)
		}
		return value, nil
	case "route_probe_timeout_s":
		if _, err := parseRouteProbeTimeout(value); err != nil {
			return "", err
		}
		return value, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
}

// RouteMachineList returns route_machines as a list (nil when empty).
func (c Config) RouteMachineList() []string {
	return routeList(c.RouteMachines)
}

// RouteDisabledList returns route_disabled as a list (nil when empty).
func (c Config) RouteDisabledList() []string {
	return routeList(c.RouteDisabled)
}

// UnmatchedDisabled returns the route_disabled labels that are not in
// route_machines, in route_disabled order. It returns nil when
// route_machines or route_disabled is empty: with no fleet configured
// yet there is nothing to compare against, so staging an exclusion
// before the fleet is not reported.
func (c Config) UnmatchedDisabled() []string {
	machines := c.RouteMachineList()
	disabled := c.RouteDisabledList()
	if len(machines) == 0 || len(disabled) == 0 {
		return nil
	}
	inFleet := make(map[string]struct{}, len(machines))
	for _, m := range machines {
		inFleet[m] = struct{}{}
	}
	var unmatched []string
	for _, d := range disabled {
		if _, ok := inFleet[d]; !ok {
			unmatched = append(unmatched, d)
		}
	}
	return unmatched
}

// routeList splits a normalized route list (nil when empty).
func routeList(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

// parseLabelList validates a route_machines or route_disabled value: a
// comma-separated list of machine labels. Each item is trimmed of surrounding
// spaces and must match router.LabelPattern; the empty value is valid, while
// an empty item, a non-matching label, a duplicate and more than
// maxRouteListItems items are not.
func parseLabelList(key, value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var list []string
	for _, item := range strings.Split(value, ",") {
		label := strings.TrimSpace(item)
		if label == "" {
			return nil, fmt.Errorf("%s: empty machine label", key)
		}
		if !router.LabelPattern.MatchString(label) {
			return nil, fmt.Errorf("%s: invalid machine label %q", key, label)
		}
		if _, dup := seen[label]; dup {
			return nil, fmt.Errorf("%s: duplicate machine label %q", key, label)
		}
		seen[label] = struct{}{}
		list = append(list, label)
		if len(list) > maxRouteListItems {
			return nil, fmt.Errorf("%s: at most %d machine labels", key, maxRouteListItems)
		}
	}
	return list, nil
}

// parseRouteProbeTimeout validates a route_probe_timeout_s value: an integer
// from 1 to 120.
func parseRouteProbeTimeout(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < routeProbeTimeoutMin || n > routeProbeTimeoutMax {
		return 0, fmt.Errorf("route_probe_timeout_s must be an integer from 1 to 120")
	}
	return n, nil
}
