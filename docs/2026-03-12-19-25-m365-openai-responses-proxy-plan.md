# M365 Copilot Chat → OpenAI Responses 兼容代理（供 Codex CLI 调用）项目方案

更新时间：2026-03-12


## 1. 背景与目标

本项目目标是把 **Microsoft 365 Copilot Chat API（Microsoft Graph beta）** 包装成一个 **OpenAI Responses API（`/v1/responses`）兼容** 的本地/自托管服务，使 **Codex CLI** 可以将其作为“模型提供方（model provider）”来使用。

> 参考：本仓库 `docs/m365.md` 已整理 M365 Copilot Chat 的会话创建、同步聊天与流式端点、权限与前置条件。


### 1.1 核心目标（必须达成）

1. **OpenAI Responses 兼容**：实现 Codex CLI 实际会调用的 OpenAI 风格 HTTP 端点，至少包含：
   - `POST /v1/responses`（SSE 流式）
   - `POST /v1/responses/compact`（可先做最小实现）
   - `GET /v1/models`（模型列表，用于生态兼容；Codex CLI 在 API Key 模式下可能不强依赖，但建议提供）
2. **Codex CLI 可用**：保证 Codex CLI 在指向本代理后能够：
   - 正常流式输出（SSE）
   - 正常发起并处理工具调用（function/custom/local_shell 等）
   - 正常处理图片输入（至少不报错，并提供可解释的降级行为）
   - 兼容接收模型选择与“思考等级”（`reasoning.effort`）参数，但它们不会改变微软上游请求行为
3. **上游为 M365 Copilot Chat API**：代理内部通过 Graph beta 的 Copilot Chat API 完成对话与流式生成（`chatOverStream` 优先）。


### 1.2 非目标（明确不做，或后置）

1. 不承诺 M365 Copilot Chat API 的 **稳定性与生产可用性**（该 API 为 beta，官方提示不可用于生产）。
2. 不承诺 100% 复刻 OpenAI 所有 Responses API 字段与事件类型；以 **Codex CLI 与常见 OpenAI SDK 实际使用路径** 为主。
3. 不在第一阶段实现所有高级内置工具（例如 OpenAI 托管的 `web_search` / `file_search` / `computer_use`）。这些能力如需支持，优先走“代理侧工具实现”或“后续路由到其他模型提供方”。


## 2. 上游 M365 Copilot Chat API 关键约束（来自 docs/m365.md）

### 2.1 会话与端点

M365 Copilot Chat API 没有单一 `/chat` 端点，而是“两步”：

1. 创建会话：
   - `POST https://graph.microsoft.com/beta/copilot/conversations`
   - 请求体：空 JSON `{}`（不要传 `null`）
   - 返回：`201 Created`，响应体 `id` 作为 `conversationId`
   - 兼容性注意：文档示例中可能出现 `status: active`，而资源类型定义为 `state` 枚举；实现端需兼容 `state/status`
2. 发送消息：
   - 同步：`POST https://graph.microsoft.com/beta/copilot/conversations/{conversationId}/chat`
   - 流式：`POST https://graph.microsoft.com/beta/copilot/conversations/{conversationId}/chatOverStream`（`text/event-stream`）


### 2.2 权限与鉴权形态

重点约束：

- **仅支持 Delegated（委派）权限**，且仅工作/学校账号。
- 调用用户必须具备 **Microsoft 365 Copilot 许可证**。
- 必须带 `locationHint.timeZone`（影响时间类问题）。

这决定了本项目更适合：

- **本地代理**（用户在本机登录一次，后续自动刷新 token）
- 或内网部署 + 明确的用户授权与 token 托管策略（复杂度更高）


### 2.3 官方已知限制（对本项目的含义）

根据微软官方 “Chat API 概述（预览）” 的已知限制，本项目在设计与实现上必须**显式承认并应对**以下边界（否则会出现预期偏差）：

1. **不支持 actions / 内容生成类技能**
   - 含义：M365 Chat API 不能像 Agent 那样原生执行 “创建文件/修改代码/运行命令/发邮件/建会议” 等动作。
   - 对策：本项目的 “工具调用（exec/apply_patch 等）” 必须是**代理侧编排**：通过提示词协议让上游在文本里输出结构化指令，再由 Codex CLI/代理执行。
2. **只支持文本响应**
   - 含义：上游不会像 OpenAI Responses 一样输出原生 `function_call`/`tool_call` 结构，也不保证支持图片输入；输出以 `copilotConversationResponseMessage.text` 为主。
   - 对策：多模态（图片）必须走降级策略（OCR/描述）或外部视觉能力；工具调用必须走“文本协议”。
3. **不支持 Code Interpreter、图形艺术等工具**
   - 含义：不能期望 M365 端提供代码执行或图片生成等能力。
   - 对策：若 Codex 侧启用这些工具，需在代理层做禁用/替代路由/明确报错。
4. **不支持长时间运行任务（可能网关超时）**
   - 含义：把“长时间执行 + 多轮工具循环”全部压到单次 M365 `chatOverStream` 里会不稳定。
   - 对策：控制每轮 M365 请求的工作量与超时；必要时将任务拆成多轮（多次 `chatOverStream`）。
5. **Web grounding 开关是“单轮”**
   - 含义：如需关闭 web grounding，需要在**每一轮**请求的 `contextualResources.webContext.isWebEnabled` 里显式关闭；否则默认可能启用。
   - 对策：在 OpenAI 工具/配置层与 M365 `webContext` 之间建立稳定映射（见第 7 节）。


### 2.4 微软官方文档（建议作为实现验收依据）

