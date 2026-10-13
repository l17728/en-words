package api

// 总验收门补齐端点的测试：词条 CRUD/导入解析/CSV 导出/用户管理/待办/覆盖删除/话术层。
import (
	"bytes"
	"net/http/httptest"
	"testing"

	"enwords/internal/auth"
	"enwords/internal/classroom"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- 词条 CRUD + 导入解析 + 导出（FR-B2/B3/B6）----

func TestGate_EntryCRUDParseExport(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	router := app.Router()
	bid := seedBook(t, app, atok, "词条库", "g7", []string{"apple"})

	// 新增词条
	resp := req2(t, router, "POST", "/api/v1/wordbooks/"+itoa64(bid)+"/entries", atok,
		map[string]any{"word": "dog", "meanings": []string{"n. 狗"}})
	require.Equal(t, 0, resp.Code, resp.Msg)
	em, _ := resp.Data.(map[string]any)
	eid := int64(em["ID"].(float64))

	// 修改词条
	resp = req2(t, router, "PUT", "/api/v1/wordbooks/"+itoa64(bid)+"/entries/"+itoa64(eid), atok,
		map[string]any{"word": "dog", "meanings": []string{"n. 狗狗"}})
	require.Equal(t, 0, resp.Code)
	got, _ := resp.Data.(map[string]any)
	assert.Equal(t, "n. 狗狗", meaningsFirst(got["Meanings"]))

	// 修改不存在词条
	resp = req2(t, router, "PUT", "/api/v1/wordbooks/"+itoa64(bid)+"/entries/9999", atok,
		map[string]any{"word": "x", "meanings": []string{"n. x"}})
	assert.Equal(t, errNotFound, resp.Code)

	// 导入解析（Mock LLM，不入库）
	resp = req2(t, router, "POST", "/api/v1/wordbooks/"+itoa64(bid)+"/import/parse", atok,
		map[string]any{"rawText": "cat|n.|猫\n这行没有英文词"})
	require.Equal(t, 0, resp.Code)
	pm, _ := resp.Data.(map[string]any)
	entries, _ := pm["entries"].([]any)
	assert.Len(t, entries, 1, "1 条可解析")
	warnings, _ := pm["warnings"].([]any)
	assert.Len(t, warnings, 1, "1 条告警")

	// 导出 CSV（BOM + 内容）
	req := httptest.NewRequest("GET", "/api/v1/wordbooks/"+itoa64(bid)+"/export", nil)
	req.Header.Set("Authorization", "Bearer "+atok)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	body := w.Body.Bytes()
	assert.True(t, bytes.HasPrefix(body, []byte("\xEF\xBB\xBF")), "UTF-8 BOM")
	assert.Contains(t, string(body), "apple")
	assert.Contains(t, string(body), "dog")

	// 删除词条
	resp = req2(t, router, "DELETE", "/api/v1/wordbooks/"+itoa64(bid)+"/entries/"+itoa64(eid), atok, nil)
	require.Equal(t, 0, resp.Code)
	resp = req2(t, router, "DELETE", "/api/v1/wordbooks/"+itoa64(bid)+"/entries/"+itoa64(eid), atok, nil)
	assert.Equal(t, errNotFound, resp.Code, "重复删除404")

	// 坏库导出
	req = httptest.NewRequest("GET", "/api/v1/wordbooks/999/export", nil)
	req.Header.Set("Authorization", "Bearer "+atok)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	respJ := doJSON(t, w)
	assert.Equal(t, errBadRequest, respJ.Code)
}

func meaningsFirst(v any) string {
	if arr, ok := v.([]any); ok && len(arr) > 0 {
		if s, ok := arr[0].(string); ok {
			return s
		}
	}
	return ""
}

// ---- 用户管理 + 待办（TX-01 / FR-G5）----

func TestGate_UserManagementAndTodos(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	router := app.Router()
	_, _ = app.Auth.CreateUser("kid", "kidpw123", "小明", auth.RoleStudent)

	// 列表
	resp := req2(t, router, "GET", "/api/v1/admin/users", atok, nil)
	require.Equal(t, 0, resp.Code)
	list, _ := resp.Data.([]any)
	assert.GreaterOrEqual(t, len(list), 2, "admin+student 至少2人")

	// 重置密码 → 新密码可登录
	resp = req2(t, router, "POST", "/api/v1/admin/users/kid/reset_password", atok,
		map[string]any{"password": "newpw999"})
	require.Equal(t, 0, resp.Code)
	ntok, _ := loginGet(t, app, "kid", "newpw999")
	require.NotEmpty(t, ntok, "重置后新密码登录成功")

	// 停用 → 登录拒；启用 → 恢复
	resp = req2(t, router, "POST", "/api/v1/admin/users/kid/disable", atok, nil)
	require.Equal(t, 0, resp.Code)
	_, _, err := app.Auth.Login("kid", "newpw999")
	assert.ErrorIs(t, err, auth.ErrDisabled)
	resp = req2(t, router, "POST", "/api/v1/admin/users/kid/enable", atok, nil)
	require.Equal(t, 0, resp.Code)
	_, _, err = app.Auth.Login("kid", "newpw999")
	assert.NoError(t, err)

	// 不存在用户操作报错
	resp = req2(t, router, "POST", "/api/v1/admin/users/ghost/disable", atok, nil)
	assert.Equal(t, errNotFound, resp.Code)

	// 待办：投诉反馈 → escalated → 出现在 todos
	_, _ = app.Auth.CreateUser("fb2", "pw12345", "小", auth.RoleStudent)
	stok, _ := loginGet(t, app, "fb2", "pw12345")
	resp = req2(t, router, "POST", "/api/v1/feedbacks", stok, map[string]string{"text": "发音读错了"})
	require.Equal(t, 0, resp.Code)
	resp = req2(t, router, "GET", "/api/v1/admin/todos", atok, nil)
	require.Equal(t, 0, resp.Code)
	todos, _ := resp.Data.([]any)
	require.Len(t, todos, 1, "escalated 反馈进待办")
}

// ---- 配置覆盖删除（BE-08）----

func TestGate_OverrideDelete(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	router := app.Router()
	_, _ = app.Auth.CreateUser("ov1", "pw12345", "", auth.RoleStudent)
	_, uid := loginGet(t, app, "ov1", "pw12345")

	// set 学生覆盖
	resp := req2(t, router, "POST", "/api/v1/config/set", atok,
		map[string]any{"scope": "student", "studentId": uid, "key": "wordsPerLesson",
			"value": 50, "reason": "t"})
	require.Equal(t, 0, resp.Code, resp.Msg)
	assert.Equal(t, 50, app.Cfg.Int(uid, "wordsPerLesson"))

	// 删除覆盖 → 回落全局 60
	resp = req2(t, router, "DELETE", "/api/v1/config/override", atok,
		map[string]any{"studentId": uid, "key": "wordsPerLesson"})
	require.Equal(t, 0, resp.Code, resp.Msg)
	assert.Equal(t, 60, app.Cfg.Int(uid, "wordsPerLesson"), "回落全局默认")

	// 未知键删除报错
	resp = req2(t, router, "DELETE", "/api/v1/config/override", atok,
		map[string]any{"studentId": uid, "key": "nope"})
	assert.Equal(t, errBadRequest, resp.Code)
}

// ---- 改密 + 词库 PUT/DELETE（§10 一致性补齐端点）----

func TestGate_ChangePasswordAndBookCRUD(t *testing.T) {
	app, _ := newTestApp(t)
	router := app.Router()
	atok, _ := loginGet(t, app, "admin", "adminpw")

	// 改密：旧密码错拒
	resp := req2(t, router, "POST", "/api/v1/auth/change_password", atok,
		map[string]string{"oldPassword": "wrong", "newPassword": "newpw999"})
	assert.Equal(t, errBadRequest, resp.Code)
	// 正确改密 → 新密码可登录
	resp = req2(t, router, "POST", "/api/v1/auth/change_password", atok,
		map[string]string{"oldPassword": "adminpw", "newPassword": "newpw999"})
	require.Equal(t, 0, resp.Code, resp.Msg)
	ntok, _ := loginGet(t, app, "admin", "newpw999")
	require.NotEmpty(t, ntok, "改密后新密码可登录")
	// 改回（供其他测试用）
	_ = req2(t, router, "POST", "/api/v1/auth/change_password", ntok,
		map[string]string{"oldPassword": "newpw999", "newPassword": "adminpw"})

	// 词库 PUT
	bid := seedBook(t, app, ntok, "改名库", "g7", []string{"apple"})
	resp = req2(t, router, "PUT", "/api/v1/wordbooks/"+itoa64(bid), ntok,
		map[string]string{"name": "改名后", "description": "已更新"})
	require.Equal(t, 0, resp.Code, resp.Msg)
	bm, _ := resp.Data.(map[string]any)
	assert.Equal(t, "改名后", bm["Name"])
	// 坏库 PUT
	resp = req2(t, router, "PUT", "/api/v1/wordbooks/999", ntok,
		map[string]string{"name": "x"})
	assert.Equal(t, errNotFound, resp.Code)
	// 词库 DELETE
	resp = req2(t, router, "DELETE", "/api/v1/wordbooks/"+itoa64(bid), ntok, nil)
	require.Equal(t, 0, resp.Code)
	resp = req2(t, router, "DELETE", "/api/v1/wordbooks/"+itoa64(bid), ntok, nil)
	assert.Equal(t, errNotFound, resp.Code, "重复删除 404")
}

func TestGate_TeacherUtterances(t *testing.T) {
	_, clk, _, room := setupRoom(t)
	fc := &fakeClient{}
	room.Join(fc)

	// 教完一组触发小测 → transition 话术（say 帧）
	cfg := ttsCfg{en: 3, rp: 3}
	for i := 0; i < 5; i++ {
		teachOneRoom(room, cfg)
	}
	// 小测答错一题 → correct 话术
	frames := room.HandleEvent(classroom.Event{Type: "answer_submitted", Text: "完全不对"})
	sayCount := 0
	for _, f := range frames {
		if a, _ := f["action"].(map[string]any); a != nil && a["type"] == "say" {
			sayCount++
		}
	}
	assert.GreaterOrEqual(t, sayCount, 1, "答错触发纠错话术(say 帧)")

	// 全帧流中也应有 say
	totalSay := 0
	for _, f := range fc.frames {
		if a, _ := f["action"].(map[string]any); a != nil && a["type"] == "say" {
			totalSay++
		}
	}
	assert.GreaterOrEqual(t, totalSay, 1, "客户端收到 say 帧")
	_ = clk
}
