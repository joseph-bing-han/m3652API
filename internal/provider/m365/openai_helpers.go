package m365

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

type openAITool struct {
	ToolType      string
	Name          string
	Description   string
	RawJSONSchema string
}

func parseOpenAITools(rawRequest []byte) []openAITool {
	tools := gjson.GetBytes(rawRequest, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return nil
	}

	out := make([]openAITool, 0, len(tools.Array()))
	for _, t := range tools.Array() {
		tt := strings.ToLower(strings.TrimSpace(t.Get("type").String()))
		if tt == "" {
			continue
		}

		name := strings.TrimSpace(t.Get("name").String())
		desc := strings.TrimSpace(t.Get("description").String())
		schema := ""

		if tt == "function" {
			fn := t.Get("function")
			if fn.Exists() {
				if name == "" {
					name = strings.TrimSpace(fn.Get("name").String())
				}
				if desc == "" {
					desc = strings.TrimSpace(fn.Get("description").String())
				}
				if params := fn.Get("parameters"); params.Exists() {
					schema = params.Raw
				}
			}
			if schema == "" {
				if params := t.Get("parameters"); params.Exists() {
					schema = params.Raw
				}
			}
		}

		if name == "" {
			switch tt {
			case "local_shell", "tool_search", "web_search", "image_generation":
				name = tt
			}
		}
		if name == "" {
			continue
		}

		out = append(out, openAITool{
			ToolType:      tt,
			Name:          name,
			Description:   desc,
			RawJSONSchema: schema,
		})
	}
	return out
}

type turnExtract struct {
	UserTaskText string
	ImageURLs    []string
	ToolOutputs  []string
}

func extractTurnData(newItems []gjson.Result) turnExtract {
	var userTextParts []string
	var imageURLs []string
	var toolOutputs []string

	// 提取最后一条用户消息。
	for i := len(newItems) - 1; i >= 0; i-- {
		it := newItems[i]
		if strings.TrimSpace(it.Get("type").String()) != "message" {
			continue
		}
		if strings.TrimSpace(it.Get("role").String()) != "user" {
			continue
		}
		content := it.Get("content")
		if !content.IsArray() {
			break
		}
		for _, part := range content.Array() {
			pt := strings.TrimSpace(part.Get("type").String())
			switch pt {
			case "input_text":
				if txt := part.Get("text").String(); strings.TrimSpace(txt) != "" {
					userTextParts = append(userTextParts, txt)
				}
			case "input_image":
				img := part.Get("image_url")
				u := ""
				if img.Type == gjson.String {
					u = strings.TrimSpace(img.String())
				} else if img.Exists() {
					u = strings.TrimSpace(img.Get("url").String())
				}
				if u != "" {
					imageURLs = append(imageURLs, u)
				}
			}
		}
		break
	}

	// 按顺序收集工具输出。
	for _, it := range newItems {
		switch strings.TrimSpace(it.Get("type").String()) {
		case "function_call_output", "custom_tool_call_output", "tool_search_output":
			callID := strings.TrimSpace(it.Get("call_id").String())
			out := it.Get("output")
			outText := ""
			switch out.Type {
			case gjson.String:
				outText = out.String()
			default:
				if out.Exists() {
					outText = out.Raw
				}
			}
			outText = strings.TrimSpace(outText)
			if outText == "" {
				continue
			}
			toolOutputs = append(toolOutputs, buildToolOutputLine(callID, outText))
		}
	}

	userTaskText := strings.TrimSpace(strings.Join(userTextParts, "\n"))
	return turnExtract{
		UserTaskText: userTaskText,
		ImageURLs:    imageURLs,
		ToolOutputs:  toolOutputs,
	}
}

func extractPendingTurn(inputVal gjson.Result, st *sessionState) (turnExtract, int, bool) {
	nextProcessedLen := -1
	resetConversation := false
	var newItems []gjson.Result

	switch {
	case inputVal.IsArray():
		allItems := inputVal.Array()
		prevProcessedLen := 0
		if st != nil {
			prevProcessedLen = st.processedInputLen
		}
		if len(allItems) < prevProcessedLen {
			prevProcessedLen = 0
			resetConversation = true
		}
		if prevProcessedLen < len(allItems) {
			newItems = allItems[prevProcessedLen:]
		}
		nextProcessedLen = len(allItems)
	case inputVal.Type == gjson.String:
		// 无历史模式。
		newItems = []gjson.Result{
			{Type: gjson.String, Str: inputVal.String()},
		}
	}

	return extractTurnData(newItems), nextProcessedLen, resetConversation
}

func buildToolOutputLine(callID, outText string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return outText
	}
	return "call_id=" + callID + "\n" + outText
}

func jsonString(v any) (string, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}