```text
Chat API overview (Preview)
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/overview

Create conversation
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/copilotroot-post-conversations

Continue synchronous conversations (chat)
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/copilotconversation-chat

Continue streamed conversations (chatOverStream)
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/copilotconversation-chatoverstream

Resource types (key ones used in this project)
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/resources/copilotconversation
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/resources/copilotconversationlocation
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/resources/copilotconversationrequestmessageparameter
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/resources/copilotconversationresponsemessage
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/resources/copilotcontextmessage
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/resources/copilotcontextualresources
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/resources/copilotfile
https://learn.microsoft.com/en-us/microsoft-365-copilot/extensibility/api/ai-services/chat/resources/copilotwebcontext
```


## 3. Codex CLI 兼容性需求（基于 openai/codex 源码核对）

本节不是复述文档，而是给出“真实实现必须对齐的接口行为”。结论来自对 `openai/codex` 仓库中 Responses 客户端与 SSE 解析器的代码核对（例如 `codex-rs/codex-api`、`codex-rs/core`、`codex-rs/protocol`）。


### 3.1 Codex CLI 请求侧（`POST /v1/responses`）关键字段

Codex CLI 发送的 Responses 请求体在其 Rust 类型中（示意字段）包含：

- `model`：模型名字符串（由用户选择或默认）
- `instructions`：一段系统级指令字符串（base instructions）
- `input`：`array`，内部 item 类型多样（message/tool output 等）
- `tools`：`array`，包含 function tool、custom tool、local_shell、web_search 等
- `tool_choice`：常见为 `"auto"`
- `parallel_tool_calls`：布尔
- `reasoning`：可选对象（`effort`、`summary`）
- `text`：可选对象（`verbosity`、`format: json_schema`）
- `prompt_cache_key`：可选字符串（Codex 用其 conversation id 填充，用于缓存/归因）
- `include`：数组（例如请求 `reasoning.encrypted_content`）
- `stream`：布尔（Codex 基本走 SSE 流式）
- `service_tier`：可选（例如 `"priority"`）

兼容策略：代理服务 **必须容忍并解析这些字段**；即便上游 M365 无同构能力，也应做到“可忽略但不报错”。


### 3.2 Codex CLI 流式解析侧：必须支持的 SSE 事件集合

Codex 的 SSE 解析器会把 `data:` 的 JSON 解析为对象，并主要关注这些 `type`：

- `response.created`
- `response.output_text.delta`
- `response.output_item.added`（可选但建议）
- `response.output_item.done`（关键：工具调用与最终 message 通常在这里）
- `response.reasoning_summary_text.delta`（可选）
- `response.reasoning_text.delta`（可选）
- `response.reasoning_summary_part.added`（可选）
- `response.completed`（必须；用于结束流与 token usage）
- `response.failed` / `response.incomplete`（错误与中断）

最小可用：`response.created` → 多个 `response.output_text.delta` → `response.completed`。

对 Codex CLI 的“代理/工具回合”能力来说，`response.output_item.done` 必须可用，因为工具调用 item（例如 `function_call`、`custom_tool_call`、`local_shell_call`）会以该事件承载。


### 3.3 Codex CLI 工具调用：必须理解/生成的 item 类型

Codex CLI 的 Responses `item`（即 `response.output_item.done` 中的 `item` 字段）常见类型包括：

- `message`：assistant 的文本输出（`content` 内含 `output_text`）
- `function_call`：函数工具调用（`name` + `arguments` 字符串 + `call_id`）
- `function_call_output`：客户端执行函数后回传的输出（在下一次请求的 `input[]` 中出现）
- `custom_tool_call`：自定义工具调用（例如 `apply_patch` freeform）
- `custom_tool_call_output`：自定义工具输出回传
- `local_shell_call`：本地 shell 调用（Codex 在某些模式下会声明 `local_shell` 工具）

注意：Codex CLI 的测试中明确允许 `function_call_output` 去匹配 `local_shell_call` 的 `call_id`，即“local_shell 的输出可能仍以 function_call_output 形态回传”。因此代理在接收 `input[]` 时，应按 `call_id` 做宽松匹配，不要假设输出类型与调用类型严格一一对应。


### 3.4 图片输入

Codex CLI 会把图片作为 message content part 的 `input_image`，其 `image_url` 往往是 **data URL**（例如 `data:image/png;base64,...`）。

因此代理至少要做到：

1. 请求解析不报错
2. 能将图片信息降级成文本（OCR/描述）并提供给上游 M365（见后文方案）


## 4. OpenAI Responses API 规范对齐（我们要实现的“外观”）

### 4.1 端点清单（建议实现）

1. `POST /v1/responses`
   - 支持 `stream=true` 的 SSE（`Content-Type: text/event-stream`）
2. `POST /v1/responses/compact`
   - 最小实现：返回包含 `output` 的 JSON（字段可多不可少）
3. `GET /v1/models`
   - 返回 OpenAI 标准模型列表结构（`object=list`, `data=[...]`）
4. 可选：
   - `GET /healthz` / `GET /readyz`
   - `GET /v1/responses` WebSocket upgrade（若要追求与 Codex 更强的“增量续写”兼容）


### 4.2 错误输出约定（强烈建议统一）

兼容实现要区分两种错误出口：

1. **HTTP 层错误**（尚未进入 SSE）：
   - HTTP 非 2xx
   - body 形如：
     ```json
     {
       "error": {
         "message": "Unauthorized",
         "type": "invalid_request_error",
         "param": null,
         "code": "unauthorized"
       }
     }
     ```
