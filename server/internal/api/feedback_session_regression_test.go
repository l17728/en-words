package api

import (
	"encoding/json"
	"testing"

	"enwords/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestFeedbackHTTPTokenSwitchKeepsHistoryNotificationsAndWritesOwned(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	other, err := a.Auth.CreateUser("feedback-session-other", "fixturepw", "Other", auth.RoleStudent)
	require.NoError(t, err)
	a.persistUser(other.Username)
	tokenA, _ := loginGet(t, a, "feedback-student", "fixturepw")
	tokenB, _ := loginGet(t, a, other.Username, "fixturepw")
	router := a.Router()
	post := func(token, id string) Resp {
		return req2(t, router, "POST", "/api/v1/feedbacks", token, map[string]any{"submission_id": id, "text": "每次词太多了", "channel": "page"})
	}
	require.Zero(t, post(tokenA, "session-a-only").Code)
	for _, endpoint := range []string{"/api/v1/feedbacks/submissions/session-a-only", "/api/v1/feedbacks/submissions/session-a-only/retry"} {
		method := "GET"
		if endpoint[len(endpoint)-6:] == "/retry" {
			method = "POST"
		}
		require.Equal(t, 404, req2(t, router, method, endpoint, tokenB, nil).Code)
	}
	for _, endpoint := range []string{"/api/v1/feedbacks/mine", "/api/v1/student/notifications"} {
		response := req2(t, router, "GET", endpoint, tokenB, nil)
		require.Zero(t, response.Code)
		require.Empty(t, response.Data)
	}
	require.Zero(t, post(tokenB, "session-a-only").Code)
	require.Equal(t, 60, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	require.Equal(t, 50, submissionStoredConfig(t, a, other.ID).Int(other.ID, "wordsPerLesson"))
	for _, tc := range []struct {
		token string
		sid   int64
	}{{tokenA, sid}, {tokenB, other.ID}} {
		response := req2(t, router, "GET", "/api/v1/feedbacks/mine", tc.token, nil)
		require.Zero(t, response.Code)
		data, err := json.Marshal(response.Data)
		require.NoError(t, err)
		var records []FeedbackRecord
		require.NoError(t, json.Unmarshal(data, &records))
		require.Len(t, records, 1)
		require.Equal(t, tc.sid, records[0].StudentID)
		noticeResponse := req2(t, router, "GET", "/api/v1/student/notifications", tc.token, nil)
		require.Zero(t, noticeResponse.Code)
		noticeJSON, err := json.Marshal(noticeResponse.Data)
		require.NoError(t, err)
		var notices []Notification
		require.NoError(t, json.Unmarshal(noticeJSON, &notices))
		require.Len(t, notices, 1)
		require.Equal(t, tc.sid, notices[0].UserID)
	}
}
