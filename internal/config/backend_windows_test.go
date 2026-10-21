//go:build windows

package config

import (
	"fmt"
	"testing"

	"golang.org/x/sys/windows"
)

func TestBackendDACL(t *testing.T) {
	for _, sid := range []string{"WD", "BU", "AU", "IU"} {
		for _, rights := range []string{"GW", "GA", "WD", "WO", "SD", "0x2", "0x4", "0x10", "0x40", "0x100"} {
			t.Run(sid+rights, func(t *testing.T) {
				sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:(A;;%s;;;%s)", rights, sid))
				if err != nil {
					t.Fatal(err)
				}
				dacl, _, err := sd.DACL()
				if err != nil {
					t.Fatal(err)
				}
				if err := trustedBackendDACL(dacl); err == nil {
					t.Fatal("broad write ACE accepted")
				}
			})
		}
	}
	for _, sddl := range []string{"D:", "D:(A;;GRGX;;;WD)", "D:(A;;GA;;;SY)(A;;GA;;;BA)", "D:(A;IO;GA;;;WD)", "D:(D;;GA;;;WD)(A;;GRGX;;;WD)"} {
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		dacl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		if err := trustedBackendDACL(dacl); err != nil {
			t.Fatalf("safe ACL %q refused: %v", sddl, err)
		}
	}
	if err := trustedBackendDACL(nil); err == nil {
		t.Fatal("null DACL accepted")
	}
	for _, sddl := range []string{"D:(OA;;GW;;;WD)", "D:(OA;;GW;00112233-4455-6677-8899-aabbccddeeff;;BU)", "D:(A;;GA;;;WD)(D;;GA;;;WD)"} {
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		dacl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		if err := trustedBackendDACL(dacl); err == nil {
			t.Fatalf("broad write ACL %q accepted", sddl)
		}
	}
}