2. **SSE 流内错误**（headers 已经是 `text/event-stream`）：
   - 发送 `response.failed` 事件（或 `type=error` 事件，取决于你要兼容的客户端范围）
   - Codex CLI 至少能处理 `response.failed`，且会从 `response.error.code/message` 提取信息


## 5. 推荐总体架构（Go 实现）

### 5.1 形态选择：本地代理优先

由于 M365 Copilot Chat API 需要 Delegated 授权与用户许可证，本项目推荐以 **本地代理** 为默认落地形态：

- 用户在 `config.yaml` 中配置 `tenant-id/client-id/client-secret`
- 启动 `m3652api server` 在 `localhost:PORT` 监听
- 首次使用在浏览器打开 `http://localhost:PORT/m365/oauth/start` 完成授权码登录（写入 `refresh_token`），之后由服务端自动刷新
- Codex CLI 通过自定义 model provider 指向该 base_url


### 5.2 组件划分

建议按“协议面/转换面/上游执行面/状态面”拆分，避免耦合：

1. `OpenAI Compatibility Server`
   - 实现 `/v1/responses`（SSE）
   - 实现 `/v1/models`
   - 实现 `/v1/responses/compact`
2. `Session Store`
   - 维护 `session_id` → `m365_conversation_id` 映射
   - 缓存“已注入的工具定义/协议版本/模型偏好”等
   - 支持 TTL 过期与清理
3. `M365 Client`
   - Token 获取/刷新（**授权码 + refresh_token** 为主，client_secret 参与换取与刷新；必要时保留 app-only client_credentials 兜底）
   - 创建 conversation
   - `chatOverStream` 流式调用 + SSE 解析
4. `Protocol Translator`
   - OpenAI Responses request → M365 message payload
   - M365 stream → OpenAI Responses SSE events
5. `Tool Call Protocol (Bridge)`
   - 解决“上游不原生支持工具调用”的结构化输出问题
   - 约束上游输出一种可解析的 tool-call 格式


### 5.3 数据流（ASCII）

```text
[Codex CLI]
  |  POST /v1/responses  (tools + input + reasoning)
  v
[m3652api Proxy]
  |  (1) session_id -> find/create M365 conversation
  |  (2) translate new input/tool outputs -> message.text + additionalContext (+ contextualResources)
  |  (3) call M365 /chatOverStream
  v
[Microsoft Graph beta]
  |  SSE stream (copilotConversation updates)
  v
[m3652api Proxy]
  |  emit OpenAI Responses SSE:
  |   - response.created
  |   - response.output_text.delta (computed)
  |   - response.output_item.done (message or tool call)
  |   - response.completed
  v
[Codex CLI]
  |  executes tools locally, then sends tool outputs back as input items
```


## 6. 核心难点与解决方案

### 6.1 难点 A：M365 不原生输出 OpenAI 工具调用 item

**问题**：Codex CLI 依赖“模型发起工具调用 → 客户端执行 → 回传 tool output → 模型继续”的闭环；但 M365 Copilot Chat API 并不提供 OpenAI Responses 等价的 function calling 结构。

**官方边界提醒**：结合第 2.3 节，M365 Chat API 不支持 actions/工具调用，且仅返回文本。因此本项目的“工具调用闭环”必须由代理侧通过**文本协议模拟**，不能期望上游返回 OpenAI 原生 `function_call`/`tool_call` 结构。

**解决思路（推荐）**：在 M365 对话中注入一套“工具调用协议”，让上游在需要工具时输出一个严格可解析的结构块；代理把它转换成 OpenAI Responses 的 `function_call` / `custom_tool_call` item 返回给 Codex，然后由 Codex CLI 执行工具并回传 `*_call_output`。

#### 6.1.1 工具调用协议（建议 v1）

推荐将“协议声明 + 工具目录 + 本轮上下文（例如上轮工具输出摘要）”通过 `additionalContext` 注入到每一轮 `chat/chatOverStream` 请求中，而不是只在会话初始化时发送一次固定消息。

原因：

- 官方把 `additionalContext` 定位为 extra grounding，更适合作为“协议与约束”的载体
- 只在首轮注入容易被长对话稀释，且不利于无状态重试/故障恢复

建议的承载分工：

- `message.text`：仅放“用户本轮问题/任务本体”（尽量短）
- `additionalContext[]`：放“工具调用协议 v1、允许工具白名单、工具参数示例、上轮工具输出摘要”等
- `contextualResources`：放 OneDrive/SharePoint 文件 URI 与 web grounding 开关（见第 7 节）

一个推荐的 `chatOverStream` 请求体（示意）：

```json
{
  "message": {
    "text": "Please help me update the repository README."
  },
  "locationHint": {
    "timeZone": "Asia/Shanghai"
  },
  "additionalContext": [
    {
      "description": "Tool calling protocol v1",
      "text": "If you need to run a tool, output a single JSON object only. Do not include any extra text or markdown fences."
    },
    {
      "description": "Allowed tools",
      "text": "exec_command, apply_patch"
    }
  ]
}
```

协议要求（关键约束）：

- 普通输出：自然语言文本
- 需要调用工具时：只输出一个 JSON 对象（单对象、无多余文本、不要使用 markdown code fence），例如：

```json
{
  "type": "tool_call",
  "tool": "exec_command",
  "arguments": {
    "cmd": "ls -lah",
    "workdir": ".",
    "tty": false
  }
}
```

对于 `apply_patch`（freeform custom tool），可约定：

```json
{
  "type": "tool_call",
  "tool": "apply_patch",
  "input": "*** Begin Patch\n*** Update File: README.md\n@@\n- old\n+ new\n*** End Patch\n"
}
```

代理负责：

