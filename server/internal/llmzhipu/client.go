// Package llmzhipu 智谱 GLM 真实客户端（OpenAI 兼容 /chat/completions）。
// D-12：判分 L2 / 反馈意图解析等纯文本任务；密钥从 secrets 读取，严禁入库出日志。
package llmzhipu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client 智谱 GLM 客户端。
type Client struct {
	APIKey  string
	BaseURL string // https://open.bigmodel.cn/api/coding/paas/v4
	Model   string // glm-4-flash
	HTTP    *http.Client
}

// New 构造（timeout 默认 30s）。
func New(apiKey, baseURL, model string) *Client {
	if model == "" {
		model = "glm-4-flash"
	}
	return &Client{APIKey: apiKey, BaseURL: baseURL, Model: model,
		HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Chat 一次对话补全（system+user → content）。
func (c *Client) Chat(ctx context.Context, system, user string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.2,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/chat/completions",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("zhipu %d: %.200s", resp.StatusCode, string(raw))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("zhipu: empty choices")
	}
	return out.Choices[0].Message.Content, nil
}

// ChatJSON 要求严格 JSON 输出（response_format json_object）并解析到 out。
func (c *Client) ChatJSON(ctx context.Context, system, user string, out any) error {
	body, _ := json.Marshal(map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.1,
		"response_format": map[string]string{"type": "json_object"},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/chat/completions",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("zhipu %d: %.200s", resp.StatusCode, string(raw))
	}
	var wrap struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return err
	}
	if len(wrap.Choices) == 0 {
		return fmt.Errorf("zhipu: empty choices")
	}
	return json.Unmarshal([]byte(wrap.Choices[0].Message.Content), out)
}
