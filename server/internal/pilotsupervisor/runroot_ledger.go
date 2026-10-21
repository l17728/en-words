//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"enwords/internal/obs"
	"github.com/fxamacker/cbor/v2"
)

var (
	errRunRootLedgerInvalid  = errors.New("pilot run-root ledger invalid")
	errRunRootLedgerConflict = errors.New("ipc_request_conflict")
	errRunRootLedgerCAS      = errors.New("pilot run-root ledger compare-and-swap lost")
	errRunRootLedgerFull     = errors.New("ipc_registry_full")
	errRunRootLedgerCorrupt  = errors.New("pilot run-root ledger corrupt")
)

const maxRunRootLedgerReceiptBytes = 64 << 10
const maxRunRootEpochAllocations = 1_000_000
const maxRunRootEpochAllocationsPerPrincipal = 100_000

var runRootDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// RunRootLedger persists the permanent epoch tombstone, operation key, owner
// generation, and terminal receipt in SQLite transactions. The supplied DB
// must point at the protected control filesystem and use FULL synchronous
// durability; callers must not expose its path over IPC.
type RunRootLedger struct {
	mu              sync.Mutex
	conn            *sql.Conn
	fenced          bool
	logger          *obs.ModuleLogger
	commitBootstrap func(context.Context, *sql.Conn) error
}

type RunRootLedgerRequest struct {
	Epoch             RunEpoch
	RequestID         string
	PrincipalRef      string
	SessionKeyHash    string
	GatewayGeneration uint64
	ProcessIdentity   [6]uint64
	SchemaVersion     uint32
	PayloadDigest     string
}

type RunRootLedgerRecord struct {
	State            LedgerState
	AllocationState  LedgerState
	RunTerminalState string
	LeaseHeld        bool
	OwnerGeneration  uint64
	CachedResponse   string
	EvidenceReceipt  []byte
}

type RunRootTerminal string

const (
	RunRootTerminalCreated       RunRootTerminal = "created"
	RunRootTerminalNoEffect      RunRootTerminal = "no_effect"
	RunRootTerminalCollision     RunRootTerminal = "collision"
	RunRootTerminalIndeterminate RunRootTerminal = "indeterminate"
)

// RunRootLedgerEvidence can only be produced after typed source validation and
// deterministic digest composition. Its private fields prevent IPC callers
// from manufacturing terminal proofs by filling exported structs.
type RunRootLedgerEvidence struct {
	kind     string
	identity any
	payload  any
	item     runRootEvidenceItem
}

// OperationEvidenceReceipt is the existing docs/43 envelope used by durable
// operations. Its canonical bytes and receipt ID are immutable after creation.
type OperationEvidenceReceipt struct {
	encoded   []byte
	receiptID string
}

func (r OperationEvidenceReceipt) Bytes() []byte { return append([]byte(nil), r.encoded...) }
func (r OperationEvidenceReceipt) ID() string    { return r.receiptID }

func NewRunRootLedgerEvidence(kind string, identity, payload any) (RunRootLedgerEvidence, error) {
	item, err := buildRunRootEvidenceItem(kind, identity, payload)
	if err != nil {
		return RunRootLedgerEvidence{}, errRunRootEvidenceInvalid
	}
	return RunRootLedgerEvidence{kind: kind, identity: identity, payload: payload, item: item}, nil
}

func newRunRootOperationReceipt(req RunRootLedgerRequest, owner uint64, outcome string, evidence RunRootLedgerEvidence) (OperationEvidenceReceipt, error) {
	if validateRunRootLedgerRequest(req) != nil || owner == 0 ||
		(evidence.kind != "run-root-created-v1" && evidence.kind != "run-root-create-collision-v1" && evidence.kind != "run-root-create-effect-absent-v1") ||
		!runRootEvidenceMatchesOperation(evidence, req.Epoch, req.RequestID, req.GatewayGeneration, mustJSON(req.ProcessIdentity)) {
		return OperationEvidenceReceipt{}, errRunRootEvidenceInvalid
	}
	if outcome != "completed" && outcome != "abandoned" {
		return OperationEvidenceReceipt{}, errRunRootEvidenceInvalid
	}
	operationID, err := hex.DecodeString(string(req.Epoch))
	if err != nil || len(operationID) != 16 {
		return OperationEvidenceReceipt{}, errRunRootEvidenceInvalid
	}
	items := []any{[]any{evidence.kind, runRootDigestHex(evidence.item.SourceIdentityDigest),
		runRootDigestHex(evidence.item.PayloadDigest), runRootDigestHex(evidence.item.EvidenceDigest)}}
	fields := []any{operationID, req.PrincipalRef, "run_root_create", uint64(req.SchemaVersion), req.PayloadDigest,
		string(req.Epoch), owner, items, outcome, uint64(1)}
	encodedFields, err := encodeRunRootCBOR(fields)
	if err != nil {
		return OperationEvidenceReceipt{}, errRunRootEvidenceInvalid
	}
	receiptID := runRootDigestHex(hashRunRootDomain("en-words-operation-evidence-v1\x00", encodedFields))
	encodedReceipt, err := encodeRunRootCBOR([]any{fields, receiptID})
	if err != nil || len(encodedReceipt) > maxRunRootLedgerReceiptBytes {
		return OperationEvidenceReceipt{}, errRunRootEvidenceInvalid
	}
	return OperationEvidenceReceipt{encoded: encodedReceipt, receiptID: receiptID}, nil
}

func validatePersistedRunRootOperationReceipt(encoded []byte, req RunRootLedgerRequest, owner uint64, outcome, response string) error {
	if len(encoded) == 0 || len(encoded) > maxRunRootLedgerReceiptBytes || owner == 0 || validateRunRootLedgerRequest(req) != nil || (outcome != "completed" && outcome != "abandoned") {
		return errRunRootLedgerCorrupt
	}
	var envelope []any
	if err := cbor.Unmarshal(encoded, &envelope); err != nil || len(envelope) != 2 {
		return errRunRootLedgerCorrupt
	}
	canonical, err := encodeRunRootCBOR(envelope)
	if err != nil || string(canonical) != string(encoded) {
		return errRunRootLedgerCorrupt
	}
	fields, ok := envelope[0].([]any)
	if !ok || len(fields) != 10 {
		return errRunRootLedgerCorrupt
	}
	operationID, err := hex.DecodeString(string(req.Epoch))
	if err != nil || string(asBytes(fields[0])) != string(operationID) || fields[1] != req.PrincipalRef || fields[2] != "run_root_create" ||
		!cborUintEquals(fields[3], uint64(req.SchemaVersion)) || fields[4] != req.PayloadDigest || fields[5] != string(req.Epoch) ||
		!cborUintEquals(fields[6], owner) || fields[8] != outcome || !cborUintEquals(fields[9], 1) {
		return errRunRootLedgerCorrupt
	}
	items, ok := fields[7].([]any)
	if !ok || len(items) != 1 {
		return errRunRootLedgerCorrupt
	}
	item, ok := items[0].([]any)
	if !ok || len(item) != 4 {
		return errRunRootLedgerCorrupt
	}
	kind, ok := item[0].(string)
	if !ok || (kind != "run-root-created-v1" && kind != "run-root-create-collision-v1" && kind != "run-root-create-effect-absent-v1") {
		return errRunRootLedgerCorrupt
	}
	if (outcome == "completed" && (kind != "run-root-created-v1" || response != "ok")) ||
		(outcome == "abandoned" && (kind == "run-root-created-v1" || !validPersistedRunRootAbandonedResponse(response, kind))) {
		return errRunRootLedgerCorrupt
	}
	for _, digest := range item[1:] {
		value, ok := digest.(string)
		if !ok || !runRootDigestPattern.MatchString(value) {
			return errRunRootLedgerCorrupt
		}
	}
	encodedFields, err := encodeRunRootCBOR(fields)
	receiptID, ok := envelope[1].(string)
	if err != nil || !ok || !runRootDigestPattern.MatchString(receiptID) || runRootDigestHex(hashRunRootDomain("en-words-operation-evidence-v1\x00", encodedFields)) != receiptID {
		return errRunRootLedgerCorrupt
	}
	return nil
}