- 生成 `call_id`
- 将 `arguments` 重新序列化为字符串（符合 Responses `function_call.arguments` 习惯）
- 输出 `response.output_item.done` + `item.type=function_call/custom_tool_call`
- 结束本次 Responses 流（`response.completed`），将执行权交给 Codex CLI

实现建议：

- 每个 M365 回答最多生成 **1 个**工具调用（忽略 `parallel_tool_calls`），避免上游输出多个 JSON 块导致解析不稳定
- 工具参数必须是 JSON 可解析对象；`arguments` 在 OpenAI 侧需序列化为字符串

随后 Codex CLI 执行工具并在下一次请求的 `input[]` 中带上 `function_call_output/custom_tool_call_output`，代理把这些输出作为文本“回填”给 M365 继续下一步。


#### 6.1.2 工具列表如何给上游（避免超长）

Codex CLI 会在请求体 `tools[]` 里带完整 schema（可能很长）。直接转发给 M365 可能导致：

- 首轮提示词过大
- 上游难以遵循

推荐做法：

- 代理在 session 初始化时只注入 **精简工具目录**：tool name + 一句话用途 + 最小参数示例
- 在需要更强精确性时，再按需把某个工具的 JSON schema（来自 `tools[]`）局部注入


#### 6.1.3 协议修复与重试（必须实现）

由于上游输出是自然语言文本，工具协议非常容易被破坏（例如多输出解释文字、把 JSON 放进代码块、输出多个 JSON）。为了让系统在真实使用中可用，代理必须实现“修复与重试”：

1. **解析策略（先尽量自愈）**
   - 如果输出是纯 JSON：直接解析
   - 如果输出包含多余文本：尝试提取最外层 `{...}` 单对象（例如从第一处 `{` 到最后一处 `}`）
   - 如果仍无法解析：进入重试流程
2. **重试流程（建议最多 2 次）**
   - 追加一条 `additionalContext` 或下一轮 `message.text`，明确要求：
     - “只输出一个 JSON 对象”
     - “不要任何解释、不要 markdown code fence”
     - “字段必须为 type/tool/arguments 或 type/tool/input”
   - 若超过重试次数：降级为纯文本回答（不触发工具）
3. **白名单与安全兜底**
   - 工具名不在允许列表：视为纯文本（或返回 `response.failed` 明确提示工具不可用）
   - `arguments`/`input` 超过长度上限：拒绝并要求重试（防止超大补丁/命令注入）
   - 对 `exec_command` 可加入额外策略：默认禁止网络、限制命令前缀（由 Codex CLI 的 approvals 决策，但代理也应有最小保护）


### 6.2 难点 B：SSE 增量输出（delta）从何而来

M365 `chatOverStream` 的 SSE 事件可能每次携带“当前最新消息全文”或“增量片段”。为兼容 Codex 的 `response.output_text.delta`，代理需要输出 **增量字符串**。

#### 6.2.1 M365 `chatOverStream` SSE 事件形态（官方示例要点）

根据官方示例，`chatOverStream` 返回 `Content-Type: text/event-stream`，流中每个事件通常包含：

- `data: { ...copilotConversation JSON... }`
- `id: <number>`

其中 `data` 的 JSON 结构是 `copilotConversation`，常见字段：

- `id`：conversation id
- `turnCount`：回合计数（中间更新可能仍为旧值，最终会更新）
- `messages`：`copilotConversationResponseMessage[]`
  - 可能为空数组（仅表示中间更新）
  - 也可能同时包含 user prompt 与 Copilot 回复两条 message

因此代理实现需要“稳健解析”，不能假设每个事件都携带增量片段。


#### 6.2.2 增量计算（推荐实现）

推荐实现（按 message 维度维护增量，而不是全局一个 `lastText`）：

1. 解析每个 M365 SSE 事件，提取“当前 assistant 输出文本” `currentText`
2. 维护 `lastTextByMessageId`（session 级缓存）：`map[messageId]lastText`
3. 计算 `delta`：
   - 若 `currentText` 以 `lastText` 为前缀：`delta = currentText[len(lastText):]`
   - 否则：视为上游重写/截断，记录告警并重置（见下）
4. 发送 `response.output_text.delta` 事件携带该 delta
5. 更新 `lastTextByMessageId[messageId] = currentText`

assistant message 的选择策略（建议）：

- 优先过滤掉“等于本轮 `message.text` 的那条 message”（它通常是用户原文回显）
- 剩余 message 取最后一条作为 assistant 候选
- 若 `messages=[]`：忽略该事件

不要依赖 `createdDateTime` 的单调性来判断新旧，而应以 `message.id` + `text` 差分为准。

若不满足前缀关系（例如上游重写/截断）：

- 兜底策略：发送一个换行 + `currentText` 作为 delta，并重置该 `messageId` 的 `lastText`
- 记录告警日志（便于排查上游行为变化）


#### 6.2.3 何时结束 OpenAI Responses 流

建议以“上游 SSE 流结束（EOF/连接关闭）”作为完成信号：

- 当 M365 SSE 结束时，代理应补发：
  - `response.output_item.done`（包含最终 assistant message item）
  - `response.completed`（包含 usage；若无法获知则填 0）

特殊情况（工具调用提前结束）：

- 一旦检测到“工具调用 JSON”（协议块），代理可以选择提前结束当前 OpenAI 响应：
  - 立刻输出 `response.output_item.done`（tool call item）
  - 输出 `response.completed`
  - 并取消/关闭对 M365 的流式请求（释放资源）


### 6.3 难点 C：图片识别与多模态

