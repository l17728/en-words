package llmzhipu

// 真实 API 集成测试（读 secrets；无密钥时 skip——CI 不依赖外网）。
import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("ENWORDS_REAL_API_TESTS") != "1" {
		t.Skip("cloud integration requires explicit ENWORDS_REAL_API_TESTS=1")
	}
	p := filepath.Join("..", "..", "..", "secrets", "dashscope.local.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Skip("secrets 不存在（非本机环境）")
	}
	// 按 zhipu: 段解析（文件含 dashscope/zhipu 两段，不能全文贪匹配）
	content := string(raw)
	idx := strings.Index(content, "zhipu:")
	if idx < 0 {
		t.Skip("secrets 缺 zhipu 段")
	}
	seg := content[idx:]
	key := regexp.MustCompile(`api_key:\s*"([^"]+)"`).FindStringSubmatch(seg)
	url := regexp.MustCompile(`base_url:\s*"([^"]+)"`).FindStringSubmatch(seg)
	if key == nil || url == nil || key[1] == "" {
		t.Skip("zhipu 段未配置 api_key")
	}
	return New(key[1], url[1], "glm-4-flash")
}

func TestChatReal(t *testing.T) {
	c := loadClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := c.Chat(ctx, "你是单词学习助手，只回答一个词。", "say ok")
	require.NoError(t, err, "智谱真实调用")
	assert.NotEmpty(t, out)
	t.Log("GLM 回复:", out)
}

func TestChatJSONReal(t *testing.T) {
	c := loadClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out struct {
		Judgment string `json:"judgment"`
	}
	err := c.ChatJSON(ctx,
		"你是单词判分器。根据词义判断学生回答是否正确。只输出 JSON：{\"judgment\":\"correct|near|wrong\"}。",
		`单词: apple；标准释义: n. 苹果；学生回答: "苹果"。请判分。`,
		&out)
	require.NoError(t, err)
	assert.Contains(t, []string{"correct", "near", "wrong"}, out.Judgment)
	t.Log("GLM 判分:", out.Judgment)
	assert.Equal(t, "correct", out.Judgment, "苹果=苹果 必须判对")

	// 错误回答
	err = c.ChatJSON(ctx,
		"你是单词判分器。根据词义判断学生回答是否正确。只输出 JSON：{\"judgment\":\"correct|near|wrong\"}。同义词判 near。",
		`单词: apple；标准释义: n. 苹果；学生回答: "香蕉"。请判分。`,
		&out)
	require.NoError(t, err)
	assert.Equal(t, "wrong", out.Judgment)
}

func TestChatRefuseLongInput(t *testing.T) {
	c := loadClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	long := strings.Repeat("a", 200000) // 200KB 触发限流/拒绝路径
	_, err := c.Chat(ctx, "echo", long)
	assert.Error(t, err, "超长输入应失败（非 2xx 或超时）")
}
