package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/obs"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
)

func TestPlaybackCanceledSaveDoesNotChangeSettingsOrAudit(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "sqlite"}[persistent], func(t *testing.T) {
			a, _ := newTestApp(t)
			sid := int64(0)
			token, _ := loginGet(t, a, "admin", "adminpw")
			if persistent {
				a, _, _, _, _ = durableSettlementFixture(t)
				token = loginGet2(t, a)
			}
			before := a.Cfg.Playback(sid)
			raw, err := json.Marshal(playbackValues())
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			req := httptest.NewRequest("POST", "/api/v1/admin/playback-settings", bytes.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			a.Router().ServeHTTP(out, req)
			resp := doJSONBody(t, out)
			require.NotEqual(t, 0, resp.Code)
			require.Equal(t, before, a.Cfg.Playback(sid))
			require.NotEmpty(t, out.Header().Get("X-Trace-ID"))
			get := req2(t, a.Router(), "GET", "/api/v1/admin/playback-settings", token, nil)
			require.Equal(t, 0, get.Code)
			data := get.Data.(map[string]any)
			saved, err := json.Marshal(data["settings"])
			require.NoError(t, err)
			expected, err := json.Marshal(before)
			require.NoError(t, err)
			require.JSONEq(t, string(expected), string(saved))
			if persistent {
				var n int64
				require.NoError(t, a.DB.Model(&store.AuditLog{}).Where("tool=?", "config.playback_set").Count(&n).Error)
				require.Zero(t, n)
			}
		})
	}
}

func TestSessionTimeoutPreservesDelayedSegmentAndRESTResume(t *testing.T) {
	for _, minutes := range []int{30, 60} {
		t.Run(itoa(minutes), func(t *testing.T) {
			a, clock, lid, room := setupRoom(t)
			state := room.Machine().Snapshot()
			state.Config.SessionTimeoutMin = minutes
			state.Config.TtsIntervalMs = 10000
			require.NoError(t, room.Machine().Restore(state))
			room.HandleEvent(classroom.Event{Type: classroom.EvWordClicked})
			room.HandleEvent(classroom.Event{Type: classroom.EvTTSFinished})
			before := room.Machine().Snapshot()
			require.Equal(t, 2, before.TtsSegIdx)
			room.mu.Lock()
			oldContext := room.contextLocked()
			room.mu.Unlock()
			clock.Advance(time.Duration(minutes)*time.Minute - time.Millisecond)
			room.SessionTimeout()
			require.False(t, room.Machine().Paused())
			clock.Advance(time.Millisecond)
			frames := room.SessionTimeout()
			require.True(t, room.Machine().Paused())
			paused := false
			for _, f := range frames {
				if act, ok := f["action"].(classroom.Action); ok {
					require.NotEqual(t, classroom.ActPlayTTS, act.Type)
					if act.Type == classroom.ActLessonPaused {
						paused = true
					}
				}
			}
			require.True(t, paused, "actual pause is a ui_action.lesson_paused frame")
			clock.Advance(time.Hour)
			room.Tick()
			require.Equal(t, before.TtsSegIdx, room.Machine().Snapshot().TtsSegIdx)
			rejected := room.HandleClientEvent(ClientEnvelope{Context: oldContext, EventID: "late-before-resume"}, classroom.Event{Type: classroom.EvTTSFinished})
			require.Equal(t, "event_rejected", rejected[0]["type"])
			token, _ := loginGet(t, a, "sfin", "pw")
			resp := req2(t, a.Router(), "POST", "/api/v1/classroom/lessons/"+itoa64(lid)+"/resume", token, map[string]any{})
			require.Equal(t, 0, resp.Code)
			raw, err := json.Marshal(resp.Data)
			require.NoError(t, err)
			var out []struct {
				Type   string           `json:"type"`
				Action classroom.Action `json:"action"`
			}
			require.NoError(t, json.Unmarshal(raw, &out))
			var spoken []classroom.Action
			for _, f := range out {
				if f.Action.Type == classroom.ActPlayTTS {
					spoken = append(spoken, f.Action)
				}
			}
			require.Len(t, spoken, 1)
			require.Equal(t, 2, spoken[0].SegIdx)
			require.Equal(t, "en", spoken[0].Lang)
			require.Equal(t, 10000, spoken[0].DelayBeforeMs)
			rejected = room.HandleClientEvent(ClientEnvelope{Context: oldContext, EventID: "late-after-resume"}, classroom.Event{Type: classroom.EvTTSFinished})
			require.Equal(t, "event_rejected", rejected[0]["type"])
			clock.Advance(9 * time.Second)
			room.Tick()
			require.Equal(t, 2, room.Machine().Snapshot().TtsSegIdx, "resumed segment must receive its full frozen delay budget")
		})
	}
}