func validPersistedRunRootAbandonedResponse(response, kind string) bool {
	if kind == "run-root-create-collision-v1" {
		return response == "verified_policy_mismatch"
	}
	return kind == "run-root-create-effect-absent-v1" && (response == "verified_io_failed" || response == "verified_path_invalid" || response == "verified_policy_mismatch")
}

func validPersistedRunRootStateResponse(state, response string) bool {
	if state == string(LedgerPrepared) || state == string(LedgerExecuting) {
		return response == ""
	}
	if state == string(LedgerIndeterminate) {
		return response == "ipc_result_indeterminate" || response == "restart_required"
	}
	return false
}

func asBytes(value any) []byte { result, _ := value.([]byte); return result }

func cborUintEquals(value any, expected uint64) bool {
	switch number := value.(type) {
	case uint64:
		return number == expected
	case uint32:
		return uint64(number) == expected
	case int64:
		return number >= 0 && uint64(number) == expected
	case int:
		return number >= 0 && uint64(number) == expected
	default:
		return false
	}
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func NewRunRootLedger(ctx context.Context, db *sql.DB) (*RunRootLedger, error) {
	return NewRunRootLedgerWithLogger(ctx, db, obs.New(os.Stderr))
}

func NewRunRootLedgerWithLogger(ctx context.Context, db *sql.DB, logger *obs.Logger) (*RunRootLedger, error) {
	if ctx == nil || db == nil {
		return nil, errRunRootLedgerInvalid
	}
	if logger == nil {
		return nil, errRunRootLedgerInvalid
	}
	// Pin one physical SQL connection for the lifetime of this ledger. If that
	// connection dies, operations fail and the Reaper must restart; no replacement
	// connection is silently used with uninitialized connection-local pragmas.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, errRunRootLedgerInvalid
	}
	closeOnError := func() { discardRunRootSQLConn(conn) }
	// Keep journal-mode lock waits short so the Go context can be observed
	// between attempts. Restore the normal five-second writer wait afterwards.
	if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=50`); err != nil {
		closeOnError()
		return nil, errRunRootLedgerInvalid
	}
	var busyTimeout int
	if err = conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil || busyTimeout != 50 {
		closeOnError()
		return nil, errRunRootLedgerInvalid
	}
	if err = enableRunRootLedgerWAL(ctx, conn); err != nil {
		closeOnError()
		return nil, errRunRootLedgerInvalid
	}
	if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		closeOnError()
		return nil, errRunRootLedgerInvalid
	}
	if err = conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil || busyTimeout != 5000 {
		closeOnError()
		return nil, errRunRootLedgerInvalid
	}
	if _, err = conn.ExecContext(ctx, `PRAGMA synchronous=FULL`); err != nil {
		closeOnError()
		return nil, errRunRootLedgerInvalid
	}
	var synchronous int
	if err = conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil || synchronous != 2 {
		closeOnError()
		return nil, errRunRootLedgerInvalid
	}
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		closeOnError()
		return nil, errRunRootLedgerInvalid
	}
	var foreignKeys int
	if err = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		closeOnError()
		return nil, errRunRootLedgerInvalid
	}
	const schemaVersion = 2
	const schema = `
CREATE TABLE run_epoch_allocations (
  run_epoch TEXT PRIMARY KEY CHECK(length(run_epoch)=32),
  principal_ref TEXT NOT NULL,
  session_key_hash TEXT NOT NULL,
  gateway_generation INTEGER NOT NULL CHECK(gateway_generation>0),
  process_identity BLOB NOT NULL,
	schema_version INTEGER NOT NULL CHECK(schema_version=1),
  payload_digest TEXT NOT NULL,
  allocation_state TEXT NOT NULL CHECK(allocation_state IN ('prepared','executing','completed','abandoned','indeterminate')),
  run_terminal_state TEXT NOT NULL CHECK(run_terminal_state IN ('none','active','abandoned','indeterminate')),
  run_lease_held INTEGER NOT NULL CHECK(run_lease_held IN (0,1))
);
CREATE TABLE run_root_operations (
  run_epoch TEXT NOT NULL UNIQUE REFERENCES run_epoch_allocations(run_epoch),
  request_id TEXT NOT NULL,
  operation_kind TEXT NOT NULL CHECK(operation_kind='run_root_create'),
  principal_ref TEXT NOT NULL,
  session_key_hash TEXT NOT NULL,
  gateway_generation INTEGER NOT NULL CHECK(gateway_generation>0),
  process_identity BLOB NOT NULL,
	schema_version INTEGER NOT NULL CHECK(schema_version=1),
  payload_digest TEXT NOT NULL,
  operation_state TEXT NOT NULL CHECK(operation_state IN ('prepared','executing','completed','abandoned','indeterminate')),
  owner_generation INTEGER NOT NULL DEFAULT 0 CHECK(owner_generation>=0),
  response_code TEXT NOT NULL DEFAULT '',
  evidence_receipt BLOB,
  PRIMARY KEY(run_epoch, request_id)
	);
CREATE TABLE run_root_ledger_meta (singleton INTEGER PRIMARY KEY CHECK(singleton=1), schema_version INTEGER NOT NULL);
CREATE TABLE pilot_bootstrap_handoffs (
  generation BLOB PRIMARY KEY NOT NULL CHECK(typeof(generation)='blob' AND length(generation)=8 AND generation<>zeroblob(8)),
  launch_nonce TEXT NOT NULL UNIQUE CHECK(typeof(launch_nonce)='text' AND length(launch_nonce)=32 AND length(CAST(launch_nonce AS BLOB))=32 AND launch_nonce NOT GLOB '*[^0-9a-f]*'),
  run_epoch TEXT NOT NULL UNIQUE CHECK(typeof(run_epoch)='text' AND length(run_epoch)=32 AND length(CAST(run_epoch AS BLOB))=32 AND run_epoch NOT GLOB '*[^0-9a-f]*'),
  pid INTEGER NOT NULL CHECK(typeof(pid)='integer' AND pid BETWEEN 1 AND 2147483647),
  uid INTEGER NOT NULL CHECK(typeof(uid)='integer' AND uid BETWEEN 1 AND 4294967295),
  start_time_ticks BLOB NOT NULL CHECK(typeof(start_time_ticks)='blob' AND length(start_time_ticks)=8 AND start_time_ticks<>zeroblob(8)),
  pidns_device BLOB NOT NULL CHECK(typeof(pidns_device)='blob' AND length(pidns_device)=8 AND pidns_device<>zeroblob(8)),
  pidns_inode BLOB NOT NULL CHECK(typeof(pidns_inode)='blob' AND length(pidns_inode)=8 AND pidns_inode<>zeroblob(8)),
  cgroup_mount_id BLOB NOT NULL CHECK(typeof(cgroup_mount_id)='blob' AND length(cgroup_mount_id)=8 AND cgroup_mount_id<>zeroblob(8)),
  cgroup_device BLOB NOT NULL CHECK(typeof(cgroup_device)='blob' AND length(cgroup_device)=8 AND cgroup_device<>zeroblob(8)),
  cgroup_inode BLOB NOT NULL CHECK(typeof(cgroup_inode)='blob' AND length(cgroup_inode)=8 AND cgroup_inode<>zeroblob(8))
);
CREATE UNIQUE INDEX run_root_one_operation_per_epoch ON run_root_operations(run_epoch);
CREATE UNIQUE INDEX run_root_one_held_lease_per_session ON run_epoch_allocations(principal_ref,session_key_hash) WHERE run_lease_held=1;
CREATE TRIGGER pilot_bootstrap_handoffs_no_update BEFORE UPDATE ON pilot_bootstrap_handoffs BEGIN SELECT RAISE(ABORT,'immutable pilot bootstrap handoff'); END;
CREATE TRIGGER pilot_bootstrap_handoffs_no_delete BEFORE DELETE ON pilot_bootstrap_handoffs BEGIN SELECT RAISE(ABORT,'immutable pilot bootstrap handoff'); END;`
	if err := initializeRunRootLedgerSchema(ctx, conn, schema, schemaVersion); err != nil {
		closeOnError()
		return nil, errRunRootLedgerInvalid
	}
	return &RunRootLedger{conn: conn, logger: logger.With("pilot_runroot_ledger")}, nil
}

// WAL mode changes require an exclusive SQLite lock and some SQLite drivers
// return BUSY immediately for concurrent journal-mode transitions, even when
// busy_timeout is configured. Retry only that transient condition within the
// same five-second initialization budget; all other errors fail closed.
func enableRunRootLedgerWAL(ctx context.Context, conn *sql.Conn) error {
	deadline := time.Now().Add(5 * time.Second)
	attemptCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	delay := 10 * time.Millisecond
	for {
		if err := attemptCtx.Err(); err != nil {
			return err
		}
		var journalMode string
		err := conn.QueryRowContext(attemptCtx, `PRAGMA journal_mode=WAL`).Scan(&journalMode)
		if err == nil {
			if deadlineErr := attemptCtx.Err(); deadlineErr != nil {
				return deadlineErr
			}
			if strings.EqualFold(journalMode, "wal") {
				return nil
			}
			return errRunRootLedgerInvalid
		}
		if !runRootLedgerSQLiteBusyOrLocked(err) {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		if delay > remaining {
			delay = remaining
		}
		timer := time.NewTimer(delay)
		select {
		case <-attemptCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return attemptCtx.Err()
		case <-timer.C:
		}
		if delay < 100*time.Millisecond {
			delay *= 2
		}
	}
}

func runRootLedgerSQLiteBusyOrLocked(err error) bool {
	type sqliteCoder interface{ Code() int }
	var coded sqliteCoder
	if !errors.As(err, &coded) {
		return false
	}
	primaryCode := coded.Code() & 0xff
	return primaryCode == 5 || primaryCode == 6 // SQLITE_BUSY or SQLITE_LOCKED, including extended codes.
}

func initializeRunRootLedgerSchema(ctx context.Context, conn *sql.Conn, schema string, schemaVersion int) error {
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var userVersion int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, `SELECT type,name FROM sqlite_master`)
	if err != nil {
		return err
	}
	objects := 0
	for rows.Next() {
		var objectType, name string
		if err = rows.Scan(&objectType, &name); err != nil {
			_ = rows.Close()
			return err
		}
		if !strings.HasPrefix(name, "sqlite_") {
			objects++
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if objects == 0 {
		if userVersion != 0 {
			return errRunRootLedgerInvalid
		}
		if _, err = conn.ExecContext(ctx, schema); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO run_root_ledger_meta(singleton,schema_version) VALUES(1,?)`, schemaVersion); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, `PRAGMA user_version=`+strconv.Itoa(schemaVersion)); err != nil {
			return err
		}
	} else {
		var storedVersion int
		if userVersion != schemaVersion || conn.QueryRowContext(ctx, `SELECT schema_version FROM run_root_ledger_meta WHERE singleton=1`).Scan(&storedVersion) != nil || storedVersion != schemaVersion {
			return errRunRootLedgerInvalid
		}
	}
	if err = validateRunRootLedgerSchema(ctx, conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

func discardRunRootSQLConn(conn *sql.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

func validateRunRootLedgerSchema(ctx context.Context, conn *sql.Conn) error {
	required := map[string][]string{
		"run_epoch_allocations":    {"run_epoch", "principal_ref", "session_key_hash", "gateway_generation", "process_identity", "schema_version", "payload_digest", "allocation_state", "run_terminal_state", "run_lease_held"},
		"run_root_operations":      {"run_epoch", "request_id", "operation_kind", "principal_ref", "session_key_hash", "gateway_generation", "process_identity", "schema_version", "payload_digest", "operation_state", "owner_generation", "response_code", "evidence_receipt"},
		"run_root_ledger_meta":     {"singleton", "schema_version"},
		"pilot_bootstrap_handoffs": {"generation", "launch_nonce", "run_epoch", "pid", "uid", "start_time_ticks", "pidns_device", "pidns_inode", "cgroup_mount_id", "cgroup_device", "cgroup_inode"},
	}
	for table, columns := range required {
		rows, err := conn.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
		if err != nil {
			return err
		}
		found := make(map[string]bool, len(columns))
		for rows.Next() {
			var cid, notnull, primaryKey int
			var name, dataType string
			var defaultValue any
			if err = rows.Scan(&cid, &name, &dataType, &notnull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return err
			}
			found[name] = true
		}
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		for _, column := range columns {
			if !found[column] {
				return errRunRootLedgerInvalid
			}
		}
	}
	if !validRunRootIndex(ctx, conn, "run_root_operations", "run_root_one_operation_per_epoch", []string{"run_epoch"}, false, "") ||
		!validRunRootIndex(ctx, conn, "run_epoch_allocations", "run_root_one_held_lease_per_session", []string{"principal_ref", "session_key_hash"}, true, "run_lease_held=1") {
		return errRunRootLedgerInvalid
	}
	if !validRunRootTableDDL(ctx, conn, "run_epoch_allocations", nil) ||
		!validRunRootTableDDL(ctx, conn, "run_root_operations", nil) ||
		!validRunRootTableDDL(ctx, conn, "run_root_ledger_meta", nil) ||
		!validRunRootTableDDL(ctx, conn, "pilot_bootstrap_handoffs", nil) ||
		!validRunRootTrigger(ctx, conn, "pilot_bootstrap_handoffs_no_update", "UPDATE") ||
		!validRunRootTrigger(ctx, conn, "pilot_bootstrap_handoffs_no_delete", "DELETE") ||
		!validRunRootObjectInventory(ctx, conn) || !validatePersistedBootstrapHandoffs(ctx, conn) {
		return errRunRootLedgerInvalid
	}
	return nil
}

func validRunRootTableDDL(ctx context.Context, conn *sql.Conn, table string, _ []string) bool {
	var ddl string
	if conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl) != nil {
		return false
	}
	expected := `CREATE TABLE run_epoch_allocations (
  run_epoch TEXT PRIMARY KEY CHECK(length(run_epoch)=32),
  principal_ref TEXT NOT NULL,
  session_key_hash TEXT NOT NULL,
  gateway_generation INTEGER NOT NULL CHECK(gateway_generation>0),
  process_identity BLOB NOT NULL,
  schema_version INTEGER NOT NULL CHECK(schema_version=1),
  payload_digest TEXT NOT NULL,
  allocation_state TEXT NOT NULL CHECK(allocation_state IN ('prepared','executing','completed','abandoned','indeterminate')),
  run_terminal_state TEXT NOT NULL CHECK(run_terminal_state IN ('none','active','abandoned','indeterminate')),
  run_lease_held INTEGER NOT NULL CHECK(run_lease_held IN (0,1))
)`
	if table == "run_root_operations" {
		expected = `CREATE TABLE run_root_operations (
  run_epoch TEXT NOT NULL UNIQUE REFERENCES run_epoch_allocations(run_epoch),
  request_id TEXT NOT NULL,
  operation_kind TEXT NOT NULL CHECK(operation_kind='run_root_create'),
  principal_ref TEXT NOT NULL,
  session_key_hash TEXT NOT NULL,
  gateway_generation INTEGER NOT NULL CHECK(gateway_generation>0),
  process_identity BLOB NOT NULL,
  schema_version INTEGER NOT NULL CHECK(schema_version=1),
  payload_digest TEXT NOT NULL,
  operation_state TEXT NOT NULL CHECK(operation_state IN ('prepared','executing','completed','abandoned','indeterminate')),
  owner_generation INTEGER NOT NULL DEFAULT 0 CHECK(owner_generation>=0),
  response_code TEXT NOT NULL DEFAULT '',
  evidence_receipt BLOB,
  PRIMARY KEY(run_epoch, request_id)
)`
	}
	if table == "run_root_ledger_meta" {
		expected = `CREATE TABLE run_root_ledger_meta (singleton INTEGER PRIMARY KEY CHECK(singleton=1), schema_version INTEGER NOT NULL)`
	}
	if table == "pilot_bootstrap_handoffs" {
		expected = `CREATE TABLE pilot_bootstrap_handoffs (
  generation BLOB PRIMARY KEY NOT NULL CHECK(typeof(generation)='blob' AND length(generation)=8 AND generation<>zeroblob(8)),
  launch_nonce TEXT NOT NULL UNIQUE CHECK(typeof(launch_nonce)='text' AND length(launch_nonce)=32 AND length(CAST(launch_nonce AS BLOB))=32 AND launch_nonce NOT GLOB '*[^0-9a-f]*'),
  run_epoch TEXT NOT NULL UNIQUE CHECK(typeof(run_epoch)='text' AND length(run_epoch)=32 AND length(CAST(run_epoch AS BLOB))=32 AND run_epoch NOT GLOB '*[^0-9a-f]*'),
  pid INTEGER NOT NULL CHECK(typeof(pid)='integer' AND pid BETWEEN 1 AND 2147483647),
  uid INTEGER NOT NULL CHECK(typeof(uid)='integer' AND uid BETWEEN 1 AND 4294967295),
  start_time_ticks BLOB NOT NULL CHECK(typeof(start_time_ticks)='blob' AND length(start_time_ticks)=8 AND start_time_ticks<>zeroblob(8)),
  pidns_device BLOB NOT NULL CHECK(typeof(pidns_device)='blob' AND length(pidns_device)=8 AND pidns_device<>zeroblob(8)),
  pidns_inode BLOB NOT NULL CHECK(typeof(pidns_inode)='blob' AND length(pidns_inode)=8 AND pidns_inode<>zeroblob(8)),
  cgroup_mount_id BLOB NOT NULL CHECK(typeof(cgroup_mount_id)='blob' AND length(cgroup_mount_id)=8 AND cgroup_mount_id<>zeroblob(8)),
  cgroup_device BLOB NOT NULL CHECK(typeof(cgroup_device)='blob' AND length(cgroup_device)=8 AND cgroup_device<>zeroblob(8)),
  cgroup_inode BLOB NOT NULL CHECK(typeof(cgroup_inode)='blob' AND length(cgroup_inode)=8 AND cgroup_inode<>zeroblob(8))
)`
	}
	return normalizeRunRootDDL(ddl) == normalizeRunRootDDL(expected)
}

func validRunRootTrigger(ctx context.Context, conn *sql.Conn, name, operation string) bool {
	var ddl string
	if conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl) != nil {
		return false
	}
	expected := `CREATE TRIGGER pilot_bootstrap_handoffs_no_` + strings.ToLower(operation) + ` BEFORE ` + operation + ` ON pilot_bootstrap_handoffs BEGIN SELECT RAISE(ABORT,'immutable pilot bootstrap handoff'); END`
	return normalizeRunRootDDL(ddl) == normalizeRunRootDDL(expected)
}

