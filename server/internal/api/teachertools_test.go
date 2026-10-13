package api

import (
	"context"
	"path/filepath"
	"testing"

	"enwords/internal/classroom"
	"enwords/internal/store"
	"enwords/internal/tools"
	"github.com/stretchr/testify/require"
)

func teacherSayCount(frames []Msg, category string) int {
	n := 0
	for _, f := range frames {
		if a, ok := f["action"].(map[string]any); ok && a["type"] == "say" && (category == "" || a["category"] == category) {
			n++
		}
	}
	return n
}

func TestTeacherToolRequiresTrustedTaskAndHasNoClassroomSideEffect(t *testing.T) {
	a, _, lid, r := setupRoom(t)
	caller := tools.Caller{Role: tools.RoleTeacherAgent, ID: "teacher_agent"}
	raw := []byte(`{"category":"nudge","text":"Please try again."}`)
	_, err := a.Tools.Call(context.Background(), caller, "classroom.say", raw)
	require.Error(t, err)
	ctx, err := a.newAgentTaskContext(context.Background(), r.studentID, r.bookID, lid, "classroom_event", tools.RoleTeacherAgent)
	require.NoError(t, err)
	before := r.machine.Snapshot()
	result, err := a.Tools.Call(ctx, caller, "classroom.say", raw)
	require.NoError(t, err)
	require.Equal(t, 1, teacherSayCount([]Msg{result.(Msg)}, "nudge"))
	require.Equal(t, before, r.machine.Snapshot(), "tool only returns a frame, never advances classroom")
	_, err = a.Tools.Call(ctx, caller, "classroom.say", []byte(`{"category":"nudge","text":"Hi","student_id":999}`))
	require.Error(t, err)
	_, err = a.Tools.Call(ctx, tools.Caller{Role: tools.RoleSystemAgent, ID: "system_agent"}, "classroom.say", raw)
	require.Error(t, err)
}

func TestTeacherOpeningAndFrozenEncouragement(t *testing.T) {
	a, _, lid, r := setupRoom(t)
	require.NotEmpty(t, r.teacherText, "REST enter snapshot can show the already executed opening")
	initial := r.teacherText
	_, snapshot, err := a.Rooms.Enter(lid, r.studentID)
	require.NoError(t, err)
	require.Equal(t, initial, snapshot["teacher_text"])
	client := &fakeClient{}
	r.Join(client)
	r.Join(client)
	require.Zero(t, teacherSayCount(client.frames, "open"), "join replays display snapshot, not a new Agent action")
	require.NoError(t, a.Cfg.SetGlobal("admin", "encourageEveryN", 3, "change after class begins"))
	frozen := r.machine.Snapshot().Config.EncourageEveryN
	require.Greater(t, frozen, 3)
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 1; i < frozen; i++ {
		r.lastActions = []classroom.Action{{Type: classroom.ActShowCheck}}
		require.Zero(t, teacherSayCount(r.emitTeacherUtterancesLocked(), "encourage"))
	}
	r.lastActions = []classroom.Action{{Type: classroom.ActShowCheck}}
	require.Equal(t, 1, teacherSayCount(r.emitTeacherUtterancesLocked(), "encourage"))
	require.Zero(t, r.encourageCounter)
}

func TestTeacherClosingOnlyAfterSettlementCommit(t *testing.T) {
	a, _, _, r := setupRoom(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "teacher-failure.db"))
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sql.Close())
	a.DB = db
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.machine.Snapshot()
	st.Phase = classroom.PhaseLessonEnd
	st.Finished = true
	require.NoError(t, r.machine.Restore(st))
	require.Zero(t, teacherSayCount(r.maybeFinishLocked(), "closing"))
	a.DB = nil
	require.Equal(t, 1, teacherSayCount(r.maybeFinishLocked(), "closing"))
	require.Empty(t, r.maybeFinishLocked(), "successful replay does not speak twice")
}

func TestTeacherAuditFailureDoesNotPublishSpeech(t *testing.T) {
	a, _, _, r := setupRoom(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "teacher-audit.db"))
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sql.Close() })
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_teacher_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`).Error)
	a.DB = db
	r.mu.Lock()
	defer r.mu.Unlock()
	before := r.teacherText
	require.Empty(t, r.teacherLifecycleLocked("state_restored"))
	require.Equal(t, before, r.teacherText, "uncommitted speech must not be displayed")
	var count int64
	require.NoError(t, db.Model(&store.AuditLog{}).Count(&count).Error)
	require.Zero(t, count)
}
