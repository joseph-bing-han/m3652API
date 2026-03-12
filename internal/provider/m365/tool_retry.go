package m365

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	maxToolCallRepairAttempts = 2
	toolCallRepairTimeout     = 120 * time.Second
)

// repairToolCall 在上游输出“疑似工具调用但无法解析”时，最多重试两次要求其只输出 JSON。
// 注意：这会在同一 conversation 中额外增加 1~2 条 user message，属于“以稳定性换取纯净上下文”的折中。
func (e *Executor) repairToolCall(ctx context.Context, a *coreauth.Auth, accessToken, conversationID string, basePayload m365ChatOverStreamRequest, tools []openAITool) (any, bool) {
	if e == nil || a == nil || strings.TrimSpace(accessToken) == "" || strings.TrimSpace(conversationID) == "" || len(tools) == 0 {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for attempt := 1; attempt <= maxToolCallRepairAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, false
		}

		retryPayload := basePayload
		retryPayload.Message = m365RequestMessage{Text: toolCallRepairUserMessage(attempt)}

		ctxMsgs := make([]m365ContextMessage, len(basePayload.AdditionalContext))
		copy(ctxMsgs, basePayload.AdditionalContext)
		ctxMsgs = append(ctxMsgs, m365ContextMessage{
			Description: "Tool call repair request",
			Text:        toolCallRepairContext(),
		})
		retryPayload.AdditionalContext = ctxMsgs

		upCtx, cancel := withTimeoutIfNone(ctx, toolCallRepairTimeout)
		resp, err := e.chatOverStream(upCtx, a, accessToken, conversationID, retryPayload)
		if err != nil {
			cancel()
			continue
		}

		var item any
		var ok bool
		var finalAssistantText string

		_ = readSSEStream(upCtx, resp.Body, func(ev sseEvent) bool {
			_, current := selectAssistantMessage(ev.Data, retryPayload.Message.Text)
			if strings.TrimSpace(current) == "" {
				return true
			}
			finalAssistantText = current
			if tc, parsed := tryParseToolCall(current); parsed {
				callID := "call_" + uuid.NewString()
				if it, allowed := buildToolCallItem(callID, tc, tools); allowed {
					item = it
					ok = true
					cancel()
					return false
				}
			}
			return true
		})

		_ = resp.Body.Close()
		cancel()

		if ok {
			return item, true
		}

		// 兜底：尝试解析最终完整文本。
		if tc, parsed := tryParseToolCall(finalAssistantText); parsed {
			callID := "call_" + uuid.NewString()
			if it, allowed := buildToolCallItem(callID, tc, tools); allowed {
				return it, true
			}
		}
	}

	return nil, false
}

func toolCallRepairUserMessage(attempt int) string {
	if attempt <= 1 {
		return "Output ONLY a single JSON object for the tool call. No markdown. No extra text."
	}
	return "Output ONLY a single JSON object for the tool call. No markdown. No extra text. Use fields: type, tool, arguments OR type, tool, input."
}

func toolCallRepairContext() string {
	return strings.TrimSpace(`
You MUST output exactly one JSON object and nothing else.

Rules:
- No markdown code fences.
- No explanations.
- Only use one tool from the available tools list.

Schema:
{"type":"tool_call","tool":"<tool_name>","arguments":{...}}
{"type":"tool_call","tool":"<tool_name>","input":"..."}
`)
}
