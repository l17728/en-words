package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"enwords/internal/auth"
	"enwords/internal/classroom"
	"enwords/internal/obs"
	"enwords/internal/store"
	"enwords/internal/tools"
	"github.com/stretchr/testify/require"
)

func TestClosureOwnershipAllEntryPoints(t *testing.T) {
	app, _, lid, _ := setupRoom(t)
	_, err := app.Auth.CreateUser("outsider", "fixturepw", "Other", auth.RoleStudent)
	require.NoError(t, err)
	token, outsider := loginGet(t, app, "outsider", "fixturepw")
	router := app.Router()
	for _, warm := range []bool{true, false} {
		if !warm {
			app.Rooms.Drop(lid)
		}
		for _, tc := range []struct{ method, path, body string }{
			{"POST", fmt.Sprintf("/api/v1/classroom/lessons/%d/enter", lid), `{}`},
			{"POST", fmt.Sprintf("/api/v1/classroom/lessons/%d/event", lid), `{"event":"word_clicked"}`},
			{"POST", fmt.Sprintf("/api/v1/classroom/lessons/%d/voice", lid), `{"text":"cat"}`},
			{"POST", fmt.Sprintf("/api/v1/classroom/lessons/%d/resume", lid), `{}`},
			{"POST", fmt.Sprintf("/api/v1/classroom/lessons/%d/transcribe", lid), `{}`},
			{"GET", fmt.Sprintf("/api/v1/student/lessons/%d/report", lid), ``},
		} {
			t.Run(fmt.Sprintf("warm=%v/%s", warm, tc.path), func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				require.Equal(t, errForbidden, doJSON(t, w).Code, w.Body.String())
			})
		}
		_, _, err = app.Rooms.Enter(lid, outsider)
		require.Error(t, err, "Hub must independently enforce ownership")
		if !warm {
			_, exists := app.Rooms.Room(lid)
			require.False(t, exists, "denied requests must not create room")
		}
	}
	// Even a malformed trusted ticket cannot override the lesson owner.
	ticket := app.IssueTicket(lid, outsider)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/ws/classroom?lessonId=%d&ticket=%s", lid, ticket), nil))
	require.Equal(t, 401, w.Code)
}

func TestClosureRuntimeSurvivesFreshApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "closure.db")
	open := func() *App {
		db, err := store.Open(path)
		require.NoError(t, err)
		sql, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sql.Close() })
		return NewApp(nil, db)
	}
	first := open()
	require.NoError(t, first.Cfg.SetStudent("system_agent", 7, "wordsPerLesson", 60, "fixture"))
	id := classroom.QuizItemID{WordID: 8, Kind: "final", Round: 1, Seq: 2}
	first.saveLessonState(9, classroom.State{QuizQueue: []classroom.QuizItemID{id}, Judged: map[classroom.QuizItemID]classroom.Judgment{id: classroom.JudgmentWrong}})
	first.saveReport(9, &classroom.Result{TotalWords: 1, Correct: map[int64]bool{8: false}})
	first.Notify(7, "fixture title", "fixture body")
	second := open()
	require.Equal(t, 60, second.Cfg.Int(7, "wordsPerLesson"))
	state := second.lessonState(9)
	require.NotNil(t, state)
	require.Equal(t, classroom.JudgmentWrong, state.Judged[id])
	report, ok := second.Report(9)
	require.True(t, ok)
	value, present := report.Correct[8]
	require.True(t, present, "false result must not be dropped")
	require.False(t, value)
	notifications := second.Notifications(7)
	require.Len(t, notifications, 1)
	require.Equal(t, "fixture body", notifications[0].Body)
}