func TestTTSSegmentLogTracksRealEmittedSegmentWithoutText(t *testing.T) {
	a, _, _, room := setupRoom(t)
	var sink bytes.Buffer
	a.SetLogger(obs.NewWithSink(&sink))
	room.mu.Lock()
	envelope := ClientEnvelope{Context: room.contextLocked(), EventID: "log-first-segment"}
	room.mu.Unlock()
	frames := room.HandleClientEvent(envelope, classroom.Event{Type: classroom.EvWordClicked})
	var action classroom.Action
	for _, frame := range frames {
		if emitted, ok := frame["action"].(classroom.Action); ok && emitted.Type == classroom.ActPlayTTS {
			action = emitted
		}
	}
	require.Equal(t, classroom.ActPlayTTS, action.Type)
	matched := 0
	for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry["event"] != "tts_segment_scheduled" {
			continue
		}
		matched++
		require.Equal(t, "classroom", entry["module"])
		require.EqualValues(t, room.studentID, entry["student_id"])
		require.EqualValues(t, room.lessonID, entry["lesson_id"])
		require.Equal(t, room.epoch, entry["epoch"])
		require.EqualValues(t, action.SegIdx, entry["segment"])
		require.Equal(t, action.Lang, entry["language"])
		require.EqualValues(t, action.DelayBeforeMs, entry["delay_before_ms"])
		// Logger.Info creates an independent real trace. Do not claim request-context propagation.
		require.Regexp(t, `^tr-[0-9a-f]+$`, entry["trace_id"])
		for _, field := range []string{"text", "word", "meanings", "asr_text", "prompt", "content"} {
			require.NotContains(t, entry, field)
		}
		require.NotContains(t, line, action.Word)
	}
	require.Equal(t, 1, matched)
}

func TestPlaybackFailureLogsUseRequestTraceAndSafeCodes(t *testing.T) {
	for _, kind := range []string{"get_query", "post_query", "read_failed", "invalid_settings", "request_cancelled", "save_failed"} {
		t.Run(kind, func(t *testing.T) {
			a, _, _, _, _ := durableSettlementFixture(t)
			token := loginGet2(t, a)
			method, path := "POST", "/api/v1/admin/playback-settings"
			data := playbackValues()
			code := kind
			if kind == "get_query" || kind == "post_query" {
				path += "?student_id=999"
				code = "invalid_query"
				if kind == "get_query" {
					method = "GET"
				}
			}
			if kind == "invalid_settings" {
				data["unexpected"] = "private fixture input"
			}
			if kind == "save_failed" {
				require.NoError(t, a.DB.Exec(`CREATE TRIGGER fail_playback_log BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'private fixture storage detail'); END`).Error)
			}
			if kind == "read_failed" {
				method = "GET"
				db, err := a.DB.DB()
				require.NoError(t, err)
				require.NoError(t, db.Close())
			}
			raw, err := json.Marshal(data)
			require.NoError(t, err)
			req := httptest.NewRequest(method, path, bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			if kind == "request_cancelled" {
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			var sink bytes.Buffer
			a.SetLogger(obs.NewWithSink(&sink))
			out := httptest.NewRecorder()
			a.Router().ServeHTTP(out, req)
			require.NotEqual(t, 0, doJSONBody(t, out).Code)
			trace := out.Header().Get("X-Trace-ID")
			require.NotEmpty(t, trace)
			found := false
			for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
				var entry map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &entry))
				if entry["event"] != "playback_settings_failed" {
					continue
				}
				found = true
				require.Equal(t, code, entry["error_code"])
				require.Equal(t, trace, entry["trace_id"])
				require.Equal(t, "global", entry["scope"])
			}
			require.True(t, found, "failed requests need a searchable failure event")
			require.NotContains(t, sink.String(), "private fixture")
		})
	}
}
