package m365

import (
	"strings"

	"github.com/tidwall/gjson"
)

func extractAssistantText(data []byte, userTaskText string) (messageID string, text string, ok bool) {
	msgs := gjson.GetBytes(data, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return "", "", false
	}
	arr := msgs.Array()
	if len(arr) == 0 {
		return "", "", false
	}

	userTaskText = strings.TrimSpace(userTaskText)

	// 优先选择最后一条非空文本，并尽量避免回显用户任务（尽力而为）。
	for i := len(arr) - 1; i >= 0; i-- {
		m := arr[i]
		t := strings.TrimSpace(m.Get("text").String())
		if t == "" {
			continue
		}
		if userTaskText != "" && t == userTaskText {
			continue
		}
		id := strings.TrimSpace(m.Get("id").String())
		if id == "" {
			id = strings.TrimSpace(m.Get("createdDateTime").String())
		}
		if id == "" {
			id = "assistant"
		}
		return id, t, true
	}

	// 兜底：选择最后一条非空文本。
	for i := len(arr) - 1; i >= 0; i-- {
		m := arr[i]
		t := strings.TrimSpace(m.Get("text").String())
		if t == "" {
			continue
		}
		id := strings.TrimSpace(m.Get("id").String())
		if id == "" {
			id = "assistant"
		}
		return id, t, true
	}

	return "", "", false
}
