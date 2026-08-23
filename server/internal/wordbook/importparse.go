// 批量导入解析（Mock LLM，05 §2.1 ParseImport 规格：确定性、可单测）
// 与 CSV 导出（FR-B6/TX-02：UTF-8 BOM）。真实版换智谱 GLM Provider，接口不变。
package wordbook

import (
	"fmt"
	"strings"
)

// ImportParse 解析自由文本为词条预览（B3 Mock：行解析；LLM 版语义一致）。
// 支持格式：`word|词性|释义`、`word 释义`；无法解析行进 warnings。
func (s *Service) ImportParse(_ int64, rawText string) (entries []EntryInput, warnings []string) {
	for i, line := range strings.Split(rawText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "|") {
			parts := strings.Split(line, "|")
			if len(parts) >= 3 {
				w := strings.TrimSpace(parts[0])
				pos := strings.TrimSpace(parts[1])
				m := strings.TrimSpace(parts[2])
				if w != "" && m != "" {
					entries = append(entries, EntryInput{Word: w,
						Meanings: []string{pos + " " + m}})
					continue
				}
			}
			warnings = append(warnings, fmt.Sprintf("第%d行格式不完整: %s", i+1, line))
			continue
		}
		// 空格分隔：首段英文为词，其余为释义
		fields := strings.Fields(line)
		if len(fields) >= 2 && isASCIIWord(fields[0]) {
			entries = append(entries, EntryInput{Word: fields[0],
				Meanings: []string{strings.Join(fields[1:], " ")}})
			continue
		}
		warnings = append(warnings, fmt.Sprintf("第%d行无法解析: %s", i+1, line))
	}
	return entries, warnings
}

func isASCIIWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '\'') {
			return false
		}
	}
	return true
}

// ExportCSV 导出词库为 CSV（UTF-8 BOM，Excel 可直接打开；TX-02）。
func (s *Service) ExportCSV(bookID int64) ([]byte, error) {
	if _, err := s.GetBook(bookID); err != nil {
		return nil, err
	}
	es, _, err := s.ListEntries(bookID, 1, 1<<30)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("\xEF\xBB\xBF") // BOM
	b.WriteString("word,phonetic,meanings,unit\n")
	for _, e := range es {
		meanings := ""
		for i, m := range e.Meanings {
			if i > 0 {
				meanings += "；"
			}
			meanings += m
		}
		b.WriteString(fmt.Sprintf("%s,%s,%s,%s\n",
			csvField(e.Word), csvField(e.Phonetic), csvField(meanings), csvField(e.Unit)))
	}
	return []byte(b.String()), nil
}

// csvField CSV 字段转义（含逗号/引号包裹）。
func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
}
