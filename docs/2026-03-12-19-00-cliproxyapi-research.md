# CLIProxyAPI 调研：Codex CLI 与 OpenAI Responses 兼容层

本文记录对 `router-for-me/CLIProxyAPI` 的调研结论，重点关注：

1. 是否已支持 Codex CLI（客户端侧）接入
2. 是否已实现 OpenAI Responses 兼容层（含 SSE / WebSocket）
3. SDK/适配层哪些设计与组件可复用到「M365 Copilot Chat → OpenAI Responses」项目


## 1. 项目定位（What / Why）

- CLIProxyAPI 的核心定位是：把多种「CLI/订阅/OAuth」来源的模型能力，包装成 **OpenAI/Gemini/Claude/Codex 兼容的 API 服务**，使现有客户端与 SDK 可以无感接入。
- 其目标用户主要是各类 AI 编程工具（如 Codex、Claude Code 等），以及需要统一 API 网关、统一鉴权、统一路由/回退能力的场景。


## 2. 协议与端点能力（尤其 OpenAI、Responses、SSE、工具调用）

### 2.1 OpenAI 兼容端点

典型能力包含：

- `POST /v1/chat/completions`
- `POST /v1/completions`
- `GET /v1/models`

### 2.2 OpenAI Responses 端点（HTTP + SSE）

- `POST /v1/responses`：支持 `stream=true` 的 SSE
- `POST /v1/responses/compact`：非流式的“紧凑响应”变体（用于特定客户端/场景）

### 2.3 OpenAI Responses WebSocket（Codex 相关）

- `GET /v1/responses`：支持 WebSocket Upgrade
- 典型下行/上行事件类型：
  - 上行：`response.create`、`response.append`
  - 下行：`response.*` 事件与 `error`
- 关键头：`x-codex-turn-state`（用于保持 turn-state 粘性）

### 2.4 工具调用 / 多模态

- Requests 侧：支持把 Responses 风格的 `tools`、`tool_choice`、`parallel_tool_calls` 等字段转换为下游可理解的结构（或在必要时忽略内建工具）。
- Streaming Responses 侧：会把下游（如 Chat Completions chunk）中的 `tool_calls`、文本 delta、reasoning 等，转换为 Responses 的 `response.output_text.delta`、`response.output_item.added` 等事件流。


## 3. SDK/适配层能力（可复用点在哪里）

### 3.1 可嵌入的 Service（sdk/cliproxy）

CLIProxyAPI 把代理服务以 Go 库方式暴露，核心能力包括：

- 路由（按 provider / model）
- 鉴权与凭据管理（含刷新）
- 热更新（配置与凭据变更）
- 协议翻译（OpenAI/Gemini/Claude/Codex 互转）

### 3.2 可插拔 Provider 执行器（Executor）

它把“出站调用上游”的部分抽象为 Provider 执行器接口：

- 非流式：返回 JSON payload
- 流式：返回 chunk channel（可用于 SSE/WS）
- 可选 RequestPreparer：在原始 HTTP 请求上注入凭据

这对「把 M365 Copilot Chat 当作一个新 provider」非常契合。

### 3.3 翻译器注册表（sdk/translator）

SDK 自带一个“翻译器注册表 + 翻译流水线（带 middleware）”的抽象：

- 请求：Format A → Format B 的 JSON transform
- 响应：Format B → Format A 的 transform
  - Streaming：每个 chunk 转成一个或多个 chunk（可维护 state）
  - Non-stream：一次性转换

该抽象对「把 M365 的事件流转为 OpenAI Responses SSE」非常有参考价值。

### 3.4 入站访问控制（sdk/access）

SDK 的 access manager 允许把多个认证 provider 串起来，并汇总 `no_credentials/invalid_credential` 等错误语义，适合复用到你自己的 API 服务。

### 3.5 Watcher 增量更新队列（sdk-watcher 文档描述）

其 watcher 设计重点在“高频变更下不阻塞、合并同一凭据的重复更新、队列抽干降低切换开销”。对需要频繁刷新 M365 token、或多账号轮询的场景很有价值。


## 4. 对「M365 Copilot Chat → OpenAI Responses」最有价值的参考设计

### 4.1 Responses 兼容层的“请求与响应双向翻译”

CLIProxyAPI 在 OpenAI Responses 与 Chat Completions 之间做了双向适配：

- Requests：Responses → Chat Completions（便于复用下游生态/避免工具字段丢失）
- Streaming Responses：Chat Completions chunk → Responses SSE event（并维护序号、item 状态）

你的项目同样需要一套“上游协议（M365）↔ Responses”的双向适配：

- 入站：Responses request → M365 的输入结构
- 出站：M365 streaming → Responses SSE/WS

### 4.2 SSE 的工程化处理：先窥探首包，再提交 headers

要点是：**在首个 chunk 到来之前，不要提交 SSE headers**。这样当上游立即失败时，可以返回正常的 JSON 错误码；一旦进入 SSE 流，错误需要以 `event: error` 的形式输出并 flush。

### 4.3 WebSocket 的“增量输入”语义（response.create/append）

如果你希望兼容 Codex CLI 或类似客户端，WebSocket 方案值得直接借鉴：

- 用 `response.create` 建立一次“turn”
- 用 `response.append` 追加输入，并复用上一次输出（或 previous_response_id 语义）
- 用 `x-codex-turn-state` 做 session 粘性

### 4.4 Responses 流的“严格 schema 兼容”

实践上很多客户端会校验 Responses streaming event 的 JSON schema。

建议在实现时固定保证：

- `response.created` / `response.in_progress` 中 `response.model` 等字段不缺失
- `response.completed` 中 `response.usage` 若上游拿不到也要给默认值（0 或空结构）

### 4.5 插件化架构：provider executor + translator registry

如果你预计未来会接入多种上游（不仅仅 M365），CLIProxyAPI 这种“执行器/翻译器/模型注册”三件套是一个可扩展的骨架。


## 5. 需要改造/补齐的点（针对 M365 上游）

当你把上游换成 M365 Copilot Chat，需要重点补齐：

- 会话与上下文：M365 往往是 conversation/thread 驱动；Responses 可能是 `input` + `previous_response_id` 风格，需要明确映射与缓存策略
- 工具调用：M365 未必支持 function calling；需决定：
  - 直接不支持并返回合理错误
  - 或在代理侧实现 tool execution（高复杂度）
- Usage 与计费字段：M365 不一定给 token usage，需要估算/置零并保持 Responses schema 完整
- 多模态：图片/附件的语义差异，需要在入站 `input_image` 与 M365 附件能力之间做折中
- 错误模型：把 M365 的错误码/错误体映射为 OpenAI 的 error 结构，并区分“未进入流/已进入流”的错误写法


## 6. 下一步建议（落地路径）

建议两条落地路线二选一：

1) **基于 CLIProxyAPI SDK 扩展一个 M365 Provider**
   - 实现 `ProviderExecutor`（出站到 M365）
   - 注册翻译器：`openai.responses` ↔ `m365.chat`
   - 直接复用其 `/v1/responses` SSE 与 WebSocket 兼容层

2) **抽取其 Responses SSE/WS 适配思路，自研轻量网关**
   - 复用其状态机/事件顺序/错误处理的设计
   - 仅实现你需要的最小端点集（如 `/v1/responses` + `/v1/models`）