func validRunRootObjectInventory(ctx context.Context, conn *sql.Conn) bool {
	expected := map[string]bool{
		"table:run_epoch_allocations": true, "table:run_root_operations": true,
		"table:run_root_ledger_meta": true, "table:pilot_bootstrap_handoffs": true,
		"index:run_root_one_operation_per_epoch": true, "index:run_root_one_held_lease_per_session": true,
		"trigger:pilot_bootstrap_handoffs_no_update": true, "trigger:pilot_bootstrap_handoffs_no_delete": true,
	}
	rows, err := conn.QueryContext(ctx, `SELECT type,name FROM sqlite_master`)
	if err != nil {
		return false
	}
	found := make(map[string]bool, len(expected))
	for rows.Next() {
		var objectType, name string
		if rows.Scan(&objectType, &name) != nil {
			_ = rows.Close()
			return false
		}
		if strings.HasPrefix(name, "sqlite_") {
			continue
		}
		key := objectType + ":" + name
		if !expected[key] {
			_ = rows.Close()
			return false
		}
		found[key] = true
	}
	if rows.Err() != nil || rows.Close() != nil || len(found) != len(expected) {
		return false
	}
	return true
}

func validatePersistedBootstrapHandoffs(ctx context.Context, conn *sql.Conn) bool {
	rows, err := conn.QueryContext(ctx, `SELECT typeof(generation),generation,typeof(launch_nonce),launch_nonce,typeof(run_epoch),run_epoch,typeof(pid),pid,typeof(uid),uid,typeof(start_time_ticks),start_time_ticks,typeof(pidns_device),pidns_device,typeof(pidns_inode),pidns_inode,typeof(cgroup_mount_id),cgroup_mount_id,typeof(cgroup_device),cgroup_device,typeof(cgroup_inode),cgroup_inode FROM pilot_bootstrap_handoffs`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var genType, nonceType, epochType, pidType, uidType string
		var startType, pidnsDeviceType, pidnsInodeType, cgroupMountType, cgroupDeviceType, cgroupInodeType string
		var generation, nonce, epoch, pid, uid, startTicks, pidnsDevice, pidnsInode, cgroupMountID, cgroupDevice, cgroupInode any
		if rows.Scan(&genType, &generation, &nonceType, &nonce, &epochType, &epoch, &pidType, &pid, &uidType, &uid,
			&startType, &startTicks, &pidnsDeviceType, &pidnsDevice, &pidnsInodeType, &pidnsInode,
			&cgroupMountType, &cgroupMountID, &cgroupDeviceType, &cgroupDevice, &cgroupInodeType, &cgroupInode) != nil ||
			genType != "blob" || nonceType != "text" || epochType != "text" || pidType != "integer" || uidType != "integer" ||
			startType != "blob" || pidnsDeviceType != "blob" || pidnsInodeType != "blob" || cgroupMountType != "blob" || cgroupDeviceType != "blob" || cgroupInodeType != "blob" {
			return false
		}
		nonceText, nonceOK := nonce.(string)
		epochText, epochOK := epoch.(string)
		pidValue, pidOK := pid.(int64)
		uidValue, uidOK := uid.(int64)
		if !nonceOK || !epochOK || !pidOK || !uidOK || !isLowerHexString(nonceText, 32) || !isLowerHexString(epochText, 32) ||
			pidValue <= 0 || pidValue > 1<<31-1 || uidValue <= 0 || uidValue > 1<<32-1 ||
			!validRunRootUint64Blob(generation) || !validRunRootUint64Blob(startTicks) || !validRunRootUint64Blob(pidnsDevice) ||
			!validRunRootUint64Blob(pidnsInode) || !validRunRootUint64Blob(cgroupMountID) || !validRunRootUint64Blob(cgroupDevice) || !validRunRootUint64Blob(cgroupInode) {
			return false
		}
	}
	return rows.Err() == nil
}

