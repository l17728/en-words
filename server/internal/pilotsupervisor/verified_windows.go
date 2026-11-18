//go:build pilot_e2e && windows

package pilotsupervisor

import (
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsFileReadAttributes       = uint32(0x00000080)
	windowsReadControl              = uint32(0x00020000)
	windowsSynchronize              = uint32(0x00100000)
	windowsListDirectory            = uint32(0x00000001)
	windowsTraverse                 = uint32(0x00000020)
	windowsAllAccess                = uint32(0x001F01FF)
	windowsDirMask                  = uint32(0x001200A1)
	windowsFileMask                 = uint32(0x00120080)
	windowsStatusNoMoreFiles        = uint32(0x80000006)
	windowsStatusObjectNameNotFound = uint32(0xC0000034)
	windowsStatusObjectPathNotFound = uint32(0xC000003A)
	windowsStatusFileIsDirectory    = uint32(0xC00000BA)
	windowsStatusNotDirectory       = uint32(0xC0000103)
	windowsFileIdBothDirectoryInfo  = uint32(37)
)

type windowsVerifiedOps struct {
	expectedWorkerSID string
	closeHandle       func(windows.Handle) error
}

// An empty expected SID is deliberately fail-closed. The trusted launcher
// must pin this value before constructing a production anchor.
var windowsVerifiedNative verifiedNative = windowsVerifiedOps{}

type windowsFileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

type windowsFileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

type windowsFileStandardInfo struct {
	AllocationSize int64
	EndOfFile      int64
	NumberOfLinks  uint32
	DeletePending  uint8
	Directory      uint8
	Reserved       uint16
}

type windowsFileIDBothDirInfo struct {
	NextEntryOffset uint32
	FileIndex       uint32
	CreationTime    int64
	LastAccessTime  int64
	LastWriteTime   int64
	ChangeTime      int64
	EndOfFile       int64
	AllocationSize  int64
	FileAttributes  uint32
	FileNameLength  uint32
	EaSize          uint32
	ShortNameLength uint8
	Reserved        uint8
	ShortName       [12]uint16
	FileID          int64
	FileName        [1]uint16
}

var ntQueryDirectoryFileProc = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryDirectoryFile")
var windowsDirectoryEnumerationMu sync.Mutex

func (windowsVerifiedOps) open(parent uintptr, name string, kind verifiedKind) (uintptr, error) {
	if name == "" || strings.ContainsAny(name, "/\\:\x00") || name == "." || name == ".." {
		return 0, errVerifiedPathInvalid
	}
	ntName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, errVerifiedPathInvalid
	}
	oa := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(parent),
		ObjectName:    ntName,
		Attributes:    0, // Exact case: OBJ_CASE_INSENSITIVE is intentionally absent.
	}
	access := windowsFileReadAttributes | windowsReadControl | windowsSynchronize
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if kind == verifiedDirectory {
		access |= windowsListDirectory | windowsTraverse
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, access, &oa, &iosb, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN, options, 0, 0)
	if err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			switch uint32(status) {
			case windowsStatusObjectNameNotFound, windowsStatusObjectPathNotFound:
				return 0, errVerifiedMemberInvalid
			case windowsStatusFileIsDirectory, windowsStatusNotDirectory:
				return 0, errVerifiedPolicyMismatch
			}
		}
		return 0, errVerifiedIOFailed
	}
	return uintptr(h), nil
}

func getWindowsInfo[T any](handle windows.Handle, class uint32) (T, error) {
	var result T
	err := windows.GetFileInformationByHandleEx(handle, class, (*byte)(unsafe.Pointer(&result)), uint32(unsafe.Sizeof(result)))
	return result, err
}

