//go:build cgo && pilot_e2e && linux

package pilotcontrol

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
)

const (
	pinnedSQLiteVersion      = "3.45.1"
	pinnedSQLiteVersionNum   = 3045001
	pinnedSQLiteSourceID     = "2024-01-30 16:01:20 e876e51a0ed5c5b3126f52e532044363a014bc594cfefa87ffb5b82257cc467a"
	pinnedVFSCaseCount       = 81
	pinnedVFSManifestState   = "DESIGN_CASES_NOT_EXECUTED"
	pinnedVFSDesignPath      = "docs/55-pilot-control-pinned-sqlite-vfs-sdd.md"
	pinnedVFSDesignSHA256    = "a19cb22a25e2312fc37cc5094886ad421cd284d022f5da6ab39e97b12eb2bdef"
	pinnedVFSAmendmentPath   = "docs/58-pilot-vfs-eexist-evidence-layer-sdd.md"
	pinnedVFSAmendmentSHA256 = "1b8538da1c5dd342e8c078db42c81dda02ee87c3a4b2246da1fa560c2c96cfe6"
	pinnedVFSManifestSHA256  = "5f11776eabb9a139672b35dd2ee92a71a52ca8687db2c6f4607a01342c191f2d"
)

var pinnedVFSRequiredEEXISTCases = []string{
	"VFS-OPEN-SIDECAR-01",
	"VFS-OPEN-SIDECAR-02",
	"VFS-OPEN-SIDECAR-07",
	"VFS-OPEN-SIDECAR-08",
}

//go:embed testdata/vfs-callback-cases.json
var pinnedVFSManifestFS embed.FS

type pinnedVFSManifest struct {
	ManifestVersion       int             `json:"manifest_version"`
	SourceSDD             string          `json:"source_sdd"`
	SourceSDDSHA256       string          `json:"source_sdd_sha256"`
	DesignAmendment       string          `json:"design_amendment"`
	DesignAmendmentSHA256 string          `json:"design_amendment_sha256"`
	Status                string          `json:"status"`
	Cases                 []pinnedVFSCase `json:"cases"`
}

type pinnedVFSCase struct {
	ID                    string          `json:"id"`
	Callback              string          `json:"callback"`
	Input                 json.RawMessage `json:"input"`
	ExpectedSQLiteRC      string          `json:"expected_sqlite_rc"`
	ExpectedOutputOrState json.RawMessage `json:"expected_output_or_state"`
	Injection             string          `json:"injection"`
	Barriers              []string        `json:"barriers"`
}

func TestPinnedSQLiteRuntimeIdentity(t *testing.T) {
	version, number, sourceID := sqlite3.Version()
	if version != pinnedSQLiteVersion || number != pinnedSQLiteVersionNum || sourceID != pinnedSQLiteSourceID {
		t.Fatalf("pinned SQLite identity mismatch: version=%q number=%d source_id=%q; want %q/%d/%q; golden generation is NOT RUN", version, number, sourceID, pinnedSQLiteVersion, pinnedSQLiteVersionNum, pinnedSQLiteSourceID)
	}
	t.Logf("sqlite_runtime_identity version=%s number=%d source_id=%s status=PASS", version, number, sourceID)
}

