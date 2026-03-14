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

func buildUpstreamPayload(userTask, timeZone string, webEnabled bool, instructions, verbosity string, tools []openAITool, toolOutputs []string) m365ChatOverStreamRequest {
	payload := m365ChatOverStreamRequest{
		Message:      m365RequestMessage{Text: userTask},
		LocationHint: m365LocationHint{TimeZone: timeZone},
		ContextualResource: &m365ContextualResource{
			WebContext: &m365WebContext{IsWebEnabled: webEnabled},
		},
	}
	payload.AdditionalContext = buildAdditionalContext(instructions, verbosity, tools, toolOutputs)
	return payload
}

type preparedTurnState struct {
	rawReq       []byte
	model        string
	responseID   string
	inputVal     gjson.Result
	instructions string
	verbosity    string
	tools        []openAITool
	turn         turnExtract
	webEnabled   bool
	st           *sessionState
	end          func()
}

func (e *Executor) prepareTurnState(req clipexec.Request, opts clipexec.Options) (preparedTurnState, error) {
	if e == nil {
		return preparedTurnState{}, errors.New("m365 executor: executor is nil")
	}
	if e.sessions == nil {
		e.sessions = newSessionStore(30 * time.Minute)
	}

	rawReq := opts.OriginalRequest
	if len(rawReq) == 0 {
		rawReq = req.Payload
	}

	model := strings.TrimSpace(gjson.GetBytes(rawReq, "model").String())
	if model == "" {
		model = req.Model
	}
	model = normalizeModelLabel(model)
	responseID := "resp_" + uuid.NewString()

	// 定期清理会话（尽力而为）。
	if e.gcTick.Add(1)%128 == 0 {
		e.sessions.gc()
	}

	sessionKey := strings.TrimSpace(gjson.GetBytes(rawReq, "prompt_cache_key").String())
	if sessionKey == "" {
		sessionKey = responseID
	}

	st, end, err := e.sessions.tryStart(sessionKey)
	if err != nil {
		return preparedTurnState{}, err
	}

	releaseNeeded := true
	defer func() {
		if releaseNeeded && end != nil {
			end()
		}
	}()

	inputVal := gjson.GetBytes(rawReq, "input")
	instructions := gjson.GetBytes(rawReq, "instructions").String()
	verbosity := gjson.GetBytes(rawReq, "text.verbosity").String()
	toolChoice := strings.TrimSpace(gjson.GetBytes(rawReq, "tool_choice").String())

	tools := parseOpenAITools(rawReq)
	if strings.EqualFold(toolChoice, "none") {
		tools = nil
	}

	turn, nextProcessedLen, resetConversation := extractPendingTurn(inputVal, st)
	if err := validateNoImageInputs(turn.ImageURLs); err != nil {
		return preparedTurnState{}, err
	}

	if resetConversation {
		st.processedInputLen = 0
		st.conversationID = ""
	}
	if nextProcessedLen >= 0 {
		st.processedInputLen = nextProcessedLen
	}

	webEnabled := true
	if v := gjson.GetBytes(rawReq, "metadata.web_enabled"); v.Exists() {
		webEnabled = v.Bool()
	}

	releaseNeeded = false
	return preparedTurnState{
		rawReq:       rawReq,
		model:        model,
		responseID:   responseID,
		inputVal:     inputVal,
		instructions: instructions,
		verbosity:    verbosity,
		tools:        tools,
		turn:         turn,
		webEnabled:   webEnabled,
		st:           st,
		end:          end,
	}, nil
}

func resolveUserTask(turn turnExtract, inputVal gjson.Result) string {
	userTask := strings.TrimSpace(turn.UserTaskText)
	if userTask != "" {
		return userTask
	}
	if len(turn.ToolOutputs) > 0 {
		return "Continue."
	}
	if len(turn.ImageURLs) > 0 {
		return "Analyze the provided image(s) and follow the user's intent."
	}
	if inputVal.Type == gjson.String {
		return strings.TrimSpace(inputVal.String())
	}
	return "Continue."
}

