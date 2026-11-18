package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"enwords/internal/obs"

	"github.com/stretchr/testify/require"
)

type cleanupCloseFailureDriver struct{}
type cleanupCloseFailureConn struct{}

func (cleanupCloseFailureDriver) Open(string) (driver.Conn, error) {
	return cleanupCloseFailureConn{}, nil
}
func (cleanupCloseFailureConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (cleanupCloseFailureConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (cleanupCloseFailureConn) Close() error                        { return errors.New("private driver close path and SQL") }
func (cleanupCloseFailureConn) Ping(context.Context) error          { return nil }

var registerCleanupFailureDriver sync.Once

func TestOwnedRuntimeCleanupPoolCloseFailureIsObservableAndPrivate(t *testing.T) {
	registerCleanupFailureDriver.Do(func() { sql.Register("cleanup_close_failure", cleanupCloseFailureDriver{}) })
	pool, err := sql.Open("cleanup_close_failure", "")
	require.NoError(t, err)
	require.NoError(t, pool.Ping())
	var logs bytes.Buffer
	owned := ownedRuntimeCleanup{pool: pool, log: obs.New(&logs), ctx: obs.WithTrace(context.Background(), "cleanup-test")}
	err = owned.Close()
	require.EqualError(t, err, "database close incomplete")
	require.Error(t, pool.Ping())
	require.Contains(t, logs.String(), "shutdown_incomplete")
	require.Contains(t, logs.String(), "pool_close_failed")
	require.Contains(t, logs.String(), "cleanup-test")
	require.NotContains(t, logs.String(), "private driver")
	require.NotContains(t, logs.String(), "SQL")
}

func cleanupTestPool(t *testing.T) *sql.DB {
	t.Helper()
	pool, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "owned.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	_, err = pool.Exec("CREATE TABLE lifecycle_probe (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	return pool
}

func cleanupTestHTTP(t *testing.T) (*http.Server, net.Listener, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + listener.Addr().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.NoError(t, response.Body.Close())
	client.CloseIdleConnections()
	return server, listener, done
}

func requireCleanupHTTPStopped(t *testing.T, listener net.Listener, serveDone <-chan error) {
	t.Helper()
	select {
	case err := <-serveDone:
		require.ErrorIs(t, err, http.ErrServerClosed)
	case <-time.After(time.Second):
		t.Fatal("owned HTTP Serve did not exit")
	}
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
}

func TestOwnedRuntimeCleanupWaitsBeforeClosingPool(t *testing.T) {
	pool := cleanupTestPool(t)
	server, listener, serveDone := cleanupTestHTTP(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	workerResult := make(chan error, 1)
	go func() {
		defer close(done)
		<-ctx.Done()
		_, err := pool.Exec("INSERT INTO lifecycle_probe(id) VALUES (1)")
		workerResult <- err
	}()
	var logs bytes.Buffer
	owned := ownedRuntimeCleanup{pool: pool, server: server, listener: listener, cancel: cancel, done: done, timeout: time.Second, log: obs.New(&logs), ctx: obs.WithTrace(context.Background(), "cleanup-test")}
	require.NoError(t, owned.Close())
	require.NoError(t, <-workerResult, "worker must retain its pool until it has relinquished ownership")
	require.Error(t, pool.Ping())
	requireCleanupHTTPStopped(t, listener, serveDone)
	require.NotContains(t, logs.String(), "shutdown_incomplete")
}

func TestOwnedRuntimeCleanupTimeoutRetainsLiveWorkerPool(t *testing.T) {
	pool := cleanupTestPool(t)
	server, listener, serveDone := cleanupTestHTTP(t)
	ctx, cancel := context.WithCancel(context.Background())
	done, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
	workerResult := make(chan error, 1)
	go func() {
		defer close(done)
		<-ctx.Done()
		<-release
		_, err := pool.Exec("INSERT INTO lifecycle_probe(id) VALUES (2)")
		workerResult <- err
	}()
	t.Cleanup(func() { releaseWorker(); <-done })
	var logs bytes.Buffer
	owned := ownedRuntimeCleanup{pool: pool, server: server, listener: listener, cancel: cancel, done: done, timeout: time.Millisecond, log: obs.New(&logs), ctx: obs.WithTrace(context.Background(), "cleanup-test")}
	original := errors.New("original request failure")
	invoke := func() (err error) {
		defer func() {
			if cleanupErr := owned.Close(); err == nil {
				err = cleanupErr
			}
		}()
		return original
	}
	require.ErrorIs(t, invoke(), original, "cleanup timeout must not replace the original failure")
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.NoError(t, pool.Ping(), "timeout must not close a pool still owned by a worker")
	requireCleanupHTTPStopped(t, listener, serveDone)
	require.Contains(t, logs.String(), "shutdown_incomplete")
	require.Contains(t, logs.String(), "runtime_stop_timeout")
	require.NotContains(t, logs.String(), "owned.db")
	// Release and assert the real SQL operation before test cleanup closes the pool.
	releaseWorker()
	<-done
	require.NoError(t, <-workerResult)
}

func TestOwnedRuntimeCleanupOnPreRuntimeErrorAndPanic(t *testing.T) {
	for _, mode := range []string{"error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			pool := cleanupTestPool(t)
			var logs bytes.Buffer
			owned := ownedRuntimeCleanup{pool: pool, log: obs.New(&logs), ctx: obs.WithTrace(context.Background(), "cleanup-test")}
			original := errors.New("post-open failure")
			invoke := func() (err error) {
				defer func() {
					if cleanupErr := owned.Close(); err == nil {
						err = cleanupErr
					}
				}()
				if mode == "panic" {
					panic(original)
				}
				return original
			}
			if mode == "panic" {
				require.PanicsWithValue(t, original, func() { _ = invoke() })
			} else {
				require.ErrorIs(t, invoke(), original)
			}
			require.Error(t, pool.Ping())
			require.NotContains(t, logs.String(), original.Error())
		})
	}
}

func TestOwnedRuntimeCleanupOnActiveRuntimeErrorAndPanic(t *testing.T) {
	for _, mode := range []string{"error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			pool := cleanupTestPool(t)
			server, listener, serveDone := cleanupTestHTTP(t)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); <-ctx.Done() }()
			var logs bytes.Buffer
			owned := ownedRuntimeCleanup{pool: pool, server: server, listener: listener, cancel: cancel, done: done, timeout: time.Second, log: obs.New(&logs), ctx: obs.WithTrace(context.Background(), "cleanup-test")}
			original := errors.New("private post-listen error")
			invoke := func() (err error) {
				defer func() {
					if cleanupErr := owned.Close(); err == nil {
						err = cleanupErr
					}
				}()
				if mode == "panic" {
					panic(original)
				}
				return original
			}
			if mode == "panic" {
				require.PanicsWithValue(t, original, func() { _ = invoke() })
			} else {
				require.ErrorIs(t, invoke(), original)
			}
			require.Error(t, pool.Ping())
			requireCleanupHTTPStopped(t, listener, serveDone)
			require.Contains(t, logs.String(), "runtime_stopped")
			require.NotContains(t, logs.String(), original.Error())
		})
	}
}

func TestRunPostOpenErrorsReleaseOwnedSQLite(t *testing.T) {
	for _, mode := range []string{"invalid_provider", "occupied_port"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.db")
			oldFlags, oldArgs := flag.CommandLine, os.Args
			flag.CommandLine = flag.NewFlagSet("startup-test", flag.ContinueOnError)
			os.Args = []string{"startup-test", "-testmode", "-db", path, "-providers", "invalid"}
			t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
			if mode == "occupied_port" {
				listener, err := net.Listen("tcp", ":0")
				require.NoError(t, err)
				defer listener.Close()
				_, port, err := net.SplitHostPort(listener.Addr().String())
				require.NoError(t, err)
				os.Args = []string{"startup-test", "-testmode", "-db", path, "-providers", "mock", "-port", port}
			}
			require.Error(t, run())
			// Windows refuses removal while SQLite still owns an open database handle.
			require.FileExists(t, path)
			require.NoError(t, os.Remove(path))
		})
	}
}
