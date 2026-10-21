//go:build windows

package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func backendExecutable(path string, _ os.FileInfo) bool {
	_, err := exec.LookPath(path)
	return err == nil
}

func trustedBackendChain(path string) error {
	for {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return fmt.Errorf("cannot inspect backend ACL")
		}
		dacl, _, err := sd.DACL()
		if err != nil {
			return fmt.Errorf("cannot inspect backend DACL")
		}
		if err := trustedBackendDACL(dacl); err != nil {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func trustedBackendDACL(dacl *windows.ACL) error {
	// A null DACL grants everyone full access. An empty DACL grants none.
	if dacl == nil {
		return fmt.Errorf("backend has an unrestricted DACL")
	}
	const fileDeleteChild = 0x40
	const writeMask = windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | fileDeleteChild
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("cannot inspect backend ACE")
		}
		// INHERIT_ONLY_ACE does not grant rights on the current object.
		if ace.Header.AceFlags&0x08 != 0 {
			continue
		}
		offset := uintptr(8) // header and mask, followed by the SID
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE, 9: // ordinary and callback allow
		case 5, 11: // object and callback-object allow
			// Object ACEs insert flags and up to two GUIDs before the SID.
			offset = 12
			flags := ace.SidStart
			if flags&1 != 0 {
				offset += 16
			}
			if flags&2 != 0 {
				offset += 16
			}
		default:
			continue // deny and audit ACEs do not grant permissions
		}
		if ace.Mask&writeMask == 0 {
			continue
		}
		if uintptr(ace.Header.AceSize) < offset+8 {
			return fmt.Errorf("backend path has a malformed ACE")
		}
		sid := (*windows.SID)(unsafe.Add(unsafe.Pointer(ace), offset))
		if !sid.IsValid() || uintptr(sid.Len()) > uintptr(ace.Header.AceSize)-offset {
			return fmt.Errorf("backend path has a malformed SID")
		}
		if broadBackendPrincipal(sid) {
			return fmt.Errorf("backend path grants write access to an untrusted group")
		}
	}
	return nil
}

func broadBackendPrincipal(sid *windows.SID) bool {
	for _, kind := range []windows.WELL_KNOWN_SID_TYPE{windows.WinWorldSid, windows.WinBuiltinUsersSid, windows.WinAuthenticatedUserSid, windows.WinInteractiveSid} {
		if sid.IsWellKnown(kind) {
			return true
		}
	}
	return false
}
