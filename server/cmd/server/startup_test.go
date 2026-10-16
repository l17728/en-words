package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"enwords/internal/obs"
	"enwords/internal/store"

	"github.com/stretchr/testify/require"
)

func TestStopRuntimeIsBoundedAndWaitsForWorkers(t *testing.T) {
	t.Run("already_done", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		called := false
		require.True(t, stopRuntime(func() { called = true }, done, time.Second))
		require.True(t, called)
	})
	t.Run("cancel_exits", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); <-ctx.Done() }()
		require.True(t, stopRuntime(cancel, done, time.Second))
	})
	t.Run("bounded", func(t *testing.T) {
		done := make(chan struct{})
		called := false
		require.False(t, stopRuntime(func() { called = true }, done, time.Millisecond))
		require.True(t, called)
		close(done)
	})
}

func startupEvents(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(b.Bytes()), []byte("\n")) {
		var e map[string]any
		require.NoError(t, json.Unmarshal(line, &e))
		require.Equal(t, "startup-test", e["trace_id"])
		require.Equal(t, "startup", e["module"])
		require.GreaterOrEqual(t, e["duration_ms"].(float64), float64(0))
		out = append(out, e)
	}
	return out
}

func TestStartupStageFailuresArePrivateAndNeverReady(t *testing.T) {
	for _, mode := range []string{"error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			var b bytes.Buffer
			log := obs.New(&b)
			ctx := obs.WithTrace(context.Background(), "startup-test")
			run := func() error {
				return runStartupStage(ctx, log, "app_restore", "restore_failed", func() error {
					if mode == "panic" {
						panic("secret-path private SQL")
					}
					return errors.New("secret-path private SQL")
				})
			}
			if mode == "panic" {
				require.PanicsWithValue(t, "secret-path private SQL", func() { _ = run() })
			} else {
				require.Error(t, run())
			}
			events := startupEvents(t, &b)
			require.Len(t, events, 2)
			require.Equal(t, "startup_stage_failed", events[1]["event"])
			require.Equal(t, "restore_failed", events[1]["reason"])
			require.NotContains(t, b.String(), "secret-path")
			require.NotContains(t, b.String(), "private SQL")
			require.NotContains(t, b.String(), "server_started")
		})
	}
}

func TestStartupListenOnlyCompletesAfterRealBind(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer held.Close()
	var b bytes.Buffer
	ctx := obs.WithTrace(context.Background(), "startup-test")
	log := obs.New(&b)
	listener, err := listenStartup(ctx, log, held.Addr().String())
	require.Error(t, err)
	require.Nil(t, listener)
	events := startupEvents(t, &b)
	require.Equal(t, "startup_stage_failed", events[1]["event"])
	require.Equal(t, "listen_failed", events[1]["reason"])
	require.NotContains(t, b.String(), held.Addr().String())
	require.NotContains(t, b.String(), "startup_stage_completed")
	b.Reset()
	listener, err = listenStartup(ctx, log, "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	events = startupEvents(t, &b)
	require.Equal(t, "startup_stage_completed", events[1]["event"])
	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func TestStartupObserverCopiesOnlyStructuredSummary(t *testing.T) {
	var b bytes.Buffer
	ctx := obs.WithTrace(context.Background(), "startup-test")
	observe := startupObserver(ctx, obs.New(&b))
	fresh := true
	count := int64(0)
	observe(store.StageEvent{Event: "startup_stage_completed", Stage: "schema_probe", StorageMode: "sqlite", Fresh: &fresh, SchemaObjectCount: &count, DurationMS: 2})
	observe(store.StageEvent{Event: "startup_stage_failed", Stage: "finalize", Reason: "commit_failed", TransactionOutcome: "rolled_back", DurationMS: 3})
	events := startupEvents(t, &b)
	require.Equal(t, true, events[0]["fresh"])
	require.EqualValues(t, 0, events[0]["schema_object_count"])
	require.Equal(t, "rolled_back", events[1]["transaction_outcome"])
	require.Equal(t, "error", events[1]["level"])
}
