package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"enwords/internal/api"
	"enwords/internal/obs"
	"enwords/internal/store"

	"github.com/stretchr/testify/require"
)

func cleanupHarnessDB(t *testing.T, path string) string {
	t.Helper()
	require.True(t, filepath.IsAbs(path), "absolute isolated artifact path required")
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root, err := filepath.Abs(filepath.Join(filepath.Dir(source), "../../.."))
	require.NoError(t, err)
	abs, err := filepath.Abs(path)
	require.NoError(t, err)
	allowed := false
	for _, base := range []string{filepath.Join(root, ".test-artifacts"), filepath.Join(root, "web", ".test-artifacts")} {
		rel, e := filepath.Rel(base, abs)
		if e == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			allowed = true
		}
	}
	require.True(t, allowed, "artifact must remain inside this workspace")
	parent := filepath.Dir(abs)
	resolved, err := filepath.EvalSymlinks(parent)
	require.NoError(t, err)
	require.Equal(t, strings.ToLower(filepath.Clean(parent)), strings.ToLower(filepath.Clean(resolved)))
	if _, err = os.Lstat(abs); err == nil {
		resolved, err = filepath.EvalSymlinks(abs)
		require.NoError(t, err)
		require.Equal(t, strings.ToLower(abs), strings.ToLower(filepath.Clean(resolved)))
	} else {
		require.True(t, os.IsNotExist(err))
	}
	return abs
}

// Real Router and SQLite exercise the same resource closer as run. The worker
// is deliberately controlled by this test; this is not a full run() process.
func TestStartupCleanupHarness(t *testing.T) {
	if os.Getenv("ENWORDS_STARTUP_CLEANUP_HARNESS") != "1" {
		t.Skip("explicit Playwright resource cleanup harness only")
	}
	path := cleanupHarnessDB(t, os.Getenv("ENWORDS_STARTUP_DB"))
	port, err := strconv.Atoi(os.Getenv("ENWORDS_STARTUP_PORT"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, port, 18000)
	require.Less(t, port, 19000)
	mode := os.Getenv("ENWORDS_STARTUP_CLEANUP_MODE")
	require.Contains(t, []string{"normal", "timeout"}, mode)
	ctx := obs.WithTrace(context.Background(), obs.NewTraceID())
	log := obs.New(os.Stdout)
	db, err := store.OpenContext(ctx, path, startupObserver(ctx, log))
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	app := api.NewApp(nil, db)
	require.NoError(t, app.StartupError)
	app.SetLogger(log)
	app.SetTestMode()
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	defer listener.Close()
	server := &http.Server{Handler: app.Router(), ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	workerCtx, cancel := context.WithCancel(context.Background())
	done, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseWorker := func() { once.Do(func() { close(release) }) }
	go func() {
		defer close(done)
		<-workerCtx.Done()
		if mode == "timeout" {
			<-release
		}
	}()
	defer func() { cancel(); releaseWorker(); <-done }()
	owned := ownedRuntimeCleanup{pool: pool, server: server, listener: listener, cancel: cancel, done: done, timeout: 5 * time.Second, log: log, ctx: ctx}
	log.InfoCtx(ctx, "main", "server_started", map[string]any{"port": port, "testmode": true})
	scanner := bufio.NewScanner(os.Stdin)
	// EOF also releases resources, but never falsely satisfies the explicit stop journey.
	stop := scanner.Scan() && scanner.Text() == "stop"
	closeErr := owned.Close()
	poolClosed := pool.Ping() != nil
	joined := false
	select {
	case <-done:
		joined = true
	default:
	}
	log.InfoCtx(ctx, "startup", "startup_cleanup_observed", map[string]any{"mode": mode, "pool_closed": poolClosed, "worker_joined": joined})
	select {
	case err = <-serveDone:
		require.ErrorIs(t, err, http.ErrServerClosed)
	case <-time.After(time.Second):
		t.Fatal("owned HTTP did not stop")
	}
	require.True(t, stop, "explicit stop command required")
	if mode == "timeout" {
		require.Error(t, closeErr)
		require.False(t, poolClosed)
		require.False(t, joined)
		releaseWorker()
		<-done
		t.Error("expected runtime stop incomplete")
		return
	}
	require.NoError(t, closeErr)
	require.True(t, poolClosed)
	require.True(t, joined)
}
