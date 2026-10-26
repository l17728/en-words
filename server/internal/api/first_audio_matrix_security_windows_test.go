//go:build windows

package api

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const firstAudioWindowsDirectoryFullAccess = 0x001F01FF

func verifyFirstAudioPrivateDirectory(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return fmt.Errorf("read run-root security descriptor")
	}

	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || tokenUser == nil || tokenUser.User.Sid == nil {
		return fmt.Errorf("read current user SID")
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(tokenUser.User.Sid) {
		return fmt.Errorf("run-root owner does not match current user")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("run-root DACL is not protected")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return fmt.Errorf("run-root DACL must contain one allow entry")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil ||
		ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		ace.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE ||
		uint32(ace.Mask) != firstAudioWindowsDirectoryFullAccess {
		return fmt.Errorf("run-root DACL is not owner-only full access")
	}
	aceSID := (*windows.SID)(unsafe.Pointer(uintptr(unsafe.Pointer(ace)) + unsafe.Offsetof(ace.SidStart)))
	if aceSID == nil || !aceSID.Equals(tokenUser.User.Sid) {
		return fmt.Errorf("run-root DACL grants access to a different SID")
	}
	return nil
}

func createFirstAudioPrivateTestDirectory(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return fmt.Errorf("read current user SID")
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sD:P(A;OICI;FA;;;%s)", sid, sid))
	if err != nil {
		return fmt.Errorf("create private test DACL")
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	if err := windows.CreateDirectory(windows.StringToUTF16Ptr(path), attributes); err != nil {
		return err
	}
	return nil
}

func TestFirstAudioMatrixWindowsRejectsUnprotectedRunRoot(t *testing.T) {
	artifactRoot := filepath.Join(t.TempDir(), ".test-artifacts")
	if err := os.Mkdir(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(artifactRoot, "first-audio-matrix-run_20261026")
	if err := os.Mkdir(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyFirstAudioPrivateDirectory(runRoot); err == nil {
		t.Fatal("default inherited run-root DACL was accepted")
	}
	if err := os.Remove(runRoot); err != nil {
		t.Fatal(err)
	}
	if err := createFirstAudioPrivateTestDirectory(runRoot); err != nil {
		t.Fatal(err)
	}
	if err := verifyFirstAudioPrivateDirectory(runRoot); err != nil {
		t.Fatalf("owner-only protected DACL rejected: %v", err)
	}
}
