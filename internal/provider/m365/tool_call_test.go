package m365

import (
	"strings"
	"testing"
)

func TestStripMarkdownCodeFence(t *testing.T) {
	in := "```json\n{\"type\":\"tool_call\",\"tool\":\"exec_command\",\"arguments\":{\"cmd\":\"ls\"}}\n```"
	got := stripMarkdownCodeFence(in)
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Fatalf("unexpected output: %q", got)
	}
	if !strings.Contains(got, "\"tool\"") {
		t.Fatalf("expected tool field: %q", got)
	}
}

func TestExtractJSONObject_FirstBalanced(t *testing.T) {
	in := "prefix {\"a\":1} trailing {\"b\":2}"
	got := extractJSONObject(in)
	if got != "{\"a\":1}" {
		t.Fatalf("unexpected json object: %q", got)
	}
}

func TestExtractJSONObject_IgnoresBracesInStrings(t *testing.T) {
	in := "prefix {\"a\":\"{not a brace}\",\"b\":2} suffix"
	got := extractJSONObject(in)
	if !strings.Contains(got, "\"a\"") || !strings.Contains(got, "\"b\"") {
		t.Fatalf("unexpected json object: %q", got)
	}
}

func TestTryParseToolCall_ExtraTextAndFence(t *testing.T) {
	in := "Sure.\n```json\n{\"type\":\"tool_call\",\"tool\":\"exec_command\",\"arguments\":{\"cmd\":\"ls -la\"}}\n```\nThanks."
	tc, ok := tryParseToolCall(in)
	if !ok || tc == nil {
		t.Fatalf("expected tool call to be parsed")
	}
	if tc.Tool != "exec_command" {
		t.Fatalf("unexpected tool: %q", tc.Tool)
	}
}

func TestBuildToolCallItem_WhitelistAndLimits(t *testing.T) {
	tools := []openAITool{
		{ToolType: "function", Name: "exec_command"},
		{ToolType: "custom", Name: "apply_patch"},
	}

	// 未在 tools 列表声明的 local_shell 应被拒绝。
	if it, ok := buildToolCallItem("call_x", &toolCall{Tool: "local_shell", Arguments: map[string]any{"command": []any{"ls"}}}, tools); ok || it != nil {
		t.Fatalf("expected local_shell to be rejected when not in tools list")
	}

	// 已声明的 function 工具应被接受。
	it, ok := buildToolCallItem("call_x", &toolCall{Tool: "exec_command", Arguments: map[string]any{"cmd": "ls -la"}}, tools)
	if !ok {
		t.Fatalf("expected function call to be accepted")
	}
	m, ok := it.(map[string]any)
	if !ok {
		t.Fatalf("expected map item")
	}
	if m["type"] != "function_call" {
		t.Fatalf("unexpected type: %v", m["type"])
	}

	// 自定义工具 input 长度限制。
	tooLarge := strings.Repeat("a", maxCustomToolInputChars+1)
	if it, ok := buildToolCallItem("call_x", &toolCall{Tool: "apply_patch", Input: tooLarge}, tools); ok || it != nil {
		t.Fatalf("expected oversized custom input to be rejected")
	}
}
