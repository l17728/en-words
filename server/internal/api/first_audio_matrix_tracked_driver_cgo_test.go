//go:build cgo

package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
)

var errFirstAudioSQLiteRegistrationNotLive = errors.New("first-audio SQLite registration is not live")

type firstAudioTrackedSQLiteDriver struct {
	inner  *sqlite3.SQLiteDriver
	retire func(*sqlite3.SQLiteConn)
}

type firstAudioSQLiteConnectionRegistration struct {
	ID  string
	Raw *sqlite3.SQLiteConn
}

// firstAudioSQLiteConnectionRegistry assigns IDs only from ConnectHook and
// retires them only from the physical driver.Conn.Close wrapper.
type firstAudioSQLiteConnectionRegistry struct {
	mu     sync.Mutex
	nextID atomic.Uint64
	byRaw  map[*sqlite3.SQLiteConn]firstAudioSQLiteConnectionRegistration
	byID   map[string]*sqlite3.SQLiteConn
}

func newFirstAudioSQLiteConnectionRegistry() *firstAudioSQLiteConnectionRegistry {
	return &firstAudioSQLiteConnectionRegistry{
		byRaw: make(map[*sqlite3.SQLiteConn]firstAudioSQLiteConnectionRegistration),
		byID:  make(map[string]*sqlite3.SQLiteConn),
	}
}