func TestPinnedVFSManifestRequiresEEXISTCases(t *testing.T) {
	manifestBytes, err := pinnedVFSManifestFS.ReadFile("testdata/vfs-callback-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest pinnedVFSManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := validatePinnedVFSRequiredEEXISTCases(manifest.Cases); err != nil {
		t.Fatalf("complete EEXIST row set rejected: %v", err)
	}
	duplicate := append([]pinnedVFSCase(nil), manifest.Cases...)
	duplicate = append(duplicate, manifest.Cases[0])
	if err := validatePinnedVFSRequiredEEXISTCases(duplicate); err == nil {
		t.Fatal("duplicate manifest ID was accepted by the EEXIST validator")
	}

	for _, missingID := range pinnedVFSRequiredEEXISTCases {
		t.Run("missing_"+missingID, func(t *testing.T) {
			mutated := make([]pinnedVFSCase, 0, len(manifest.Cases)-1)
			for _, testCase := range manifest.Cases {
				if testCase.ID != missingID {
					mutated = append(mutated, testCase)
				}
			}
			if err := validatePinnedVFSRequiredEEXISTCases(mutated); err == nil {
				t.Fatalf("missing required manifest row %q was accepted", missingID)
			}
		})
		t.Run("renamed_"+missingID, func(t *testing.T) {
			mutated := append([]pinnedVFSCase(nil), manifest.Cases...)
			for index := range mutated {
				if mutated[index].ID == missingID {
					mutated[index].ID += "-RENAMED"
				}
			}
			if err := validatePinnedVFSRequiredEEXISTCases(mutated); err == nil {
				t.Fatalf("renamed required manifest row %q was accepted", missingID)
			}
		})
	}

	for _, field := range []string{"caller_flags", "adapter_create_syscall", "adapter_create_flags", "adapter_create_mode", "adapter_create_resolve", "adapter_create_result", "eexist_inspect_syscall", "eexist_inspect_flags", "eexist_inspect_resolve", "retry_open_syscall", "retry_open_flags", "retry_open_resolve", "registered_generation", "role", "name"} {
		t.Run("semantic_"+field, func(t *testing.T) {
			mutated := append([]pinnedVFSCase(nil), manifest.Cases...)
			for index := range mutated {
				if mutated[index].ID != "VFS-OPEN-SIDECAR-02" {
					continue
				}
				var input map[string]any
				if err := json.Unmarshal(mutated[index].Input, &input); err != nil {
					t.Fatal(err)
				}
				input[field] = "INVALID"
				mutated[index].Input, err = json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := validatePinnedVFSRequiredEEXISTCases(mutated); err == nil {
				t.Fatalf("invalid %s mapping was accepted", field)
			}
		})
	}
	for _, field := range []string{"caller_flags", "adapter_create_syscall", "adapter_create_flags", "adapter_create_mode", "adapter_create_resolve", "adapter_create_result", "eexist_inspect_syscall", "eexist_inspect_flags", "eexist_inspect_resolve", "retry_open_syscall", "retry_open_flags", "retry_open_resolve"} {
		t.Run("exclusive_input_"+field, func(t *testing.T) {
			mutated := append([]pinnedVFSCase(nil), manifest.Cases...)
			for index := range mutated {
				if mutated[index].ID != "VFS-OPEN-SIDECAR-08" {
					continue
				}
				var input map[string]any
				if err := json.Unmarshal(mutated[index].Input, &input); err != nil {
					t.Fatal(err)
				}
				input[field] = "INVALID"
				mutated[index].Input, err = json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := validatePinnedVFSRequiredEEXISTCases(mutated); err == nil {
				t.Fatalf("invalid exclusive-create %s mapping was accepted", field)
			}
		})
	}

	for _, mutation := range []struct {
		name  string
		id    string
		field string
		value any
	}{
		{name: "sqlite_result", id: "VFS-OPEN-SIDECAR-02", field: "expected_sqlite_rc", value: "SQLITE_OK"},
		{name: "foreign_identity", id: "VFS-OPEN-SIDECAR-02", field: "readiness", value: "ready"},
		{name: "foreign_namespace_mutation", id: "VFS-OPEN-SIDECAR-02", field: "namespace_mutated", value: true},
		{name: "same_inode_namespace_mutation", id: "VFS-OPEN-SIDECAR-01", field: "namespace_mutated", value: true},
		{name: "same_inode_content_changed", id: "VFS-OPEN-SIDECAR-07", field: "registered_file_content_unchanged", value: false},
		{name: "foreign_sql_dispatch", id: "VFS-OPEN-SIDECAR-02", field: "application_sql_dispatch", value: 1},
		{name: "same_inode_generation_advance", id: "VFS-OPEN-SIDECAR-01", field: "generation_advanced", value: true},
		{name: "same_inode_out_flags", id: "VFS-OPEN-SIDECAR-01", field: "out_flags", value: "unexpected"},
		{name: "same_inode_read_dispatch", id: "VFS-OPEN-SIDECAR-07", field: "post_ready_read_dispatch", value: 0},
		{name: "same_inode_opath_close_count", id: "VFS-OPEN-SIDECAR-01", field: "opath_fd_close_count", value: 0},
		{name: "same_inode_retained_fd", id: "VFS-OPEN-SIDECAR-07", field: "retained_fd", value: "opath_inspection_fd"},
		{name: "exclusive_eexist_result", id: "VFS-OPEN-SIDECAR-08", field: "expected_sqlite_rc", value: "SQLITE_OK"},
		{name: "exclusive_namespace_mutation", id: "VFS-OPEN-SIDECAR-08", field: "namespace_mutated", value: true},
		{name: "exclusive_sql_dispatch", id: "VFS-OPEN-SIDECAR-08", field: "application_sql_dispatch", value: 1},
		{name: "exclusive_opath_inspected", id: "VFS-OPEN-SIDECAR-08", field: "opath_fd_close_count", value: 1},
		{name: "exclusive_retry_fd_retained", id: "VFS-OPEN-SIDECAR-08", field: "retained_fd", value: "usable_retry_fd"},
		{name: "exclusive_readiness_changed", id: "VFS-OPEN-SIDECAR-08", field: "readiness", value: "fenced"},
		{name: "exclusive_generation_advanced", id: "VFS-OPEN-SIDECAR-08", field: "generation_advanced", value: true},
		{name: "exclusive_registered_content_changed", id: "VFS-OPEN-SIDECAR-08", field: "registered_file_content_unchanged", value: false},
		{name: "exclusive_post_ready_read_dispatch", id: "VFS-OPEN-SIDECAR-08", field: "post_ready_read_dispatch", value: 1},
	} {
		t.Run("oracle_"+mutation.name, func(t *testing.T) {
			mutated := append([]pinnedVFSCase(nil), manifest.Cases...)
			for index := range mutated {
				if mutated[index].ID != mutation.id {
					continue
				}
				if mutation.field == "expected_sqlite_rc" {
					mutated[index].ExpectedSQLiteRC = mutation.value.(string)
					continue
				}
				var state map[string]any
				if err := json.Unmarshal(mutated[index].ExpectedOutputOrState, &state); err != nil {
					t.Fatal(err)
				}
				state[mutation.field] = mutation.value
				mutated[index].ExpectedOutputOrState, err = json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := validatePinnedVFSRequiredEEXISTCases(mutated); err == nil {
				t.Fatalf("invalid %s oracle was accepted", mutation.name)
			}
		})
	}
	t.Run("barriers", func(t *testing.T) {
		mutated := append([]pinnedVFSCase(nil), manifest.Cases...)
		for index := range mutated {
			if mutated[index].ID == "VFS-OPEN-SIDECAR-02" {
				mutated[index].Barriers = append([]string(nil), mutated[index].Barriers...)
				mutated[index].Barriers[0] = "unexpected_barrier"
			}
		}
		if err := validatePinnedVFSRequiredEEXISTCases(mutated); err == nil {
			t.Fatal("altered EEXIST barrier list was accepted")
		}
	})
}

func validatePinnedVFSRequiredEEXISTCases(cases []pinnedVFSCase) error {
	byID := make(map[string]pinnedVFSCase, len(cases))
	for _, testCase := range cases {
		if _, exists := byID[testCase.ID]; exists {
			return fmt.Errorf("duplicate manifest ID %q", testCase.ID)
		}
		byID[testCase.ID] = testCase
	}
	expected := map[string]struct {
		role                    string
		name                    string
		callerFlags             string
		adapterCreateSyscall    string
		adapterCreateFlags      string
		adapterCreateMode       string
		adapterCreateResolve    string
		adapterCreateResult     string
		existInspectSyscall     string
		existInspectFlags       string
		existInspectResolve     string
		retryOpenSyscall        string
		retryOpenFlags          string
		retryOpenResolve        string
		registeredGeneration    string
		injection               string
		sqliteRC                string
		identity                string
		outFlags                string
		readiness               string
		generationAdvanced      *bool
		namespaceMutated        *bool
		applicationSQLDispatch  *int
		postReadyReadDispatch   *int
		registeredContentStable *bool
		opathFDClosedCount      *int
		retainedFD              string
		barriers                []string
	}{
		"VFS-OPEN-SIDECAR-01": {role: "WAL", name: "operations.sqlite-wal", callerFlags: "READWRITE|CREATE|WAL", adapterCreateSyscall: "openat2", adapterCreateFlags: "O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC", adapterCreateMode: "0600", adapterCreateResolve: "RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV", adapterCreateResult: "EEXIST", existInspectSyscall: "openat2", existInspectFlags: "O_PATH|O_NOFOLLOW|O_CLOEXEC", existInspectResolve: "RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV", retryOpenSyscall: "openat2", retryOpenFlags: "O_RDWR|O_NOFOLLOW|O_CLOEXEC", retryOpenResolve: "RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV", registeredGeneration: "same_inode", injection: "pause_after_eexist_before_reopen", sqliteRC: "SQLITE_OK", identity: "registered_generation_inode", outFlags: "exact_sqlite3451_vfs_flags_golden", generationAdvanced: boolPointer(false), namespaceMutated: boolPointer(false), postReadyReadDispatch: intPointer(1), registeredContentStable: boolPointer(true), opathFDClosedCount: intPointer(1), retainedFD: "usable_retry_fd", barriers: []string{"eexist.wal.same.after_eexist_before_inspect", "eexist.wal.same.after_opath_before_open", "eexist.wal.same.after_open_before_final_name", "eexist.wal.same.before_publish"}},
		"VFS-OPEN-SIDECAR-02": {role: "JOURNAL", name: "operations.sqlite-journal", callerFlags: "READWRITE|CREATE|MAIN_JOURNAL", adapterCreateSyscall: "openat2", adapterCreateFlags: "O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC", adapterCreateMode: "0600", adapterCreateResolve: "RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV", adapterCreateResult: "EEXIST", existInspectSyscall: "openat2", existInspectFlags: "O_PATH|O_NOFOLLOW|O_CLOEXEC", existInspectResolve: "RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV", retryOpenSyscall: "openat2", retryOpenFlags: "O_RDWR|O_NOFOLLOW|O_CLOEXEC", retryOpenResolve: "RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV", registeredGeneration: "different_inode", injection: "identity_mismatch_on_retry_open", sqliteRC: "SQLITE_CANTOPEN", readiness: "fenced", generationAdvanced: boolPointer(false), namespaceMutated: boolPointer(false), applicationSQLDispatch: intPointer(0), registeredContentStable: boolPointer(true), barriers: []string{"eexist.journal.foreign.after_eexist_before_inspect", "eexist.journal.foreign.after_opath_before_open", "eexist.journal.foreign.after_open_before_final_name"}},
		"VFS-OPEN-SIDECAR-07": {role: "JOURNAL", name: "operations.sqlite-journal", callerFlags: "READWRITE|CREATE|MAIN_JOURNAL", adapterCreateSyscall: "openat2", adapterCreateFlags: "O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC", adapterCreateMode: "0600", adapterCreateResolve: "RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV", adapterCreateResult: "EEXIST", existInspectSyscall: "openat2", existInspectFlags: "O_PATH|O_NOFOLLOW|O_CLOEXEC", existInspectResolve: "RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV", retryOpenSyscall: "openat2", retryOpenFlags: "O_RDWR|O_NOFOLLOW|O_CLOEXEC", retryOpenResolve: "RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV", registeredGeneration: "same_inode", injection: "pause_after_eexist_before_reopen", sqliteRC: "SQLITE_OK", identity: "registered_generation_inode", outFlags: "exact_sqlite3451_vfs_flags_golden", generationAdvanced: boolPointer(false), namespaceMutated: boolPointer(false), postReadyReadDispatch: intPointer(1), registeredContentStable: boolPointer(true), opathFDClosedCount: intPointer(1), retainedFD: "usable_retry_fd", barriers: []string{"eexist.journal.same.after_eexist_before_inspect", "eexist.journal.same.after_opath_before_open", "eexist.journal.same.after_open_before_final_name", "eexist.journal.same.before_publish"}},
		"VFS-OPEN-SIDECAR-08": {role: "WAL", name: "operations.sqlite-wal", callerFlags: "READWRITE|CREATE|EXCLUSIVE|WAL", adapterCreateSyscall: "openat2", adapterCreateFlags: "O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC", adapterCreateMode: "0600", adapterCreateResolve: "RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV", adapterCreateResult: "EEXIST", existInspectSyscall: "not_called", existInspectFlags: "", existInspectResolve: "", retryOpenSyscall: "not_called", retryOpenFlags: "", retryOpenResolve: "", registeredGeneration: "same_inode", injection: "exclusive_create_eexist_no_reopen", sqliteRC: "SQLITE_CANTOPEN", identity: "registered_generation_inode", readiness: "ready", generationAdvanced: boolPointer(false), namespaceMutated: boolPointer(false), applicationSQLDispatch: intPointer(0), postReadyReadDispatch: intPointer(0), registeredContentStable: boolPointer(true), opathFDClosedCount: intPointer(0), retainedFD: "none", barriers: []string{}},
	}
	for _, requiredID := range pinnedVFSRequiredEEXISTCases {
		testCase, ok := byID[requiredID]
		if !ok {
			return fmt.Errorf("required EEXIST manifest row %q is missing", requiredID)
		}
		want := expected[requiredID]
		var input struct {
			Role                 string `json:"role"`
			Name                 string `json:"name"`
			CallerFlags          string `json:"caller_flags"`
			AdapterCreateSyscall string `json:"adapter_create_syscall"`
			AdapterCreateFlags   string `json:"adapter_create_flags"`
			AdapterCreateMode    string `json:"adapter_create_mode"`
			AdapterCreateResolve string `json:"adapter_create_resolve"`
			AdapterCreateResult  string `json:"adapter_create_result"`
			ExistInspectSyscall  string `json:"eexist_inspect_syscall"`
			ExistInspectFlags    string `json:"eexist_inspect_flags"`
			ExistInspectResolve  string `json:"eexist_inspect_resolve"`
			RetryOpenSyscall     string `json:"retry_open_syscall"`
			RetryOpenFlags       string `json:"retry_open_flags"`
			RetryOpenResolve     string `json:"retry_open_resolve"`
			RegisteredGeneration string `json:"registered_generation"`
		}
		if err := json.Unmarshal(testCase.Input, &input); err != nil {
			return fmt.Errorf("decode required EEXIST manifest row %q input: %w", requiredID, err)
		}
		if testCase.Callback != "xOpen" || input.Role != want.role || input.Name != want.name || input.CallerFlags != want.callerFlags || input.AdapterCreateSyscall != want.adapterCreateSyscall || input.AdapterCreateFlags != want.adapterCreateFlags || input.AdapterCreateMode != want.adapterCreateMode || input.AdapterCreateResolve != want.adapterCreateResolve || input.AdapterCreateResult != want.adapterCreateResult || input.ExistInspectSyscall != want.existInspectSyscall || input.ExistInspectFlags != want.existInspectFlags || input.ExistInspectResolve != want.existInspectResolve || input.RetryOpenSyscall != want.retryOpenSyscall || input.RetryOpenFlags != want.retryOpenFlags || input.RetryOpenResolve != want.retryOpenResolve || input.RegisteredGeneration != want.registeredGeneration || testCase.Injection != want.injection || testCase.ExpectedSQLiteRC != want.sqliteRC || !equalStringSlices(testCase.Barriers, want.barriers) {
			return fmt.Errorf("required EEXIST manifest row %q has unexpected callback/input/result mapping", requiredID)
		}
		var state struct {
			Identity                       string `json:"identity"`
			Readiness                      string `json:"readiness"`
			OutFlags                       string `json:"out_flags"`
			GenerationAdvanced             *bool  `json:"generation_advanced"`
			NamespaceMutated               *bool  `json:"namespace_mutated"`
			ApplicationSQLDispatch         *int   `json:"application_sql_dispatch"`
			PostReadyReadDispatch          *int   `json:"post_ready_read_dispatch"`
			RegisteredFileContentUnchanged *bool  `json:"registered_file_content_unchanged"`
			OPathFDCloseCount              *int   `json:"opath_fd_close_count"`
			RetainedFD                     string `json:"retained_fd"`
		}
		if err := json.Unmarshal(testCase.ExpectedOutputOrState, &state); err != nil {
			return fmt.Errorf("decode required EEXIST manifest row %q oracle: %w", requiredID, err)
		}
		if state.Identity != want.identity || state.Readiness != want.readiness || state.OutFlags != want.outFlags || state.RetainedFD != want.retainedFD || !equalIntPointer(state.OPathFDCloseCount, want.opathFDClosedCount) || !equalBoolPointer(state.GenerationAdvanced, want.generationAdvanced) || !equalBoolPointer(state.NamespaceMutated, want.namespaceMutated) || !equalBoolPointer(state.RegisteredFileContentUnchanged, want.registeredContentStable) || !equalIntPointer(state.ApplicationSQLDispatch, want.applicationSQLDispatch) || !equalIntPointer(state.PostReadyReadDispatch, want.postReadyReadDispatch) {
			return fmt.Errorf("required EEXIST manifest row %q has unexpected state oracle", requiredID)
		}
	}
	return nil
}

func boolPointer(value bool) *bool { return &value }

func intPointer(value int) *int { return &value }

func equalBoolPointer(left, right *bool) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func equalIntPointer(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestPinnedVFSCallbackManifestIntegrityAndNotRunStatus(t *testing.T) {
	manifestBytes, err := pinnedVFSManifestFS.ReadFile("testdata/vfs-callback-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	manifestSum := sha256.Sum256(manifestBytes)
	if got := hex.EncodeToString(manifestSum[:]); got != pinnedVFSManifestSHA256 {
		t.Fatalf("callback manifest SHA-256=%s, want %s; manifest integrity FAIL", got, pinnedVFSManifestSHA256)
	}
	var manifest pinnedVFSManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode callback manifest: %v", err)
	}
	if manifest.ManifestVersion != 1 || manifest.SourceSDD != pinnedVFSDesignPath || manifest.SourceSDDSHA256 != pinnedVFSDesignSHA256 || manifest.DesignAmendment != pinnedVFSAmendmentPath || manifest.DesignAmendmentSHA256 != pinnedVFSAmendmentSHA256 || manifest.Status != pinnedVFSManifestState {
		t.Fatalf("unexpected manifest metadata: version=%d source=%q source_sha=%q amendment=%q amendment_sha=%q status=%q", manifest.ManifestVersion, manifest.SourceSDD, manifest.SourceSDDSHA256, manifest.DesignAmendment, manifest.DesignAmendmentSHA256, manifest.Status)
	}
	if len(manifest.Cases) != pinnedVFSCaseCount {
		t.Fatalf("manifest case count=%d, want %d", len(manifest.Cases), pinnedVFSCaseCount)
	}
	if err := validatePinnedVFSRequiredEEXISTCases(manifest.Cases); err != nil {
		t.Fatal(err)
	}

	seen := make(map[string]struct{}, len(manifest.Cases))
	for _, testCase := range manifest.Cases {
		if testCase.ID == "" || testCase.Callback == "" || len(testCase.Input) == 0 || testCase.ExpectedSQLiteRC == "" || len(testCase.ExpectedOutputOrState) == 0 || testCase.Injection == "" {
			t.Errorf("incomplete callback case: %+v", testCase)
			continue
		}
		var expected map[string]json.RawMessage
		if err := json.Unmarshal(testCase.ExpectedOutputOrState, &expected); err != nil {
			t.Errorf("case %q expected state must be a JSON object: %v", testCase.ID, err)
			continue
		}
		var errorLayer string
		if raw, ok := expected["error_layer"]; ok {
			if err := json.Unmarshal(raw, &errorLayer); err != nil {
				t.Errorf("case %q error_layer must be a string: %v", testCase.ID, err)
				continue
			}
		}
		if testCase.ExpectedSQLiteRC == "N/A_PRE_SQLITE_PREFLIGHT" {
			switch {
			case testCase.Callback == "xFullPathname" && errorLayer == "PilotControl_URI_preflight":
				if string(expected["filesystem_callbacks"]) != "0" || string(expected["application_sql_dispatch"]) != "0" {
					t.Errorf("case %q URI preflight must assert zero filesystem callbacks and application SQL dispatch", testCase.ID)
				}
			case testCase.Callback == "root_preflight" && errorLayer == "PilotControl_root_preflight":
				if string(expected["application_sql_dispatch"]) != "0" || string(expected["readiness"]) != `"not_ready"` {
					t.Errorf("case %q root preflight must assert zero SQL dispatch and not-ready state", testCase.ID)
				}
			default:
				t.Errorf("case %q has invalid pre-SQLite callback/error layer %q/%q", testCase.ID, testCase.Callback, errorLayer)
			}
		} else if errorLayer != "" {
			if testCase.Callback != "xOpen" || errorLayer != "SQLite_VFS_xOpen" || testCase.ExpectedSQLiteRC != "SQLITE_CANTOPEN" {
				t.Errorf("case %q has invalid SQLite callback/error layer/result %q/%q/%q", testCase.ID, testCase.Callback, errorLayer, testCase.ExpectedSQLiteRC)
			}
		}
		if _, duplicate := seen[testCase.ID]; duplicate {
			t.Errorf("duplicate callback case ID %q", testCase.ID)
		}
		seen[testCase.ID] = struct{}{}
	}
	if t.Failed() {
		return
	}
	if len(seen) != pinnedVFSCaseCount {
		t.Fatalf("unique case IDs=%d, want %d", len(seen), pinnedVFSCaseCount)
	}

	manifestPath := filepath.Join("testdata", "vfs-callback-cases.json")
	sourcePath, err := currentPilotControlTestFile(t)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", "..", ".."))
	sddPath := filepath.Join(root, filepath.FromSlash(manifest.SourceSDD))
	sdd, err := os.ReadFile(sddPath)
	if err != nil {
		t.Fatalf("read bound design document %q: %v", sddPath, err)
	}
	sum := sha256.Sum256(sdd)
	if got := hex.EncodeToString(sum[:]); got != manifest.SourceSDDSHA256 {
		t.Fatalf("manifest design binding=%s, actual=%s; manifest integrity FAIL", manifest.SourceSDDSHA256, got)
	}
	amendmentPath := filepath.Join(root, filepath.FromSlash(manifest.DesignAmendment))
	amendment, err := os.ReadFile(amendmentPath)
	if err != nil {
		t.Fatalf("read bound design amendment %q: %v", amendmentPath, err)
	}
	amendmentSum := sha256.Sum256(amendment)
	if got := hex.EncodeToString(amendmentSum[:]); got != manifest.DesignAmendmentSHA256 {
		t.Fatalf("manifest design amendment binding=%s, actual=%s; manifest integrity FAIL", manifest.DesignAmendmentSHA256, got)
	}
	t.Logf("manifest=%s cases=%d unique=%d source_sdd_sha256=%s design_amendment_sha256=%s integrity=PASS", manifestPath, len(manifest.Cases), len(seen), manifest.SourceSDDSHA256, manifest.DesignAmendmentSHA256)

	statusRows := make([]map[string]string, 0, len(manifest.Cases))
	for _, testCase := range manifest.Cases {
		statusRows = append(statusRows, map[string]string{"id": testCase.ID, "status": "NOT RUN", "reason": "private Pilot Control VFS is not implemented; manifest loading is not callback execution"})
	}
	status, err := json.MarshalIndent(struct {
		SourceSHA256 string              `json:"manifest_sha256"`
		Status       string              `json:"overall_status"`
		Cases        []map[string]string `json:"cases"`
	}{SourceSHA256: hex.EncodeToString(manifestSum[:]), Status: "NOT RUN", Cases: statusRows}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	artifactDir, err := pilotVFSManifestArtifactDir(root, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PILOT_VFS_MANIFEST_ARTIFACT_DIR") == "" {
		if err := os.MkdirAll(artifactDir, 0700); err != nil {
			t.Fatalf("create manifest artifact parent: %v", err)
		}
		artifactDir, err = os.MkdirTemp(artifactDir, "run-")
		if err != nil {
			t.Fatalf("create unique manifest artifact directory: %v", err)
		}
	} else if err := os.MkdirAll(artifactDir, 0700); err != nil {
		t.Fatalf("create artifact directory: %v", err)
	}
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatalf("inspect manifest artifact directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("manifest artifact directory must be new and empty to preserve immutable evidence; found %d existing entries", len(entries))
	}
	statusPath := filepath.Join(artifactDir, "vfs-callback-status-not-run.json")
	file, err := os.OpenFile(statusPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatalf("write callback status report: %v", err)
	}
	if _, err := file.Write(append(status, '\n')); err != nil {
		_ = file.Close()
		t.Fatalf("write callback status report: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close callback status report: %v", err)
	}
	t.Logf("callback_behavior_status=NOT RUN cases=%d artifact=%s", len(statusRows), filepath.Base(statusPath))
}

func pilotVFSManifestArtifactDir(root string, getenv func(string) string) (string, error) {
	manifestDir := getenv("PILOT_VFS_MANIFEST_ARTIFACT_DIR")
	if manifestDir == "" {
		manifestDir = filepath.Join(root, ".test-artifacts", "pilot-vfs-manifest")
	}
	manifestDir, err := filepath.Abs(manifestDir)
	if err != nil {
		return "", fmt.Errorf("resolve manifest artifact directory: %w", err)
	}
	if candidateDir := getenv("PILOT_VFS_ARTIFACT_DIR"); candidateDir != "" {
		candidateDir, err = pilotVFSCanonicalProspectivePath(candidateDir)
		if err != nil {
			return "", fmt.Errorf("resolve candidate artifact directory: %w", err)
		}
		manifestCanonical, err := pilotVFSCanonicalProspectivePath(manifestDir)
		if err != nil {
			return "", fmt.Errorf("resolve manifest artifact directory: %w", err)
		}
		if pilotVFSPathsOverlap(manifestCanonical, candidateDir) {
			return "", fmt.Errorf("manifest artifact directory must be separate from PILOT_VFS_ARTIFACT_DIR")
		}
	}
	return manifestDir, nil
}

func pilotVFSCanonicalProspectivePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	missing := make([]string, 0)
	current := filepath.Clean(absolute)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %q", absolute)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pilotVFSPathsOverlap(a, b string) bool {
	within := func(path, parent string) bool {
		rel, err := filepath.Rel(parent, path)
		return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
	}
	return within(a, b) || within(b, a)
}

func TestPilotVFSManifestArtifactDirIsolatedFromCandidateDir(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	manifest := filepath.Join(root, "manifest")
	lookup := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	got, err := pilotVFSManifestArtifactDir(root, lookup(map[string]string{
		"PILOT_VFS_ARTIFACT_DIR":          candidate,
		"PILOT_VFS_MANIFEST_ARTIFACT_DIR": manifest,
	}))
	if err != nil {
		t.Fatalf("separate artifact directories rejected: %v", err)
	}
	if got != manifest {
		t.Fatalf("manifest artifact dir=%q, want %q", got, manifest)
	}
	for _, overlapping := range []string{candidate, filepath.Join(candidate, "nested"), filepath.Dir(candidate)} {
		_, err := pilotVFSManifestArtifactDir(root, lookup(map[string]string{
			"PILOT_VFS_ARTIFACT_DIR":          candidate,
			"PILOT_VFS_MANIFEST_ARTIFACT_DIR": overlapping,
		}))
		if err == nil {
			t.Errorf("overlapping manifest artifact directory %q was accepted", overlapping)
		}
	}
	alias := filepath.Join(root, "candidate-alias")
	if err := os.Mkdir(candidate, 0700); err != nil {
		t.Fatalf("create candidate directory for symlink case: %v", err)
	}
	if err := os.Symlink(candidate, alias); err != nil {
		t.Fatalf("create symlink alias: %v", err)
	}
	_, err = pilotVFSManifestArtifactDir(root, lookup(map[string]string{
		"PILOT_VFS_ARTIFACT_DIR":          candidate,
		"PILOT_VFS_MANIFEST_ARTIFACT_DIR": alias,
	}))
	if err == nil {
		t.Fatal("symlink alias overlapping candidate artifact directory was accepted")
	}
	targetRoot := filepath.Join(root, "symlink-target")
	if err := os.Mkdir(targetRoot, 0700); err != nil {
		t.Fatalf("create symlink target root: %v", err)
	}
	ancestorAlias := filepath.Join(root, "target-alias")
	if err := os.Symlink(targetRoot, ancestorAlias); err != nil {
		t.Fatalf("create symlink ancestor: %v", err)
	}
	_, err = pilotVFSManifestArtifactDir(root, lookup(map[string]string{
		"PILOT_VFS_ARTIFACT_DIR":          filepath.Join(targetRoot, "missing", "candidate"),
		"PILOT_VFS_MANIFEST_ARTIFACT_DIR": filepath.Join(ancestorAlias, "missing"),
	}))
	if err == nil {
		t.Fatal("symlink ancestor plus missing path suffix overlap was accepted")
	}
}

func currentPilotControlTestFile(t *testing.T) (string, error) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller did not return source path")
	}
	return filepath.Abs(file)
}
