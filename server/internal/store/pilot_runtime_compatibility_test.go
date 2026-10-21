package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/scheduler"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type pilotRuntimeClock struct{}

func (pilotRuntimeClock) Now() time.Time { return time.Unix(1700000000, 0).UTC() }

// The private API settlement types cannot be imported without a package cycle.
// These field/tag shapes mirror lessonStep and lessonScheduleResult. Results
// remain heterogeneous JSON, not a newly invented tool-response schema.
type pilotHistoricalStep struct {
	Index               int
	Kind                string
	Delta               int
	MemoryKind, Content string
	Importance          int
}
type pilotHistoricalNext struct {
	Status               string            `json:"status"`
	Lesson               *scheduler.Lesson `json:"lesson,omitempty"`
	RequestedRatio       float64           `json:"requested_ratio"`
	SelectedNewCount     int               `json:"selected_new_count"`
	FirstLessonExemption bool              `json:"first_lesson_exemption"`
}
type pilotHistoricalAgentResult struct {
	Actions []pilotHistoricalStep   `json:"actions"`
	Results map[int]json.RawMessage `json:"results"`
	Next    *pilotHistoricalNext    `json:"next"`
	Replies []string                `json:"replies"`
}

func pilotRuntimeJSON(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	return string(b)
}

func pilotHistoricalRuntime(t *testing.T) map[string]string {
	t.Helper()
	m := classroom.NewMachine(31, 9, 7, []classroom.Word{{ID: 91, Word: "cat", Meanings: []string{"feline"}}}, config.EffectiveSnapshot{}, pilotRuntimeClock{})
	require.NoError(t, m.Start())
	return map[string]string{
		"snapshot": pilotRuntimeJSON(t, m.Snapshot()),
		"report":   pilotRuntimeJSON(t, classroom.Result{TotalWords: 1, Correct: map[int64]bool{91: true}}),
		"settled":  `{"version":1,"student_id":7,"book_id":9,"fingerprint":"` + strings.Repeat("a", 64) + `"}`,
		"agent_result": pilotRuntimeJSON(t, pilotHistoricalAgentResult{
			Actions: []pilotHistoricalStep{{Index: 0, Kind: "plan_next"}},
			Results: map[int]json.RawMessage{0: json.RawMessage(`{"arbitrary":[null,{"nested":true}]}`)},
			Next:    &pilotHistoricalNext{Status: "planned", Lesson: &scheduler.Lesson{ID: 32, StudentID: 7, BookID: 9}},
			Replies: []string{"recorded"},
		}),
	}
}

func pilotRuntimeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, _ := pilotMatrixDB(t)
	require.NoError(t, db.Create(&User{ID: 7, Username: "historical", Role: "student", Status: "active"}).Error)
	require.NoError(t, db.Create(&WordBook{ID: 9, Name: "historical", Status: "published"}).Error)
	require.NoError(t, db.Create(&Lesson{ID: 31, StudentID: 7, BookID: 9, Status: "completed"}).Error)
	return db
}

func TestPilotLegacyRuntimeValidHistoricalMigration(t *testing.T) {
	for _, emptyMarker := range []bool{false, true} {
		t.Run(map[bool]string{false: "version1_marker", true: "legacy_empty_marker"}[emptyMarker], func(t *testing.T) {
			db := pilotRuntimeDB(t)
			payloads := pilotHistoricalRuntime(t)
			if emptyMarker {
				payloads["settled"] = `{}`
			}
			for _, kind := range []string{"snapshot", "report", "settled", "agent_result"} {
				require.NoError(t, db.Create(&RuntimeRecord{Key: kind + "/31", Value: payloads[kind]}).Error)
			}
			require.NoError(t, migratePilotSchema(context.Background(), db))
			_, err := newPilotStore(context.Background(), db)
			require.NoError(t, err)
			var scope pilotScopeRow
			require.NoError(t, db.First(&scope).Error)
			require.Equal(t, "legacy", scope.Mode)
			require.EqualValues(t, 7, scope.StudentID)
			require.EqualValues(t, 9, scope.BookID)
			for kind, raw := range payloads {
				var record RuntimeRecord
				require.NoError(t, db.Where("key=?", kind+"/31").Take(&record).Error)
				require.Equal(t, raw, record.Value)
			}
		})
	}
}

