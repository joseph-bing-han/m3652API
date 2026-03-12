package m365

// 本文件定义与 Microsoft Graph Copilot Chat API 交互用的数据结构（仅覆盖本项目用到的字段）。

type m365ConversationCreateResponse struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
	State  string `json:"state,omitempty"`
}

type m365ChatOverStreamRequest struct {
	Message            m365RequestMessage      `json:"message"`
	LocationHint       m365LocationHint        `json:"locationHint"`
	AdditionalContext  []m365ContextMessage    `json:"additionalContext,omitempty"`
	ContextualResource *m365ContextualResource `json:"contextualResources,omitempty"`
}

type m365RequestMessage struct {
	Text string `json:"text"`
}

type m365LocationHint struct {
	TimeZone string `json:"timeZone"`
}

type m365ContextMessage struct {
	Text        string `json:"text"`
	Description string `json:"description,omitempty"`
}

type m365ContextualResource struct {
	WebContext *m365WebContext `json:"webContext,omitempty"`
	Files      []m365File      `json:"files,omitempty"`
}

type m365WebContext struct {
	IsWebEnabled bool `json:"isWebEnabled"`
}

type m365File struct {
	URI string `json:"uri"`
}