func TestClosureFeedbackLowerBoundAndFailedWrite(t *testing.T) {
	app, _, _, room := setupRoom(t)
	sid := room.studentID
	require.NoError(t, app.Cfg.SetStudent("system_agent", sid, "wordsPerLesson", 50, "fixture"))
	result := app.HandleFeedback(sid, "每次词太多了")
	require.Equal(t, "applied", result.Status)
	require.Equal(t, 50, app.Cfg.Int(sid, "wordsPerLesson"))
	require.Contains(t, result.Reply, "50")

	db, err := store.Open(filepath.Join(t.TempDir(), "failed.db"))
	require.NoError(t, err)
	persistent := NewApp(nil, db)
	sql, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sql.Close())
	failed := persistent.HandleFeedback(sid, "每次词太多了")
	require.Equal(t, "failed", failed.Status, "write failure cannot claim applied")
}

func TestClosureProvidersRequiresAuthentication(t *testing.T) {
	app, _ := newTestApp(t)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/providers", nil))
	require.Equal(t, 401, doJSON(t, w).Code)
	token, _ := loginGet(t, app, "admin", "adminpw")
	req := httptest.NewRequest("GET", "/api/v1/providers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	require.Equal(t, 0, doJSON(t, w).Code)
	require.NotEmpty(t, w.Header().Get("X-Trace-ID"))
}

func TestClosureStudentToolRejectsUntrustedIdentity(t *testing.T) {
	app, _ := newTestApp(t)
	before := app.Cfg.Int(7, "wordsPerLesson")
	args := []byte(`{"student_id":7,"key":"wordsPerLesson","value":50,"reason":"fixture"}`)
	ctx := tools.WithEventBudget(context.Background(), 8)
	_, err := app.Tools.Call(ctx, tools.Caller{Role: tools.RoleSystemAgent, ID: "untrusted"}, "config.set_student", args)
	require.Error(t, err, "JSON student_id cannot manufacture a trusted task")
	require.Equal(t, before, app.Cfg.Int(7, "wordsPerLesson"))
	require.NoError(t, app.setStudentFromAgent(7, "wordsPerLesson", 50, "fixture trusted task"))
	require.Equal(t, 50, app.Cfg.Int(7, "wordsPerLesson"))
	require.Error(t, app.setStudentFromAgent(7, "wordsPerLesson", 49, "fixture below boundary"))
	require.Equal(t, 50, app.Cfg.Int(7, "wordsPerLesson"))
}

func TestInvalidRestorableSnapshotDoesNotCreateOrOverwriteRoom(t *testing.T) {
	a, sid, _, lid, _ := durableSettlementFixture(t)
	key := fmt.Sprintf("snapshot/%d", lid)
	// Valid JSON is insufficient: an empty phase cannot restore a state machine.
	const invalid = `{"Phase":""}`
	require.NoError(t, a.DB.Create(&store.RuntimeRecord{Key: key, Value: invalid}).Error)
	room, snapshot, err := a.Rooms.Enter(lid, sid)
	require.ErrorContains(t, err, "saved classroom unavailable")
	require.Nil(t, room)
	require.Nil(t, snapshot)
	_, exists := a.Rooms.Room(lid)
	require.False(t, exists)
	var saved store.RuntimeRecord
	require.NoError(t, a.DB.Where("key=?", key).First(&saved).Error)
	require.Equal(t, invalid, saved.Value, "invalid source must remain available for diagnosis")
}

func TestQuizLogContainsAnswerLengthWithoutRawStudentText(t *testing.T) {
	a, _, _, room := setupRoom(t)
	var sink bytes.Buffer
	a.SetLogger(obs.NewWithSink(&sink))
	for i := 0; i < 5; i++ {
		teachOneRoom(room, ttsCfg{en: 3, rp: 1})
	}
	require.Equal(t, classroom.PhaseWaitAnswer, room.Machine().Phase())
	const answer = "private-student-answer-marker"
	room.HandleEvent(classroom.Event{Type: classroom.EvAnswerSubmitted, Text: answer})
	require.Contains(t, sink.String(), `"event":"quiz_judged"`)
	require.Contains(t, sink.String(), `"answer_length":29`)
	require.NotContains(t, sink.String(), answer)
	require.NotContains(t, sink.String(), `"asr_text"`)
}
