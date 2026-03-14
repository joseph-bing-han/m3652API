package m365

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestExtractTurnData_ImageURLObject(t *testing.T) {
	item := gjson.Parse(`{
  "type":"message",
  "role":"user",
  "content":[
    {"type":"input_text","text":"hello"},
    {"type":"input_image","image_url":{"url":"data:image/png;base64,AAAA"}}
  ]
}`)
	turn := extractTurnData([]gjson.Result{item})
	if turn.UserTaskText != "hello" {
		t.Fatalf("unexpected user text: %q", turn.UserTaskText)
	}
	if len(turn.ImageURLs) != 1 {
		t.Fatalf("expected 1 image url, got %d", len(turn.ImageURLs))
	}
	if turn.ImageURLs[0] != "data:image/png;base64,AAAA" {
		t.Fatalf("unexpected image url: %q", turn.ImageURLs[0])
	}
}

func TestParseOpenAITools_ResponsesStyleFunction(t *testing.T) {
	raw := []byte(`{
  "tools": [
    {
      "type": "function",
      "name": "exec_command",
      "description": "Run command",
      "parameters": {"type":"object","properties":{"cmd":{"type":"string"}}}
    }
  ]
}`)
	tools := parseOpenAITools(raw)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].ToolType != "function" || tools[0].Name != "exec_command" {
		t.Fatalf("unexpected tool: %#v", tools[0])
	}
	if tools[0].RawJSONSchema == "" {
		t.Fatalf("expected non-empty schema")
	}
}

func TestParseOpenAITools_ChatCompletionsStyleFunction(t *testing.T) {
	raw := []byte(`{
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "write_file",
        "description": "Write file",
        "parameters": {"type":"object","properties":{"path":{"type":"string"}}}
      }
    },
    {
      "type": "local_shell"
    }
  ]
}`)
	tools := parseOpenAITools(raw)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "write_file" || tools[0].Description != "Write file" {
		t.Fatalf("unexpected function tool: %#v", tools[0])
	}
	if tools[1].Name != "local_shell" || tools[1].ToolType != "local_shell" {
		t.Fatalf("unexpected local_shell tool: %#v", tools[1])
	}
}

func TestExtractPendingTurn_DoesNotIncludeHistoryItems(t *testing.T) {
	input := gjson.Parse(`[
  {
    "type": "message",
    "role": "user",
    "content": [
      {"type": "input_text", "text": "first"}
    ]
  },
  {
    "type": "message",
    "role": "user",
    "content": [
      {"type": "input_text", "text": "second"}
    ]
  }
]`)

	st := &sessionState{processedInputLen: 1}
	turn, nextLen, resetConversation := extractPendingTurn(input, st)
	if resetConversation {
		t.Fatal("did not expect conversation reset")
	}
	if nextLen != 2 {
		t.Fatalf("expected next processed length 2, got %d", nextLen)
	}
	if turn.UserTaskText != "second" {
		t.Fatalf("unexpected user task: %q", turn.UserTaskText)
	}
}

func TestExtractPendingTurn_ResetsWhenHistoryShrinks(t *testing.T) {
	input := gjson.Parse(`[
  {
    "type": "message",
    "role": "user",
    "content": [
      {"type": "input_text", "text": "rewritten"}
    ]
  }
]`)

	st := &sessionState{processedInputLen: 3}
	turn, nextLen, resetConversation := extractPendingTurn(input, st)
	if !resetConversation {
		t.Fatal("expected conversation reset")
	}
	if nextLen != 1 {
		t.Fatalf("expected next processed length 1, got %d", nextLen)
	}
	if turn.UserTaskText != "rewritten" {
		t.Fatalf("unexpected user task: %q", turn.UserTaskText)
	}
}