M365 Copilot Chat API 的主路径并不保证支持图片输入。为满足 Codex CLI 的“图片输入不崩溃 + 有可用结果”，建议提供可插拔策略：

1. `vision=disabled`（默认可用）：
   - 将图片替换为文本占位符，例如：`"[Image #1 provided: vision is disabled]"`，并继续走 M365
2. `vision=ocr`：
   - 对 `data:image/...;base64,...` 解码并做 OCR
   - 把识别出的文本注入到 M365 提示词，例如：`"Image #1 OCR text: ..."`
3. `vision=caption`（可选）：
   - 使用外部视觉模型生成 caption（例如 Azure Vision / 其他服务）

注意：无论哪种策略，OpenAI 侧接口都要支持解析 `input_image`，并保证不会返回 500。


### 6.4 难点 D：模型选择与思考等级

M365 并无 OpenAI 同构的 `reasoning.effort`，当前实现采取“兼容接收但统一映射”的策略：

- 对外仍暴露多个 model id（例如 `gpt-5.2-codex`, `gpt-5.2`, `gpt-5.3-codex`, `gpt-5.4`），用于兼容客户端模型选择界面与已有配置
- 所有 model alias 最终都映射到相同的 Microsoft Graph Copilot Chat API 调用路径
- `reasoning.effort` 仅做协议兼容，不参与提示词构造，也不会改变上游请求


## 7. API 设计（对外 OpenAI 兼容层）

### 7.1 `POST /v1/responses`（SSE）

#### 7.1.1 接收（兼容范围）

必须兼容以下输入形态：

- `input` 为字符串：
  ```json
  { "model": "gpt-5.2-codex", "input": "Hello" }
  ```
- `input` 为数组（Codex 常用）：
  ```json
  {
    "model": "gpt-5.2-codex",
    "instructions": "You are a helpful coding agent.",
    "input": [
      {
        "type": "message",
        "role": "user",
        "content": [
          { "type": "input_text", "text": "List files in the repo." }
        ]
      }
    ],
    "tools": [
      { "type": "function", "name": "exec_command", "parameters": { "type": "object" } },
      { "type": "custom", "name": "apply_patch", "format": { "type": "grammar", "syntax": "lark", "definition": "..." } }
    ],
    "tool_choice": "auto",
    "stream": true
  }
  ```

代理服务建议做以下“兼容而不强依赖”的策略：

- 未提供 `instructions`：使用空串
- 未提供 `tools`：视为无工具模式
- `stream=false`：可以先返回 400（明确告知仅支持 SSE），或实现非流式（建议后续补齐）

将 OpenAI Responses 请求落到上游 M365 `chat/chatOverStream` 请求时，代理必须保证以下映射与必填项（与 M365 官方文档一致）：

1. **`locationHint.timeZone` 必填**
   - 必须是 IANA 时区（例如 `Asia/Shanghai`、`America/New_York`）
   - 建议优先使用代理配置中的默认时区；如请求里携带用户时区信息，则覆盖
2. **`message.text` 只承载“本轮用户任务本体”**
   - 将 OpenAI `input` items 扁平化：提取用户输入文本（`input_text`）为主
   - 工具输出（`*_call_output`）与协议声明不要直接堆到 `message.text`（避免污染对话）
3. **`additionalContext[]` 承载协议与上下文（强烈建议）**
   - 注入：工具调用协议、允许工具白名单、工具参数最小示例
   - 回填：上轮工具输出摘要（例如命令输出/补丁应用结果的精简版本）
   - 注：这是官方定义的 extra grounding 载体，更贴合用途
4. **`contextualResources` 用于文件上下文与 web grounding 开关**
   - 文件：仅支持 OneDrive/SharePoint 的 `uri`（`copilotFile.uri`），不支持直接上传本地文件二进制
   - web grounding：如需关闭，必须在每一轮显式设置 `contextualResources.webContext.isWebEnabled=false`（单轮开关）


#### 7.1.1.1 工程化映射表：OpenAI Responses → M365 chatOverStream

本小节用于把 **OpenAI Responses 请求**稳定映射为 **M365 `chatOverStream` 请求**，以便直接落地编码实现。

默认已在第 13 节确认以下落地前提（本映射表按此假设设计）：

- 落地形态：本地单用户代理（local single-user）
- 传输：只支持 HTTP SSE（`supports_websockets=false`，不做 WebSocket）
- Vision：必须 OCR/识别（`vision=ocr`，不允许 silent drop）

本映射分为三层：

1. **会话与端点**：如何从 Responses 请求得到 Graph `conversationId` 与选用 `chatOverStream`
2. **字段映射**：逐字段映射到 `message/locationHint/additionalContext/contextualResources`
3. **关键算法**：输入增量、工具/图片注入、截断与重试


##### A. Graph 调用序列（每次 `POST /v1/responses`）

1. **解析 OpenAI Responses 请求**（允许出现未识别字段，但不可报错）
2. **解析/选择 sessionKey**（用于会话映射；见下文 `prompt_cache_key`）
3. **确保有 `conversationId`**
   - 若 session 未绑定 `conversationId`：调用 `POST /beta/copilot/conversations`（body 必须为 `{}`）创建会话
   - 若已有绑定：直接复用
4. **发送上游流式请求**
   - 若 `stream=true`：调用 `POST /beta/copilot/conversations/{conversationId}/chatOverStream`
   - 若 `stream=false`：建议先返回 400（仅支持 SSE）；或降级到同步 `chat`（后续再补齐）


##### B. 字段级映射表（请求 → 请求）

说明：

