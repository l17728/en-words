package api

import (
	"encoding/json"
	"enwords/internal/auth"
	"enwords/internal/config"
	"enwords/internal/store"
	"errors"
	"github.com/stretchr/testify/require"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func playbackValues() map[string]any {
	return map[string]any{"ttsEnRepeat": 2, "ttsZhRepeat": 2, "ttsIntervalMs": 0, "ttsZhIntervalMs": 300, "ttsSwitchIntervalMs": 10000}
}
func playbackExpect() config.PlaybackSettings {
	return config.PlaybackSettings{TtsEnRepeat: 2, TtsZhRepeat: 2, TtsIntervalMs: 0, TtsZhIntervalMs: 300, TtsSwitchIntervalMs: 10000}
}

func TestPlaybackStrictFiveFieldContract(t *testing.T) {
	keys := []string{"ttsEnRepeat", "ttsZhRepeat", "ttsIntervalMs", "ttsZhIntervalMs", "ttsSwitchIntervalMs"}
	for _, key := range keys {
		for _, kind := range []string{"missing", "null", "float", "string", "low", "high"} {
			t.Run(key+"_"+kind, func(t *testing.T) {
				data := playbackValues()
				switch kind {
				case "missing":
					delete(data, key)
				case "null":
					data[key] = nil
				case "float":
					data[key] = 1.5
				case "string":
					data[key] = "2"
				case "low":
					data[key] = -1
				case "high":
					data[key] = 10001
				}
				if strings.HasSuffix(key, "Repeat") && kind == "low" {
					data[key] = 0
				}
				raw, err := json.Marshal(data)
				require.NoError(t, err)
				_, err = decodePlaybackSettings(strings.NewReader(string(raw)))
				require.Error(t, err)
			})
		}
	}
	for _, raw := range []string{`null`, `[]`, `{}`, `{"ttsEnRepeat":2}`, strings.Repeat(" ", 4097)} {
		_, err := decodePlaybackSettings(strings.NewReader(raw))
		require.Error(t, err)
	}
	data := playbackValues()
	data["student_id"] = 99
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	_, err = decodePlaybackSettings(strings.NewReader(string(raw)))
	require.Error(t, err)
	raw, err = json.Marshal(playbackValues())
	require.NoError(t, err)
	actual, err := decodePlaybackSettings(strings.NewReader(string(raw)))
	require.NoError(t, err)
	require.Equal(t, playbackExpect(), actual)
	_, err = decodePlaybackSettings(playbackBadReader{})
	require.Error(t, err)
}

type playbackBadReader struct{}

func (playbackBadReader) Read([]byte) (int, error) { return 0, errors.New("fixture reader failure") }

var _ io.Reader = playbackBadReader{}

func TestPlaybackScopeIsolationAndAdminDefaults(t *testing.T) {
	a, _ := newTestApp(t)
	_, err := a.Auth.CreateUser("playback-a", "pw", "A", auth.RoleStudent)
	require.NoError(t, err)
	_, err = a.Auth.CreateUser("playback-b", "pw", "B", auth.RoleStudent)
	require.NoError(t, err)
	tok, sid := loginGet(t, a, "playback-a", "pw")
	tokB, sidB := loginGet(t, a, "playback-b", "pw")
	admin, _ := loginGet(t, a, "admin", "adminpw")
	resp := req2(t, a.Router(), "POST", "/api/v1/student/playback-settings", tok, playbackValues())
	require.Equal(t, 0, resp.Code)
	require.Equal(t, playbackExpect(), a.Cfg.Playback(sid))
	require.Equal(t, 3, a.Cfg.Playback(sidB).TtsEnRepeat)
	defaults := playbackValues()
	defaults["ttsEnRepeat"] = 5
	defaults["ttsZhRepeat"] = 4
	resp = req2(t, a.Router(), "POST", "/api/v1/admin/playback-settings", admin, defaults)
	require.Equal(t, 0, resp.Code)
	require.Equal(t, playbackExpect(), a.Cfg.Playback(sid))
	require.Equal(t, 5, a.Cfg.Playback(sidB).TtsEnRepeat)
	require.Equal(t, 4, a.Cfg.Playback(sidB).TtsZhRepeat)
	for _, method := range []string{"GET", "POST"} {
		resp = req2(t, a.Router(), method, "/api/v1/student/playback-settings?student_id=999", tok, playbackValues())
		require.Equal(t, errBadRequest, resp.Code)
		resp = req2(t, a.Router(), method, "/api/v1/admin/playback-settings", tokB, playbackValues())
		require.Equal(t, errForbidden, resp.Code)
		resp = req2(t, a.Router(), method, "/api/v1/student/playback-settings", admin, playbackValues())
		require.Equal(t, errForbidden, resp.Code)
	}
	data := playbackValues()
	data["ttsZhRepeat"] = nil
	resp = req2(t, a.Router(), "POST", "/api/v1/student/playback-settings", tok, data)
	require.Equal(t, errBadRequest, resp.Code)
	require.Equal(t, playbackExpect(), a.Cfg.Playback(sid))
}

func TestPlaybackTransactionFailureRollsBackAllFiveValues(t *testing.T) {
	for _, global := range []bool{false, true} {
		for _, failure := range []string{"config", "audit"} {
			t.Run(map[bool]string{false: "student", true: "global"}[global]+"_"+failure, func(t *testing.T) {
				a, sid, _, _, _ := durableSettlementFixture(t)
				token, _ := loginGet(t, a, "settlement-student", "fixturepw")
				path := "/api/v1/student/playback-settings"
				table := "config_overrides"
				if global {
					token = loginGet2(t, a)
					sid = 0
					path = "/api/v1/admin/playback-settings"
					table = "config_globals"
				}
				before := a.Cfg.Playback(sid)
				trigger := "CREATE TRIGGER fail_playback BEFORE INSERT ON " + table + " WHEN NEW.key='ttsZhRepeat' BEGIN SELECT RAISE(ABORT,'fixture config failure'); END"
				if failure == "audit" {
					trigger = "CREATE TRIGGER fail_playback BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'fixture audit failure'); END"
				}
				require.NoError(t, a.DB.Exec(trigger).Error)
				resp := req2(t, a.Router(), "POST", path, token, playbackValues())
				require.NotEqual(t, 0, resp.Code)
				persisted, err := config.NewStore(store.NewConfigPersister(a.DB), nil)
				require.NoError(t, err)
				require.Equal(t, before, persisted.Playback(sid))
				require.Equal(t, before, a.Cfg.Playback(sid))
				var n int64
				require.NoError(t, a.DB.Model(&store.AuditLog{}).Where("tool=?", "config.playback_set").Count(&n).Error)
				require.Zero(t, n)
				require.NoError(t, a.DB.Exec("DROP TRIGGER fail_playback").Error)
				resp = req2(t, a.Router(), "POST", path, token, playbackValues())
				require.Equal(t, 0, resp.Code)
				require.Equal(t, playbackExpect(), a.Cfg.Playback(sid))
			})
		}
	}
}

func TestPlaybackPersistenceAndCommittedRefreshFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playback.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	initialHandle, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = initialHandle.Close() })
	clock := &fakeClock{t: time.Now()}
	a := NewApp(clock, db)
	require.Equal(t, 0, newAppBootstrap(t, a))
	token := loginGet2(t, a)
	p := &refreshFailurePersister{Persister: store.NewConfigPersister(db)}
	a.Cfg, err = config.NewStore(p, nil)
	require.NoError(t, err)
	before := a.Cfg.Playback(0)
	p.fail = true
	resp := req2(t, a.Router(), "POST", "/api/v1/admin/playback-settings", token, playbackValues())
	require.Equal(t, 0, resp.Code)
	data := resp.Data.(map[string]any)
	require.Equal(t, true, data["refresh_pending"])
	require.Equal(t, before, a.Cfg.Playback(0))
	resp = req2(t, a.Router(), "GET", "/api/v1/admin/playback-settings", token, nil)
	require.Equal(t, 0, resp.Code)
	require.Equal(t, true, resp.Data.(map[string]any)["refresh_pending"], "reading committed values must not conceal stale runtime configuration")
	sql, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sql.Close())
	db, err = store.Open(path)
	require.NoError(t, err)
	sql, err = db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sql.Close() })
	restored := NewApp(clock, db)
	require.Equal(t, playbackExpect(), restored.Cfg.Playback(0))
	token = loginGet2(t, restored)
	resp = req2(t, restored.Router(), "GET", "/api/v1/admin/playback-settings", token, nil)
	require.Equal(t, 0, resp.Code)
	var rows []store.AuditLog
	require.NoError(t, db.Where("tool=?", "config.playback_set").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.True(t, rows[0].OK)
	require.NoError(t, sql.Close())
	resp = req2(t, restored.Router(), "GET", "/api/v1/admin/playback-settings", token, nil)
	require.NotEqual(t, 0, resp.Code)
}