func (e *Executor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	rawReq := opts.OriginalRequest
	if len(rawReq) == 0 {
		rawReq = req.Payload
	}
	if err := validateNoImageInput(rawReq); err != nil {
		return clipexec.Response{}, err
	}

	state, err := e.prepareTurnState(req, opts)
	if err != nil {
		return clipexec.Response{}, err
	}
	defer state.end()

	ai, accessToken, err := e.ensureAccessToken(ctx, a)
	if err != nil {
		return clipexec.Response{}, err
	}

	userTask := resolveUserTask(state.turn, state.inputVal)
	payload := buildUpstreamPayload(userTask, ai.TimeZone, state.webEnabled, state.instructions, state.verbosity, state.tools, state.turn.ToolOutputs)

	if strings.TrimSpace(state.st.conversationID) == "" {
		convID, err := e.createConversation(ctx, a, accessToken)
		if err != nil {
			return clipexec.Response{}, err
		}
		state.st.conversationID = convID
	}

	upCtx, upCancel := withTimeoutIfNone(ctx, 300*time.Second)
	defer upCancel()

	resp, err := e.chatOverStream(upCtx, a, accessToken, state.st.conversationID, payload)
	if err != nil {
		return clipexec.Response{}, err
	}
	defer resp.Body.Close()

	var finalAssistantText string
	sawToolCandidate := false
	toolEmitted := false
	var toolItems []any

	_ = readSSEStream(upCtx, resp.Body, func(ev sseEvent) bool {
		_, current := selectAssistantMessage(ev.Data, userTask)
		if strings.TrimSpace(current) == "" {
			return true
		}
		finalAssistantText = current

		if len(state.tools) > 0 && looksLikeToolCallCandidate(current) {
			sawToolCandidate = true
		}
		if len(state.tools) > 0 {
			if items := parseToolCallItems(current, state.tools); len(items) > 0 {
				toolItems = items
				toolEmitted = true
				upCancel()
				return false
			}
		}
		return true
	})

	if toolEmitted && len(toolItems) > 0 {
		out, err := buildNonStreamingResponse(state.responseID, state.model, toolItems)
		if err != nil {
			return clipexec.Response{}, err
		}
		return clipexec.Response{Payload: out}, nil
	}

	// 兜底解析：上游可能会把 JSON 包在额外文本中。
	if len(state.tools) > 0 {
		if items := parseToolCallItems(finalAssistantText, state.tools); len(items) > 0 {
			out, err := buildNonStreamingResponse(state.responseID, state.model, items)
			if err != nil {
				return clipexec.Response{}, err
			}
			return clipexec.Response{Payload: out}, nil
		}
	}

	if !sawToolCandidate && looksLikeToolCallCandidate(finalAssistantText) {
		sawToolCandidate = true
	}
	forceRepair := sawToolCandidate || shouldForceToolRepair(userTask, finalAssistantText, state.tools)
	if forceRepair && len(state.tools) > 0 && ctx.Err() == nil {
		if items, ok := e.repairToolCall(ctx, a, accessToken, state.st.conversationID, payload, state.tools); ok {
			out, err := buildNonStreamingResponse(state.responseID, state.model, items)
			if err != nil {
				return clipexec.Response{}, err
			}
			return clipexec.Response{Payload: out}, nil
		}
	}

	item := buildAssistantMessageItem(sanitizeAssistantTextForDisplay(finalAssistantText))
	out, err := buildNonStreamingResponse(state.responseID, state.model, []any{item})
	if err != nil {
		return clipexec.Response{}, err
	}
	return clipexec.Response{Payload: out}, nil
}