func (ops windowsVerifiedOps) inspect(handle uintptr, kind verifiedKind) (verifiedIdentity, error) {
	h := windows.Handle(handle)
	var idInfo windowsFileIDInfo
	if err := windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&idInfo)), uint32(unsafe.Sizeof(idInfo))); err != nil {
		return verifiedIdentity{}, errVerifiedIOFailed
	}
	tag, err := getWindowsInfo[windowsFileAttributeTagInfo](h, windows.FileAttributeTagInfo)
	if err != nil {
		return verifiedIdentity{}, errVerifiedIOFailed
	}
	if tag.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || tag.ReparseTag != 0 {
		return verifiedIdentity{}, errVerifiedPathInvalid
	}
	standard, err := getWindowsInfo[windowsFileStandardInfo](h, windows.FileStandardInfo)
	if err != nil {
		return verifiedIdentity{}, errVerifiedIOFailed
	}
	actualKind := verifiedRegularFile
	if standard.Directory != 0 {
		actualKind = verifiedDirectory
	}
	if actualKind != kind || standard.DeletePending != 0 {
		return verifiedIdentity{}, errVerifiedPolicyMismatch
	}
	if kind == verifiedRegularFile && standard.NumberOfLinks != 1 {
		return verifiedIdentity{}, errVerifiedPolicyMismatch
	}
	if err := verifyWindowsSecurity(h, kind, ops.expectedWorkerSID); err != nil {
		return verifiedIdentity{}, err
	}
	return verifiedIdentity{
		volume:     idInfo.VolumeSerialNumber,
		object:     binary.LittleEndian.Uint64(idInfo.FileID[:8]),
		objectHigh: binary.LittleEndian.Uint64(idInfo.FileID[8:]),
		kind:       kind,
	}, nil
}

func verifyWindowsSecurity(handle windows.Handle, kind verifiedKind, expectedWorkerSID string) error {
	if !strings.HasPrefix(expectedWorkerSID, "S-1-5-80-") {
		return errVerifiedConfigurationMissing
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return errVerifiedIOFailed
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || owner.String() != "S-1-5-18" {
		return errVerifiedPolicyMismatch
	}
	control, _, err := sd.Control()
	if err != nil {
		return errVerifiedIOFailed
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errVerifiedPolicyMismatch
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 2 {
		return errVerifiedPolicyMismatch
	}
	workerSID, err := currentWorkerServiceSID()
	if err != nil {
		return errVerifiedIOFailed
	}
	if workerSID != expectedWorkerSID {
		return errVerifiedPolicyMismatch
	}
	workerMask := windowsFileMask
	if kind == verifiedDirectory {
		workerMask = windowsDirMask
	}
	seenWorker, seenSystem := false, false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return errVerifiedIOFailed
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
			return errVerifiedPolicyMismatch
		}
		sidPtr := (*windows.SID)(unsafe.Pointer(uintptr(unsafe.Pointer(ace)) + unsafe.Offsetof(ace.SidStart)))
		if sidPtr == nil {
			return errVerifiedIOFailed
		}
		sid := sidPtr.String()
		switch sid {
		case workerSID:
			if seenWorker || uint32(ace.Mask) != workerMask {
				return errVerifiedPolicyMismatch
			}
			seenWorker = true
		case "S-1-5-18":
			if seenSystem || uint32(ace.Mask) != windowsAllAccess {
				return errVerifiedPolicyMismatch
			}
			seenSystem = true
		default:
			return errVerifiedPolicyMismatch
		}
	}
	if !seenWorker || !seenSystem {
		return errVerifiedPolicyMismatch
	}
	return nil
}

func currentWorkerServiceSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	groups, err := token.GetTokenGroups()
	if err != nil || groups == nil {
		return "", errVerifiedIOFailed
	}
	var found string
	for _, group := range groups.AllGroups() {
		if group.Sid == nil {
			continue
		}
		sid := group.Sid.String()
		if strings.HasPrefix(sid, "S-1-5-80-") {
			if found != "" {
				return "", errVerifiedPolicyMismatch
			}
			found = sid
		}
	}
	if found == "" {
		return "", errVerifiedPolicyMismatch
	}
	return found, nil
}