- “OpenAI 字段”指 `POST /v1/responses` 请求体字段（以及 `input[]` 内部 item）
- “M365 字段”指 `chatOverStream` 请求体字段或 URL path 参数
- 复杂字段（例如 `input[]`、`tools[]`）只在表里给“规则摘要”，详细算法在后文

| OpenAI（Responses） | M365（chatOverStream） | 规则摘要（工程化） |
| --- | --- | --- |
| `stream` | 端点选择 | `true` → `chatOverStream`；`false` → 400 或降级 `chat` |
| `prompt_cache_key` | `conversationId`（会话映射 key） | 作为 sessionKey 查表；无绑定则创建会话并写回绑定 |
| `model` | 无直接同构字段 | 仅作为 OpenAI 兼容层标签使用；不改变微软上游请求 |
| `instructions` | `additionalContext[]` | 作为 `System instructions` 注入（不要塞进 `message.text`） |
| `reasoning.effort` | 无映射 | 协议兼容接收，但直接忽略，不参与上游请求构造 |
| `text.verbosity` | `additionalContext[]` | 转成输出风格约束（brief/normal/verbose） |
| `text.format`（含 `json_schema`） | `additionalContext[]` | 仅能“提示词约束”，无法强保证；必要时加“只输出 JSON”规则 |
| `tool_choice` | `additionalContext[]` | `none`：显式禁止工具；`auto`：允许；指定工具：要求优先调用该工具 |
| `parallel_tool_calls` | `additionalContext[]` | `false`：要求一次只发一个 tool call；`true`：允许输出数组（实现可先仍串行执行） |
| `tools[]` | `additionalContext[]` | 编码成“工具目录 + 调用协议”（强烈建议裁剪/去噪，见后文） |
| `input`（string） | `message.text` | 直接作为本轮 user task（必要时截断） |
| `input[]` → `message(role=user)` | `message.text` | 取“本轮新增 items”里的最后一条 user message 作为 user task（见后文增量算法） |
| `input[]` → `function_call_output` / `custom_tool_call_output` | `additionalContext[]` | 作为 `Tool outputs` 注入；强制截断并保留结构边界 |
| `input[]` → `message` 中的 `input_image` | OCR → `additionalContext[]` | 解码 data URL → OCR → 注入 `Image OCR #n`（必须有结果或明确错误文本） |
| （自定义）web 开关（例如请求 `metadata.web_enabled`） | `contextualResources.webContext.isWebEnabled` | 默认 `true`；可按配置 / 自定义字段覆盖；注意是单轮开关 |
| （可选）文件 URI（非本地文件） | `contextualResources.files[]` | 仅支持 OneDrive/SharePoint 可访问 `uri`；本地文件必须走工具读取并注入文本 |
| （代理配置）时区 | `locationHint.timeZone` | 必填；优先代理配置 IANA 时区（例如 `Asia/Shanghai`） |


##### C. 输入增量算法（避免重复把整段历史发给 M365）

**背景**：Codex CLI 往往会把“整段会话历史”作为 `input[]` 反复发送。M365 会话本身已经持久化历史，如果代理把 `input[]` 全量转成 `message.text` 或 context，就会造成**重复**与**指数膨胀**。

因此代理必须按 session 做“增量消费”：

1. 若 `input` 是字符串：视为“无历史”模式，直接把字符串作为本轮 `userTaskText`
2. 若 `input` 是数组：
   - 维护 session 状态：`processed_input_len`
   - `newItems = input[processed_input_len:]`（只处理新增尾部）
   - 若 `len(input) < processed_input_len`：视为客户端重置会话，清零并重新绑定/新建 conversation
3. 本轮 `message.text` 的选择：
   - 从 `newItems` 中找 `type="message" && role="user"` 的最后一条
   - 将其 content parts 扁平化为文本（`input_text` 直接拼接；`input_image` 走 OCR 注入到 additionalContext，不直接拼进正文）
   - 若本轮没有新增 user message（常见于“工具输出 → 继续推理”的自动回合）：使用合成文本作为 `message.text`，例如 `"Continue."`
4. 本轮 `additionalContext` 的来源：
   - 从 `newItems` 中收集所有 `function_call_output/custom_tool_call_output/...`，统一编码成 `Tool outputs`
   - 同时注入工具目录/协议、verbosity、OCR 结果（见下）
5. 当本轮上游请求成功发出后：更新 `processed_input_len = len(input)`


##### D. `additionalContext[]` 组装建议（可直接编码）

建议每轮固定生成以下 4~6 个 `copilotContextMessage`，以便稳定调试与逐项截断（顺序建议保持一致）：

1. `description="System instructions"`：来自 `instructions` + 代理固定基座（英文）
2. `description="Output style"`：来自 `text.verbosity` 的输出风格约束（英文）
3. `description="Tool calling protocol"`：工具调用文本协议（英文，要求输出严格 JSON）
4. `description="Available tools"`：从 `tools[]` 编码出的工具目录（英文，必要时裁剪）
5. `description="Tool outputs"`：从 `function_call_output/...` 编码的工具结果（英文，必须截断）
6. `description="Image OCR results"`：从 `input_image` OCR 出来的文本（英文，必须截断）

工程化注意点：

- **强制截断**：对每个 `additionalContext[i].text` 做上限（例如 8k～16k chars），避免 Graph 413/500
- **保留边界**：截断时保留开头与结尾，并插入类似 `"...(truncated)..."` 的标记
- **稳定格式**：对 `Tool outputs` 与 `Available tools` 采用稳定的块格式，方便上游遵循与代理解析


##### E. OCR（`input_image`）→ 文本注入规则（必须实现）

由于 M365 Chat API 不保证原生图片输入能力，且本项目已确认 **必须 OCR/识别**，因此代理在接收 `input_image` 时必须：

