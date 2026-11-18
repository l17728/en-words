package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enwords/internal/obs"
	"enwords/internal/store"
	"enwords/internal/tools"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var toolTimingKeys = []string{"lock_wait_ms", "transaction_ms", "begin_wait_ms", "config_load_ms", "audit_write_ms", "finalize_ms", "rollback_audit_write_ms", "storage_callback_ms", "storage_commit_ms", "storage_rollback_ms", "storage_raw_cleanup_ms", "storage_connection_release_ms", "storage_total_ms"}

func TestSafeAtomicTimingLogCannotReplaceResultsOrPanics(t *testing.T) {
	require.NotPanics(t, func() { safeAtomicTimingLog(func() { panic("diagnostic sink") }) })
	require.PanicsWithValue(t, "operation panic", func() {
		defer safeAtomicTimingLog(func() { panic("diagnostic sink") })
		panic("operation panic")
	})
}

func TestToolTransactionTimingIsBoundedAndSeparatesAuditStages(t *testing.T) {
	for _, mode := range []string{"memory", "commit", "rollback", "canceled_before_begin", "lock_wait"} {
		t.Run(mode, func(t *testing.T) {
			app, _, lid, room := setupRoom(t)
			var output bytes.Buffer
			app.SetLogger(obs.New(&output))
			if mode != "memory" {
				db, err := store.Open(filepath.Join(t.TempDir(), "timing.db"))
				require.NoError(t, err)
				pool, err := db.DB()
				require.NoError(t, err)
				defer pool.Close()
				app.DB = db
				calls := 0
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:timed_audit", func(tx *gorm.DB) {
					if tx.Statement.Table == "audit_logs" {
						calls++
						time.Sleep(25 * time.Millisecond)
						if mode == "rollback" && calls == 1 {
							tx.AddError(errors.New("private-error-must-not-appear"))
						}
					}
				}))
			}
			ctx, err := app.newAgentTaskContext(obs.WithTrace(context.Background(), "lesson-timing"), room.studentID, room.bookID, lid, "classroom_event", tools.RoleTeacherAgent)
			require.NoError(t, err)
			if mode == "canceled_before_begin" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			invoke := func() error {
				_, err := app.callAgentTool(ctx, "classroom.say", map[string]any{"text": "private-answer-must-not-appear", "category": "nudge"})
				return err
			}
			if mode == "lock_wait" {
				app.agentWriteMu.Lock()
				entered := make(chan struct{})
				done := make(chan error, 1)
				go func() { close(entered); done <- invoke() }()
				<-entered
				select {
				case <-done:
					app.agentWriteMu.Unlock()
					t.Fatal("tool passed held write lock")
				case <-time.After(50 * time.Millisecond):
				}
				app.agentWriteMu.Unlock()
				err = <-done
			} else {
				err = invoke()
			}
			if mode == "rollback" || mode == "canceled_before_begin" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			var event map[string]any
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				var item map[string]any
				if json.Unmarshal([]byte(line), &item) == nil && item["event"] == "tool_called" {
					event = item
				}
			}
			require.NotNil(t, event)
			require.Equal(t, "lesson-timing", event["trace_id"])
			for _, key := range toolTimingKeys {
				value, ok := event[key].(float64)
				require.True(t, ok, key)
				require.GreaterOrEqual(t, value, float64(0), key)
				if mode == "memory" {
					require.Zero(t, value, key)
				}
			}
			if mode == "commit" || mode == "rollback" || mode == "lock_wait" {
				require.GreaterOrEqual(t, event["audit_write_ms"].(float64), float64(20))
			}
			if mode == "rollback" {
				require.Equal(t, "failed_cleaned", event["transaction_outcome"])
				require.GreaterOrEqual(t, event["rollback_audit_write_ms"].(float64), float64(20))
				require.Equal(t, "failed_cleaned", event["storage_outcome"])
				require.Equal(t, "completed", event["storage_rollback_state"])
				require.Equal(t, "not_started", event["storage_commit_state"])
			}
			if mode == "commit" {
				require.Equal(t, "committed", event["storage_outcome"])
				require.Equal(t, "completed", event["storage_commit_state"])
				require.Equal(t, "not_started", event["storage_rollback_state"])
			}
			if mode == "canceled_before_begin" {
				require.Equal(t, event["transaction_ms"], event["begin_wait_ms"])
				require.Zero(t, event["finalize_ms"])
			}
			if mode == "lock_wait" {
				require.GreaterOrEqual(t, event["lock_wait_ms"].(float64), float64(25))
			}
			require.NotContains(t, output.String(), "private-answer")
			require.NotContains(t, output.String(), "private-error")
		})
	}
}
