// api 包内部工具：schema 构造、参数解码、token 生成、错误定义。
package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"enwords/internal/tools"
)

var errBookNotPublished = errors.New("api: book not published")

// mustSchema 将 JSON 字符串转为 RawMessage（装配期字面量，语法错误即 panic）。
func mustSchema(s string) json.RawMessage {
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		panic(fmt.Sprintf("bad inline schema: %v", err))
	}
	return raw
}

// decode 工具 Handler 入参解码。
func decode(args tools.Args, v any) error {
	return json.Unmarshal(args, v)
}

// newToken 随机 token（ticket 用）。
func newToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// ---- REST 统一响应（SDD §10）----

type Resp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

func ok(c any) Resp    { return Resp{Code: 0, Msg: "ok", Data: c} }
func fail(code int, msg string) Resp {
	return Resp{Code: code, Msg: msg}
}

// 错误码约定（HTTP 200 + 业务码；网络层错误用 HTTP 状态）
const (
	errUnauthorized = 401
	errForbidden    = 403
	errNotFound     = 404
	errBadRequest   = 400
	errInternal     = 500
)

// ---- R4-sec#3/#6：输入清洗与事件白名单 ----

// validClassroomEvent 课堂事件白名单（防任意 event 字符串触发未定义行为）。
func validClassroomEvent(ev string) bool {
	switch ev {
	case "word_clicked", "tts_finished", "repeat_done", "repeat_skipped", "answer_submitted":
		return true
	}
	return false
}

// sanitizeText 文本清洗：长度 ≤200、去控制字符与 HTML 标签（防注入下游）。
func sanitizeText(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case r < 0x20 && r != '\n' && r != '\t':
			// 控制字符丢弃
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
