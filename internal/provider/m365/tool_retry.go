package m365

import (
	"context"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	maxToolCallRepairAttempts = 2
	toolCallRepairTimeout     = 120 * time.Second
)

// repairToolCall 在上游输出“疑似工具调用但无法解析”时，最多重试两次要求其按协议输出工具调用块。
// 注意：这会在同一 conversation 中额外增加 1~2 条 user message，属于“以稳定性换取纯净上下文”的折中。
func (e *Executor) repairToolCall(ctx context.Context, a *coreauth.Auth, accessToken, conversationID string, basePayload m365ChatOverStreamRequest, tools []openAITool) ([]any, bool) {
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

		var items []any
		var ok bool
		var finalAssistantText string

		_ = readSSEStream(upCtx, resp.Body, func(ev sseEvent) bool {
			_, current := selectAssistantMessage(ev.Data, retryPayload.Message.Text)
			if strings.TrimSpace(current) == "" {
				return true
			}
			finalAssistantText = current
			if parsed := parseToolCallItems(current, tools); len(parsed) > 0 {
				items = parsed
				ok = true
				cancel()
				return false
			}
			return true
		})

		_ = resp.Body.Close()
		cancel()

		if ok {
			return items, true
		}

		// 兜底：尝试解析最终完整文本。
		if parsed := parseToolCallItems(finalAssistantText, tools); len(parsed) > 0 {
			return parsed, true
		}
	}

	return nil, false
}

func toolCallRepairUserMessage(attempt int) string {
	if attempt <= 1 {
		return "Output ONLY tool call block(s) using the delimiter protocol. No markdown. No extra text."
	}
	return "Output ONLY tool call block(s) using delimiters. No markdown. No extra text. Use arguments markers for function/local_shell, input markers for custom."
}

func toolCallRepairContext() string {
	return strings.TrimSpace(`
You MUST output only tool call block(s) and nothing else.

Rules:
- No markdown code fences.
- No explanations.
- Only use tools from the available tools list.

Delimiter protocol:
` + toolCallBlockStart + ` ... ` + toolCallBlockEnd + `
` + toolCallNameStart + `tool_name` + toolCallNameEnd + `
` + toolCallArgsStart + `{"k":"v"}` + toolCallArgsEnd + `  (for function/local_shell)
` + toolCallInputStart + `<raw>` + toolCallInputEnd + `    (for custom)
`)
}
