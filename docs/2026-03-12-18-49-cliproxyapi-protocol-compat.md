# CLIProxyAPI 协议兼容性调研（Codex CLI / OpenAI Responses）

调研对象：`router-for-me/CLIProxyAPI`（以 `main` 分支公开文档与代码为准）


## 1. 结论（协议兼容性视角）

### 1.1 是否可直接作为 Codex CLI 兼容层？

可以。

项目官方文档给出了 Codex CLI 的配置方式：在 `~/.codex/config.toml` 中将 `wire_api` 设为 `responses`，并将 `base_url` 指向 CLIProxyAPI 的 `/v1` 前缀，从而让 Codex CLI 走 OpenAI Responses 协议打到 CLIProxyAPI。

### 1.2 是否可直接作为 OpenAI Responses 兼容层？

可以（至少覆盖 Responses 的核心端点与核心事件流）。

从仓库实现可以看到它提供 OpenAI Responses API handler、SSE 流式输出拼装、以及（额外的）WebSocket 形态的 `/v1/responses/ws`。


## 2. 它具体做了哪些“Responses 兼容”的关键点

### 2.1 端点层

- `/v1/responses`：接受 Responses schema 的请求，并支持 `stream=true` 的 SSE 事件流。
- `/v1/models`：提供模型列表（供客户端探测/展示）。
- `/v1/responses/ws`：提供 WebSocket 版本的 responses 交互（包含 `response.create`/`response.append` 形态）。

### 2.2 SSE 事件流（对 Codex CLI 很关键）

它不仅“转发 SSE”，而是会在转换层补齐/重排事件，以适配 Codex 这类强依赖事件序与 item 生命周期的客户端。

典型事件包括（非穷举）：

- `response.created` / `response.in_progress`
- `response.output_item.added`
- `response.content_part.added`
- `response.output_text.delta` / `response.output_text.done`
- `response.function_call_arguments.delta` / `response.function_call_arguments.done`
- `response.output_item.done`
- `response.completed`

并且在工具调用出现前，会先补齐并关闭正在进行中的 message item（保证“文本 -> 工具调用”的顺序稳定）。

### 2.3 工具调用结构

它在 Responses 的 `input[]` 中支持/处理：

- `type=function_call`（模型发起工具调用）
- `type=function_call_output`（客户端回填工具输出）

并在需要桥接到 Chat Completions 时，将其转换成 `tool_calls` / `role=tool` 的结构。

### 2.4 鉴权与多凭证

它提供基于 `api-keys` 的 Bearer Key 认证，并且整体系统面向“多账户/多凭证”的代理形态（支持轮询/路由/按模型选择凭证）。

### 2.5 模型路由与请求改写

它提供比较强的“配置驱动”能力：

- 路由策略（如 round-robin）
- 模型别名与匹配规则（前缀/后缀/通配）
- payload 的 `default/override/filter` 规则（按模型、协议、请求类型等做字段注入/删除/替换）

这对“把上游不支持的字段过滤掉，同时保持下游协议稳定”很有价值。


## 3. 作为“完全等价 OpenAI Responses 官方实现”的潜在差距

这里强调的是“完全等价”，并非否定其可用性。

### 3.1 内置工具（Built-in tools）在跨协议桥接时可能被丢弃

在 Responses -> Chat Completions 的转换逻辑里，内置工具（例如 `{"type":"web_search"}`）会被直接忽略（注释中也解释了原因：大多数上游不支持）。

这意味着：

- 如果下游客户端强依赖 built-in tools（而不是 function tools），在某些上游链路上可能退化为“纯文本”。

### 3.2 多模态覆盖范围以“文本/图片输入”为主

从转换代码可见，消息内容主要处理 `input_text` / `output_text` / `input_image`（`image_url`）。

对于音频、文件检索等更复杂的 input/output item（如果你的客户端会发）需要额外确认/补齐。

### 3.3 WebSocket 形态是加分项，但也意味着更多兼容细节

`/v1/responses/ws` 引入了更多状态管理与增量输入策略（例如 `previous_response_id`、turn-state 头等），这对兼容性有帮助，但也会增加实现与排错复杂度。


## 4. 对 m3652API（M365 Copilot Chat -> OpenAI Responses）最有价值的参考设计

### 4.1 “Executor + Translator”的两层架构

把系统拆成两层：

1. **Executor（上游执行器）**：只负责和上游（M365 Copilot Chat）对话，输出“上游原生响应/流”。
2. **Translator（协议翻译器）**：只负责把上游输出变成 OpenAI Responses（含 SSE 事件流、completed 聚合输出）。

优点：新增上游（比如不同 M365 租户/不同接入方式）不会污染协议层；协议层迭代也不影响上游接入。

### 4.2 流式输出的“状态聚合器”

Responses 协议的难点不在于“发 delta”，而在于：

- item 生命周期（added/delta/done）
- tool_call 的 arguments 分片拼接
- completed 时输出 `response.output[]` 的一致性
- 事件顺序稳定（尤其是文本与工具调用的排序）

CLIProxyAPI 的实现思路非常值得直接借鉴：用一个状态对象在流式过程中持续聚合，最后在 `response.completed` 里补齐完整 output。

### 4.3 协议容错：自动识别“客户端发错端点/格式”

它在 `/v1/chat/completions` 中会检测到“其实是 Responses payload”的请求，并做格式转换。

对我们的意义：可以同时兼容更多客户端（Cursor / OpenCode / 自研脚本 / 历史版本），减少接入成本。

### 4.4 配置驱动的 payload filter/override

M365 上游大概率不支持很多 OpenAI 参数（例如 `reasoning`、`parallel_tool_calls`、`response_format` 等）。

参考 CLIProxyAPI 的做法：

- 在“进入上游前”统一过滤/降级不支持字段
- 在“返回下游前”尽量保持 Responses 输出结构稳定
- 让差异通过配置表达，而不是硬编码散落在 handler 里

### 4.5 会话粘性与多账户路由

M365 Copilot Chat 具有强会话属性（cookie/session/conversation id）。

CLIProxyAPI 的“多账户轮询 + 会话粘性（turn-state / pinned auth）”理念很适合迁移过来：

- 同一 `response_id` / 对话线程必须绑定同一个上游会话
- 不要在一段对话中途切换上游账号，否则上下文会断裂


## 5. 建议的下一步落地路线（供参考）

1. 以 Codex CLI 为验收客户端：优先打通 `/v1/responses` + SSE 事件流（只做文本）。
2. 增加 `previous_response_id` 的会话映射：把它映射到 M365 的 conversation/thread。
3. 明确工具调用策略：
   - 若 M365 不支持 tool calling：先做“工具禁用/忽略”，保证文本可用；
   - 若需要工具：在服务端实现 tool orchestration（属于更大工程）。
4. 逐步补齐：错误 chunk、keepalive、WebSocket（如果你的客户端确实用到）。

