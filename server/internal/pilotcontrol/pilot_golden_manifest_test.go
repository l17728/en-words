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
	pinnedVFSDesignSHA256    = "fa957bfc9b2b039abfa7b7d72d831fc55e616f72074dd8dc8f8e9b4367014dea"
	pinnedVFSAmendmentPath   = "docs/58-pilot-vfs-eexist-evidence-layer-sdd.md"
	pinnedVFSAmendmentSHA256 = "1b8538da1c5dd342e8c078db42c81dda02ee87c3a4b2246da1fa560c2c96cfe6"
	pinnedVFSRecoveryPath = "docs/62-pilot-vfs-initialization-and-recovery-amendment.md"
	pinnedVFSRecoverySHA256 = "44333074581018d3a98d0b0cf8aab8edaf53d0c195ea3429bb71a33221e04ed8"
	pinnedVFSRecoveryManifestPath = "server/internal/pilotcontrol/testdata/pilot-vfs-recovery-cases.json"
	pinnedVFSRecoveryManifestSHA256 = "9e4899bac00f448a790d7d34e2fb5d720a74b2d7c839496a52ade948346ed3ea"
	pinnedVFSRecoveryCaseCount = 50
	pinnedVFSManifestSHA256  = "3933ca6822d6566b8d87c0224abc6419688ff4b3006472857dd9cc0892ebe6a3"
)

var pinnedVFSRequiredEEXISTCases = []string{
	"VFS-OPEN-SIDECAR-01",
	"VFS-OPEN-SIDECAR-02",
	"VFS-OPEN-SIDECAR-07",
	"VFS-OPEN-SIDECAR-08",
}

//go:embed testdata/vfs-callback-cases.json
var pinnedVFSManifestFS embed.FS

//go:embed testdata/pilot-vfs-recovery-cases.json
var pinnedVFSRecoveryManifestFS embed.FS

type pinnedVFSManifest struct {
	ManifestVersion       int             `json:"manifest_version"`
	SourceSDD             string          `json:"source_sdd"`
	SourceSDDSHA256       string          `json:"source_sdd_sha256"`
	DesignAmendment       string          `json:"design_amendment"`
	DesignAmendmentSHA256 string          `json:"design_amendment_sha256"`
	RecoveryAmendment     string          `json:"recovery_amendment"`
	RecoveryAmendmentSHA256 string       `json:"recovery_amendment_sha256"`
	RecoveryManifest      string          `json:"recovery_manifest"`
	RecoveryManifestSHA256 string         `json:"recovery_manifest_sha256"`
	Status                string          `json:"status"`
	Cases                 []pinnedVFSCase `json:"cases"`
}

type pinnedVFSRecoveryManifest struct {
	ManifestVersion int                  `json:"manifest_version"`
	SourceSDD       string               `json:"source_sdd"`
	SourceSDDSHA256 string               `json:"source_sdd_sha256"`
	Status          string               `json:"status"`
	Cases           []pinnedVFSRecoveryCase `json:"cases"`
}

