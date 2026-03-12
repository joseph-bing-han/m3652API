package m365

import (
	"encoding/json"
	"fmt"
)

func sseDataFromObject(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("data: %s\n\n", raw)), nil
}

func buildResponseCreatedEvent(responseID, model string) ([]byte, error) {
	ev := map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":     responseID,
			"object": "response",
			"model":  model,
		},
	}
	return sseDataFromObject(ev)
}

func buildResponseOutputTextDeltaEvent(delta string) ([]byte, error) {
	ev := map[string]any{
		"type":  "response.output_text.delta",
		"delta": delta,
	}
	return sseDataFromObject(ev)
}

func buildResponseOutputItemDoneEvent(item any) ([]byte, error) {
	ev := map[string]any{
		"type": "response.output_item.done",
		"item": item,
	}
	return sseDataFromObject(ev)
}

func buildResponseCompletedEvent(responseID string) ([]byte, error) {
	usage := map[string]any{
		"input_tokens":  0,
		"output_tokens": 0,
		"total_tokens":  0,
	}
	ev := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":    responseID,
			"usage": usage,
		},
	}
	return sseDataFromObject(ev)
}

func buildResponseFailedEvent(responseID, code, message string) ([]byte, error) {
	ev := map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"id": responseID,
			"error": map[string]any{
				"type":    "api_error",
				"code":    code,
				"message": message,
			},
		},
	}
	return sseDataFromObject(ev)
}

func buildAssistantMessageItem(text string) any {
	return map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []any{
			map[string]any{
				"type": "output_text",
				"text": text,
			},
		},
	}
}

func buildFunctionCallItem(callID, name, argumentsJSON string) any {
	return map[string]any{
		"type":      "function_call",
		"call_id":   callID,
		"name":      name,
		"arguments": argumentsJSON,
	}
}

func buildCustomToolCallItem(callID, name, input string) any {
	return map[string]any{
		"type":    "custom_tool_call",
		"call_id": callID,
		"name":    name,
		"input":   input,
	}
}

func buildLocalShellCallItem(callID string, command []string, workingDir string, timeoutMs int64) any {
	action := map[string]any{
		"type":    "exec",
		"command": command,
	}
	if workingDir != "" {
		action["working_directory"] = workingDir
	}
	if timeoutMs > 0 {
		action["timeout_ms"] = timeoutMs
	}

	return map[string]any{
		"type":    "local_shell_call",
		"call_id": callID,
		"status":  "completed",
		"action":  action,
	}
}
