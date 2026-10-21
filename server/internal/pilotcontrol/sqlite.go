//go:build cgo

package pilotcontrol

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sqlite3 "github.com/mattn/go-sqlite3"
)

var (
	errStoreClosed = errors.New("pilot control store closed")
)

const approvedDDLManifestSHA256 = "54558381ea66be5305c4ba037e93ad7be53d23f6b8c28785b8e3ca0222ac2324"
const approvedSchemaInventorySHA256 = "38173f3c9cddae29e2627505e17058a9a97bab265502cd87d84d78c6fc8af672"

//go:embed schema_inventory_v3.json
var schemaInventoryJSON []byte

// sqliteRecursiveAction is SQLITE_RECURSIVE (33). go-sqlite3 v1.14.22 omits
// the exported Go constant even though the bundled SQLite authorizer emits it.
const sqliteRecursiveAction = 33

type capability uint8

const (
	capResolvePrincipal capability = iota + 1
	capPublishAuthorizationFence
	capAdvanceJournalAnchor
	capConsumeBootstrap
	capCommitLifecycleBoundary
	capConsumeRecoveryControl
	capPrepareRunAllocation
)

type connectionState struct {
	mu             sync.RWMutex
	capability     capability
	allowed        map[string]map[int]struct{}
	trustedTrigger bool
}

func (s *connectionState) authorize(action int, arg1, arg2, _ string, triggerName string) int {
	if action == sqlite3.SQLITE_SELECT || action == sqlite3.SQLITE_READ ||
		action == sqlite3.SQLITE_FUNCTION || action == sqliteRecursiveAction ||
		action == sqlite3.SQLITE_TRANSACTION || action == sqlite3.SQLITE_SAVEPOINT {
		return sqlite3.SQLITE_OK
	}
	if action == sqlite3.SQLITE_PRAGMA && arg2 == "" && readOnlyPragma(arg1) {
		return sqlite3.SQLITE_OK
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.trustedTrigger && s.capability == capPrepareRunAllocation &&
		triggerName == "pilot_allocation_capacity_after_insert" && triggerWriteAllowed(action, arg1) {
		return sqlite3.SQLITE_OK
	}
	if s.capability != 0 {
		if _, alwaysDenied := protectedCapacityTables[arg1]; !alwaysDenied {
			if actions, ok := s.allowed[arg1]; ok {
				if _, ok := actions[action]; ok {
					return sqlite3.SQLITE_OK
				}
			}
		}
	}
	return sqlite3.SQLITE_DENY
}

var protectedCapacityTables = map[string]struct{}{
	"pilot_business_capacity_counts": {},
	"pilot_capacity_alert_events":    {},
}

func readOnlyPragma(name string) bool {
	switch name {
	case "foreign_keys", "recursive_triggers", "trusted_schema", "journal_mode", "user_version", "schema_version", "application_id", "page_size", "encoding", "integrity_check", "quick_check", "table_list":
		return true
	default:
		return false
	}
}

func triggerWriteAllowed(action int, table string) bool {
	switch table {
	case "pilot_business_capacity_counts":
		return action == sqlite3.SQLITE_INSERT || action == sqlite3.SQLITE_UPDATE
	case "pilot_capacity_alert_events":
		return action == sqlite3.SQLITE_INSERT
	default:
		return false
	}
}

func capabilityActions(cap capability) map[string][]int {
	switch cap {
	case capResolvePrincipal:
		return map[string][]int{"pilot_principal_aliases": {sqlite3.SQLITE_INSERT}}
	case capPublishAuthorizationFence:
		return map[string][]int{
			"pilot_authorization_state":   {sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE},
			"pilot_session_fences":        {sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE},
			"pilot_fixture_grants":        {sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE},
			"pilot_authz_update_receipts": {sqlite3.SQLITE_INSERT},
		}
	case capAdvanceJournalAnchor:
		return map[string][]int{"pilot_launcher_journal_anchor": {sqlite3.SQLITE_UPDATE}}
	case capConsumeBootstrap:
		return map[string][]int{
			"pilot_control_generations": {sqlite3.SQLITE_INSERT},
			"pilot_bootstrap_handoffs":  {sqlite3.SQLITE_INSERT},
			"pilot_run_lifecycle":       {sqlite3.SQLITE_INSERT},
		}
	case capCommitLifecycleBoundary:
		return map[string][]int{"pilot_run_lifecycle": {sqlite3.SQLITE_UPDATE}}
	case capConsumeRecoveryControl:
		return map[string][]int{"pilot_control_generations": {sqlite3.SQLITE_INSERT}}
	case capPrepareRunAllocation:
		return map[string][]int{"run_epoch_allocations": {sqlite3.SQLITE_INSERT}}
	default:
		return nil
	}
}

type storeOptions struct {
	connectHook            func(*sqlite3.SQLiteConn) error
	beforeValidate         func(*sqlite3.SQLiteConn) error
	skipPreflight          bool
	trustedTrigger         bool
	trustedTriggerForTests bool
}

type sqliteConnector struct {
	driver *sqlite3.SQLiteDriver
	dsn    string
	store  *Store
}

type protectedConn struct {
	*sqlite3.SQLiteConn
	state *connectionState
}

func (c *protectedConn) Close() error { return c.SQLiteConn.Close() }

func (c *sqliteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	if c.store == nil { // Test-only raw connection used to prepare isolated fixtures.
		return conn, nil
	}
	sqliteConn, ok := conn.(*sqlite3.SQLiteConn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("unexpected SQLite driver connection type")
	}
	c.store.mu.Lock()
	state := c.store.states[sqliteConn]
	delete(c.store.states, sqliteConn)
	closed := c.store.closed
	c.store.mu.Unlock()
	if state == nil || closed {
		_ = sqliteConn.Close()
		if closed {
			return nil, errStoreClosed
		}
		return nil, errors.New("SQLite connection hook state missing")
	}
	return &protectedConn{SQLiteConn: sqliteConn, state: state}, nil
}

func (c *sqliteConnector) Driver() driver.Driver { return c.driver }

// Store owns an isolated database/sql pool. It deliberately exposes no DB or raw SQL API.
type Store struct {
	db        *sql.DB
	mu        sync.Mutex
	admission sync.RWMutex
	hookMu    sync.Mutex
	states    map[*sqlite3.SQLiteConn]*connectionState
	closed    bool
	fatal     error
}

// Open validates an existing v3 schema read-only before creating an authorizer-protected pool.
// It never creates or mutates schema and cannot publish a Pilot service by itself.
func Open(ctx context.Context, dsn string, maxOpen int) (*Store, error) {
	return openWithOptions(ctx, dsn, maxOpen, storeOptions{trustedTrigger: true})
}

type schemaInventory struct {
	SchemaVersion       int               `json:"schema_version"`
	DDLManifestSHA256   string            `json:"ddl_manifest_sha256"`
	ExplicitObjectCount int               `json:"explicit_object_count"`
	Objects             []schemaObjectKey `json:"objects"`
}

type schemaObjectKey struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	TableName string `json:"table_name"`
	SQLSHA256 string `json:"sql_sha256"`
}

