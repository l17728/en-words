// Package voiceclone 实现叮当老师音色克隆（D-17 保留通道 / docs/05 §4.6）：
// DashScope qwen-voice-enrollment；Mock=确定性（mock-vc-<name>）；真实=HTTP 调用。
// 音色创建后填入 Omni 会话 voice 参数（config voice.teacherVoice）。
package voiceclone

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CreateInput 创建音色入参。
type CreateInput struct {
	AudioWAV      []byte // 16bit 单声道 ≥24kHz，10~60s，<10MB
	PreferredName string // ≤16 字符字母数字下划线
	Text          string // 可选：音频对应文本（提升复刻质量）
	Language      string // 可选：zh/en
}

// VoiceInfo 音色信息。
type VoiceInfo struct {
	Voice   string    `json:"voice"` // 用于 Omni session voice 参数
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

// Provider 克隆通道（mock | dashscope）。
type Provider interface {
	Create(ctx context.Context, in CreateInput) (string, error)
	List(ctx context.Context) ([]VoiceInfo, error)
}

// 校验（docs/05 §4.6 音频要求；服务端还会二次校验）
func Validate(in CreateInput) error {
	if len(in.AudioWAV) == 0 {
		return fmt.Errorf("voiceclone: audio required")
	}
	if len(in.AudioWAV) > 10<<20 {
		return fmt.Errorf("voiceclone: audio >10MB")
	}
	if !validName(in.PreferredName) {
		return fmt.Errorf("voiceclone: name must be [A-Za-z0-9_]{1,16}")
	}
	return nil
}

func validName(s string) bool {
	if len(s) == 0 || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// ---- Mock ----

type Mock struct {
	mu     sync.Mutex
	voices []VoiceInfo
}

func NewMock() *Mock { return &Mock{} }

func (m *Mock) Create(_ context.Context, in CreateInput) (string, error) {
	if err := Validate(in); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v := fmt.Sprintf("mock-vc-%s-%d", in.PreferredName, len(m.voices)+1)
	m.voices = append(m.voices, VoiceInfo{Voice: v, Name: in.PreferredName, Created: time.Now()})
	return v, nil
}

func (m *Mock) List(_ context.Context) ([]VoiceInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]VoiceInfo(nil), m.voices...), nil
}

// ---- DashScope 真实实现（qwen-voice-enrollment，docs/05 §4.6）----

type DashScope struct {
	APIKey     string
	BaseURL    string // https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com
	TargetModel string // 须与 Omni 会话模型一致（qwen3.5-omni-plus-realtime）
	Client     *http.Client
	mu         sync.Mutex
	voices     []VoiceInfo
}

func NewDashScope(apiKey, workspaceID, targetModel string) *DashScope {
	return &DashScope{
		APIKey: apiKey,
		BaseURL: fmt.Sprintf("https://%s.cn-beijing.maas.aliyuncs.com", workspaceID),
		TargetModel: targetModel,
		Client: &http.Client{Timeout: 60 * time.Second},
	}
}

func (d *DashScope) Create(ctx context.Context, in CreateInput) (string, error) {
	if err := Validate(in); err != nil {
		return "", err
	}
	payload := map[string]any{
		"model": "qwen-voice-enrollment",
		"input": map[string]any{
			"action":        "create",
			"target_model":  d.TargetModel,
			"preferred_name": in.PreferredName,
			"audio": map[string]any{
				"data": "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(in.AudioWAV),
			},
		},
	}
	if in.Text != "" {
		payload["input"].(map[string]any)["text"] = in.Text
	}
	if in.Language != "" {
		payload["input"].(map[string]any)["language"] = in.Language
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST",
		d.BaseURL+"/api/v1/services/audio/tts/customization", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+d.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Output struct {
			Voice string `json:"voice"`
		} `json:"output"`
		Code string `json:"code"`
		Msg  string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Code != "" && out.Code != "0" && out.Output.Voice == "" {
		return "", fmt.Errorf("voiceclone: %s %s", out.Code, out.Msg)
	}
	if out.Output.Voice == "" {
		return "", fmt.Errorf("voiceclone: empty voice in response")
	}
	d.mu.Lock()
	d.voices = append(d.voices, VoiceInfo{Voice: out.Output.Voice, Name: in.PreferredName, Created: time.Now()})
	d.mu.Unlock()
	return out.Output.Voice, nil
}

func (d *DashScope) List(_ context.Context) ([]VoiceInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]VoiceInfo(nil), d.voices...), nil
}
