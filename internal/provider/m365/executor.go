package m365

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	clipexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

type Executor struct {
	cfg      *config.Config
	sessions *sessionStore
	gcTick   atomic.Uint64
}

func NewExecutor(cfg *config.Config) *Executor {
	return &Executor{
		cfg:      cfg,
		sessions: newSessionStore(30 * time.Minute),
	}
}

func (Executor) Identifier() string { return providerKey }

func (e *Executor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	rawReq := opts.OriginalRequest
	if len(rawReq) == 0 {
		rawReq = req.Payload
	}

	model := strings.TrimSpace(gjson.GetBytes(rawReq, "model").String())
	if model == "" {
		model = req.Model
	}
	preset := resolveModelPreset(model)
	model = preset.ModelID
	responseID := "resp_" + uuid.NewString()

	// 定期清理会话（尽力而为）。
	if e != nil && e.gcTick.Add(1)%128 == 0 {
		e.sessions.gc()
	}

	ai, accessToken, err := e.ensureAccessToken(ctx, a)
	if err != nil {
		return clipexec.Response{}, err
	}

	sessionKey := strings.TrimSpace(gjson.GetBytes(rawReq, "prompt_cache_key").String())
	if sessionKey == "" {
		sessionKey = responseID
	}

	st, end, err := e.sessions.tryStart(sessionKey)
	if err != nil {
		return clipexec.Response{}, err
	}
	defer end()

	inputVal := gjson.GetBytes(rawReq, "input")
	instructions := gjson.GetBytes(rawReq, "instructions").String()
	reasoningEffort := gjson.GetBytes(rawReq, "reasoning.effort").String()
	if strings.TrimSpace(reasoningEffort) == "" {
		reasoningEffort = preset.DefaultReasoningEffort
	}
	verbosity := gjson.GetBytes(rawReq, "text.verbosity").String()
	toolChoice := strings.TrimSpace(gjson.GetBytes(rawReq, "tool_choice").String())

	tools := parseOpenAITools(rawReq)
	if strings.EqualFold(toolChoice, "none") {
		tools = nil
	}

	var newItems []gjson.Result
	if inputVal.IsArray() {
		allItems := inputVal.Array()
		if len(allItems) < st.processedInputLen {
			// 客户端可能重置了对话状态。
			st.processedInputLen = 0
			st.conversationID = ""
		}
		if st.processedInputLen < len(allItems) {
			newItems = allItems[st.processedInputLen:]
		} else {
			newItems = nil
		}
		// 提前更新已处理长度，避免缓存无限增长。
		st.processedInputLen = len(allItems)
	} else if inputVal.Type == gjson.String {
		// 无历史模式。
		newItems = []gjson.Result{
			gjson.Result{Type: gjson.String, Str: inputVal.String()},
		}
	}

	turn := extractTurnData(newItems)
	userTask := strings.TrimSpace(turn.UserTaskText)
	if userTask == "" {
		if len(turn.ToolOutputs) > 0 {
			userTask = "Continue."
		} else if len(turn.ImageURLs) > 0 {
			userTask = "Analyze the provided image(s) and follow the user's intent."
		} else if inputVal.Type == gjson.String {
			userTask = strings.TrimSpace(inputVal.String())
		} else {
			userTask = "Continue."
		}
	}

	webEnabled := true
	if v := gjson.GetBytes(rawReq, "metadata.web_enabled"); v.Exists() {
		webEnabled = v.Bool()
	}

	ocrResults := e.buildOCRResults(ctx, a, turn.ImageURLs)

	payload := m365ChatOverStreamRequest{
		Message:      m365RequestMessage{Text: userTask},
		LocationHint: m365LocationHint{TimeZone: ai.TimeZone},
		ContextualResource: &m365ContextualResource{
			WebContext: &m365WebContext{IsWebEnabled: webEnabled},
		},
	}

	payload.AdditionalContext = buildAdditionalContext(instructions, reasoningEffort, verbosity, tools, turn.ToolOutputs, ocrResults)

	if strings.TrimSpace(st.conversationID) == "" {
		convID, err := e.createConversation(ctx, a, accessToken)
		if err != nil {
			return clipexec.Response{}, err
		}
		st.conversationID = convID
	}

	upCtx, upCancel := withTimeoutIfNone(ctx, 300*time.Second)
	defer upCancel()

	resp, err := e.chatOverStream(upCtx, a, accessToken, st.conversationID, payload)
	if err != nil {
		return clipexec.Response{}, err
	}
	defer resp.Body.Close()

	var finalAssistantText string
	toolMode := false
	sawToolCandidate := false
	toolEmitted := false
	var toolItem any

	_ = readSSEStream(upCtx, resp.Body, func(ev sseEvent) bool {
		_, current := selectAssistantMessage(ev.Data, userTask)
		if strings.TrimSpace(current) == "" {
			return true
		}
		finalAssistantText = current

		if len(tools) > 0 && !toolMode && looksLikeToolCallCandidate(current) {
			toolMode = true
			sawToolCandidate = true
		}
		if toolMode {
			if tc, ok := tryParseToolCall(current); ok {
				callID := "call_" + uuid.NewString()
				if item, ok := buildToolCallItem(callID, tc, tools); ok {
					toolItem = item
					toolEmitted = true
					upCancel()
					return false
				}
			}
			return true
		}
		return true
	})

	if toolEmitted && toolItem != nil {
		out, err := buildNonStreamingResponse(responseID, model, []any{toolItem})
		if err != nil {
			return clipexec.Response{}, err
		}
		return clipexec.Response{Payload: out}, nil
	}

	// 兜底解析：上游可能会把 JSON 包在额外文本中。
	if len(tools) > 0 {
		if tc, ok := tryParseToolCall(finalAssistantText); ok {
			callID := "call_" + uuid.NewString()
			if item, ok := buildToolCallItem(callID, tc, tools); ok {
				out, err := buildNonStreamingResponse(responseID, model, []any{item})
				if err != nil {
					return clipexec.Response{}, err
				}
				return clipexec.Response{Payload: out}, nil
			}
		}
	}

	if !sawToolCandidate && looksLikeToolCallCandidate(finalAssistantText) {
		sawToolCandidate = true
	}
	if sawToolCandidate && len(tools) > 0 && ctx.Err() == nil {
		if item, ok := e.repairToolCall(ctx, a, accessToken, st.conversationID, payload, tools); ok {
			out, err := buildNonStreamingResponse(responseID, model, []any{item})
			if err != nil {
				return clipexec.Response{}, err
			}
			return clipexec.Response{Payload: out}, nil
		}
	}

	item := buildAssistantMessageItem(finalAssistantText)
	out, err := buildNonStreamingResponse(responseID, model, []any{item})
	if err != nil {
		return clipexec.Response{}, err
	}
	return clipexec.Response{Payload: out}, nil
}

