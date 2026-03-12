package m365

import (
	"fmt"
	"strings"
)

const (
	maxContextBlockChars = 12000
	maxToolsInDirectory  = 60
)

func buildAdditionalContext(instructions, reasoningEffort, verbosity string, tools []openAITool, toolOutputs []string, ocrResults []string) []m365ContextMessage {
	out := make([]m365ContextMessage, 0, 6)

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

	style := strings.TrimSpace(buildStyleHints(reasoningEffort, verbosity))
	if style != "" {
		out = append(out, m365ContextMessage{
			Description: "Reasoning/verbosity",
			Text:        truncateMiddle(style, maxContextBlockChars),
		})
	}

	out = append(out, m365ContextMessage{
		Description: "Tool calling protocol",
		Text:        truncateMiddle(toolCallingProtocolV1(), maxContextBlockChars),
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

	if len(ocrResults) > 0 {
		txt := strings.Join(ocrResults, "\n\n")
		out = append(out, m365ContextMessage{
			Description: "Image OCR results",
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

func buildStyleHints(reasoningEffort, verbosity string) string {
	reasoningEffort = strings.ToLower(strings.TrimSpace(reasoningEffort))
	verbosity = strings.ToLower(strings.TrimSpace(verbosity))

	var parts []string
	if reasoningEffort != "" {
		switch reasoningEffort {
		case "none", "low":
			parts = append(parts, "Reasoning effort: low. Be concise and fast.")
		case "medium":
			parts = append(parts, "Reasoning effort: medium. Be structured and practical.")
		case "high", "xhigh":
			parts = append(parts, "Reasoning effort: high. Be thorough and check edge cases.")
		default:
			parts = append(parts, fmt.Sprintf("Reasoning effort: %s.", reasoningEffort))
		}
	}

	if verbosity != "" {
		parts = append(parts, fmt.Sprintf("Verbosity: %s.", verbosity))
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func toolCallingProtocolV1() string {
	return strings.TrimSpace(`
If you need to use a tool, output ONLY a single JSON object.

Rules:
- No extra text.
- No markdown code fences.
- Do not output multiple JSON objects.
- Only use tools from the available tools list.

Schema:
- Function tool:
  {"type":"tool_call","tool":"<tool_name>","arguments":{...}}
- Custom/freeform tool:
  {"type":"tool_call","tool":"<tool_name>","input":"..."}

Special case: local_shell
  {"type":"tool_call","tool":"local_shell","arguments":{"command":["ls","-la"],"working_directory":".","timeout_ms":60000}}
`)
}

func buildToolDirectory(tools []openAITool) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available tools:\n")
	count := 0
	for _, t := range tools {
		if t.Name == "" {
			continue
		}
		count++
		if count > maxToolsInDirectory {
			b.WriteString("- ...(truncated)\n")
			break
		}
		line := fmt.Sprintf("- %s: %s", t.ToolType, t.Name)
		if t.Description != "" {
			line += " - " + t.Description
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
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