func (e *Executor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (*clipexec.StreamResult, error) {
	rawReq := opts.OriginalRequest
	if len(rawReq) == 0 {
		rawReq = req.Payload
	}
	if err := validateNoImageInput(rawReq); err != nil {
		return nil, err
	}

	state, err := e.prepareTurnState(req, opts)
	if err != nil {
		return nil, err
	}

	ch := make(chan clipexec.StreamChunk, 64)

	go func() {
		defer close(ch)
		defer state.end()

		ai, accessToken, err := e.ensureAccessToken(ctx, a)
		if err != nil {
			e.emitFailed(ch, state.responseID, "unauthorized", err.Error())
			return
		}

		created, err := buildResponseCreatedEvent(state.responseID, state.model)
		if err == nil {
			ch <- clipexec.StreamChunk{Payload: created}
		}

		userTask := resolveUserTask(state.turn, state.inputVal)
		payload := buildUpstreamPayload(userTask, ai.TimeZone, state.webEnabled, state.instructions, state.verbosity, state.tools, state.turn.ToolOutputs)

		if strings.TrimSpace(state.st.conversationID) == "" {
			convID, err := e.createConversation(ctx, a, accessToken)
			if err != nil {
				e.emitFailed(ch, state.responseID, "upstream_error", err.Error())
				return
			}
			state.st.conversationID = convID
		}

		upCtx, upCancel := withTimeoutIfNone(ctx, 300*time.Second)
		defer upCancel()

		resp, err := e.chatOverStream(upCtx, a, accessToken, state.st.conversationID, payload)
		if err != nil {
			e.emitFailed(ch, state.responseID, "upstream_error", err.Error())
			return
		}
		defer resp.Body.Close()

		lastTextByMsgID := make(map[string]string, 4)
		var finalAssistantText string
		toolEmitted := false
		sawToolCandidate := false

		_ = readSSEStream(upCtx, resp.Body, func(ev sseEvent) bool {
			msgID, current := selectAssistantMessage(ev.Data, userTask)
			if strings.TrimSpace(current) == "" {
				return true
			}
			finalAssistantText = current

			visibleCurrent := current
			if len(state.tools) > 0 {
				if looksLikeToolCallCandidate(current) {
					sawToolCandidate = true
				}
				if items := parseToolCallItems(current, state.tools); len(items) > 0 {
					for _, item := range items {
						if outEv, err := buildResponseOutputItemDoneEvent(item); err == nil {
							ch <- clipexec.StreamChunk{Payload: outEv}
						}
					}
					if done, err := buildResponseCompletedEvent(state.responseID); err == nil {
						ch <- clipexec.StreamChunk{Payload: done}
					}
					toolEmitted = true
					upCancel()
					return false
				}
				visibleCurrent = sanitizeAssistantTextForDisplay(current)
			}

			msgID = strings.TrimSpace(msgID)
			if msgID == "" {
				msgID = "unknown"
			}
			last := lastTextByMsgID[msgID]
			delta := visibleCurrent
			if strings.HasPrefix(visibleCurrent, last) {
				delta = visibleCurrent[len(last):]
			} else if last != "" {
				delta = "\n" + visibleCurrent
			}
			lastTextByMsgID[msgID] = visibleCurrent
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

		// 兜底解析：上游可能会把 JSON 或定界符块包在额外文本中。
		if len(state.tools) > 0 {
			if items := parseToolCallItems(finalAssistantText, state.tools); len(items) > 0 {
				for _, item := range items {
					if outEv, err := buildResponseOutputItemDoneEvent(item); err == nil {
						ch <- clipexec.StreamChunk{Payload: outEv}
					}
				}
				if done, err := buildResponseCompletedEvent(state.responseID); err == nil {
					ch <- clipexec.StreamChunk{Payload: done}
				}
				return
			}
		}

		if !sawToolCandidate && looksLikeToolCallCandidate(finalAssistantText) {
			sawToolCandidate = true
		}
		forceRepair := sawToolCandidate || shouldForceToolRepair(userTask, finalAssistantText, state.tools)
		if forceRepair && len(state.tools) > 0 && ctx.Err() == nil {
			if items, ok := e.repairToolCall(ctx, a, accessToken, state.st.conversationID, payload, state.tools); ok {
				for _, item := range items {
					if outEv, err := buildResponseOutputItemDoneEvent(item); err == nil {
						ch <- clipexec.StreamChunk{Payload: outEv}
					}
				}
				if done, err := buildResponseCompletedEvent(state.responseID); err == nil {
					ch <- clipexec.StreamChunk{Payload: done}
				}
				return
			}
		}

		if strings.TrimSpace(finalAssistantText) == "" {
			finalAssistantText = ""
		}

		item := buildAssistantMessageItem(sanitizeAssistantTextForDisplay(finalAssistantText))
		if outEv, err := buildResponseOutputItemDoneEvent(item); err == nil {
			ch <- clipexec.StreamChunk{Payload: outEv}
		}
		if done, err := buildResponseCompletedEvent(state.responseID); err == nil {
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

	// 优先选择“明确是 assistant”且非空的消息，并尽量避免回显 userTask。
	for _, m := range msgs.Array() {
		if !isAssistantLikeMessage(m) {
			continue
		}
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
		if userTask != "" && strings.TrimSpace(txt) == userTask {
			continue
		}
		candidateText = txt
		candidateID = strings.TrimSpace(m.Get("id").String())
	}
	return candidateID, candidateText
}

func isAssistantLikeMessage(m gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(m.Get("role").String()))
	if role == "" {
		role = strings.ToLower(strings.TrimSpace(m.Get("author.role").String()))
	}
	if role == "" {
		role = strings.ToLower(strings.TrimSpace(m.Get("from.role").String()))
	}
	if role == "" {
		return false
	}
	switch role {
	case "assistant", "model", "copilot":
		return true
	case "user", "system", "tool":
		return false
	default:
		return false
	}
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
		argsStr, ok := normalizeFunctionArguments(tc.Arguments)
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

func parseToolCallItems(text string, tools []openAITool) []any {
	if len(tools) == 0 {
		return nil
	}
	calls, ok := parseToolCalls(text)
	if !ok || len(calls) == 0 {
		return nil
	}
	items := make([]any, 0, len(calls))
	for _, tc := range calls {
		if tc == nil {
			continue
		}
		callID := "call_" + uuid.NewString()
		if item, ok := buildToolCallItem(callID, tc, tools); ok {
			items = append(items, item)
		}
	}
	return items
}

func sanitizeAssistantTextForDisplay(text string) string {
	if text == "" {
		return ""
	}
	if idx := strings.Index(text, toolCallV2Start); idx >= 0 {
		return strings.TrimSpace(text[:idx])
	}
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "```") {
		return ""
	}
	return text
}

func normalizeFunctionArguments(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" || !json.Valid([]byte(s)) {
			return "", false
		}
		return s, true
	default:
		return jsonString(v)
	}
}

func shouldForceToolRepair(userTask, assistantText string, tools []openAITool) bool {
	if len(tools) == 0 {
		return false
	}
	task := strings.TrimSpace(strings.ToLower(userTask))
	if task == "" || task == "continue" || task == "continue." {
		return false
	}

	// 当助手已经明确拒绝或说明无法执行时，不强制触发修复回合，避免死循环。
	reply := strings.TrimSpace(strings.ToLower(assistantText))
	if strings.Contains(reply, "can't") || strings.Contains(reply, "cannot") ||
		strings.Contains(reply, "无法") || strings.Contains(reply, "不能") {
		return false
	}

	// 针对“有副作用”的任务做强制修复：创建/修改文件、执行命令等。
	keywords := []string{
		"create file", "create a file", "write file", "write to", "append to",
		"modify file", "edit file", "delete file", "rename file",
		"run command", "execute command", "shell command", "apply patch",
		"touch ", "mkdir ", "rm ", "mv ", "cp ", "chmod ", "chown ",
		"创建文件", "新建文件", "写入", "追加", "修改文件", "编辑文件",
		"删除文件", "重命名", "执行命令", "运行命令", "应用补丁",
	}
	for _, kw := range keywords {
		if strings.Contains(task, kw) {
			return true
		}
	}
	return false
}

func parseLocalShellArgs(v any) ([]string, string, int64, bool) {
	m, ok := asMap(v)
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
		if s, ok := m["cmd"].(string); ok && strings.TrimSpace(s) != "" {
			cmd = strings.Fields(s)
		}
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

func asMap(v any) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	case string:
		raw := strings.TrimSpace(x)
		if raw == "" || !json.Valid([]byte(raw)) {
			return nil, false
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, false
		}
		return out, true
	default:
		return nil, false
	}
}