func (e *Executor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (*clipexec.StreamResult, error) {
	ch := make(chan clipexec.StreamChunk, 64)

	go func() {
		defer close(ch)

		rawReq := opts.OriginalRequest
		if len(rawReq) == 0 {
			rawReq = req.Payload
		}

		model := strings.TrimSpace(gjson.GetBytes(rawReq, "model").String())
		if model == "" {
			model = req.Model
		}
		preset := resolveModelPreset(model)
		model = preset.ModelID
		responseID := "resp_" + uuid.NewString()

		// 定期清理会话（尽力而为）。
		if e.gcTick.Add(1)%128 == 0 {
			e.sessions.gc()
		}

		created, err := buildResponseCreatedEvent(responseID, model)
		if err == nil {
			ch <- clipexec.StreamChunk{Payload: created}
		}

		ai, accessToken, err := e.ensureAccessToken(ctx, a)
		if err != nil {
			e.emitFailed(ch, responseID, "unauthorized", err.Error())
			return
		}

		sessionKey := strings.TrimSpace(gjson.GetBytes(rawReq, "prompt_cache_key").String())
		if sessionKey == "" {
			sessionKey = responseID
		}

		st, end, err := e.sessions.tryStart(sessionKey)
		if err != nil {
			e.emitFailed(ch, responseID, "conflict", err.Error())
			return
		}
		defer end()

		inputVal := gjson.GetBytes(rawReq, "input")
		instructions := gjson.GetBytes(rawReq, "instructions").String()
		reasoningEffort := gjson.GetBytes(rawReq, "reasoning.effort").String()
		if strings.TrimSpace(reasoningEffort) == "" {
			reasoningEffort = preset.DefaultReasoningEffort
		}
		verbosity := gjson.GetBytes(rawReq, "text.verbosity").String()
		toolChoice := strings.TrimSpace(gjson.GetBytes(rawReq, "tool_choice").String())

		tools := parseOpenAITools(rawReq)
		if strings.EqualFold(toolChoice, "none") {
			tools = nil
		}

		var newItems []gjson.Result
		if inputVal.IsArray() {
			allItems := inputVal.Array()
			if len(allItems) < st.processedInputLen {
				// 客户端可能重置了对话状态。
				st.processedInputLen = 0
				st.conversationID = ""
			}
			if st.processedInputLen < len(allItems) {
				newItems = allItems[st.processedInputLen:]
			} else {
				newItems = nil
			}
			// 提前更新已处理长度，避免缓存无限增长。
			st.processedInputLen = len(allItems)
		} else if inputVal.Type == gjson.String {
			// 无历史模式。
			newItems = []gjson.Result{
				gjson.Result{Type: gjson.String, Str: inputVal.String()},
			}
		}

		turn := extractTurnData(newItems)
		userTask := strings.TrimSpace(turn.UserTaskText)
		if userTask == "" {
			if len(turn.ToolOutputs) > 0 {
				userTask = "Continue."
			} else if len(turn.ImageURLs) > 0 {
				userTask = "Analyze the provided image(s) and follow the user's intent."
			} else if inputVal.Type == gjson.String {
				userTask = strings.TrimSpace(inputVal.String())
			} else {
				userTask = "Continue."
			}
		}

		webEnabled := true
		if v := gjson.GetBytes(rawReq, "metadata.web_enabled"); v.Exists() {
			webEnabled = v.Bool()
		}

		ocrResults := e.buildOCRResults(ctx, a, turn.ImageURLs)

		payload := m365ChatOverStreamRequest{
			Message:      m365RequestMessage{Text: userTask},
			LocationHint: m365LocationHint{TimeZone: ai.TimeZone},
			ContextualResource: &m365ContextualResource{
				WebContext: &m365WebContext{IsWebEnabled: webEnabled},
			},
		}

		payload.AdditionalContext = buildAdditionalContext(instructions, reasoningEffort, verbosity, tools, turn.ToolOutputs, ocrResults)

		if strings.TrimSpace(st.conversationID) == "" {
			convID, err := e.createConversation(ctx, a, accessToken)
			if err != nil {
				e.emitFailed(ch, responseID, "upstream_error", err.Error())
				return
			}
			st.conversationID = convID
		}

		upCtx, upCancel := withTimeoutIfNone(ctx, 300*time.Second)
		defer upCancel()

		resp, err := e.chatOverStream(upCtx, a, accessToken, st.conversationID, payload)
		if err != nil {
			e.emitFailed(ch, responseID, "upstream_error", err.Error())
			return
		}
		defer resp.Body.Close()

		lastTextByMsgID := make(map[string]string, 4)
		var finalAssistantText string
		toolEmitted := false
		toolMode := false
		sawToolCandidate := false

		_ = readSSEStream(upCtx, resp.Body, func(ev sseEvent) bool {
			msgID, current := selectAssistantMessage(ev.Data, userTask)
			if strings.TrimSpace(current) == "" {
				return true
			}
			finalAssistantText = current

			if len(tools) > 0 && !toolMode && looksLikeToolCallCandidate(current) {
				toolMode = true
				sawToolCandidate = true
			}
			if toolMode {
				if tc, ok := tryParseToolCall(current); ok {
					callID := "call_" + uuid.NewString()
					if item, ok := buildToolCallItem(callID, tc, tools); ok {
						if outEv, err := buildResponseOutputItemDoneEvent(item); err == nil {
							ch <- clipexec.StreamChunk{Payload: outEv}
						}
						if done, err := buildResponseCompletedEvent(responseID); err == nil {
							ch <- clipexec.StreamChunk{Payload: done}
						}
						toolEmitted = true
						upCancel()
						return false
					}
				}
				return true
			}

			msgID = strings.TrimSpace(msgID)
			if msgID == "" {
				msgID = "unknown"
			}
			last := lastTextByMsgID[msgID]
			delta := current
			if strings.HasPrefix(current, last) {
				delta = current[len(last):]
			} else if last != "" {
				delta = "\n" + current
			}
			lastTextByMsgID[msgID] = current
			delta = strings.TrimSpace(delta)
			if delta == "" {
				return true
			}
			if outEv, err := buildResponseOutputTextDeltaEvent(delta); err == nil {
				ch <- clipexec.StreamChunk{Payload: outEv}
			}
			return true
		})

		if toolEmitted {
			return
		}

		// 兜底解析：上游可能会把 JSON 包在额外文本中。
		if len(tools) > 0 {
			if tc, ok := tryParseToolCall(finalAssistantText); ok {
				callID := "call_" + uuid.NewString()
				if item, ok := buildToolCallItem(callID, tc, tools); ok {
					if outEv, err := buildResponseOutputItemDoneEvent(item); err == nil {
						ch <- clipexec.StreamChunk{Payload: outEv}
					}
					if done, err := buildResponseCompletedEvent(responseID); err == nil {
						ch <- clipexec.StreamChunk{Payload: done}
					}
					return
				}
			}
		}

		if !sawToolCandidate && looksLikeToolCallCandidate(finalAssistantText) {
			sawToolCandidate = true
		}
		if sawToolCandidate && len(tools) > 0 && ctx.Err() == nil {
			if item, ok := e.repairToolCall(ctx, a, accessToken, st.conversationID, payload, tools); ok {
				if outEv, err := buildResponseOutputItemDoneEvent(item); err == nil {
					ch <- clipexec.StreamChunk{Payload: outEv}
				}
				if done, err := buildResponseCompletedEvent(responseID); err == nil {
					ch <- clipexec.StreamChunk{Payload: done}
				}
				return
			}
		}

		if strings.TrimSpace(finalAssistantText) == "" {
			finalAssistantText = ""
		}

		item := buildAssistantMessageItem(finalAssistantText)
		if outEv, err := buildResponseOutputItemDoneEvent(item); err == nil {
			ch <- clipexec.StreamChunk{Payload: outEv}
		}
		if done, err := buildResponseCompletedEvent(responseID); err == nil {
			ch <- clipexec.StreamChunk{Payload: done}
		}
	}()

	return &clipexec.StreamResult{Chunks: ch}, nil
}

func (e *Executor) Refresh(ctx context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	_, _, err := e.ensureAccessToken(ctx, a)
	if err != nil {
		return a, err
	}
	return a, nil
}

func (e *Executor) CountTokens(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	return clipexec.Response{}, errors.New("m365 executor: token counting is not implemented")
}

func (e *Executor) HttpRequest(ctx context.Context, a *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("m365 executor: request is nil")
	}
	_, accessToken, err := e.ensureAccessToken(ctx, a)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Header.Get("Authorization")) == "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	client := e.httpClientForAuth(a)
	return client.Do(req.WithContext(ctx))
}