1. 解析 `image_url`：
   - 主路径：支持 `data:image/<type>;base64,...`（Codex 常见）
   - 可选：支持 `{ "url": "data:..." }` 形态（取决于客户端实现）
   - 不建议默认支持远程 URL 下载（安全与可控性差），如要支持应加 allowlist
2. 解码 base64 得到图片 bytes
3. 执行 OCR（例如 tesseract / Windows OCR / 云服务均可；实现细节见 6.3）
4. 生成 `additionalContext` 块（英文），示例格式：
   - `Image #1 OCR text:\n<ocr_text>`
   - 如 OCR 失败：必须注入明确错误文本（例如 `Image #1 OCR failed: <reason>`），避免 silent drop
5. 对 OCR 文本做截断与清洗（去除超长空白、控制字符）


##### F. 最小示例（便于对照抓包）

**1) 入站 OpenAI Responses（节选）**

```json
{
  "model": "gpt-5.2",
  "instructions": "You are a helpful coding agent. Use tools when needed.",
  "reasoning": { "effort": "high" },
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [
        { "type": "input_text", "text": "Read README and summarize the project." },
        { "type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgoAAA..." }
      ]
    }
  ],
  "tools": [
    {
      "type": "function",
      "name": "exec_command",
      "description": "Run a shell command and return stdout/stderr.",
      "parameters": { "type": "object", "properties": { "cmd": { "type": "string" } }, "required": ["cmd"] }
    }
  ],
  "tool_choice": "auto",
  "parallel_tool_calls": false,
  "stream": true,
  "prompt_cache_key": "sess_123"
}
```

**2) 出站 M365 chatOverStream（节选）**

```json
{
  "message": { "text": "Read README and summarize the project." },
  "locationHint": { "timeZone": "Asia/Shanghai" },
  "contextualResources": { "webContext": { "isWebEnabled": true } },
  "additionalContext": [
    { "description": "System instructions", "text": "You are a helpful coding agent. Use tools when needed." },
    { "description": "Output style", "text": "Verbosity: verbose." },
    { "description": "Tool calling protocol", "text": "When you need to use a tool, output a single JSON object with keys: tool_name, arguments. Do not add extra text." },
    { "description": "Available tools", "text": "Tool: exec_command\nDescription: Run a shell command and return stdout/stderr.\nParameters JSON Schema: {\"type\":\"object\",\"properties\":{\"cmd\":{\"type\":\"string\"}},\"required\":[\"cmd\"]}" },
    { "description": "Image OCR results", "text": "Image #1 OCR text:\n...(truncated)..." }
  ]
}
```


#### 7.1.2 返回（最小事件序列）

建议返回如下 SSE：

1. `response.created`
2. `response.output_text.delta`（多次）
3. `response.output_item.done`（最终 message 或 tool call）
4. `response.completed`（包含 usage）

其中 `response.completed.response.usage` 建议始终存在（即便填 0），以减少客户端分支判断。


### 7.2 `POST /v1/responses/compact`

MVP 可实现为“对输入 items 做截断/总结”的轻量逻辑：

- 输入：接受任意 JSON（至少兼容 `{model,input,instructions}`）
- 输出：返回包含 `output: []` 的 JSON

第一阶段可以做简化：

- 直接返回原始 input（不 compact）
- 或返回一条短 summary message item

后续再做真正 compaction（例如基于规则压缩、或调用 M365 生成摘要）。


### 7.3 `GET /v1/models`

返回 OpenAI 标准结构：

```json
{
  "object": "list",
  "data": [
    { "id": "gpt-5.2-codex", "object": "model", "created": 0, "owned_by": "m3652api" },
    { "id": "gpt-5.2", "object": "model", "created": 0, "owned_by": "m3652api" },
    { "id": "gpt-5.3-codex", "object": "model", "created": 0, "owned_by": "m3652api" },
    { "id": "gpt-5.4", "object": "model", "created": 0, "owned_by": "m3652api" }
  ]
}
```

并支持查询参数（例如 `client_version`）但忽略它们，以提高兼容性。


## 8. 认证与配置设计（Go）

### 8.1 两层认证

1. **代理自身的访问控制**（面对 Codex CLI）：
   - 使用 `Authorization: Bearer <proxy_api_key>`
   - 本 key 仅用于保护本地端口不被滥用（尤其是局域网暴露场景）
2. **M365 的 OAuth token**（代理访问 Graph）：
   - 启动时从 `config.yaml` 读取 `m365.tenant-id/client-id/client-secret`
   - 代理按需向 Entra token endpoint 申请 access token 并落盘缓存


### 8.2 配置文件（建议 YAML）

建议 `config.yaml`（示意）：

```yaml
server:
  listen: "localhost:8080"
  api_key: "${M3652API_KEY}"

m365:
  tenant-id: "your-tenant-id"
  client-id: "your-app-client-id"
  client-secret: "your-client-secret"
  timezone: "Pacific/Auckland"

models:
  - id: "gpt-5.2-codex"
    display_name: "GPT-5.2 Codex (M365 Compatible Alias)"
  - id: "gpt-5.2"
    display_name: "GPT-5.2 (M365 Compatible Alias)"
  - id: "gpt-5.3-codex"
    display_name: "GPT-5.3 Codex (M365 Compatible Alias)"
  - id: "gpt-5.4"
    display_name: "GPT-5.4 (M365 Compatible Alias)"

compat:
  supports_websocket: false
  vision_mode: "disabled" # disabled|ocr|caption
  max_tool_calls_per_turn: 8
```

