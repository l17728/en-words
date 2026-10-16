package api

import (
	"enwords/internal/auth"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestDuplexPreflightOwnershipAndCapabilityGates(t *testing.T) {
	a, _, lid, _ := setupRoom(t)
	student, _ := loginGet(t, a, "sfin", "pw")
	admin, _ := loginGet(t, a, "admin", "adminpw")
	_, err := a.Auth.CreateUser("other-duplex", "pw", "other", auth.RoleStudent)
	require.NoError(t, err)
	other, _ := loginGet(t, a, "other-duplex", "pw")
	router := a.Router()
	for _, on := range []bool{false, true} {
		a.DuplexPreflight = on
		for _, tc := range []struct {
			name, token, id string
			code            int
		}{{"owner", student, itoa64(lid), 0}, {"other", other, itoa64(lid), 403}, {"admin", admin, itoa64(lid), 403}, {"anonymous", "", itoa64(lid), 401}, {"malformed", student, "abc", 400}, {"zero", student, "0", 400}, {"missing", student, "999999", 403}} {
			t.Run(tc.name+map[bool]string{false: "_off", true: "_on"}[on], func(t *testing.T) {
				req := httptest.NewRequest("GET", "/api/v1/classroom/lessons/"+tc.id+"/duplex-capabilities", nil)
				if tc.token != "" {
					req.Header.Set("Authorization", "Bearer "+tc.token)
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				resp := doJSON(t, w)
				require.Equal(t, tc.code, resp.Code)
				if tc.code != 0 {
					return
				}
				data := resp.Data.(map[string]any)
				require.Equal(t, on, data["pilot_enabled"])
				require.Equal(t, false, data["native_duplex"])
				require.Equal(t, false, data["automatic_teaching"])
				require.Equal(t, "preflight", data["mode"])
				require.NotEmpty(t, data["reasons"])
				limits := data["limits"].(map[string]any)
				require.EqualValues(t, 8000, limits["confirmation_ms"])
				require.EqualValues(t, 15000, limits["max_utterance_ms"])
			})
		}
	}
}