func (e *Executor) emitFailed(ch chan<- clipexec.StreamChunk, responseID, code, msg string) {
	if ch == nil {
		return
	}
	ev, err := buildResponseFailedEvent(responseID, code, msg)
	if err != nil {
		alt := map[string]any{"type": "response.failed", "response": map[string]any{"id": responseID, "error": map[string]any{"code": code, "message": msg}}}
		raw, _ := json.Marshal(alt)
		ch <- clipexec.StreamChunk{Payload: []byte(fmt.Sprintf("data: %s\n\n", raw))}
		return
	}
	ch <- clipexec.StreamChunk{Payload: ev}
}

func selectAssistantMessage(conversationJSON []byte, userTask string) (string, string) {
	msgs := gjson.GetBytes(conversationJSON, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return "", ""
	}

	userTask = strings.TrimSpace(userTask)
	var candidateID string
	var candidateText string

	// 优先选择最后一条非空消息，并尽量避免回显 userTask。
	for _, m := range msgs.Array() {
		txt := strings.TrimSpace(m.Get("text").String())
		if txt == "" {
			continue
		}
		if userTask != "" && strings.TrimSpace(txt) == userTask {
			continue
		}
		candidateText = txt
		candidateID = strings.TrimSpace(m.Get("id").String())
	}
	if candidateText != "" {
		return candidateID, candidateText
	}

	// 兜底：选择最后一条非空消息。
	for _, m := range msgs.Array() {
		txt := strings.TrimSpace(m.Get("text").String())
		if txt == "" {
			continue
		}
		candidateText = txt
		candidateID = strings.TrimSpace(m.Get("id").String())
	}
	return candidateID, candidateText
}

