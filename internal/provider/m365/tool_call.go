package m365

import (
	"encoding/json"
	"strings"
)

type toolCall struct {
	Tool      string
	Arguments any
	Input     string
}

func looksLikeToolCallCandidate(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "```")
}

func tryParseToolCall(text string) (*toolCall, bool) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return nil, false
	}

	raw = stripMarkdownCodeFence(raw)

	obj := extractJSONObject(raw)
	if obj == "" {
		return nil, false
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(obj), &m); err != nil {
		return nil, false
	}

	tool := firstString(m, "tool", "tool_name", "name")
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return nil, false
	}

	tc := &toolCall{Tool: tool}

	if v, ok := m["arguments"]; ok {
		tc.Arguments = v
	}
	if v, ok := m["input"].(string); ok {
		tc.Input = v
	}

	// 至少需要包含 arguments 或 input 之一。
	if tc.Arguments == nil && strings.TrimSpace(tc.Input) == "" {
		return nil, false
	}

	return tc, true
}

func stripMarkdownCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// 移除第一行围栏标记。
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	} else {
		return ""
	}
	// 移除结尾围栏标记。
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}

	// 提取第一个括号配平的 JSON 对象，并忽略字符串内的花括号。
	inString := false
	escape := false
	depth := 0
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[start : i+1])
			}
			if depth < 0 {
				return ""
			}
		}
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}
