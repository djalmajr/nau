package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetRejectsRecordSeparators(t *testing.T) {
	for _, key := range Keys() {
		for _, sep := range []string{"\r", "\n", "\x00"} {
			t.Run(key+strings.ReplaceAll(sep, "\x00", "NUL"), func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "config")
				const initial = "# keep\nmachine_label=original\n"
				if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
					t.Fatal(err)
				}
				if err := Set(dir, key, "x"+sep+"machine_label=injected"); err == nil {
					t.Fatal("record separator accepted")
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != initial {
					t.Fatalf("refusal changed config: %q, %v", got, err)
				}
			})
		}
	}
}

func TestSetPreservesText(t *testing.T) {
	for _, tc := range []struct{ name, initial, key, value, want string }{
		{"LF", "# note\n\nmachine_label=old\npush_timeout_s=19\n", "machine_label", "new", "# note\n\nmachine_label=new\npush_timeout_s=19\n"},
		{"CRLF", "# note\r\n\r\nmachine_label=old\r\npush_timeout_s=19\r\n", "machine_label", "new", "# note\r\n\r\nmachine_label=new\r\npush_timeout_s=19\r\n"},
		// Load uses the last occurrence; the earlier duplicate must remain verbatim.
		{"duplicate", " machine_label = first\n# middle\n\u3000machine_label = last\r\n", "machine_label", "new", " machine_label = first\n# middle\nmachine_label=new\r\n"},
		{"append", "# note\r\npush_timeout_s=19\r\n", "machine_label", "new", "# note\r\npush_timeout_s=19\r\nmachine_label=new\r\n"},
		{"no final newline", "# note\r\npush_timeout_s=19", "machine_label", "new", "# note\r\npush_timeout_s=19\r\nmachine_label=new\r\n"},
		{"replace no newline", "# note\nmachine_label=old", "machine_label", "new", "# note\nmachine_label=new"},
		{"empty", "", "machine_label", "new", "machine_label=new\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config")
			if err := os.WriteFile(path, []byte(tc.initial), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Set(dir, tc.key, tc.value); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %q, want %q; err=%v", got, tc.want, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("mode: %v %v", info, err)
			}
		})
	}
}