func validRunRootUint64Blob(value any) bool {
	encoded, ok := value.([]byte)
	return ok && len(encoded) == 8 && decodeRunRootUint64(encoded) != 0
}

func validRunRootIndex(ctx context.Context, conn *sql.Conn, table, name string, columns []string, partial bool, _ string) bool {
	rows, err := conn.QueryContext(ctx, `PRAGMA index_list(`+table+`)`)
	if err != nil {
		return false
	}
	found, unique, partialFlag := false, 0, 0
	for rows.Next() {
		var seq, isUnique, isPartial int
		var indexName, origin string
		if rows.Scan(&seq, &indexName, &isUnique, &origin, &isPartial) != nil {
			_ = rows.Close()
			return false
		}
		if indexName == name {
			found, unique, partialFlag = true, isUnique, isPartial
		}
	}
	if rows.Err() != nil {
		_ = rows.Close()
		return false
	}
	_ = rows.Close()
	if !found || unique != 1 || (partialFlag == 1) != partial {
		return false
	}
	info, err := conn.QueryContext(ctx, `PRAGMA index_info(`+name+`)`)
	if err != nil {
		return false
	}
	var got []string
	for info.Next() {
		var seq, cid int
		var column string
		if info.Scan(&seq, &cid, &column) != nil {
			_ = info.Close()
			return false
		}
		got = append(got, column)
	}
	if info.Err() != nil {
		_ = info.Close()
		return false
	}
	_ = info.Close()
	if len(got) != len(columns) {
		return false
	}
	for i := range columns {
		if got[i] != columns[i] {
			return false
		}
	}
	var ddl string
	if conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&ddl) != nil {
		return false
	}
	expected := `CREATE UNIQUE INDEX run_root_one_operation_per_epoch ON run_root_operations(run_epoch)`
	if partial {
		expected = `CREATE UNIQUE INDEX run_root_one_held_lease_per_session ON run_epoch_allocations(principal_ref,session_key_hash) WHERE run_lease_held=1`
	}
	return normalizeRunRootDDL(ddl) == normalizeRunRootDDL(expected)
}