func TestPlaybackSettingsFreezeEnteredLessonAndApplyToNext(t *testing.T) {
	a, clock, _, room := setupRoom(t)
	before := *room.Machine().Snapshot().Config
	token, sid := loginGet(t, a, "sfin", "pw")
	resp := req2(t, a.Router(), "POST", "/api/v1/student/playback-settings", token, playbackValues())
	require.Equal(t, 0, resp.Code)
	require.Equal(t, before, *room.Machine().Snapshot().Config)
	admin, _ := loginGet(t, a, "admin", "adminpw")
	bid := seedBook(t, a, admin, "Next playback lesson", "g7", []string{"cat", "dog"})
	require.NoError(t, a.EnableBook(sid, bid))
	require.NoError(t, a.PlacementSubmit(sid, bid, nil, true))
	lessons, err := a.Sched.Lessons(sid, bid)
	require.NoError(t, err)
	require.NotEmpty(t, lessons)
	clock.Advance(25 * time.Hour)
	next, _, err := a.Rooms.Enter(lessons[0].ID, sid)
	require.NoError(t, err)
	cfg := next.Machine().Snapshot().Config
	require.Equal(t, 2, cfg.TtsEnRepeat)
	require.Equal(t, 2, cfg.TtsZhRepeat)
	require.Equal(t, 0, cfg.TtsIntervalMs)
	require.Equal(t, 300, cfg.TtsZhIntervalMs)
	require.Equal(t, 10000, cfg.TtsSwitchIntervalMs)
}
