package api

import (
	"enwords/internal/auth"
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLadderEffectiveHTTPStrictTargetAndRole(t *testing.T) {
	a, _ := newTestApp(t)
	admin, _ := loginGet(t, a, "admin", "adminpw")
	u, err := a.Auth.CreateUser("ladder-student", "studentpw", "Ladder", auth.RoleStudent)
	require.NoError(t, err)
	student, _ := loginGet(t, a, u.Username, "studentpw")
	r := a.Router()
	for _, path := range []string{"/api/v1/config/effective?studentId=", "/api/v1/config/effective?studentId=-1", "/api/v1/config/effective?studentId=1&studentId=2", "/api/v1/config/effective?studentId=not-a-number"} {
		res := req2(t, r, "GET", path, admin, nil)
		require.NotZero(t, res.Code)
	}
	res := req2(t, r, "GET", fmt.Sprintf("/api/v1/config/effective?studentId=%d", u.ID), student, nil)
	require.Equal(t, errForbidden, res.Code)
	res = req2(t, r, "POST", "/api/v1/config/set", admin, map[string]any{"scope": "typo", "key": "ladder", "value": []int{1, 2, 3}, "reason": "must reject"})
	require.Equal(t, errBadRequest, res.Code)
	res = req2(t, r, "POST", "/api/v1/config/set", admin, map[string]any{"scope": "student", "studentId": u.ID, "key": "goalMode", "value": "exam_sprint", "reason": "explicit goal"})
	require.Zero(t, res.Code, res.Msg)
	res = req2(t, r, "GET", fmt.Sprintf("/api/v1/config/effective?studentId=%d", u.ID), admin, nil)
	require.Zero(t, res.Code, res.Msg)
	view := res.Data.(map[string]any)
	ladder := view["ladder"].(map[string]any)
	require.Equal(t, "default", ladder["source"])
	require.Equal(t, "student", ladder["goal_mode_source"])
}