type pinnedVFSRecoveryCase struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Oracle struct {
		ExpectedReadiness       string `json:"expected_readiness"`
		ApplicationDispatch     int    `json:"application_dispatch"`
		ApplicationWritesAllowed bool   `json:"application_writes_allowed"`
		Reseed                  bool   `json:"reseed"`
		RequiredAssertion       string `json:"required_assertion"`
		PagerRecoveryWriteAllowed bool  `json:"pager_recovery_write_allowed"`
		NamespaceMutation       string `json:"namespace_mutation"`
		Setup                   string `json:"setup"`
		Barrier                 string `json:"barrier"`
		SuspectEntryPreserved bool `json:"suspect_entry_preserved"`
		ServiceNamespaceMutations int `json:"service_namespace_mutations"`
		SQLiteEntryCalls int `json:"sqlite_entry_calls"`
		CloseEveryAcquiredFDOnce bool `json:"close_every_acquired_fd_once"`
		AcquiredFDCloseBalance int `json:"acquired_fd_close_balance"`
		PriorProcessDrainProven *bool `json:"prior_process_drain_proven"`
		UnlinkDone *bool `json:"unlink_done"`
		Durability string `json:"durability"`
		RollbackReported *bool `json:"rollback_reported"`
		LifecycleLockOutcome string `json:"lifecycle_lock_outcome"`
	} `json:"oracle"`
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
	if manifest.ManifestVersion != 1 || manifest.SourceSDD != pinnedVFSDesignPath || manifest.SourceSDDSHA256 != pinnedVFSDesignSHA256 || manifest.DesignAmendment != pinnedVFSAmendmentPath || manifest.DesignAmendmentSHA256 != pinnedVFSAmendmentSHA256 || manifest.RecoveryAmendment != pinnedVFSRecoveryPath || manifest.RecoveryAmendmentSHA256 != pinnedVFSRecoverySHA256 || manifest.Status != pinnedVFSManifestState {
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

func TestPilotVFSRecoveryManifestIntegrityAndNotRunStatus(t *testing.T) {
	data, err := pinnedVFSRecoveryManifestFS.ReadFile("testdata/pilot-vfs-recovery-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != pinnedVFSRecoveryManifestSHA256 {
		t.Fatalf("recovery manifest SHA-256=%s, want %s", got, pinnedVFSRecoveryManifestSHA256)
	}
	var recovery pinnedVFSRecoveryManifest
	if err := json.Unmarshal(data, &recovery); err != nil {
		t.Fatalf("decode recovery manifest: %v", err)
	}
	if recovery.ManifestVersion != 1 || recovery.SourceSDD != pinnedVFSRecoveryPath || recovery.SourceSDDSHA256 != pinnedVFSRecoverySHA256 || recovery.Status != pinnedVFSManifestState {
		t.Fatalf("unexpected recovery manifest metadata: version=%d source=%q hash=%q status=%q", recovery.ManifestVersion, recovery.SourceSDD, recovery.SourceSDDSHA256, recovery.Status)
	}
	if len(recovery.Cases) != pinnedVFSRecoveryCaseCount {
		t.Fatalf("recovery cases=%d, want %d", len(recovery.Cases), pinnedVFSRecoveryCaseCount)
	}
	callbackBytes, err := pinnedVFSManifestFS.ReadFile("testdata/vfs-callback-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var callbackManifest pinnedVFSManifest
	if err := json.Unmarshal(callbackBytes, &callbackManifest); err != nil {
		t.Fatalf("decode callback manifest for recovery binding: %v", err)
	}
	if callbackManifest.RecoveryManifest != pinnedVFSRecoveryManifestPath || callbackManifest.RecoveryManifestSHA256 != pinnedVFSRecoveryManifestSHA256 {
		t.Fatalf("callback manifest recovery binding path=%q hash=%q", callbackManifest.RecoveryManifest, callbackManifest.RecoveryManifestSHA256)
	}

	wantCounts := map[string]int{
		"VFS-INIT-PREFLIGHT": 3, "VFS-INIT-PRAGMA": 4, "VFS-INIT-PREFLIGHT-JOURNAL": 1,
		"VFS-INIT-WAL": 1, "VFS-INIT-WAL-CRASH": 2, "VFS-INIT-TX": 1, "VFS-INIT-TX-CRASH": 3,
		"VFS-ADOPT-REJECT": 11, "VFS-ADOPT-SHM-LOCK": 3, "VFS-RECOVER": 4, "VFS-RECOVER-CRASH": 3,
		"VFS-ADOPT-PROCESS-DRAIN": 1, "VFS-ADOPT-SHM-DIRSYNC-ERR": 1,
		"VFS-ADOPT-LIFECYCLE-LOCK-ACQUIRE-ERR": 1, "VFS-ADOPT-LIFECYCLE-LOCK-RELEASE-ERR": 1,
	}
	seen := make(map[string]struct{}, len(recovery.Cases))
	for _, testCase := range recovery.Cases {
		if testCase.ID == "" || testCase.Status != pinnedVFSManifestState || testCase.Oracle.ExpectedReadiness == "" || testCase.Oracle.ApplicationDispatch != 0 || testCase.Oracle.ApplicationWritesAllowed || testCase.Oracle.Reseed || testCase.Oracle.RequiredAssertion == "" || testCase.Oracle.NamespaceMutation == "" || testCase.Oracle.Setup == "" || testCase.Oracle.Barrier == "" {
			t.Errorf("incomplete/unsafe recovery oracle: %+v", testCase)
		}
			if testCase.Oracle.ExpectedReadiness != "QUARANTINED" && testCase.Oracle.ExpectedReadiness != "INITIALIZATION_ALLOWED" && testCase.Oracle.ExpectedReadiness != "CONTINUE_TO_INITIALIZATION" && testCase.Oracle.ExpectedReadiness != "READY_AFTER_FULL_VALIDATION" && testCase.Oracle.ExpectedReadiness != "RESTART_VALIDATION_REQUIRED" && testCase.Oracle.ExpectedReadiness != "MAY_READY_AFTER_FULL_VALIDATION" && testCase.Oracle.ExpectedReadiness != "NOT_APPLICABLE" {
				t.Errorf("unknown expected readiness %q for %s", testCase.Oracle.ExpectedReadiness, testCase.ID)
			}
		if strings.HasPrefix(testCase.ID, "VFS-ADOPT-REJECT-") && (testCase.Oracle.ExpectedReadiness != "QUARANTINED" || testCase.Oracle.NamespaceMutation != "FORBIDDEN" || testCase.Oracle.PagerRecoveryWriteAllowed) {
			t.Errorf("unsafe object rejection oracle permits effects: %+v", testCase)
		}
		if strings.HasPrefix(testCase.ID, "VFS-ADOPT-REJECT-") && (!testCase.Oracle.SuspectEntryPreserved || testCase.Oracle.ServiceNamespaceMutations != 0 || testCase.Oracle.SQLiteEntryCalls != 0 || !testCase.Oracle.CloseEveryAcquiredFDOnce || testCase.Oracle.AcquiredFDCloseBalance != 0) {
			t.Errorf("rejected object identity/cleanup oracle incomplete: %+v", testCase)
		}
		if testCase.ID == "VFS-INIT-PREFLIGHT-JOURNAL-01" && (!testCase.Oracle.PagerRecoveryWriteAllowed || testCase.Oracle.NamespaceMutation != "PAGER_RECOVERY_ONLY") {
			t.Errorf("hot-journal pager recovery oracle is not explicitly scoped: %+v", testCase)
		}
		if _, exists := seen[testCase.ID]; exists {
			t.Errorf("duplicate recovery case ID %q", testCase.ID)
		}
		seen[testCase.ID] = struct{}{}
	}
	wantAdoption := []string{"VFS-ADOPT-ABSENT-01", "VFS-ADOPT-WAL-01", "VFS-ADOPT-JOURNAL-01", "VFS-ADOPT-OBSOLETE-JOURNAL-01", "VFS-ADOPT-ZERO-JOURNAL-DELETE-01", "VFS-ADOPT-PAGER-ZERO-JOURNAL-01", "VFS-ADOPT-SHM-01", "VFS-ADOPT-WAL-SHM-01", "VFS-ADOPT-WAL-JOURNAL-01", "VFS-ADOPT-WAL-ZERO-JOURNAL-01", "VFS-ADOPT-PROCESS-DRAIN-01", "VFS-ADOPT-SHM-DIRSYNC-ERR-01", "VFS-ADOPT-LIFECYCLE-LOCK-ACQUIRE-ERR-01", "VFS-ADOPT-LIFECYCLE-LOCK-RELEASE-ERR-01"}
	wantAdoptionAssertions := map[string]string{
		"VFS-ADOPT-ABSENT-01": "empty schema preflight succeeds; creator-only WAL/init path may proceed",
		"VFS-ADOPT-WAL-01": "adoption leaves registered WAL identity/content unchanged; pager may create verified SHM and recover before validation",
		"VFS-ADOPT-JOURNAL-01": "SQLite alone performs rollback; journal delete/sync only as pager recovery requires",
		"VFS-ADOPT-OBSOLETE-JOURNAL-01": "SQLite pager determines disposition; no app initialization or reseed",
		"VFS-ADOPT-ZERO-JOURNAL-DELETE-01": "xDelete returns SQLITE_OK; exact registered inode unlinks, directory sync succeeds, generation retires exactly once",
		"VFS-ADOPT-PAGER-ZERO-JOURNAL-01": "no journal xOpen/xClose/xDelete; quarantine for missing schema/marker",
		"VFS-ADOPT-SHM-01": "quarantine the orphan SHM untouched; do not call SQLite or dispatch application work",
		"VFS-ADOPT-WAL-SHM-01": "WAL remains pinned; stale SHM unlink and directory sync occur under lifecycle EXCLUSIVE; SQLite recreates/uses SHM once",
		"VFS-ADOPT-WAL-JOURNAL-01": "quarantine with all names untouched and no SQLite call",
		"VFS-ADOPT-WAL-ZERO-JOURNAL-01": "quarantine with all names untouched and no SQLite call",
		"VFS-ADOPT-PROCESS-DRAIN-01": "prior-generation absence is unproven; quarantine before SQLite, preserve names and close every acquired fd exactly once",
		"VFS-ADOPT-SHM-DIRSYNC-ERR-01": "SHM unlink succeeded but directory fsync returned EIO; record unlink_done=true and durability=unknown, quarantine without rollback claim or SQLite entry",
		"VFS-ADOPT-LIFECYCLE-LOCK-ACQUIRE-ERR-01": "lifecycle OFD EXCLUSIVE acquisition fails; quarantine before identity mutation or SQLite, with exact-once fd cleanup",
		"VFS-ADOPT-LIFECYCLE-LOCK-RELEASE-ERR-01": "lifecycle OFD EXCLUSIVE release fails after durable SHM unlink; quarantine without SQLite entry or rollback claim and close every acquired fd exactly once",
	}
	for _, id := range wantAdoption {
		if _, ok := seen[id]; !ok {
			t.Errorf("missing adoption scenario %s", id)
		}
	}
	for _, testCase := range recovery.Cases {
		if want, ok := wantAdoptionAssertions[testCase.ID]; ok && testCase.Oracle.RequiredAssertion != want {
			t.Errorf("adoption oracle drift for %s: got %q want %q", testCase.ID, testCase.Oracle.RequiredAssertion, want)
		}
	}
	wantReadiness := map[string]string{
		"VFS-INIT-PREFLIGHT-01": "INITIALIZATION_ALLOWED",
		"VFS-INIT-PRAGMA-03": "MAY_READY_AFTER_FULL_VALIDATION", "VFS-INIT-PRAGMA-04": "MAY_READY_AFTER_FULL_VALIDATION",
		"VFS-INIT-WAL-01": "CONTINUE_TO_INITIALIZATION", "VFS-INIT-TX-01": "READY_AFTER_FULL_VALIDATION",
		"VFS-INIT-TX-CRASH-03": "RESTART_VALIDATION_REQUIRED",
		"VFS-ADOPT-ABSENT-01": "INITIALIZATION_ALLOWED", "VFS-ADOPT-WAL-01": "MAY_READY_AFTER_FULL_VALIDATION",
		"VFS-ADOPT-JOURNAL-01": "MAY_READY_AFTER_FULL_VALIDATION", "VFS-ADOPT-OBSOLETE-JOURNAL-01": "MAY_READY_AFTER_FULL_VALIDATION",
		"VFS-ADOPT-SHM-LOCK-01": "MAY_READY_AFTER_FULL_VALIDATION", "VFS-ADOPT-SHM-LOCK-02": "MAY_READY_AFTER_FULL_VALIDATION", "VFS-ADOPT-SHM-LOCK-03": "MAY_READY_AFTER_FULL_VALIDATION",
		"VFS-ADOPT-WAL-SHM-01": "MAY_READY_AFTER_FULL_VALIDATION", "VFS-ADOPT-ZERO-JOURNAL-DELETE-01": "NOT_APPLICABLE",
		"VFS-ADOPT-PROCESS-DRAIN-01": "QUARANTINED", "VFS-ADOPT-SHM-DIRSYNC-ERR-01": "QUARANTINED",
		"VFS-ADOPT-LIFECYCLE-LOCK-ACQUIRE-ERR-01": "QUARANTINED", "VFS-ADOPT-LIFECYCLE-LOCK-RELEASE-ERR-01": "QUARANTINED",
		"VFS-RECOVER-01": "MAY_READY_AFTER_FULL_VALIDATION", "VFS-RECOVER-02": "MAY_READY_AFTER_FULL_VALIDATION", "VFS-RECOVER-03": "MAY_READY_AFTER_FULL_VALIDATION", "VFS-RECOVER-04": "MAY_READY_AFTER_FULL_VALIDATION",
		"VFS-RECOVER-CRASH-01": "RESTART_VALIDATION_REQUIRED", "VFS-RECOVER-CRASH-02": "RESTART_VALIDATION_REQUIRED", "VFS-RECOVER-CRASH-03": "RESTART_VALIDATION_REQUIRED",
	}
	wantNamespace := map[string]string{
		"VFS-INIT-PREFLIGHT-JOURNAL-01": "PAGER_RECOVERY_ONLY",
		"VFS-ADOPT-WAL-01": "PAGER_RECOVERY_AND_REGISTERED_SIDECAR_CREATE", "VFS-ADOPT-JOURNAL-01": "PAGER_RECOVERY_ONLY", "VFS-ADOPT-OBSOLETE-JOURNAL-01": "PAGER_RECOVERY_ONLY",
		"VFS-ADOPT-WAL-SHM-01": "SHM_UNLINK_DIRSYNC_AND_REGISTERED_RECREATE", "VFS-ADOPT-ZERO-JOURNAL-DELETE-01": "SYNTHETIC_REGISTERED_FILE_UNLINK_AND_DIRSYNC", "VFS-ADOPT-SHM-LOCK-02": "SHM_UNLINK_DIRSYNC_AFTER_IDENTITY",
		"VFS-ADOPT-SHM-DIRSYNC-ERR-01": "SHM_UNLINK_DIRSYNC_FAILED", "VFS-ADOPT-LIFECYCLE-LOCK-RELEASE-ERR-01": "SHM_UNLINK_DIRSYNC_DURABLE_RELEASE_FAILED",
	}
	wantStartupFailure := map[string]struct {
		processDrain, suspectPreserved, unlinkDone, rollbackReported bool
		durability, lockOutcome, namespace string
		mutations int
	}{
		"VFS-ADOPT-PROCESS-DRAIN-01": {false, true, false, false, "NOT_APPLICABLE", "NOT_ATTEMPTED", "FORBIDDEN", 0},
		"VFS-ADOPT-SHM-DIRSYNC-ERR-01": {true, false, true, false, "UNKNOWN", "HELD_THEN_CLOSED_ON_QUARANTINE", "SHM_UNLINK_DIRSYNC_FAILED", 1},
		"VFS-ADOPT-LIFECYCLE-LOCK-ACQUIRE-ERR-01": {true, true, false, false, "NOT_APPLICABLE", "ACQUIRE_FAILED", "FORBIDDEN", 0},
		"VFS-ADOPT-LIFECYCLE-LOCK-RELEASE-ERR-01": {true, false, true, false, "DURABLE", "RELEASE_FAILED_THEN_CLOSED_ON_QUARANTINE", "SHM_UNLINK_DIRSYNC_DURABLE_RELEASE_FAILED", 1},
	}
	for _, testCase := range recovery.Cases {
		if want, ok := wantStartupFailure[testCase.ID]; ok {
			o := testCase.Oracle
			if o.ExpectedReadiness != "QUARANTINED" || o.ApplicationDispatch != 0 || o.ApplicationWritesAllowed || o.Reseed || o.SQLiteEntryCalls != 0 || o.ServiceNamespaceMutations != want.mutations || o.NamespaceMutation != want.namespace || !o.CloseEveryAcquiredFDOnce || o.AcquiredFDCloseBalance != 0 || o.PriorProcessDrainProven == nil || *o.PriorProcessDrainProven != want.processDrain || o.SuspectEntryPreserved != want.suspectPreserved || o.UnlinkDone == nil || *o.UnlinkDone != want.unlinkDone || o.Durability != want.durability || o.RollbackReported == nil || *o.RollbackReported != want.rollbackReported || o.LifecycleLockOutcome != want.lockOutcome {
				t.Errorf("unsafe pre-SQLite startup failure oracle: %+v", o)
			}
		}
		if want, ok := wantReadiness[testCase.ID]; ok && testCase.Oracle.ExpectedReadiness != want {
			t.Errorf("readiness oracle drift for %s: got %q want %q", testCase.ID, testCase.Oracle.ExpectedReadiness, want)
		}
		want := "FORBIDDEN"
		if strings.HasPrefix(testCase.ID, "VFS-RECOVER-") {
			want = "PAGER_RECOVERY_ONLY"
		}
		if value, ok := wantNamespace[testCase.ID]; ok {
			want = value
		}
		if testCase.Oracle.NamespaceMutation != want {
			t.Errorf("namespace mutation oracle drift for %s: got %q want %q", testCase.ID, testCase.Oracle.NamespaceMutation, want)
		}
	}
	wantPagerWrites := map[string]bool{"VFS-INIT-PREFLIGHT-JOURNAL-01": true, "VFS-ADOPT-WAL-01": true, "VFS-ADOPT-JOURNAL-01": true, "VFS-ADOPT-OBSOLETE-JOURNAL-01": true, "VFS-ADOPT-WAL-SHM-01": true, "VFS-RECOVER-01": true, "VFS-RECOVER-02": true, "VFS-RECOVER-03": true, "VFS-RECOVER-04": true, "VFS-RECOVER-CRASH-01": true, "VFS-RECOVER-CRASH-02": true, "VFS-RECOVER-CRASH-03": true}
	for _, testCase := range recovery.Cases {
		if testCase.Oracle.PagerRecoveryWriteAllowed != wantPagerWrites[testCase.ID] {
			t.Errorf("pager recovery write allowlist mismatch for %s: got %v", testCase.ID, testCase.Oracle.PagerRecoveryWriteAllowed)
		}
		if strings.HasPrefix(testCase.ID, "VFS-ADOPT-REJECT-") && testCase.Oracle.NamespaceMutation != "FORBIDDEN" {
			t.Errorf("rejected sidecar/object may mutate namespace in %s", testCase.ID)
		}
	}
	if len(seen) != pinnedVFSRecoveryCaseCount {
		t.Fatalf("unique recovery IDs=%d, want %d", len(seen), pinnedVFSRecoveryCaseCount)
	}
	for prefix, count := range wantCounts {
		for i := 1; i <= count; i++ {
			id := fmt.Sprintf("%s-%02d", prefix, i)
			if _, ok := seen[id]; !ok {
				t.Errorf("missing recovery case %s", id)
			}
		}
	}

	source, err := currentPilotControlTestFile(t)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	sdd, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(recovery.SourceSDD)))
	if err != nil {
		t.Fatalf("read recovery design %q: %v", recovery.SourceSDD, err)
	}
	sddSum := sha256.Sum256(sdd)
	if got := hex.EncodeToString(sddSum[:]); got != recovery.SourceSDDSHA256 {
		t.Fatalf("recovery design binding=%s, actual=%s", recovery.SourceSDDSHA256, got)
	}
	manifestPath := filepath.Join(root, filepath.FromSlash(pinnedVFSRecoveryManifestPath))
	manifestOnDisk, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read recovery manifest %q: %v", manifestPath, err)
	}
	manifestOnDiskSum := sha256.Sum256(manifestOnDisk)
	if got := hex.EncodeToString(manifestOnDiskSum[:]); got != pinnedVFSRecoveryManifestSHA256 {
		t.Fatalf("recovery manifest binding=%s, actual=%s", pinnedVFSRecoveryManifestSHA256, got)
	}
}