const (
	maxFunctionArgumentsChars  = 32 * 1024
	maxCustomToolInputChars    = 256 * 1024
	maxLocalShellCommandChars  = 8 * 1024
	maxLocalShellWorkdirChars  = 1024
	maxLocalShellTimeoutMillis = int64(30 * 60 * 1000)
)

func buildToolCallItem(callID string, tc *toolCall, tools []openAITool) (any, bool) {
	if tc == nil {
		return nil, false
	}

	toolName := strings.TrimSpace(tc.Tool)
	if toolName == "" {
		return nil, false
	}

	toolType := ""
	for _, t := range tools {
		if strings.EqualFold(t.Name, toolName) {
			toolType = t.ToolType
			toolName = t.Name
			break
		}
	}
	if toolType == "" {
		return nil, false
	}

	switch toolType {
	case "local_shell":
		cmd, wd, timeout, ok := parseLocalShellArgs(tc.Arguments)
		if !ok {
			return nil, false
		}
		// 基础安全限制（尽力而为；Codex 侧的审批机制仍然生效）。
		total := 0
		for _, p := range cmd {
			total += len(p)
		}
		if total <= 0 || total > maxLocalShellCommandChars {
			return nil, false
		}
		if len(wd) > maxLocalShellWorkdirChars {
			return nil, false
		}
		if timeout > maxLocalShellTimeoutMillis {
			timeout = maxLocalShellTimeoutMillis
		}
		return buildLocalShellCallItem(callID, cmd, wd, timeout), true
	case "custom":
		input := strings.TrimSpace(tc.Input)
		if input == "" {
			return nil, false
		}
		if len(input) > maxCustomToolInputChars {
			return nil, false
		}
		return buildCustomToolCallItem(callID, toolName, input), true
	case "function":
		argsStr, ok := jsonString(tc.Arguments)
		if !ok {
			return nil, false
		}
		if len(argsStr) > maxFunctionArgumentsChars {
			return nil, false
		}
		return buildFunctionCallItem(callID, toolName, argsStr), true
	default:
		return nil, false
	}
}

func parseLocalShellArgs(v any) ([]string, string, int64, bool) {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return nil, "", 0, false
	}
	var cmd []string
	switch c := m["command"].(type) {
	case []any:
		for _, it := range c {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				cmd = append(cmd, s)
			}
		}
	case []string:
		for _, s := range c {
			if strings.TrimSpace(s) != "" {
				cmd = append(cmd, s)
			}
		}
	case string:
		cmd = strings.Fields(c)
	}
	if len(cmd) == 0 {
		return nil, "", 0, false
	}

	wd, _ := m["working_directory"].(string)
	if wd == "" {
		wd, _ = m["workdir"].(string)
	}
	timeout := anyInt64(m["timeout_ms"])
	return cmd, strings.TrimSpace(wd), timeout, true
}
