package m365

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	maxContextBlockChars = 12000
	maxSchemaSummaryChar = 240
)

func buildAdditionalContext(instructions, verbosity string, tools []openAITool, toolOutputs []string) []m365ContextMessage {
	out := make([]m365ContextMessage, 0, 5)

	sys := strings.TrimSpace(strings.Join([]string{
		baseSystemInstructions(),
		strings.TrimSpace(instructions),
	}, "\n\n"))
	if sys != "" {
		out = append(out, m365ContextMessage{
			Description: "System instructions",
			Text:        truncateMiddle(sys, maxContextBlockChars),
		})
	}

	style := strings.TrimSpace(buildVerbosityHints(verbosity))
	if style != "" {
		out = append(out, m365ContextMessage{
			Description: "Output style",
			Text:        truncateMiddle(style, maxContextBlockChars),
		})
	}

	out = append(out, m365ContextMessage{
		Description: "Tool calling protocol",
		Text:        truncateMiddle(toolCallingProtocolV2(), maxContextBlockChars),
	})

	if dir := strings.TrimSpace(buildToolDirectory(tools)); dir != "" {
		out = append(out, m365ContextMessage{
			Description: "Available tools",
			Text:        truncateMiddle(dir, maxContextBlockChars),
		})
	}

	if len(toolOutputs) > 0 {
		txt := strings.Join(toolOutputs, "\n\n")
		out = append(out, m365ContextMessage{
			Description: "Tool outputs",
			Text:        truncateMiddle(txt, maxContextBlockChars),
		})
	}

	return out
}

func baseSystemInstructions() string {
	return strings.TrimSpace(`
You are a coding agent used via Codex CLI.

Requirements:
- All natural language responses must be in Simplified Chinese.
- Do not use emojis.
- Source code must be in English.
- Code comments must be in Simplified Chinese.
`)
}

func buildVerbosityHints(verbosity string) string {
	verbosity = strings.ToLower(strings.TrimSpace(verbosity))

	var parts []string
	if verbosity != "" {
		parts = append(parts, fmt.Sprintf("Verbosity: %s.", verbosity))
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func toolCallingProtocolV2() string {
	return strings.TrimSpace(fmt.Sprintf(`
If you need to use tools, output one or more tool blocks using this exact delimiter protocol.

Rules:
- Tool blocks can appear only at the end of your response.
- No markdown code fences for tool blocks.
- Use only tools from the available tools list.
- For function/local_shell tools, arguments must be valid JSON object.
- For custom/freeform tools, output raw input exactly between input delimiters.
- If user asks to create/modify/delete files, or run shell/command operations, you MUST call tool(s).
- Never claim file/command side effects are completed unless tool output confirms success.

Function/local_shell block:
%s
%s<tool_name>%s
%s{...}%s
%s

Custom/freeform block:
%s
%s<tool_name>%s
%s<raw_input>%s
%s

Fallback (only if delimiter protocol fails):
{"type":"tool_call","tool":"<tool_name>","arguments":{...}}
{"type":"tool_call","tool":"<tool_name>","input":"..."}
`,
		toolCallV2Start,
		toolCallV2NameStart, toolCallV2NameEnd,
		toolCallV2ArgsStart, toolCallV2ArgsEnd,
		toolCallV2End,
		toolCallV2Start,
		toolCallV2NameStart, toolCallV2NameEnd,
		toolCallV2InputStart, toolCallV2InputEnd,
		toolCallV2End,
	))
}

func buildToolDirectory(tools []openAITool) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available tools:\n")
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		line := fmt.Sprintf("- type=%s name=%s", strings.TrimSpace(t.ToolType), name)
		if t.Description != "" {
			line += " description=" + truncateMiddle(strings.TrimSpace(t.Description), 160)
		}
		if schema := summarizeToolSchema(t.RawJSONSchema); schema != "" {
			line += " schema=" + schema
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func summarizeToolSchema(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// 优先提取对象schema最关键的信息，减少上下文体积。
	parsed := gjson.Parse(raw)
	if parsed.Exists() {
		t := strings.TrimSpace(parsed.Get("type").String())
		requiredVals := parsed.Get("required").Array()
		required := make([]string, 0, len(requiredVals))
		for _, it := range requiredVals {
			if s := strings.TrimSpace(it.String()); s != "" {
				required = append(required, s)
			}
		}
		props := 0
		if p := parsed.Get("properties"); p.Exists() {
			props = len(p.Map())
		}

		var parts []string
		if t != "" {
			parts = append(parts, "type="+t)
		}
		if props > 0 {
			parts = append(parts, fmt.Sprintf("properties=%d", props))
		}
		if len(required) > 0 {
			parts = append(parts, "required="+strings.Join(required, ","))
		}
		if len(parts) > 0 {
			return truncateMiddle(strings.Join(parts, ";"), maxSchemaSummaryChar)
		}
	}

	// 兜底：保留紧凑JSON片段。
	if compact, err := compactJSON(raw); err == nil {
		return truncateMiddle(compact, maxSchemaSummaryChar)
	}
	return truncateMiddle(raw, maxSchemaSummaryChar)
}

func compactJSON(raw string) (string, error) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func truncateMiddle(s string, maxChars int) string {
	s = strings.TrimSpace(s)
	if maxChars <= 0 || len(s) <= maxChars {
		return s
	}
	if maxChars < 64 {
		return s[:maxChars]
	}

	keepHead := maxChars / 2
	keepTail := maxChars - keepHead - len("\n...(truncated)...\n")
	if keepTail < 0 {
		keepTail = 0
	}
	head := s[:keepHead]
	tail := s[len(s)-keepTail:]
	return strings.TrimSpace(head + "\n...(truncated)...\n" + tail)
}
