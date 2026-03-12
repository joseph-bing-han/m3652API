package m365

import (
	"encoding/json"
	"time"
)

func buildNonStreamingResponse(responseID, model string, output []any) ([]byte, error) {
	resp := map[string]any{
		"id":         responseID,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"model":      model,
		"status":     "completed",
		"output":     output,
		"usage": map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
			"total_tokens":  0,
		},
	}
	return json.Marshal(resp)
}