func loadApprovedSchemaInventory() (schemaInventory, error) {
	var inventory schemaInventory
	digest := sha256.Sum256(schemaInventoryJSON)
	if hash := hex.EncodeToString(digest[:]); hash != approvedSchemaInventorySHA256 {
		return inventory, errors.New("embedded v3 schema inventory content hash is not approved")
	}
	if err := json.Unmarshal(schemaInventoryJSON, &inventory); err != nil {
		return inventory, fmt.Errorf("embedded v3 inventory is invalid: %w", err)
	}
	if inventory.SchemaVersion != 3 || inventory.DDLManifestSHA256 != approvedDDLManifestSHA256 || inventory.ExplicitObjectCount != len(inventory.Objects) {
		return inventory, errors.New("embedded v3 inventory does not match the approved DDL manifest")
	}
	return inventory, nil
}

func preflightSchema(path string) error {
	if strings.HasPrefix(path, "file:") || !filepath.IsAbs(path) {
		return errors.New("Pilot v3 database path must be an absolute filesystem path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("Pilot v3 database path is not a regular non-symlink file")
	}
	inventory, err := loadApprovedSchemaInventory()
	if err != nil {
		return err
	}

	readOnlyURI := readOnlySQLiteURI(path)
	rawConn, err := (&sqlite3.SQLiteDriver{}).Open(readOnlyURI)
	if err != nil {
		return err
	}
	defer rawConn.Close()
	conn, ok := rawConn.(*sqlite3.SQLiteConn)
	if !ok {
		return errors.New("unexpected SQLite preflight connection type")
	}

	return validateSchemaConnection(conn, inventory)
}

func readOnlySQLiteURI(path string) string {
	uriPath := strings.ReplaceAll(filepath.ToSlash(path), `\`, "/")
	// url.URL interprets a Windows drive prefix as an authority unless the
	// absolute path has a leading slash: file:///C:/... is the SQLite URI form.
	if len(uriPath) >= 3 && uriPath[1] == ':' && uriPath[2] == '/' {
		uriPath = "/" + uriPath
	}
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: url.Values{"mode": []string{"ro"}}.Encode()}).String()
}

func validateSchemaConnection(conn *sqlite3.SQLiteConn, inventory schemaInventory) error {
	mode, err := querySingleValue(conn, "PRAGMA journal_mode")
	if err != nil {
		return fmt.Errorf("read existing journal mode: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("expected existing WAL database (mode=%q)", mode)
	}
	version, err := querySingleValue(conn, "PRAGMA user_version")
	if err != nil {
		return fmt.Errorf("read sqlite user_version: %w", err)
	}
	if version != "3" {
		return fmt.Errorf("expected user_version 3 (version=%q)", version)
	}
	metaVersion, err := querySingleValue(conn, "SELECT schema_version FROM run_root_ledger_meta WHERE singleton=1")
	if err != nil {
		return fmt.Errorf("read v3 metadata row: %w", err)
	}
	if metaVersion != "3" {
		return fmt.Errorf("expected one v3 metadata row (version=%q)", metaVersion)
	}
	metaRows, err := querySingleValue(conn, "SELECT count(*) FROM run_root_ledger_meta")
	if err != nil {
		return fmt.Errorf("count v3 metadata rows: %w", err)
	}
	if metaRows != "1" {
		return fmt.Errorf("expected singleton v3 metadata row (count=%q)", metaRows)
	}

	expected := make(map[string]schemaObjectKey, len(inventory.Objects))
	for _, object := range inventory.Objects {
		expected[object.Type+"\x00"+object.Name] = object
	}
	rows, err := conn.Query("SELECT type,name,tbl_name,sql FROM sqlite_master WHERE type IN ('table','index','trigger','view') AND substr(name,1,7)<>'sqlite_' ORDER BY type,name", nil)
	if err != nil {
		return err
	}
	actualCount := 0
	for {
		values := make([]driver.Value, len(rows.Columns()))
		nextErr := rows.Next(values)
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			_ = rows.Close()
			return nextErr
		}
		if len(values) != 4 {
			_ = rows.Close()
			return errors.New("unexpected sqlite_master result shape")
		}
		kind, name := fmt.Sprint(values[0]), fmt.Sprint(values[1])
		object, ok := expected[kind+"\x00"+name]
		if !ok || object.TableName != fmt.Sprint(values[2]) || object.SQLSHA256 != hashNormalizedSQL(fmt.Sprint(values[3])) {
			_ = rows.Close()
			return fmt.Errorf("sqlite schema object rejected: %s/%s", kind, name)
		}
		delete(expected, kind+"\x00"+name)
		actualCount++
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if actualCount != inventory.ExplicitObjectCount || len(expected) != 0 {
		return fmt.Errorf("sqlite schema inventory mismatch: found=%d missing=%d", actualCount, len(expected))
	}
	quickCheck, err := querySingleValue(conn, "PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("sqlite quick_check query failed: %w", err)
	}
	if quickCheck != "ok" {
		return fmt.Errorf("sqlite quick_check failed: %q", quickCheck)
	}
	fkRows, err := conn.Query("PRAGMA foreign_key_check", nil)
	if err != nil {
		return err
	}
	values := make([]driver.Value, len(fkRows.Columns()))
	if err := fkRows.Next(values); !errors.Is(err, io.EOF) {
		_ = fkRows.Close()
		if err == nil {
			return errors.New("sqlite foreign_key_check found a violation")
		}
		return err
	}
	return fkRows.Close()
}

func querySingleValue(conn *sqlite3.SQLiteConn, query string) (string, error) {
	rows, err := conn.Query(query, nil)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	values := make([]driver.Value, len(rows.Columns()))
	if err := rows.Next(values); err != nil {
		return "", err
	}
	if len(values) != 1 {
		return "", errors.New("expected a single sqlite value")
	}
	var extra []driver.Value = make([]driver.Value, len(values))
	if err := rows.Next(extra); !errors.Is(err, io.EOF) {
		return "", errors.New("expected exactly one sqlite row")
	}
	return fmt.Sprint(values[0]), nil
}

func hashNormalizedSQL(sql string) string {
	normalized := normalizeSchemaSQL(sql)
	hash := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(hash[:])
}

// normalizeSchemaSQL strips comments and folds formatting whitespace only outside
// quoted SQL tokens. SQLite treats comments as whitespace, so a comment between
// tokens must leave one separator; quoted contents are copied byte-for-byte because
// they can affect defaults, CHECK constraints, trigger messages, and identifiers.
func normalizeSchemaSQL(sql string) string {
	var out strings.Builder
	out.Grow(len(sql))
	pendingSpace := false
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			i += 2
			for i < len(sql) && sql[i] != '\n' && sql[i] != '\r' {
				i++
			}
			pendingSpace = true
			continue
		}
		if c == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			i += 2
			for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			if i+1 < len(sql) {
				i += 2
			}
			pendingSpace = true
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' {
			pendingSpace = true
			i++
			continue
		}
		if pendingSpace && out.Len() > 0 {
			out.WriteByte(' ')
		}
		pendingSpace = false
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			close := c
			if c == '[' {
				close = ']'
			}
			out.WriteByte(c)
			i++
			for i < len(sql) {
				q := sql[i]
				out.WriteByte(q)
				i++
				if q != close {
					continue
				}
				// SQL escapes quote delimiters by doubling them, except SQLite's
				// bracket-quoted identifiers.
				if close != ']' && i < len(sql) && sql[i] == close {
					out.WriteByte(sql[i])
					i++
					continue
				}
				break
			}
			continue
		}
		out.WriteByte(c)
		i++
	}
	normalized := strings.TrimSpace(out.String())
	return strings.TrimSpace(strings.TrimSuffix(normalized, ";"))
}

func openWithOptions(ctx context.Context, dsn string, maxOpen int, opts storeOptions) (*Store, error) {
	if ctx == nil || dsn == "" || maxOpen < 1 {
		return nil, errors.New("invalid pilot control store configuration")
	}
	if !opts.skipPreflight {
		if err := preflightSchema(dsn); err != nil {
			return nil, fmt.Errorf("pilot control schema preflight rejected database: %w", err)
		}
	}
	s := &Store{states: make(map[*sqlite3.SQLiteConn]*connectionState)}
	d := &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		if err := s.configureConnection(conn); err != nil {
			s.setFatal(fmt.Errorf("pilot sqlite connection configuration failed: %w", err))
			return err
		}
		trustedTrigger := opts.trustedTriggerForTests
		if opts.trustedTrigger && !opts.skipPreflight {
			if opts.beforeValidate != nil {
				if err := opts.beforeValidate(conn); err != nil {
					return err
				}
			}
			inventory, err := loadApprovedSchemaInventory()
			if err != nil {
				return err
			}
			if err := validateSchemaConnection(conn, inventory); err != nil {
				s.setFatal(fmt.Errorf("pilot sqlite connection schema validation failed: %w", err))
				return err
			}
			trustedTrigger = true
		}
		state := &connectionState{trustedTrigger: trustedTrigger}
		if opts.connectHook != nil {
			if err := opts.connectHook(conn); err != nil {
				s.setFatal(fmt.Errorf("pilot sqlite authorizer hook failed: %w", err))
				return err
			}
		}
		conn.RegisterAuthorizerWithTriggerName(func(action int, arg1, arg2, databaseName, triggerName string) int {
			return state.authorize(action, arg1, arg2, databaseName, triggerName)
		})
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return errStoreClosed
		}
		s.states[conn] = state
		s.mu.Unlock()
		return nil
	}}
	db := sql.OpenDB(&sqliteConnector{driver: d, dsn: dsn, store: s})
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	s.db = db
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		s.mu.Lock()
		s.closed = true
		s.states = nil
		s.mu.Unlock()
		return nil, fmt.Errorf("pilot control protected sqlite connection failed: %w", err)
	}
	// Materialize the whole configured pool before the caller can publish readiness.
	// This catches hook failures beyond the first lazy database/sql connection.
	connections := make([]*sql.Conn, 0, maxOpen)
	for range maxOpen {
		conn, err := db.Conn(ctx)
		if err != nil {
			for _, held := range connections {
				_ = held.Close()
			}
			_ = db.Close()
			s.mu.Lock()
			s.closed = true
			s.states = nil
			s.mu.Unlock()
			return nil, fmt.Errorf("pilot control sqlite pool authorizer validation failed: %w", err)
		}
		connections = append(connections, conn)
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pilot control sqlite pool release failed: %w", err)
		}
	}
	return s, nil
}

func (s *Store) setFatal(err error) {
	s.admission.Lock()
	s.mu.Lock()
	if s.fatal == nil {
		s.fatal = err
	}
	s.mu.Unlock()
	s.admission.Unlock()
}

func (s *Store) configureConnection(conn *sqlite3.SQLiteConn) error {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	modeRows, err := conn.Query("PRAGMA journal_mode", nil)
	if err != nil {
		return err
	}
	modeValues := make([]driver.Value, len(modeRows.Columns()))
	err = modeRows.Next(modeValues)
	_ = modeRows.Close()
	if err != nil {
		return err
	}
	if len(modeValues) != 1 || fmt.Sprint(modeValues[0]) != "wal" {
		return errors.New("pilot operations database must already use WAL mode")
	}
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA recursive_triggers=ON", "PRAGMA trusted_schema=OFF", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=5000"} {
		if _, err := conn.Exec(pragma, nil); err != nil {
			return err
		}
	}
	checks := []struct {
		query string
		want  string
	}{
		{"PRAGMA journal_mode", "wal"},
		{"PRAGMA foreign_keys", "1"},
		{"PRAGMA recursive_triggers", "1"},
		{"PRAGMA trusted_schema", "0"},
		{"PRAGMA synchronous", "2"},
		{"PRAGMA busy_timeout", "5000"},
	}
	for _, check := range checks {
		rows, err := conn.Query(check.query, nil)
		if err != nil {
			return err
		}
		values := make([]driver.Value, len(rows.Columns()))
		err = rows.Next(values)
		_ = rows.Close()
		if err != nil {
			return err
		}
		if len(values) != 1 || fmt.Sprint(values[0]) != check.want {
			return fmt.Errorf("sqlite connection pragma verification failed: %s", check.query)
		}
	}
	return nil
}

// withCapability is intentionally package-private. A capability is tied to one pinned
// physical connection and is cleared before the connection is released on every exit.
func (s *Store) withCapability(ctx context.Context, cap capability, fn func(*sql.Tx) error) (err error) {
	if s == nil || fn == nil || cap == 0 {
		return errors.New("invalid pilot control capability scope")
	}
	allowed := capabilityActions(cap)
	if allowed == nil {
		return errors.New("unknown pilot control capability")
	}
	s.mu.Lock()
	closed := s.closed || s.db == nil || s.fatal != nil
	fatal := s.fatal
	s.mu.Unlock()
	if closed {
		if fatal != nil {
			return fmt.Errorf("pilot control sqlite store fenced: %w", fatal)
		}
		return errStoreClosed
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	s.admission.RLock()
	defer s.admission.RUnlock()
	s.mu.Lock()
	closed = s.closed || s.fatal != nil
	fatal = s.fatal
	s.mu.Unlock()
	if closed {
		if fatal != nil {
			return fmt.Errorf("pilot control sqlite store fenced: %w", fatal)
		}
		return errStoreClosed
	}
	var state *connectionState
	err = conn.Raw(func(raw any) error {
		protected, ok := raw.(*protectedConn)
		if !ok {
			return errors.New("unexpected pilot sqlite driver connection")
		}
		state = protected.state
		if state == nil {
			return errors.New("pilot sqlite connection lacks authorizer state")
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.capability != 0 {
			return errors.New("nested pilot sqlite capability")
		}
		state.capability = cap
		state.allowed = make(map[string]map[int]struct{}, len(allowed))
		for table, actions := range allowed {
			set := make(map[int]struct{}, len(actions))
			for _, action := range actions {
				set[action] = struct{}{}
			}
			state.allowed[table] = set
		}
		return nil
	})
	if err != nil {
		return err
	}
	defer func() {
		state.mu.Lock()
		state.capability = 0
		state.allowed = nil
		state.mu.Unlock()
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	db := s.db
	s.states = nil
	s.mu.Unlock()
	if db != nil {
		return db.Close()
	}
	return nil
}