func normalizeRunRootDDL(value string) string {
	var normalized strings.Builder
	inString := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if inString {
			normalized.WriteByte(ch)
			if ch == '\'' {
				if i+1 < len(value) && value[i+1] == '\'' {
					i++
					normalized.WriteByte(value[i])
				} else {
					inString = false
				}
			}
			continue
		}
		if ch == '\'' {
			inString = true
			normalized.WriteByte(ch)
			continue
		}
		if ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t' {
			continue
		}
		if ch >= 'A' && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		normalized.WriteByte(ch)
	}
	return normalized.String()
}

func (l *RunRootLedger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return nil
	}
	conn := l.conn
	l.conn = nil
	return conn.Close()
}

func validateRunRootLedgerRequest(req RunRootLedgerRequest) error {
	if !runEpochPattern.MatchString(string(req.Epoch)) || !validRunRootRequestID(req.RequestID) ||
		!identifier.MatchString(req.PrincipalRef) || !runRootDigestPattern.MatchString(req.SessionKeyHash) ||
		req.GatewayGeneration == 0 || req.SchemaVersion != 1 || !runRootDigestPattern.MatchString(req.PayloadDigest) {
		return errRunRootLedgerInvalid
	}
	for _, field := range req.ProcessIdentity {
		if field == 0 {
			return errRunRootLedgerInvalid
		}
	}
	return nil
}