func TestPilotLegacyRuntimeCorruptionBlocksOnlyExplicitMigration(t *testing.T) {
	type damage struct {
		name, kind string
		change     func(string) string
	}
	replace := func(old, next string) func(string) string {
		return func(s string) string { require.Contains(t, s, old); return strings.Replace(s, old, next, 1) }
	}
	cases := []damage{
		{"snapshot_owner", "snapshot", replace(`"StudentID":7`, `"StudentID":8`)},
		{"snapshot_phase", "snapshot", func(s string) string {
			var state map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(s), &state))
			state["Phase"] = json.RawMessage(`"invented"`)
			return pilotRuntimeJSON(t, state)
		}},
		{"snapshot_scalar_null", "snapshot", replace(`"LessonID":31`, `"LessonID":null`)},
		{"snapshot_missing", "snapshot", replace(`"LessonID":31,`, ``)},
		{"snapshot_alias", "snapshot", replace(`"LessonID":31`, `"lessonid":31`)},
		{"snapshot_duplicate", "snapshot", replace(`"LessonID":31`, `"LessonID":31,"LessonID":31`)},
		{"report_negative", "report", replace(`"TotalWords":1`, `"TotalWords":-1`)},
		{"report_zero_word", "report", replace(`"91":true`, `"0":true`)},
		{"report_numeric_alias", "report", replace(`"91":true`, `"091":true`)},
		{"report_missing", "report", replace(`"TotalWords":1,`, ``)},
		{"report_null", "report", replace(`"TotalWords":1`, `"TotalWords":null`)},
		{"marker_owner", "settled", replace(`"book_id":9`, `"book_id":8`)},
		{"marker_version", "settled", replace(`"version":1`, `"version":2`)},
		{"marker_hash", "settled", replace(strings.Repeat("a", 64), strings.Repeat("A", 64))},
		{"marker_null", "settled", func(string) string { return `null` }},
		{"marker_unknown", "settled", func(s string) string { return strings.TrimSuffix(s, "}") + `,"unknown":1}` }},
		{"agent_missing", "agent_result", replace(`"replies":["recorded"]`, `"unknown":[]`)},
		{"agent_owner", "agent_result", replace(`"StudentID":7`, `"StudentID":8`)},
		{"agent_bad_step", "agent_result", replace(`"Index":0`, `"Index":-1`)},
		{"agent_empty_kind", "agent_result", replace(`"Kind":"plan_next"`, `"Kind":""`)},
		{"agent_orphan_result", "agent_result", replace(`"results":{"0":`, `"results":{"1":`)},
		{"agent_numeric_alias", "agent_result", replace(`"results":{"0":`, `"results":{"00":`)},
		{"agent_bad_array", "agent_result", replace(`"replies":["recorded"]`, `"replies":{}`)},
		{"agent_nested_duplicate", "agent_result", replace(`"nested":true`, `"nested":true,"nested":false`)},
		{"trailing_json", "report", func(s string) string { return s + ` {}` }},
		{"invalid_utf8", "report", func(s string) string { return s + string([]byte{0xff}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := pilotRuntimeDB(t)
			valid := pilotHistoricalRuntime(t)[tc.kind]
			require.NoError(t, db.Create(&RuntimeRecord{Key: tc.kind + "/31", Value: valid}).Error)
			// Establish the exact unmodified fixture passes the real persisted audit
			// before damaging it. No FK failure substitutes for payload validation.
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { _, err := auditPilotLegacyRuntimeTx(tx); return err }))
			bad := tc.change(valid)
			require.NotEqual(t, valid, bad)
			require.NoError(t, db.Model(&RuntimeRecord{}).Where("key=?", tc.kind+"/31").Update("value", bad).Error)
			before := pilotMatrixBefore(t, db)
			_, err := newPilotStore(context.Background(), db)
			require.NoError(t, err, "ordinary legacy startup remains compatible")
			require.Error(t, migratePilotSchema(context.Background(), db))
			require.Equal(t, before, pilotMatrixBefore(t, db))
			require.Empty(t, pilotMatrixOwned(t, db))
		})
	}
}
