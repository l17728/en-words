package store_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"enwords/internal/api"
	"enwords/internal/obs"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
)

func startupArtifactPath(t *testing.T, path string) string {
	t.Helper()
	require.True(t, filepath.IsAbs(path), "isolated absolute artifact path required")
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root, err := filepath.Abs(filepath.Join(filepath.Dir(source), "../../.."))
	require.NoError(t, err)
	abs, err := filepath.Abs(path)
	require.NoError(t, err)
	allowed := false
	for _, base := range []string{filepath.Join(root, ".test-artifacts"), filepath.Join(root, "web", ".test-artifacts")} {
		rel, e := filepath.Rel(base, abs)
		if e == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			allowed = true
		}
	}
	require.True(t, allowed, "test artifacts must remain inside this workspace")
	// Existing parents must not redirect the isolated test DB via symlinks/junctions.
	parent := filepath.Dir(abs)
	resolved, e := filepath.EvalSymlinks(parent)
	require.NoError(t, e, "caller must create artifact parent first")
	require.Equal(t, strings.ToLower(filepath.Clean(parent)), strings.ToLower(filepath.Clean(resolved)))
	if _, e = os.Lstat(abs); e == nil {
		resolved, e = filepath.EvalSymlinks(abs)
		require.NoError(t, e)
		require.Equal(t, strings.ToLower(filepath.Clean(abs)), strings.ToLower(filepath.Clean(resolved)))
	} else {
		require.True(t, os.IsNotExist(e))
	}
	return abs
}

// TestStartupFailureHarness exercises real initialization in an owned process.
// It exposes no fault endpoint; successful none mode serves the real App router.
func TestStartupFailureHarness(t *testing.T) {
	if os.Getenv("ENWORDS_STARTUP_HARNESS") != "1" {
		t.Skip("explicit Playwright startup process harness only")
	}
	path := startupArtifactPath(t, os.Getenv("ENWORDS_STARTUP_DB"))
	port, err := strconv.Atoi(os.Getenv("ENWORDS_STARTUP_PORT"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, port, 18000)
	require.Less(t, port, 19000)
	fault := os.Getenv("ENWORDS_STARTUP_FAULT")
	require.Contains(t, []string{"none", "schema", "trigger", "index", "commit", "cancel", "panic", "probe_outer", "probe_inner"}, fault)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = obs.WithTrace(ctx, obs.NewTraceID())
	log := obs.New(os.Stdout)
	barrier := os.Getenv("ENWORDS_STARTUP_BARRIER")
	nonce := os.Getenv("ENWORDS_STARTUP_NONCE")
	participant := os.Getenv("ENWORDS_STARTUP_PARTICIPANT")
	if barrier != "" {
		barrier = startupArtifactPath(t, barrier)
		require.Regexp(t, regexp.MustCompile(`^[a-zA-Z0-9]{8,64}$`), nonce)
		require.Contains(t, []string{"a", "b"}, participant)
		require.Equal(t, "none", fault)
	} else {
		require.Empty(t, nonce)
		require.Empty(t, participant)
	}
	waited := false
	observe := func(e store.StageEvent) {
		fields := map[string]any{"stage": e.Stage, "duration_ms": e.DurationMS, "storage_mode": e.StorageMode}
		if e.Fresh != nil {
			fields["fresh"] = *e.Fresh
		}
		if e.SchemaObjectCount != nil {
			fields["schema_object_count"] = *e.SchemaObjectCount
		}
		if e.Reason != "" {
			fields["reason"] = e.Reason
		}
		if e.TransactionOutcome != "" {
			fields["transaction_outcome"] = e.TransactionOutcome
		}
		if e.Event == "startup_stage_failed" {
			log.ErrorCtx(ctx, "startup", e.Event, fields)
		} else {
			log.InfoCtx(ctx, "startup", e.Event, fields)
		}
		if barrier != "" && !waited && e.Event == "startup_stage_completed" && e.Stage == "schema_probe" {
			waited = true
			require.NotNil(t, e.Fresh)
			require.True(t, *e.Fresh)
			require.NotNil(t, e.SchemaObjectCount)
			require.Zero(t, *e.SchemaObjectCount)
			ready := filepath.Join(barrier, "ready-"+nonce+"-"+participant)
			f, x := os.OpenFile(ready, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			require.NoError(t, x)
			require.NoError(t, f.Close())
			deadline := time.NewTimer(20 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				if _, x := os.Stat(filepath.Join(barrier, "release-"+nonce)); x == nil {
					return
				} else if !os.IsNotExist(x) {
					cancel()
					return
				}
				select {
				case <-deadline.C:
					cancel()
					return
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}
	}
	defer func() {
		if recover() != nil {
			log.ErrorCtx(ctx, "startup", "startup_harness_failed", map[string]any{"reason": "panic"})
			t.Error("startup harness failed")
		}
	}()
	db, err := store.StartupHarnessOpen(ctx, path, observe, fault, cancel)
	if err != nil {
		log.ErrorCtx(ctx, "startup", "startup_harness_failed", map[string]any{"reason": "initialization_failed"})
		t.Fatal("startup harness failed")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	require.Equal(t, "none", fault, "fault must never publish a ready server")
	app := api.NewApp(nil, db)
	require.NoError(t, app.StartupError)
	app.SetTestMode()
	app.SetLogger(log)
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	server := &http.Server{Handler: app.Router(), ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.InfoCtx(ctx, "startup", "server_started", map[string]any{"storage_mode": "sqlite"})
	eof := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
		}
		close(eof)
	}()
	select {
	case <-eof:
		require.NoError(t, server.Close())
		require.ErrorIs(t, <-done, http.ErrServerClosed)
	case e := <-done:
		require.ErrorIs(t, e, http.ErrServerClosed)
	}
}
