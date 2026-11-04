package cli

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestUserConfigRoot covers the four macOS rules in the order the
// resolver applies them. The fake getenv keeps the process environment
// out of the test, and the TempDir paths are absolute on every OS.
func TestUserConfigRoot(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	cases := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{
			name: "absolute XDG_CONFIG_HOME wins",
			env:  map[string]string{"XDG_CONFIG_HOME": xdg, "HOME": home},
			want: xdg,
		},
		{
			name:    "relative XDG_CONFIG_HOME is an error",
			env:     map[string]string{"XDG_CONFIG_HOME": ".config", "HOME": home},
			wantErr: true,
		},
		{
			name: "empty XDG_CONFIG_HOME falls back to HOME",
			env:  map[string]string{"XDG_CONFIG_HOME": "", "HOME": home},
			want: filepath.Join(home, ".config"),
		},
		{
			name:    "empty XDG_CONFIG_HOME and empty HOME is an error",
			env:     map[string]string{"XDG_CONFIG_HOME": "", "HOME": ""},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := userConfigRoot("darwin", func(key string) string {
				return tc.env[key]
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("userConfigRoot = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("userConfigRoot: %v", err)
			}
			if got != tc.want {
				t.Fatalf("userConfigRoot = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEnvFromOS: each variable gets its own temp dir, so the resolved
// ConfigDir must be the APPDATA dir on Windows and the XDG dir
// everywhere else, including on macOS.
func TestEnvFromOS(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	appdata := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("APPDATA", appdata)
	want := filepath.Join(xdg, "nau")
	if runtime.GOOS == "windows" {
		want = filepath.Join(appdata, "nau")
	}
	if got := EnvFromOS().ConfigDir; got != want {
		t.Fatalf("EnvFromOS ConfigDir = %q, want %q", got, want)
	}
}
