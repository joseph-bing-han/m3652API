package m365

import (
	"encoding/json"
	"strings"
)

const (
	toolCallV2Start          = "ꆈ龘ᐅ"
	toolCallV2End            = "ᐊ龘ꆈ"
	toolCallV2NameStart      = "ꊰ▸"
	toolCallV2NameEnd        = "◂ꊰ"
	toolCallV2ArgumentsStart = "ꊰ▹"
	toolCallV2ArgumentsEnd   = "◃ꊰ"
	toolCallV2InputStart     = "ꊰ⟪"
	toolCallV2InputEnd       = "⟫ꊰ"

	// 兼容旧命名，避免调用点分散时编译失败。
	toolCallBlockStart = toolCallV2Start
	toolCallBlockEnd   = toolCallV2End
	toolCallNameStart  = toolCallV2NameStart
	toolCallNameEnd    = toolCallV2NameEnd
	toolCallArgsStart  = toolCallV2ArgumentsStart
	toolCallArgsEnd    = toolCallV2ArgumentsEnd
	toolCallInputStart = toolCallV2InputStart
	toolCallInputEnd   = toolCallV2InputEnd

	toolCallV2ArgsStart = toolCallV2ArgumentsStart
	toolCallV2ArgsEnd   = toolCallV2ArgumentsEnd
)

type toolCall struct {
	Tool      string
	Arguments any
	Input     string
}

func looksLikeToolCallCandidate(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if strings.Contains(t, toolCallV2Start) {
		return true
	}
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "```")
}

func tryParseToolCalls(text string) ([]*toolCall, string, bool) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return nil, "", false
	}

	if calls, cleanText, ok := parseV2ToolCalls(raw); ok {
		return calls, cleanText, true
	}

	if tc, ok := parseV1ToolCall(raw); ok {
		return []*toolCall{tc}, raw, true
	}

	return nil, raw, false
}

func tryParseToolCall(text string) (*toolCall, bool) {
	calls, _, ok := tryParseToolCalls(text)
	if !ok || len(calls) == 0 {
		return nil, false
	}
	return calls[0], true
}

func parseToolCalls(text string) ([]*toolCall, bool) {
	calls, _, ok := tryParseToolCalls(text)
	if !ok || len(calls) == 0 {
		return nil, false
	}
	return calls, true
}

func parseV2ToolCalls(raw string) ([]*toolCall, string, bool) {
	remaining := raw
	calls := make([]*toolCall, 0, 2)
	cleanSegments := make([]string, 0, 4)

	for strings.TrimSpace(remaining) != "" {
		start := strings.Index(remaining, toolCallV2Start)
		if start < 0 {
			cleanSegments = append(cleanSegments, remaining)
			remaining = ""
			break
		}

		cleanSegments = append(cleanSegments, remaining[:start])
		blockStart := start + len(toolCallV2Start)
		tail := remaining[blockStart:]
		endRel := strings.Index(tail, toolCallV2End)
		if endRel < 0 {
			// 保留未闭合块，避免误删正文。
			cleanSegments = append(cleanSegments, remaining[start:])
			remaining = ""
			break
		}

		blockBody := strings.TrimSpace(tail[:endRel])
		if tc, ok := parseV2ToolCallBlock(blockBody); ok {
			calls = append(calls, tc)
		} else {
			// 未能解析时按普通文本保留。
			cleanSegments = append(cleanSegments, remaining[start:blockStart+endRel+len(toolCallV2End)])
		}

		remaining = tail[endRel+len(toolCallV2End):]
	}

	if len(calls) == 0 {
		return nil, strings.TrimSpace(raw), false
	}
	cleanText := strings.TrimSpace(strings.Join(cleanSegments, ""))
	return calls, cleanText, true
}

func parseV2ToolCallBlock(block string) (*toolCall, bool) {
	name, ok := extractDelimitedValue(block, toolCallV2NameStart, toolCallV2NameEnd)
	if !ok {
		return nil, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}

	if argsRaw, ok := extractDelimitedValue(block, toolCallV2ArgumentsStart, toolCallV2ArgumentsEnd); ok {
		var args any
		if err := json.Unmarshal([]byte(strings.TrimSpace(argsRaw)), &args); err != nil {
			return nil, false
		}
		if args == nil {
			args = map[string]any{}
		}
		if _, ok := args.(map[string]any); !ok {
			return nil, false
		}
		return &toolCall{Tool: name, Arguments: args}, true
	}

	if inputRaw, ok := extractDelimitedValue(block, toolCallV2InputStart, toolCallV2InputEnd); ok {
		input := normalizeRawInput(inputRaw)
		if strings.TrimSpace(input) == "" {
			return nil, false
		}
		return &toolCall{Tool: name, Input: input}, true
	}

	// 容错：块内若直接输出 JSON，则按 V1 解析。
	if tc, ok := parseV1ToolCall(block); ok {
		if strings.TrimSpace(tc.Tool) == "" {
			tc.Tool = name
		}
		return tc, true
	}

	return nil, false
}

func extractDelimitedValue(s, startMark, endMark string) (string, bool) {
	start := strings.Index(s, startMark)
	if start < 0 {
		return "", false
	}
	tail := s[start+len(startMark):]
	end := strings.Index(tail, endMark)
	if end < 0 {
		return "", false
	}
	return tail[:end], true
}

func normalizeRawInput(s string) string {
	s = strings.TrimPrefix(s, "\r\n")
	s = strings.TrimPrefix(s, "\n")
	s = strings.TrimSuffix(s, "\r\n")
	s = strings.TrimSuffix(s, "\n")
	return s
}

func parseV1ToolCall(text string) (*toolCall, bool) {
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

	tool := strings.TrimSpace(firstString(m, "tool", "tool_name", "name"))
	if tool == "" {
		return nil, false
	}

	tc := &toolCall{Tool: tool}
	if v, ok := m["arguments"]; ok {
		tc.Arguments = v
	} else if v, ok := m["args"]; ok {
		tc.Arguments = v
	}

	if v, ok := m["input"]; ok {
		switch x := v.(type) {
		case string:
			tc.Input = x
		default:
			if rawInput, ok := jsonString(x); ok {
				tc.Input = rawInput
			}
		}
	}

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