func (l *RunRootLedger) Prepare(ctx context.Context, req RunRootLedgerRequest) (RunRootLedgerRecord, bool, error) {
	if l == nil || ctx == nil || validateRunRootLedgerRequest(req) != nil {
		return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
	}
	processBytes, err := json.Marshal(req.ProcessIdentity)
	if err != nil {
		return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
	}
	tx, err := l.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
	}
	defer tx.Rollback()
	var existing RunRootLedgerRequest
	var principal, sessionHash, payloadDigest string
	var schemaVersion uint32
	var generation uint64
	var identityBytes []byte
	err = tx.QueryRowContext(ctx, `SELECT principal_ref,session_key_hash,gateway_generation,process_identity,schema_version,payload_digest FROM run_epoch_allocations WHERE run_epoch=?`, req.Epoch).
		Scan(&principal, &sessionHash, &generation, &identityBytes, &schemaVersion, &payloadDigest)
	if err == nil {
		if principal != req.PrincipalRef || sessionHash != req.SessionKeyHash || generation != req.GatewayGeneration ||
			string(identityBytes) != string(processBytes) || schemaVersion != req.SchemaVersion || payloadDigest != req.PayloadDigest {
			return RunRootLedgerRecord{}, false, errRunRootLedgerConflict
		}
		existing.Epoch, existing.PrincipalRef, existing.SessionKeyHash = req.Epoch, principal, sessionHash
		existing.GatewayGeneration, existing.ProcessIdentity, existing.PayloadDigest = generation, req.ProcessIdentity, payloadDigest
		var operationPrincipal, operationSession, operationDigest string
		var operationGeneration, ownerGeneration uint64
		var operationSchemaVersion uint32
		var operationIdentity, receipt []byte
		var state LedgerState
		var response string
		if err = tx.QueryRowContext(ctx, `SELECT principal_ref,session_key_hash,gateway_generation,process_identity,schema_version,payload_digest,operation_state,owner_generation,response_code,evidence_receipt FROM run_root_operations WHERE run_epoch=? AND request_id=?`, req.Epoch, req.RequestID).
			Scan(&operationPrincipal, &operationSession, &operationGeneration, &operationIdentity, &operationSchemaVersion, &operationDigest, &state, &ownerGeneration, &response, &receipt); err != nil {
			return RunRootLedgerRecord{}, false, errRunRootLedgerConflict
		}
		if operationPrincipal != principal || operationSession != sessionHash || operationGeneration != generation || operationSchemaVersion != schemaVersion ||
			string(operationIdentity) != string(processBytes) || operationDigest != payloadDigest {
			return RunRootLedgerRecord{}, false, errRunRootLedgerConflict
		}
		if state == LedgerCompleted || state == LedgerAbandoned {
			if validatePersistedRunRootOperationReceipt(receipt, req, ownerGeneration, string(state), response) != nil {
				return RunRootLedgerRecord{}, false, errRunRootLedgerCorrupt
			}
		} else if len(receipt) != 0 || !validPersistedRunRootStateResponse(string(state), response) {
			return RunRootLedgerRecord{}, false, errRunRootLedgerCorrupt
		}
		var allocationState LedgerState
		var terminal string
		var held int
		if err = tx.QueryRowContext(ctx, `SELECT allocation_state,run_terminal_state,run_lease_held FROM run_epoch_allocations WHERE run_epoch=?`, req.Epoch).
			Scan(&allocationState, &terminal, &held); err != nil {
			return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
		}
		return RunRootLedgerRecord{State: state, AllocationState: allocationState, RunTerminalState: terminal, LeaseHeld: held == 1,
			OwnerGeneration: ownerGeneration, CachedResponse: response, EvidenceReceipt: append([]byte(nil), receipt...)}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
	}
	var globalCount, principalCount int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM run_epoch_allocations`).Scan(&globalCount); err != nil {
		return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM run_epoch_allocations WHERE principal_ref=?`, req.PrincipalRef).Scan(&principalCount); err != nil {
		return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
	}
	if globalCount >= maxRunRootEpochAllocations || principalCount >= maxRunRootEpochAllocationsPerPrincipal {
		return RunRootLedgerRecord{}, false, errRunRootLedgerFull
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO run_epoch_allocations(run_epoch,principal_ref,session_key_hash,gateway_generation,process_identity,schema_version,payload_digest,allocation_state,run_terminal_state,run_lease_held) VALUES(?,?,?,?,?,?,?,'prepared','none',1)`, req.Epoch, req.PrincipalRef, req.SessionKeyHash, req.GatewayGeneration, processBytes, req.SchemaVersion, req.PayloadDigest); err != nil {
		if isSQLiteConstraint(err) {
			return RunRootLedgerRecord{}, false, errRunRootLedgerConflict
		}
		return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO run_root_operations(run_epoch,request_id,operation_kind,principal_ref,session_key_hash,gateway_generation,process_identity,schema_version,payload_digest,operation_state) VALUES(?,?,'run_root_create',?,?,?,?,?,?,'prepared')`, req.Epoch, req.RequestID, req.PrincipalRef, req.SessionKeyHash, req.GatewayGeneration, processBytes, req.SchemaVersion, req.PayloadDigest); err != nil {
		return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
	}
	if err = tx.Commit(); err != nil {
		return RunRootLedgerRecord{}, false, errRunRootLedgerInvalid
	}
	return RunRootLedgerRecord{State: LedgerPrepared, AllocationState: LedgerPrepared, RunTerminalState: "none", LeaseHeld: true}, true, nil
}