说明：配置中的字符串内容建议保持英文，避免与“代码/接口示例英文”的统一风格冲突。


### 8.3 Codex CLI 配置示例（`~/.codex/config.toml`）

```toml
model_provider = "m365"
model = "gpt-5.2-codex"

[model_providers.m365]
name = "M365 Copilot Proxy"
base_url = "http://localhost:8080/v1"
env_key = "M3652API_KEY"
wire_api = "responses"
supports_websockets = false
stream_idle_timeout_ms = 300000
```


## 9. 复用 CLIProxyAPI SDK 的可行性评估（强烈建议纳入）

`router-for-me/CLIProxyAPI` 已有大量针对 Codex CLI 的兼容工程实践，尤其是：

- OpenAI Responses SSE 的“事件状态机”与稳定转发
- Responses WebSocket（含 `x-codex-turn-state`）支持
- streaming error 的正确输出方式
- SDK 可嵌入（`sdk/cliproxy`）、可扩展 provider executor、translator registry、model registry

### 9.1 两种复用策略

**策略 A：直接使用 CLIProxyAPI SDK 作为底座（推荐）**

- 优点：
  - 立即获得更成熟的 `/v1/responses`（含 WS）与 `/v1/models` 行为
  - 复用其流式转发、错误处理、并发与 keep-alive 策略
  - 更接近 Codex CLI 的“真实生态兼容”
- 代价：
  - 工程体积更大
  - 需要理解其 provider/translator 插件机制

**策略 B：不依赖 SDK，但参考其实现（次选）**

- 优点：项目更轻、更易控
- 代价：需要自行实现 SSE/WS 细节与大量边界条件

本方案建议：MVP 先走策略 B（快速验证 M365 流式与授权），一旦确认可行，尽快切换到策略 A 以提升兼容性与长期维护性。


## 10. 测试与验收标准

### 10.1 自动化测试（建议）

1. 单元测试：
   - OpenAI request 解析（覆盖 `input` string/array，覆盖 `input_image`）
   - M365 SSE 解析与 delta 计算
   - tool-call 协议解析（合法/非法/边界）
2. 集成测试：
   - 使用 `openai/codex` 的 SSE 事件形态作为契约（contract test），验证我们输出的事件能被 Codex SSE 解析器消费
3. 端到端（本地）：
   - 启动代理，Codex CLI 指向代理，执行典型任务：
     - “List files”
     - “Create a new file and apply patch”
     - “Run tests”（触发 `exec_command`）
     - 附带图片输入（验证不崩溃与降级输出）


### 10.2 验收（最小通过线）

满足以下条件即视为 MVP 成功：

1. Codex CLI 能连接代理并获得持续流式输出（无断流/无解析错误）
2. Codex CLI 能收到工具调用 item，并能把工具输出回传后继续下一轮
3. 代理能稳定复用 M365 conversation，实现多轮对话


## 11. 里程碑与任务拆解（建议 4 个阶段）

### 阶段 0：项目骨架（0.5–1 天）

1. 初始化 Go module、目录结构、配置加载
2. `GET /healthz`
3. 基础日志与 request id（建议读取 `x-client-request-id`）


### 阶段 1：M365 通路打通（1–3 天）

1. 实现 `config.yaml` 静态凭据加载（tenant/client/secret）
2. 实现 Graph client：
   - create conversation
   - `chatOverStream` 调用与 SSE 解析
3. 实现 `POST /v1/responses`：
   - 只支持文本
   - 输出 `response.created` / `response.output_text.delta` / `response.completed`


### 阶段 2：工具调用闭环（2–5 天）

1. 设计并实现 tool-call 协议（JSON-only 或带 tag 的 JSON）
2. 支持至少两个关键工具：
   - `exec_command`（function_call）
   - `apply_patch`（custom_tool_call）
3. 支持接收 `function_call_output/custom_tool_call_output` 并回填给 M365


### 阶段 3：兼容性完善（3–7 天）

1. `GET /v1/models`（兼容模型别名列表）
2. `POST /v1/responses/compact`（最小可用 + 后续增强）
3. 图片输入策略（disabled/ocr/caption）
4. 更完整的错误映射（401/403/429/5xx → `response.failed`/HTTP error）
5. 并发与 session TTL 清理


### 阶段 4：引入 CLIProxyAPI SDK（可选但推荐，2–6 天）

1. 评估直接嵌入 `sdk/cliproxy` 的最小集成路径
2. 实现 M365 ProviderExecutor
3. 注册 M365 translator（M365 stream → Responses SSE）
4. 复用其 WS、keep-alive、错误 chunk 等成熟实现


## 12. 风险与开放问题

1. **上游 beta 变更风险**：Graph beta 端点/字段可能变动，需要版本探测与快速修复机制。
2. **工具调用稳定性**：M365 是否能稳定遵循结构化 tool-call 协议不确定；需要重试与自修复提示。
3. **图片能力缺口**：若必须真正“看图”，需要引入额外视觉能力（可能引入成本与合规议题）。
4. **安全边界**：
   - 代理是“模型提供方”，会接触本地代码仓库内容（通过工具输出回传）
   - 必须避免把敏感内容泄露到日志或第三方
5. **会话映射**：
   - Codex 的 `session_id/prompt_cache_key` 与 M365 conversationId 如何稳定绑定、何时失效、如何恢复


## 13. 下一步建议（可执行）

1. 先确定“落地形态”：
   - 本地单用户代理（推荐）
2. 确认是否“必须支持 WebSocket”：
   - 先 `supports_websockets=false`，只做 HTTP SSE，降低复杂度
3. 明确 vision 需求：
   - 必须 OCR/识别
