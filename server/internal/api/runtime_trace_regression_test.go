package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"enwords/internal/classroom"
	"enwords/internal/obs"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
)

func requireRuntimeTrace(t *testing.T, logs string, event, trace string) {
	t.Helper()
	found := 0
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) == nil && row["event"] == event {
			found++
			require.Equal(t, trace, row["trace_id"])
		}
	}
	require.Equal(t, 1, found, "event %s must be emitted once in %s", event, logs)
	require.NotContains(t, logs, "private-student-body")
}

func TestHubSnapshotRestoreLogsStableLessonTrace(t *testing.T) {
	for _, mode := range []string{"invalid_state", "restored", "read_error"} {
		t.Run(mode, func(t *testing.T) {
			a, sid, _, lid, _ := durableSettlementFixture(t)
			var logs bytes.Buffer
			a.SetLogger(obs.NewWithSink(&logs))
			if mode == "restored" {
				room, _, err := a.Rooms.Enter(lid, sid)
				require.NoError(t, err)
				lease, ctx := testGenerationContext(t, a, lid)
				require.True(t, a.saveLessonStateContext(ctx, lid, room.Machine().Snapshot()))
				lease.Release()
				a.Rooms.Drop(lid)
			}
			if mode == "invalid_state" {
				require.NoError(t, a.DB.Create(&store.RuntimeRecord{Key: fmt.Sprintf("snapshot/%d", lid), Value: `{"Phase":"private-student-body"}`}).Error)
			}
			if mode == "read_error" {
				require.NoError(t, a.DB.Exec("DROP TABLE runtime_records").Error)
			}
			logs.Reset()
			_, _, err := a.Rooms.Enter(lid, sid)
			event := "snapshot_failed"
			if mode == "restored" {
				require.NoError(t, err)
				event = "snapshot_restored"
			} else {
				require.Error(t, err)
			}
			requireRuntimeTrace(t, logs.String(), event, fmt.Sprintf("lesson-%d", lid))
		})
	}
}

func TestLegacyRuntimeKeysDeriveTraceAndPreserveExplicitTrace(t *testing.T) {
	a, _, _, lid, _ := durableSettlementFixture(t)
	var logs bytes.Buffer
	a.SetLogger(obs.NewWithSink(&logs))
	for _, prefix := range []string{"snapshot/", "report/"} {
		key := prefix + itoa64(lid)
		logs.Reset()
		require.True(t, a.saveRuntimeLocked(key, map[string]any{"safe": true}))
		requireRuntimeTrace(t, logs.String(), "snapshot_saved", fmt.Sprintf("lesson-%d", lid))
		require.NoError(t, a.DB.Model(&store.RuntimeRecord{}).Where("key=?", key).Update("value", `{"Phase":"private-student-body"}`).Error)
		logs.Reset()
		var out int
		require.False(t, a.readRuntime(key, &out))
		requireRuntimeTrace(t, logs.String(), "snapshot_failed", fmt.Sprintf("lesson-%d", lid))
	}
	logs.Reset()
	lease, ctx := testGenerationContext(t, a, lid)
	require.True(t, a.saveLessonStateContext(ctx, lid, classroom.State{}))
	lease.Release()
	requireRuntimeTrace(t, logs.String(), "snapshot_saved", fmt.Sprintf("lesson-%d", lid))
	logs.Reset()
	require.True(t, a.saveRuntimeContextLocked(obs.WithTrace(context.Background(), "explicit-parent"), "report/"+itoa64(lid), map[string]any{}))
	requireRuntimeTrace(t, logs.String(), "snapshot_saved", "explicit-parent")
}

func TestRuntimeTraceContextAcceptsOnlyCanonicalLessonKeys(t *testing.T) {
	for _, prefix := range []string{"snapshot/", "report/", "settled/", "agent_result/"} {
		for _, id := range []string{"1", "9223372036854775807"} {
			ctx := runtimeTraceContext(nil, prefix+id)
			require.NotNil(t, ctx)
			require.Equal(t, "lesson-"+id, obs.TraceID(ctx))
		}
		for _, bad := range []string{"", "0", "-1", "+1", "01", "1/extra", "9223372036854775808", "private-student-body"} {
			ctx := runtimeTraceContext(nil, prefix+bad)
			require.NotNil(t, ctx)
			require.False(t, strings.HasPrefix(obs.TraceID(ctx), "lesson-"))
		}
	}
	for _, key := range []string{"notifications", "feedbacks", "arbitrary/1", "/snapshot/1"} {
		require.False(t, strings.HasPrefix(obs.TraceID(runtimeTraceContext(nil, key)), "lesson-"))
	}
	explicit := obs.WithTrace(context.Background(), "explicit-parent")
	require.Equal(t, "explicit-parent", obs.TraceID(runtimeTraceContext(explicit, "snapshot/1")))
}
