package api

import (
	"context"
	"encoding/json"
	"enwords/internal/classroom"
	"enwords/internal/memory"
	"enwords/internal/tools"
	"github.com/stretchr/testify/require"
	"os"
	"sort"
	"testing"
)

func TestCapabilityCatalogMatchesRuntime(t *testing.T) {
	a, _ := newTestApp(t)
	raw, err := os.ReadFile("../../../review/tool-capability-catalog.json")
	require.NoError(t, err)
	var catalog struct {
		Names []string `json:"registered_tools"`
	}
	require.NoError(t, json.Unmarshal(raw, &catalog))
	got := []string{}
	seen := map[string]bool{}
	for _, role := range []tools.Role{tools.RoleAdmin, tools.RoleSystemAgent, tools.RoleTeacherAgent, tools.RoleStudent} {
		for _, d := range a.Tools.ToolsFor(role) {
			if !seen[d.Name] {
				got = append(got, d.Name)
				seen[d.Name] = true
			}
		}
	}
	sort.Strings(got)
	sort.Strings(catalog.Names)
	require.Equal(t, catalog.Names, got)
}

func TestCapabilityMemoryOwnershipAndMutation(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "sqlite"}[persistent], func(t *testing.T) {
			a, _ := newTestApp(t)
			sid := int64(7)
			if persistent {
				a, sid, _, _, _ = durableSettlementFixture(t)
			}
			own, err := a.Mem.Append(memory.OwnerSystem, memory.ScopeStudentProfile, sid, 0, "preference", "original", 3, 0)
			require.NoError(t, err)
			other, err := a.Mem.Append(memory.OwnerSystem, memory.ScopeStudentProfile, sid+1, 0, "preference", "private", 3, 0)
			require.NoError(t, err)
			call := func(name string, args any) (any, error) {
				return a.callAgentTool(taskFixtureContext(t, a, sid), name, args)
			}
			for _, name := range []string{"memory.revise", "memory.delete"} {
				_, err = call(name, map[string]any{"scope": "student_profile", "id": other.ID, "content": "changed", "importance": 4})
				require.Error(t, err)
			}
			_, err = call("memory.list", map[string]any{"scope": "student_profile", "student_id": sid + 1})
			require.Error(t, err)
			v, err := call("memory.revise", map[string]any{"scope": "student_profile", "id": own.ID, "content": "revised", "importance": 4})
			require.NoError(t, err)
			revised := v.(*memory.Memory)
			require.Equal(t, "revised", revised.Content)
			v, err = call("memory.list", map[string]any{"scope": "student_profile"})
			require.NoError(t, err)
			for _, m := range v.([]*memory.Memory) {
				require.Equal(t, sid, m.StudentID)
			}
			_, err = call("memory.delete", map[string]any{"scope": "student_profile", "id": revised.ID})
			require.NoError(t, err)
			rows, err := a.Mem.List(memory.OwnerSystem, sid+1, 0, "")
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, "private", rows[0].Content)
		})
	}
}

func TestCapabilityQueriesAreBoundAndReadOnly(t *testing.T) {
	a, _, lid, r := setupRoom(t)
	ctx, err := a.newAgentTaskContext(context.Background(), r.studentID, r.bookID, lid, "classroom_event", tools.RoleTeacherAgent)
	require.NoError(t, err)
	before, err := a.Sched.Lessons(r.studentID, r.bookID)
	require.NoError(t, err)
	for _, name := range []string{"scheduler.get_due_words", "scheduler.get_progress_summary", "progress.query"} {
		_, err = a.callAgentTool(ctx, name, map[string]any{})
		require.NoError(t, err)
		_, err = a.callAgentTool(ctx, name, map[string]any{"student_id": r.studentID + 1})
		require.Error(t, err)
	}
	after, err := a.Sched.Lessons(r.studentID, r.bookID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = a.callAgentTool(ctx, "scripts.list", map[string]any{"category": "open"})
	require.NoError(t, err)
	_, err = a.callAgentTool(ctx, "scripts.pick", map[string]any{"category": "open"})
	require.NoError(t, err)
	_, err = a.callAgentTool(ctx, "progress.query", map[string]any{})
	require.Error(t, err, "all tools share eight-call budget including rejected args")
}

func TestTeacherEffectsFreezeAndCapabilityBoundaries(t *testing.T) {
	a, _, lid, r := setupRoom(t)
	ctx, err := a.newAgentTaskContext(context.Background(), r.studentID, r.bookID, lid, "classroom_event", tools.RoleTeacherAgent)
	require.NoError(t, err)
	actions := []classroom.Action{{Type: classroom.ActShowWord, WordID: 1, Meanings: []string{"frozen"}}, {Type: classroom.ActPlayTTS, Lang: "en", Word: "apple", DelayBeforeMs: 1250}}
	ctx, err = withTeacherEffects(ctx, actions, r.machine.Snapshot())
	require.NoError(t, err)
	actions[0].Meanings[0] = "mutated"
	v, err := a.callAgentTool(ctx, "classroom.show_word", map[string]any{"index": 0})
	require.NoError(t, err)
	require.Equal(t, "frozen", v.(classroom.Action).Meanings[0])
	v.(classroom.Action).Meanings[0] = "returned alias"
	v, err = a.callAgentTool(ctx, "classroom.show_word", map[string]any{"index": 0})
	require.NoError(t, err)
	require.Equal(t, "frozen", v.(classroom.Action).Meanings[0])
	_, err = a.callAgentTool(ctx, "classroom.play_tts", map[string]any{"index": 0})
	require.Error(t, err)
	_, err = a.callAgentTool(ctx, "classroom.play_tts", map[string]any{"index": 1, "word": "injected"})
	require.Error(t, err)
	v, err = a.callAgentTool(ctx, "classroom.play_tts", map[string]any{"index": 1})
	require.NoError(t, err)
	require.Equal(t, 1250, v.(classroom.Action).DelayBeforeMs)
	_, err = a.Tools.Call(context.Background(), tools.Caller{Role: tools.RoleTeacherAgent}, "classroom.play_tts", json.RawMessage(`{"index":1}`))
	require.Error(t, err)
}
