package api

import (
	"bytes"
	"context"
	"encoding/json"
	"enwords/internal/feedback"
	"enwords/internal/localmodel"
	"enwords/internal/obs"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFeedbackWebSocketInvalidSubmissionTypeCannotCreateAnonymousTask(t *testing.T) {
	for _, kind := range []string{"numeric_submission", "object_submission", "null_submission", "invalid_utf8"} {
		t.Run(kind, func(t *testing.T) {
			a, _, lid, _ := setupRoom(t)
			token, sid := loginGet(t, a, "sfin", "pw")
			before := a.Cfg.Int(sid, "wordsPerLesson")
			server := httptest.NewServer(a.Router())
			defer server.Close()
			entered := req2(t, a.Router(), "POST", "/api/v1/classroom/lessons/"+itoa64(lid)+"/enter", token, map[string]any{})
			require.Equal(t, 0, entered.Code)
			data := entered.Data.(map[string]any)
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/classroom?lessonId="+itoa64(lid)+"&ticket="+data["ticket"].(string), nil)
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
			var eventContext any
			for eventContext == nil {
				var frame Msg
				require.NoError(t, conn.ReadJSON(&frame))
				eventContext = frame["context"]
			}
			id := any(42)
			if kind == "object_submission" {
				id = map[string]any{"id": "bad"}
			}
			if kind == "null_submission" {
				id = nil
			}
			raw, err := json.Marshal(Msg{"type": "voice_text", "event_id": "bad-id-event", "submission_id": id, "context": eventContext, "text": "我想反馈 每次词太多了"})
			require.NoError(t, err)
			if kind == "invalid_utf8" {
				raw = bytes.Replace(raw, []byte("每次"), []byte{0xff}, 1)
			}
			require.NoError(t, conn.WriteMessage(websocket.TextMessage, raw))
			for {
				var frame Msg
				err := conn.ReadJSON(&frame)
				if kind == "invalid_utf8" {
					if err != nil {
						break
					}
					continue
				}
				require.NoError(t, err)
				if frame["type"] == "event_rejected" {
					require.Equal(t, "invalid_envelope", frame["reason"])
					require.Equal(t, "bad-id-event", frame["event_id"])
					break
				}
			}
			history, err := a.FeedbacksContext(context.Background(), sid)
			require.NoError(t, err)
			require.Empty(t, history)
			require.Equal(t, before, a.Cfg.Int(sid, "wordsPerLesson"))
		})
	}
}

func TestVoiceFeedbackRealWebSocketKeepsContextAndLongBodyAcrossRESTReplay(t *testing.T) {
	a, _, lid, room := setupRoom(t)
	token, sid := loginGet(t, a, "sfin", "pw")
	body := strings.Repeat("请保留 标点 C++ 3.5秒！", 25) + "cat 与 dog 我老忘啊"
	parsed := make(chan string, 1)
	a.FB.SetLLM(submissionParser(func(ctx context.Context, text string, _ time.Time) ([]feedback.Intent, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
		parsed <- text
		return []feedback.Intent{{Kind: feedback.IntentReduce, Confidence: 1}}, nil
	}))
	server := httptest.NewServer(a.Router())
	defer server.Close()
	enter := req2(t, a.Router(), "POST", "/api/v1/classroom/lessons/"+itoa64(lid)+"/enter", token, map[string]any{})
	require.Equal(t, 0, enter.Code)
	ticket := enter.Data.(map[string]any)["ticket"].(string)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/classroom?lessonId="+itoa64(lid)+"&ticket="+ticket, nil)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	var ctx any
	for ctx == nil {
		var frame Msg
		require.NoError(t, conn.ReadJSON(&frame))
		ctx = frame["context"]
	}
	require.NoError(t, conn.WriteJSON(Msg{"type": "voice_text", "event_id": "ws-body-event", "submission_id": "ws-body-submission", "context": ctx, "text": "我 想 反 馈 " + body}))
	var reply Msg
	for reply == nil {
		var frame Msg
		require.NoError(t, conn.ReadJSON(&frame))
		require.NotEqual(t, "event_rejected", frame["type"])
		if frame["type"] == "feedback_reply" {
			reply = frame
		}
	}
	require.Equal(t, "applied", reply["status"])
	require.Equal(t, "ws-body-submission", reply["submission_id"])
	record := reply["feedback"].(map[string]any)
	require.Equal(t, body, record["text"])
	select {
	case got := <-parsed:
		require.Equal(t, body, got)
	default:
		t.Fatal("connection parser was canceled with the completed HTTP upgrade request")
	}
	replay := req2(t, a.Router(), "POST", "/api/v1/feedbacks", token, map[string]any{"submission_id": "ws-body-submission", "text": body, "channel": "page"})
	require.Equal(t, 0, replay.Code)
	require.Equal(t, record["id"], replay.Data.(map[string]any)["id"])
	require.Equal(t, 50, a.Cfg.Int(sid, "wordsPerLesson"))
	require.Empty(t, room.FeedbackCh)
}

func TestFeedbackLocalASRHTTPTranscriptAndVoicePreserveLongBody(t *testing.T) {
	a, _, lid, room := setupRoom(t)
	token, sid := loginGet(t, a, "sfin", "pw")
	require.NoError(t, a.Cfg.SetStudent("admin", sid, "wordsPerLesson", 80, "fixture detects repeated relative adjustment"))
	body := strings.Repeat("中文长反馈 C++ 3.5秒 保留  双空格。", 20) + "cat 和 dog 太难呀"
	transcript := "我想反馈 " + body
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/asr/transcriptions", r.URL.Path)
		require.NoError(t, r.ParseMultipartForm(1024))
		require.Equal(t, "zh", r.FormValue("language"))
		_ = json.NewEncoder(w).Encode(map[string]string{"text": transcript})
	}))
	defer upstream.Close()
	client, err := localmodel.New(upstream.URL, obs.Nop())
	require.NoError(t, err)
	client.Services["asr"] = localmodel.Service{ID: "asr", Model: "fixture", Capabilities: []string{"asr"}}
	a.Local = client
	a.FB.SetLLM(fixedSubmissionIntents(feedback.Intent{Kind: feedback.IntentReduce, Confidence: 1}, feedback.Intent{Kind: feedback.IntentReduce, Confidence: .8}))
	var raw bytes.Buffer
	form := multipart.NewWriter(&raw)
	file, err := form.CreateFormFile("file", "fixture.webm")
	require.NoError(t, err)
	_, err = file.Write([]byte("fixture audio bytes"))
	require.NoError(t, err)
	require.NoError(t, form.WriteField("language", "zh"))
	require.NoError(t, form.Close())
	request := httptest.NewRequest("POST", "/api/v1/classroom/lessons/"+itoa64(lid)+"/transcribe", &raw)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	a.Router().ServeHTTP(response, request)
	decoded := doJSONBody(t, response)
	require.Equal(t, 0, decoded.Code)
	text := decoded.Data.(map[string]any)["text"]
	require.Equal(t, transcript, text)
	room.mu.Lock()
	ctx := room.contextLocked()
	room.mu.Unlock()
	voice := req2(t, a.Router(), "POST", "/api/v1/classroom/lessons/"+itoa64(lid)+"/voice", token, map[string]any{"text": text, "context": ctx, "event_id": "asr-event", "submission_id": "asr-feedback"})
	require.Equal(t, 0, voice.Code)
	record := voice.Data.(map[string]any)["feedback"].(map[string]any)
	require.Equal(t, body, record["text"])
	require.Equal(t, "applied", record["status"])
	require.Len(t, record["results"].([]any), 1, "duplicate relative model candidates must compile only one adjustment")
	require.Equal(t, 70, a.Cfg.Int(sid, "wordsPerLesson"))
}