// Claim is a durable CAS. Replayed claims never dispatch and prepared may
// acquire only one monotonically increasing owner generation.
func (l *RunRootLedger) Claim(ctx context.Context, epoch RunEpoch, requestID string) (uint64, error) {
	if l == nil || ctx == nil || !runEpochPattern.MatchString(string(epoch)) || !validRunRootRequestID(requestID) {
		return 0, errRunRootLedgerInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return 0, errRunRootLedgerInvalid
	}
	tx, err := l.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, errRunRootLedgerInvalid
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE run_root_operations SET operation_state='executing',owner_generation=owner_generation+1 WHERE run_epoch=? AND request_id=? AND operation_state='prepared'`, epoch, requestID)
	if err != nil {
		return 0, errRunRootLedgerInvalid
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return 0, errRunRootLedgerCAS
	}
	result, err = tx.ExecContext(ctx, `UPDATE run_epoch_allocations SET allocation_state='executing' WHERE run_epoch=? AND allocation_state='prepared'`, epoch)
	if err != nil {
		return 0, errRunRootLedgerInvalid
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return 0, errRunRootLedgerCAS
	}
	var owner uint64
	if err = tx.QueryRowContext(ctx, `SELECT owner_generation FROM run_root_operations WHERE run_epoch=? AND request_id=?`, epoch, requestID).Scan(&owner); err != nil {
		return 0, errRunRootLedgerInvalid
	}
	if err = tx.Commit(); err != nil {
		return 0, errRunRootLedgerInvalid
	}
	return owner, nil
}

// RecoverAfterRestart fences every operation that had crossed the durable
// executing CAS. Startup must invoke this exactly once before admitting peers;
// prepared rows remain safe to claim because dispatch was not authorized.
func (l *RunRootLedger) RecoverAfterRestart(ctx context.Context) error {
	if l == nil || ctx == nil {
		return errRunRootLedgerInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return errRunRootLedgerInvalid
	}
	tx, err := l.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return errRunRootLedgerInvalid
	}
	defer tx.Rollback()
	var inconsistent int
	const invalidStates = `SELECT count(*) FROM run_epoch_allocations a
LEFT JOIN run_root_operations o ON o.run_epoch=a.run_epoch
WHERE o.run_epoch IS NULL OR
 a.principal_ref!=o.principal_ref OR a.session_key_hash!=o.session_key_hash OR
 a.gateway_generation!=o.gateway_generation OR a.process_identity!=o.process_identity OR
 a.schema_version!=o.schema_version OR a.payload_digest!=o.payload_digest OR o.operation_kind!='run_root_create' OR
 (a.allocation_state='prepared' AND (o.operation_state!='prepared' OR a.run_terminal_state!='none' OR a.run_lease_held!=1 OR o.owner_generation!=0)) OR
 (a.allocation_state='executing' AND (o.operation_state!='executing' OR a.run_terminal_state!='none' OR a.run_lease_held!=1 OR o.owner_generation<=0)) OR
 (a.allocation_state='completed' AND (o.operation_state!='completed' OR a.run_terminal_state!='active' OR a.run_lease_held!=1)) OR
 (a.allocation_state='abandoned' AND (o.operation_state!='abandoned' OR a.run_terminal_state!='abandoned' OR a.run_lease_held!=0)) OR
 (a.allocation_state='indeterminate' AND (o.operation_state NOT IN ('indeterminate','abandoned') OR a.run_terminal_state!='indeterminate' OR a.run_lease_held!=1))`
	if err = tx.QueryRowContext(ctx, invalidStates).Scan(&inconsistent); err != nil || inconsistent != 0 {
		return errRunRootLedgerCorrupt
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM run_root_operations o LEFT JOIN run_epoch_allocations a ON a.run_epoch=o.run_epoch WHERE a.run_epoch IS NULL`).Scan(&inconsistent); err != nil || inconsistent != 0 {
		return errRunRootLedgerCorrupt
	}
	var corruptReceipts int
	rows, err := tx.QueryContext(ctx, `SELECT a.run_epoch,a.principal_ref,a.session_key_hash,a.gateway_generation,a.process_identity,a.schema_version,a.payload_digest,o.request_id,o.owner_generation,o.operation_state,o.response_code,o.evidence_receipt FROM run_epoch_allocations a JOIN run_root_operations o USING(run_epoch) WHERE o.operation_state IN ('completed','abandoned')`)
	if err != nil {
		return errRunRootLedgerInvalid
	}
	for rows.Next() {
		var req RunRootLedgerRequest
		var identity []byte
		var owner uint64
		var state string
		var response string
		var receipt []byte
		if err = rows.Scan(&req.Epoch, &req.PrincipalRef, &req.SessionKeyHash, &req.GatewayGeneration, &identity, &req.SchemaVersion, &req.PayloadDigest, &req.RequestID, &owner, &state, &response, &receipt); err != nil {
			_ = rows.Close()
			return errRunRootLedgerInvalid
		}
		if json.Unmarshal(identity, &req.ProcessIdentity) != nil || validatePersistedRunRootOperationReceipt(receipt, req, owner, state, response) != nil {
			corruptReceipts++
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return errRunRootLedgerInvalid
	}
	_ = rows.Close()
	if corruptReceipts != 0 {
		return errRunRootLedgerCorrupt
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM run_root_operations WHERE
 (operation_state IN ('prepared','executing') AND (length(evidence_receipt)!=0 OR response_code!='')) OR
 (operation_state='indeterminate' AND (length(evidence_receipt)!=0 OR response_code NOT IN ('ipc_result_indeterminate','restart_required')))`).Scan(&inconsistent); err != nil || inconsistent != 0 {
		return errRunRootLedgerCorrupt
	}
	if _, err = tx.ExecContext(ctx, `UPDATE run_epoch_allocations SET allocation_state='indeterminate',run_terminal_state='indeterminate',run_lease_held=1 WHERE allocation_state='executing' AND EXISTS (SELECT 1 FROM run_root_operations o WHERE o.run_epoch=run_epoch_allocations.run_epoch AND o.operation_state='executing')`); err != nil {
		return errRunRootLedgerInvalid
	}
	if _, err = tx.ExecContext(ctx, `UPDATE run_root_operations SET operation_state='indeterminate',response_code='ipc_result_indeterminate' WHERE operation_state='executing'`); err != nil {
		return errRunRootLedgerInvalid
	}
	if err = tx.Commit(); err != nil {
		return errRunRootLedgerInvalid
	}
	return nil
}

// Finish atomically persists the operation receipt and epoch-allocation/run
// state. `run-root-created-v1`, `run-root-create-effect-absent-v1`, and
// `run-root-create-collision-v1` receipts are constructed by the caller from
// verified platform evidence; ambiguous results must use the indeterminate
// terminal and must never release the lease.
func (l *RunRootLedger) Finish(ctx context.Context, epoch RunEpoch, requestID string, owner uint64, outcome RunRootTerminal, response string, evidence RunRootLedgerEvidence) error {
	if l == nil || ctx == nil || !runEpochPattern.MatchString(string(epoch)) || !validRunRootRequestID(requestID) || owner == 0 ||
		!validRunRootResponse(outcome, response) {
		return errRunRootLedgerInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return errRunRootLedgerInvalid
	}
	var opState, allocationState, runState, lease string
	switch outcome {
	case RunRootTerminalCreated:
		opState, allocationState, runState, lease = "completed", "completed", "active", "1"
	case RunRootTerminalNoEffect:
		// An absence receipt proves only that this syscall created no entry.
		// Until the separate docs/43 worker/containment/run-terminal proof is
		// atomically committed, keep the permanent allocation fenced.
		opState, allocationState, runState, lease = "abandoned", "indeterminate", "indeterminate", "1"
	case RunRootTerminalCollision:
		opState, allocationState, runState, lease = "abandoned", "indeterminate", "indeterminate", "1"
	case RunRootTerminalIndeterminate:
		opState, allocationState, runState, lease = "indeterminate", "indeterminate", "indeterminate", "1"
	default:
		return errRunRootLedgerInvalid
	}
	expectedKind := map[RunRootTerminal]string{
		RunRootTerminalCreated: "run-root-created-v1", RunRootTerminalNoEffect: "run-root-create-effect-absent-v1",
		RunRootTerminalCollision: "run-root-create-collision-v1",
	}[outcome]
	if outcome == RunRootTerminalIndeterminate {
		if evidence.kind != "" {
			return errRunRootLedgerInvalid
		}
	} else {
		if evidence.kind != expectedKind || !validRunRootSourceIdentity(evidence.kind, evidence.identity) ||
			!validRunRootSourcePayload(evidence.kind, evidence.payload) {
			return errRunRootLedgerInvalid
		}
		verified, err := buildRunRootEvidenceItem(evidence.kind, evidence.identity, evidence.payload)
		if err != nil || verified != evidence.item {
			return errRunRootLedgerInvalid
		}
	}
	tx, err := l.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return errRunRootLedgerInvalid
	}
	defer tx.Rollback()
	var req RunRootLedgerRequest
	var processIdentityJSON []byte
	if err = tx.QueryRowContext(ctx, `SELECT principal_ref,session_key_hash,gateway_generation,process_identity,schema_version,payload_digest FROM run_root_operations WHERE run_epoch=? AND request_id=? AND operation_state='executing' AND owner_generation=?`, epoch, requestID, owner).
		Scan(&req.PrincipalRef, &req.SessionKeyHash, &req.GatewayGeneration, &processIdentityJSON, &req.SchemaVersion, &req.PayloadDigest); err != nil {
		return errRunRootLedgerCAS
	}
	req.Epoch, req.RequestID = epoch, requestID
	if err = json.Unmarshal(processIdentityJSON, &req.ProcessIdentity); err != nil {
		return errRunRootLedgerCorrupt
	}
	var receipt []byte
	if outcome != RunRootTerminalIndeterminate {
		if !runRootEvidenceMatchesOperation(evidence, epoch, requestID, req.GatewayGeneration, processIdentityJSON) {
			return errRunRootLedgerInvalid
		}
		receiptOutcome := "abandoned"
		if outcome == RunRootTerminalCreated {
			receiptOutcome = "completed"
		}
		operationReceipt, receiptErr := newRunRootOperationReceipt(req, owner, receiptOutcome, evidence)
		if receiptErr != nil {
			return errRunRootLedgerInvalid
		}
		receipt = operationReceipt.Bytes()
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_root_operations SET operation_state=?,response_code=?,evidence_receipt=? WHERE run_epoch=? AND request_id=? AND operation_state='executing' AND owner_generation=?`, opState, response, receipt, epoch, requestID, owner)
	if err != nil {
		return errRunRootLedgerInvalid
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errRunRootLedgerCAS
	}
	result, err = tx.ExecContext(ctx, `UPDATE run_epoch_allocations SET allocation_state=?,run_terminal_state=?,run_lease_held=? WHERE run_epoch=? AND allocation_state='executing' AND run_lease_held=1`, allocationState, runState, lease, epoch)
	if err != nil {
		return errRunRootLedgerInvalid
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return errRunRootLedgerCAS
	}
	if err = tx.Commit(); err != nil {
		return errRunRootLedgerInvalid
	}
	return nil
}

func validRunRootResponse(outcome RunRootTerminal, response string) bool {
	switch outcome {
	case RunRootTerminalCreated:
		return response == "ok"
	case RunRootTerminalNoEffect:
		return response == "verified_io_failed" || response == "verified_path_invalid" || response == "verified_policy_mismatch"
	case RunRootTerminalCollision:
		return response == "verified_policy_mismatch"
	case RunRootTerminalIndeterminate:
		return response == "ipc_result_indeterminate" || response == "restart_required"
	default:
		return false
	}
}

