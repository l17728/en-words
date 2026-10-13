// 真实 LLM 意图解析适配器（智谱 GLM；D-12）+ 装配注入。
package feedback

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ZhipuParser 智谱适配器。
type ZhipuParser struct {
	Client interface {
		ChatJSON(ctx context.Context, system, user string, out any) error
	}
}

// NewZhipuParser 构造。
func NewZhipuParser(c interface {
	ChatJSON(ctx context.Context, system, user string, out any) error
}) *ZhipuParser {
	return &ZhipuParser{Client: c}
}

type llmIntent struct {
	Kind          string  `json:"kind"`
	TargetWords   int     `json:"target_words"`
	LessonsPerDay int     `json:"lessons_per_day"`
	Date          string  `json:"date"` // 2006-01-02（相对今天）
	Word          string  `json:"word"`
	Confidence    float64 `json:"confidence"`
	NeedsConfirm  bool    `json:"needs_confirm"`
}

// ParseIntents LLM 解析（日期相对基准日换算为绝对日期）。
func (z *ZhipuParser) ParseIntents(ctx context.Context, text string, now time.Time) ([]Intent, error) {
	var out struct {
		Intents []llmIntent `json:"intents"`
	}
	sys := `你是学习反馈意图解析器。把学生反馈解析为意图数组，只输出 JSON：
{"intents":[{"kind":"A_reduce|B_increase|C_frequency|D_postpone|E_hard_word|F_complaint|G_other",
"target_words":整数或0,"lessons_per_day":整数或0,"date":"YYYY-MM-DD 或空","word":"英文词或空",
"confidence":0~1,"needs_confirm":布尔}]}
规则：减量=A_reduce；加量=B_increase；频率=C_frequency；请假/改期=D_postpone(date 填目标日期)；
某词难记=E_hard_word(word 填英文)；故障投诉=F_complaint；无关=G_other。`
	user := "今天是 " + now.Format("2006-01-02") + "（周" + string([]rune("日一二三四五六")[now.Weekday()]) +
		"）。学生反馈：\"" + text + "\"。请解析。"
	if err := z.Client.ChatJSON(ctx, sys, user, &out); err != nil {
		return nil, err
	}
	var intents []Intent
	for _, li := range out.Intents {
		it := Intent{Kind: IntentKind(li.Kind), Confidence: li.Confidence}
		it.TargetWords = li.TargetWords
		it.LessonsPerDay = li.LessonsPerDay
		it.Word = strings.ToLower(strings.TrimSpace(li.Word))
		if li.Date != "" {
			if t, err := time.ParseInLocation("2006-01-02", li.Date, now.Location()); err == nil {
				dd := t
				it.Date = &dd
			}
		}
		it.NeedsConfirm = li.NeedsConfirm
		if it.Kind == "" {
			it.Kind = IntentOther
		}
		intents = append(intents, it)
	}
	if len(intents) == 0 {
		return nil, fmt.Errorf("llm: no intents")
	}
	return intents, nil
}

// marshalDebug 调试用（避免未使用 import json）。
var _ = json.Marshal
