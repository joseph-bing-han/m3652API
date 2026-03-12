package m365

import (
	"encoding/json"
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

func TestParseToolCalls_DelimitedFunction(t *testing.T) {
	in := "Need command output.\n" +
		toolCallBlockStart + "\n" +
		toolCallNameStart + "exec_command" + toolCallNameEnd + "\n" +
		toolCallArgsStart + "{\"cmd\":\"ls -la\"}" + toolCallArgsEnd + "\n" +
		toolCallBlockEnd

	calls, ok := parseToolCalls(in)
	if !ok || len(calls) != 1 {
		t.Fatalf("expected 1 parsed call, ok=%v len=%d", ok, len(calls))
	}
	if calls[0].Tool != "exec_command" {
		t.Fatalf("unexpected tool: %q", calls[0].Tool)
	}
	args, ok := calls[0].Arguments.(map[string]any)
	if !ok || strings.TrimSpace(anyStringFromValue(args["cmd"])) != "ls -la" {
		t.Fatalf("unexpected args: %#v", calls[0].Arguments)
	}
}

func TestParseToolCalls_DelimitedCustomRawInput(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: hello.txt\n+hello\n*** End Patch"
	in := toolCallBlockStart + "\n" +
		toolCallNameStart + "apply_patch" + toolCallNameEnd + "\n" +
		toolCallInputStart + "\n" + patch + "\n" + toolCallInputEnd + "\n" +
		toolCallBlockEnd

	calls, ok := parseToolCalls(in)
	if !ok || len(calls) != 1 {
		t.Fatalf("expected 1 parsed call, ok=%v len=%d", ok, len(calls))
	}
	if calls[0].Tool != "apply_patch" {
		t.Fatalf("unexpected tool: %q", calls[0].Tool)
	}
	if calls[0].Input != patch {
		t.Fatalf("unexpected input: %q", calls[0].Input)
	}
}

func TestParseToolCalls_MultipleDelimitedBlocks(t *testing.T) {
	in := toolCallBlockStart + "\n" +
		toolCallNameStart + "exec_command" + toolCallNameEnd + "\n" +
		toolCallArgsStart + "{\"cmd\":\"pwd\"}" + toolCallArgsEnd + "\n" +
		toolCallBlockEnd + "\n" +
		toolCallBlockStart + "\n" +
		toolCallNameStart + "exec_command" + toolCallNameEnd + "\n" +
		toolCallArgsStart + "{\"cmd\":\"ls\"}" + toolCallArgsEnd + "\n" +
		toolCallBlockEnd

	calls, ok := parseToolCalls(in)
	if !ok || len(calls) != 2 {
		t.Fatalf("expected 2 parsed calls, ok=%v len=%d", ok, len(calls))
	}
}

func TestParseToolCalls_JSONFallback(t *testing.T) {
	in := `{"type":"tool_call","tool":"exec_command","arguments":{"cmd":"ls"}}`
	calls, ok := parseToolCalls(in)
	if !ok || len(calls) != 1 {
		t.Fatalf("expected 1 parsed call, ok=%v len=%d", ok, len(calls))
	}
	if calls[0].Tool != "exec_command" {
		t.Fatalf("unexpected tool: %q", calls[0].Tool)
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

func TestParseToolCalls_V2MultiBlocks(t *testing.T) {
	in := strings.Join([]string{
		"Need to run tools.",
		toolCallV2Start,
		toolCallV2NameStart + "exec_command" + toolCallV2NameEnd,
		toolCallV2ArgsStart + `{"cmd":"ls -la"}` + toolCallV2ArgsEnd,
		toolCallV2End,
		toolCallV2Start,
		toolCallV2NameStart + "apply_patch" + toolCallV2NameEnd,
		toolCallV2InputStart + "\n*** Begin Patch\n*** End Patch\n" + toolCallV2InputEnd,
		toolCallV2End,
	}, "\n")

	calls, ok := parseToolCalls(in)
	if !ok {
		t.Fatalf("expected v2 calls to be parsed")
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Tool != "exec_command" {
		t.Fatalf("unexpected first tool: %q", calls[0].Tool)
	}
	if calls[1].Tool != "apply_patch" {
		t.Fatalf("unexpected second tool: %q", calls[1].Tool)
	}
	if strings.TrimSpace(calls[1].Input) == "" {
		t.Fatalf("expected custom raw input")
	}
}

func TestBuildToolCallItem_FunctionArgumentsJSONString(t *testing.T) {
	tools := []openAITool{
		{ToolType: "function", Name: "exec_command"},
	}
	tc := &toolCall{
		Tool:      "exec_command",
		Arguments: `{"cmd":"pwd"}`,
	}
	it, ok := buildToolCallItem("call_1", tc, tools)
	if !ok {
		t.Fatalf("expected function call to be accepted")
	}
	m, ok := it.(map[string]any)
	if !ok {
		t.Fatalf("expected map item")
	}
	args, _ := m["arguments"].(string)
	if !json.Valid([]byte(args)) {
		t.Fatalf("expected valid json arguments, got %q", args)
	}
	if strings.Contains(args, `\"`) {
		t.Fatalf("expected non-double-encoded json, got %q", args)
	}
}

func TestNormalizeFunctionArguments_StringJSON(t *testing.T) {
	tools := []openAITool{{ToolType: "function", Name: "exec_command"}}
	it, ok := buildToolCallItem("call_x", &toolCall{Tool: "exec_command", Arguments: `{"cmd":"ls"}`}, tools)
	if !ok {
		t.Fatalf("expected function call with string arguments to be accepted")
	}
	m := it.(map[string]any)
	if got, _ := m["arguments"].(string); got != `{"cmd":"ls"}` {
		t.Fatalf("unexpected arguments: %q", got)
	}
}

func TestShouldForceToolRepair(t *testing.T) {
	tools := []openAITool{{ToolType: "local_shell", Name: "local_shell"}}

	if !shouldForceToolRepair("在当前目录下创建文件并写入日期", "我已经完成了", tools) {
		t.Fatalf("expected force repair for side-effect task")
	}
	if shouldForceToolRepair("continue", "ok", tools) {
		t.Fatalf("continue should not force repair")
	}
	if shouldForceToolRepair("请创建文件", "无法执行该操作", tools) {
		t.Fatalf("explicit refusal should not force repair")
	}
}

func anyStringFromValue(v any) string {
	s, _ := v.(string)
	return s
}
