package voiceclone

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	wav := make([]byte, 1000)
	require.NoError(t, Validate(CreateInput{AudioWAV: wav, PreferredName: "dingdang"}))
	assert.Error(t, Validate(CreateInput{AudioWAV: nil, PreferredName: "x"}), "无音频")
	assert.Error(t, Validate(CreateInput{AudioWAV: make([]byte, 11<<20), PreferredName: "x"}), ">10MB")
	for _, bad := range []string{"", "中文名", "has space", "toolongname_12345", "a-b"} {
		assert.Error(t, Validate(CreateInput{AudioWAV: wav, PreferredName: bad}), "名字 %q 拒绝", bad)
	}
}

func TestMockProvider(t *testing.T) {
	m := NewMock()
	ctx := context.Background()
	v1, err := m.Create(ctx, CreateInput{AudioWAV: make([]byte, 2000), PreferredName: "dingdang"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(v1, "mock-vc-dingdang-1"), "确定性命名")
	v2, _ := m.Create(ctx, CreateInput{AudioWAV: make([]byte, 2000), PreferredName: "mama"})
	assert.True(t, strings.HasPrefix(v2, "mock-vc-mama-2"))
	// 非法入参拒绝
	_, err = m.Create(ctx, CreateInput{AudioWAV: make([]byte, 10), PreferredName: "好!"})
	assert.Error(t, err)
	list, _ := m.List(ctx)
	require.Len(t, list, 2)
	assert.Equal(t, "dingdang", list[0].Name)
}

// ---- DashScope 真实实现（httptest 假服务）----

func TestDashScopeProvider(t *testing.T) {
	var gotPath, gotAuth, gotModel, gotName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		in := body["input"].(map[string]any)
		gotModel = in["target_model"].(string)
		gotName = in["preferred_name"].(string)
		_, _ = w.Write([]byte(`{"output":{"voice":"qwen-omni-vc-dingdang-voice-2026-838b","count":1}}`))
	}))
	defer srv.Close()

	d := &DashScope{APIKey: "sk-test", BaseURL: srv.URL, TargetModel: "qwen3.5-omni-plus-realtime",
		Client: srv.Client()}
	ctx := context.Background()

	v, err := d.Create(ctx, CreateInput{AudioWAV: make([]byte, 3000), PreferredName: "dingdang", Text: "你好", Language: "zh"})
	require.NoError(t, err)
	assert.Equal(t, "qwen-omni-vc-dingdang-voice-2026-838b", v)
	assert.Equal(t, "/api/v1/services/audio/tts/customization", gotPath)
	assert.Equal(t, "Bearer sk-test", gotAuth)
	assert.Equal(t, "qwen3.5-omni-plus-realtime", gotModel, "target_model 与 Omni 一致(D-17)")
	assert.Equal(t, "dingdang", gotName)
	list, _ := d.List(ctx)
	require.Len(t, list, 1)

	// 服务端报错（业务 code）
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"Audio.PreprocessError","message":"audio too noisy"}`))
	}))
	defer srv2.Close()
	d2 := &DashScope{APIKey: "k", BaseURL: srv2.URL, TargetModel: "m", Client: srv2.Client()}
	_, err = d2.Create(ctx, CreateInput{AudioWAV: make([]byte, 3000), PreferredName: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PreprocessError")

	// 空音色响应
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"output":{}}`))
	}))
	defer srv3.Close()
	d3 := &DashScope{APIKey: "k", BaseURL: srv3.URL, TargetModel: "m", Client: srv3.Client()}
	_, err = d3.Create(ctx, CreateInput{AudioWAV: make([]byte, 3000), PreferredName: "x"})
	assert.Error(t, err, "空 voice 拒绝")

	// 网络错误
	d4 := &DashScope{APIKey: "k", BaseURL: "http://127.0.0.1:1", TargetModel: "m",
		Client: &http.Client{Timeout: time.Second}}
	_, err = d4.Create(ctx, CreateInput{AudioWAV: make([]byte, 3000), PreferredName: "x"})
	assert.Error(t, err)

	// 非法入参在真实通道同样本地预检
	_, err = d.Create(ctx, CreateInput{AudioWAV: make([]byte, 10), PreferredName: "!!"})
	assert.Error(t, err)
}