func (r *firstAudioSQLiteConnectionRegistry) Register(raw *sqlite3.SQLiteConn) (firstAudioSQLiteConnectionRegistration, error) {
	if r == nil || raw == nil {
		return firstAudioSQLiteConnectionRegistration{}, errors.New("cannot register nil SQLite connection")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byRaw[raw]; exists {
		return firstAudioSQLiteConnectionRegistration{}, errors.New("SQLite connection identity already registered")
	}
	id := fmt.Sprintf("conn-%d", r.nextID.Add(1))
	registration := firstAudioSQLiteConnectionRegistration{ID: id, Raw: raw}
	r.byRaw[raw] = registration
	r.byID[id] = raw
	return registration, nil
}

func (r *firstAudioSQLiteConnectionRegistry) Lookup(raw *sqlite3.SQLiteConn) (firstAudioSQLiteConnectionRegistration, bool) {
	if r == nil || raw == nil {
		return firstAudioSQLiteConnectionRegistration{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	registration, ok := r.byRaw[raw]
	if !ok || r.byID[registration.ID] != raw {
		return firstAudioSQLiteConnectionRegistration{}, false
	}
	return registration, true
}

func (r *firstAudioSQLiteConnectionRegistry) IsLive(id string, raw *sqlite3.SQLiteConn) bool {
	registration, ok := r.Lookup(raw)
	return ok && registration.ID == id
}

// WithLiveRegistration holds the registry lock until fn returns, linearizing
// receipt consumption/publication against physical connection retirement.
func (r *firstAudioSQLiteConnectionRegistry) WithLiveRegistration(id string, raw *sqlite3.SQLiteConn, fn func() error) error {
	if r == nil || raw == nil || fn == nil {
		return errors.New("live SQLite registration callback is incomplete")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	registration, ok := r.byRaw[raw]
	if !ok || registration.ID != id || r.byID[id] != raw {
		return errFirstAudioSQLiteRegistrationNotLive
	}
	return fn()
}

func (r *firstAudioSQLiteConnectionRegistry) WithLiveRegistrations(registrations map[string]*sqlite3.SQLiteConn, fn func() error) error {
	if r == nil || len(registrations) == 0 || fn == nil {
		return errFirstAudioSQLiteRegistrationNotLive
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, raw := range registrations {
		registration, ok := r.byRaw[raw]
		if !ok || raw == nil || registration.ID != id || r.byID[id] != raw {
			return errFirstAudioSQLiteRegistrationNotLive
		}
	}
	return fn()
}

func (r *firstAudioSQLiteConnectionRegistry) Retire(raw *sqlite3.SQLiteConn) {
	if r == nil || raw == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if registration, ok := r.byRaw[raw]; ok {
		delete(r.byRaw, raw)
		delete(r.byID, registration.ID)
	}
}

func registerFirstAudioTrackedSQLiteDriver(name string, hook func(*sqlite3.SQLiteConn) error, retire func(*sqlite3.SQLiteConn)) error {
	if name == "" || hook == nil || retire == nil {
		return errors.New("tracked sqlite driver requires name, hook, and retirement callback")
	}
	tracked := &firstAudioTrackedSQLiteDriver{retire: retire}
	tracked.inner = &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		if err := hook(conn); err != nil {
			// SQLiteDriver.Open closes the raw connection itself when a hook
			// fails, before our wrapper exists. Retire the registration here.
			retire(conn)
			return err
		}
		return nil
	}}
	sql.Register(name, tracked)
	return nil
}

func (d *firstAudioTrackedSQLiteDriver) Open(name string) (driver.Conn, error) {
	if d == nil || d.inner == nil || d.retire == nil {
		return nil, errors.New("tracked sqlite driver is incomplete")
	}
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	sqliteConn, ok := conn.(*sqlite3.SQLiteConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("unexpected sqlite driver connection type %T", conn)
	}
	return &firstAudioTrackedSQLiteConn{Conn: conn, raw: sqliteConn, retire: d.retire}, nil
}

type firstAudioTrackedSQLiteConn struct {
	driver.Conn
	raw       *sqlite3.SQLiteConn
	retire    func(*sqlite3.SQLiteConn)
	closeOnce sync.Once
	closeErr  error
	txEpoch   atomic.Uint64
	txActive  atomic.Bool
}

func (c *firstAudioTrackedSQLiteConn) Close() error {
	if c == nil {
		return driver.ErrBadConn
	}
	c.closeOnce.Do(func() {
		// Retire the exact registry identity before releasing the physical
		// connection. A later connection can never inherit its probe receipt.
		c.retire(c.raw)
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}

func (c *firstAudioTrackedSQLiteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if conn, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return conn.PrepareContext(ctx, query)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Conn.Prepare(query)
}

func (c *firstAudioTrackedSQLiteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var tx driver.Tx
	var err error
	if conn, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = conn.BeginTx(ctx, opts)
	} else {
		if opts.Isolation != driver.IsolationLevel(0) || opts.ReadOnly {
			return nil, errors.New("sqlite driver does not support requested transaction options")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tx, err = c.Conn.Begin()
	}
	if err != nil {
		return nil, err
	}
	if !c.txActive.CompareAndSwap(false, true) {
		_ = tx.Rollback()
		return nil, errors.New("tracked SQLite connection already has an active transaction")
	}
	epoch := c.txEpoch.Add(1)
	return &firstAudioTrackedSQLiteTx{Tx: tx, owner: c, epoch: epoch}, nil
}

type firstAudioTrackedSQLiteTx struct {
	driver.Tx
	owner *firstAudioTrackedSQLiteConn
	epoch uint64
	once  sync.Once
}

func (tx *firstAudioTrackedSQLiteTx) finish(err error) {
	if tx == nil || tx.owner == nil {
		return
	}
	if err == nil || tx.owner.raw.AutoCommit() {
		tx.once.Do(func() {
			if tx.owner.txEpoch.Load() == tx.epoch {
				tx.owner.txActive.Store(false)
			}
		})
	}
}

func (tx *firstAudioTrackedSQLiteTx) Commit() error {
	err := tx.Tx.Commit()
	tx.finish(err)
	return err
}

func (tx *firstAudioTrackedSQLiteTx) Rollback() error {
	err := tx.Tx.Rollback()
	tx.finish(err)
	return err
}

func (c *firstAudioTrackedSQLiteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if conn, ok := c.Conn.(driver.ExecerContext); ok {
		return conn.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *firstAudioTrackedSQLiteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if conn, ok := c.Conn.(driver.QueryerContext); ok {
		return conn.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *firstAudioTrackedSQLiteConn) Ping(ctx context.Context) error {
	if conn, ok := c.Conn.(driver.Pinger); ok {
		return conn.Ping(ctx)
	}
	return nil
}

func (c *firstAudioTrackedSQLiteConn) ResetSession(ctx context.Context) error {
	if conn, ok := c.Conn.(driver.SessionResetter); ok {
		return conn.ResetSession(ctx)
	}
	return driver.ErrSkip
}

func (c *firstAudioTrackedSQLiteConn) IsValid() bool {
	if conn, ok := c.Conn.(driver.Validator); ok {
		return conn.IsValid()
	}
	return true
}

func (c *firstAudioTrackedSQLiteConn) CheckNamedValue(value *driver.NamedValue) error {
	if conn, ok := c.Conn.(driver.NamedValueChecker); ok {
		return conn.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

var _ driver.Driver = (*firstAudioTrackedSQLiteDriver)(nil)
var _ driver.Conn = (*firstAudioTrackedSQLiteConn)(nil)
var _ driver.ConnPrepareContext = (*firstAudioTrackedSQLiteConn)(nil)
var _ driver.ConnBeginTx = (*firstAudioTrackedSQLiteConn)(nil)
var _ driver.ExecerContext = (*firstAudioTrackedSQLiteConn)(nil)
var _ driver.QueryerContext = (*firstAudioTrackedSQLiteConn)(nil)
var _ driver.Pinger = (*firstAudioTrackedSQLiteConn)(nil)
var _ driver.SessionResetter = (*firstAudioTrackedSQLiteConn)(nil)
var _ driver.Validator = (*firstAudioTrackedSQLiteConn)(nil)
var _ driver.NamedValueChecker = (*firstAudioTrackedSQLiteConn)(nil)