func runRootEvidenceMatchesOperation(evidence RunRootLedgerEvidence, epoch RunEpoch, requestID string, gatewayGeneration uint64, processIdentityJSON []byte) bool {
	fields, ok := evidence.payload.([]any)
	if !ok || len(fields) < 4 || fields[0] != string(epoch) || fields[1] != requestID {
		return false
	}
	storedProcess := [6]uint64{}
	if json.Unmarshal(processIdentityJSON, &storedProcess) != nil {
		return false
	}
	expectedGeneration, err1 := encodeRunRootCBOR([]any{gatewayGeneration})
	actualGeneration, err2 := encodeRunRootCBOR([]any{fields[2]})
	expectedProcess, err3 := encodeRunRootCBOR([]any{[]any{storedProcess[0], storedProcess[1], storedProcess[2], storedProcess[3], storedProcess[4], storedProcess[5]}})
	actualProcess, err4 := encodeRunRootCBOR([]any{fields[3]})
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil ||
		string(expectedGeneration) != string(actualGeneration) || string(expectedProcess) != string(actualProcess) {
		return false
	}
	if evidence.kind == "run-root-created-v1" {
		identity, ok := evidence.identity.([]any)
		if !ok || len(identity) != 6 || identity[0] != identity[3] || identity[2] != identity[5] {
			return false
		}
	}
	return true
}

func (l *RunRootLedger) Lookup(ctx context.Context, epoch RunEpoch, requestID string) (RunRootLedgerRecord, error) {
	if l == nil || ctx == nil || !runEpochPattern.MatchString(string(epoch)) || !validRunRootRequestID(requestID) {
		return RunRootLedgerRecord{}, errRunRootLedgerInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return RunRootLedgerRecord{}, errRunRootLedgerInvalid
	}
	var result RunRootLedgerRecord
	var held int
	err := l.conn.QueryRowContext(ctx, `SELECT o.operation_state,a.allocation_state,a.run_terminal_state,a.run_lease_held,o.owner_generation,o.response_code,o.evidence_receipt FROM run_root_operations o JOIN run_epoch_allocations a USING(run_epoch) WHERE o.run_epoch=? AND o.request_id=?`, epoch, requestID).
		Scan(&result.State, &result.AllocationState, &result.RunTerminalState, &held, &result.OwnerGeneration, &result.CachedResponse, &result.EvidenceReceipt)
	if err != nil {
		return RunRootLedgerRecord{}, errRunRootLedgerInvalid
	}
	result.LeaseHeld = held == 1
	result.EvidenceReceipt = append([]byte(nil), result.EvidenceReceipt...)
	return result, nil
}

func encodeRunRootUint64(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}

func decodeRunRootUint64(encoded []byte) uint64 {
	if len(encoded) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(encoded)
}

// ConsumeBootstrap durably consumes the launcher's one-shot control tuple.
// It commits before the caller may acknowledge the handoff.
func (l *RunRootLedger) ConsumeBootstrap(ctx context.Context, request bootstrapHandoffRequest) (bool, error) {
	started := time.Now()
	logResult := func(result, errorClass string) {
		if l == nil || l.logger == nil {
			return
		}
		elapsed := time.Since(started).Milliseconds()
		if elapsed < 0 {
			elapsed = 0
		}
		l.logger.InfoCtx(ctx, "bootstrap.consume", map[string]any{"generation": request.Generation, "result": result,
			"stage": "bootstrap.consume", "elapsed_ms": elapsed, "error_class": errorClass})
	}
	if l == nil || ctx == nil || !validBootstrapHandoff(request) {
		logResult("rejected", "invalid_request")
		return false, errRunRootLedgerInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil || l.fenced {
		logResult("restart_required", "ledger_unavailable")
		return false, errRunRootLedgerInvalid
	}
	conn := l.conn
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		l.fenceLocked()
		logResult("restart_required", "writer_unavailable")
		return false, errRunRootLedgerInvalid
	}
	rollback := func() error {
		_, err := conn.ExecContext(context.Background(), `ROLLBACK`)
		if err != nil {
			l.fenceLocked()
		}
		return err
	}
	generation := encodeRunRootUint64(request.Generation)
	var highWater []byte
	err := conn.QueryRowContext(ctx, `SELECT generation FROM pilot_bootstrap_handoffs ORDER BY generation DESC LIMIT 1`).Scan(&highWater)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = rollback()
		l.fenceLocked()
		logResult("restart_required", "highwater_read_failed")
		return false, errRunRootLedgerInvalid
	}
	if err == nil && bytes.Compare(generation, highWater) <= 0 {
		if rollback() != nil {
			logResult("restart_required", "rollback_failed")
			return false, errRunRootLedgerInvalid
		}
		logResult("rejected", "stale_generation")
		return false, nil
	}
	var duplicateCount int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM pilot_bootstrap_handoffs WHERE launch_nonce=? OR run_epoch=?`, request.LaunchNonce, request.RunEpoch).Scan(&duplicateCount); err != nil {
		_ = rollback()
		l.fenceLocked()
		logResult("restart_required", "uniqueness_check_failed")
		return false, errRunRootLedgerInvalid
	}
	if duplicateCount != 0 {
		if rollback() != nil {
			logResult("restart_required", "rollback_failed")
			return false, errRunRootLedgerInvalid
		}
		logResult("rejected", "tuple_replay")
		return false, nil
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO pilot_bootstrap_handoffs(generation,launch_nonce,run_epoch,pid,uid,start_time_ticks,pidns_device,pidns_inode,cgroup_mount_id,cgroup_device,cgroup_inode) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		generation, request.LaunchNonce, request.RunEpoch, int64(request.PID), int64(request.UID),
		encodeRunRootUint64(request.StartTimeTicks), encodeRunRootUint64(request.PIDNSDevice), encodeRunRootUint64(request.PIDNSInode),
		encodeRunRootUint64(request.CgroupMountID), encodeRunRootUint64(request.CgroupDevice), encodeRunRootUint64(request.CgroupInode))
	if err != nil {
		_ = rollback()
		l.fenceLocked()
		logResult("restart_required", "persist_failed")
		return false, errRunRootLedgerInvalid
	}
	if l.commitBootstrap != nil {
		err = l.commitBootstrap(ctx, conn)
	} else {
		_, err = conn.ExecContext(ctx, `COMMIT`)
	}
	if err != nil {
		l.fenceLocked()
		logResult("restart_required", "commit_uncertain")
		return false, errRunRootLedgerInvalid
	}
	logResult("accepted", "")
	return true, nil
}

func (l *RunRootLedger) fenceLocked() {
	if l == nil {
		return
	}
	l.fenced = true
	if l.conn != nil {
		// Returning an uncertain transactional connection to the caller's pool
		// could let another component reuse it. ErrBadConn forces database/sql
		// to discard the physical connection before releasing the pinned Conn.
		_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = l.conn.Close()
		l.conn = nil
	}
}

func isSQLiteConstraint(err error) bool {
	return err != nil && (strings.Contains(strings.ToLower(err.Error()), "constraint") || strings.Contains(strings.ToLower(err.Error()), "unique"))
}