func (windowsVerifiedOps) enumerate(handle uintptr) ([]string, error) {
	windowsDirectoryEnumerationMu.Lock()
	defer windowsDirectoryEnumerationMu.Unlock()
	if err := ntQueryDirectoryFileProc.Find(); err != nil {
		return nil, errVerifiedIOFailed
	}
	var names []string
	seen := make(map[string]struct{})
	var buffer [64 * 1024]byte
	firstCall := true
	for {
		var iosb windows.IO_STATUS_BLOCK
		restart := uintptr(0)
		if firstCall {
			restart = 1
			firstCall = false
		}
		status, _, _ := ntQueryDirectoryFileProc.Call(
			uintptr(windows.Handle(handle)), 0, 0, 0,
			uintptr(unsafe.Pointer(&iosb)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)),
			uintptr(windowsFileIdBothDirectoryInfo), 0, 0, restart,
		)
		code := uint32(status)
		if code == windowsStatusNoMoreFiles {
			break
		}
		if code != 0 {
			return nil, errVerifiedIOFailed
		}
		valid := int(iosb.Information)
		if valid <= 0 || valid > len(buffer) {
			return nil, errVerifiedIOFailed
		}
		for offset := 0; offset < valid; {
			entry := (*windowsFileIDBothDirInfo)(unsafe.Pointer(&buffer[offset]))
			if entry.FileNameLength%2 != 0 || entry.FileNameLength == 0 || int(entry.FileNameLength) > valid-offset-104 {
				return nil, errVerifiedIOFailed
			}
			ptr := unsafe.Pointer(uintptr(unsafe.Pointer(entry)) + 104)
			filename := unsafe.Slice((*uint16)(ptr), int(entry.FileNameLength/2))
			name := windows.UTF16ToString(filename)
			if name != "." && name != ".." {
				fold := strings.ToLower(name)
				if _, duplicate := seen[fold]; duplicate {
					return nil, errVerifiedPolicyMismatch
				}
				seen[fold] = struct{}{}
				names = append(names, name)
			}
			if entry.NextEntryOffset == 0 {
				break
			}
			if int(entry.NextEntryOffset) < 104 || offset+int(entry.NextEntryOffset) > valid {
				return nil, errVerifiedIOFailed
			}
			offset += int(entry.NextEntryOffset)
		}
	}
	return names, nil
}

func (ops windowsVerifiedOps) verifyBinding(parent uintptr, name string, expected verifiedIdentity, kind verifiedKind) error {
	entries, err := ops.enumerate(parent)
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if entry == name {
			found = true
			break
		}
	}
	if !found {
		return errVerifiedIdentityChanged
	}
	h, err := ops.open(parent, name, kind)
	if err != nil {
		return classifyWindowsBindingOpenError(err)
	}
	actual, inspectErr := ops.inspect(h, kind)
	closeErr := ops.close(h)
	if closeErr != nil {
		retainVerifiedOrphan(h)
		return errVerifiedCloseUnknown
	}
	if inspectErr != nil {
		return inspectErr
	}
	if actual != expected {
		return errVerifiedIdentityChanged
	}
	return nil
}

func classifyWindowsBindingOpenError(err error) error {
	classified := classifyOpenError(err)
	if errors.Is(classified, errVerifiedMemberInvalid) {
		return errVerifiedIdentityChanged
	}
	return classified
}

func (windowsVerifiedOps) checkFilesystem(handle uintptr) error {
	var fsName [64]uint16
	var serial, maxComponent, flags uint32
	err := windows.GetVolumeInformationByHandle(windows.Handle(handle), nil, 0, &serial, &maxComponent, &flags, &fsName[0], uint32(len(fsName)))
	if err != nil {
		return errVerifiedIOFailed
	}
	if windows.UTF16ToString(fsName[:]) != "NTFS" {
		return errVerifiedPolicyMismatch
	}
	return nil
}

func (ops windowsVerifiedOps) close(handle uintptr) error {
	var err error
	if ops.closeHandle != nil {
		err = ops.closeHandle(windows.Handle(handle))
	} else {
		err = windows.CloseHandle(windows.Handle(handle))
	}
	if err != nil {
		return errVerifiedCloseUnknown
	}
	return nil
}
