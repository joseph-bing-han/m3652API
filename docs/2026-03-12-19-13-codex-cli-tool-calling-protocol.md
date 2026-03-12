# Codex CLI（Rust）工具调用 / Function Calling 协议调研（codex-rs）

调研范围：`/tmp/codex-upstream/codex-rs`（Rust workspace）

上游版本标记：`ba5b94287e21bbe3da565d2f070a14f8d4971328`


**结论速览**

1. Codex CLI（Rust）这条主链路使用的是 **OpenAI Responses API** 风格的工具调用：请求里声明 `tools`，模型在响应流里输出 `type=function_call` 等 item，工具执行完后再把 `type=function_call_output` 作为新的 `input` item 回传给模型。
2. **工具声明**以 JSON 形式放在请求体的 `tools: Vec<Value>` 中（并非强类型“OpenAI SDK Tool struct”），来源是把内部 `ToolSpec`（Rust enum）序列化为 JSON。
3. **函数调用输出**（`function_call.arguments`）在 Responses API 里是“**JSON 字符串**”，代码里也按字符串保存，随后由各工具 handler 再 `serde_json::from_str` 解析成结构体。
4. **工具输出回传**不使用类似 `tool_outputs` 顶层字段，而是把 `function_call_output` 作为 **新的 input item** 追加进会话历史；下一次请求的 `input` 会携带这条 item，从而让模型“看到工具执行结果”。
5. `parallel_tool_calls` 在请求层面是一个 `bool`；运行时层面还有“**每个工具是否允许并行**”的锁控制，保证不支持并行的工具串行执行。


**一、请求：tools 如何声明（function schema / JSON schema parameters）**

**1) 请求体字段（Responses API）**

- 请求结构体：`ResponsesApiRequest`（`codex-api/src/common.rs:144`）
  - `tools: Vec<serde_json::Value>`
  - `tool_choice: String`
  - `parallel_tool_calls: bool`
- WebSocket 版本请求：`ResponseCreateWsRequest`（`codex-api/src/common.rs:187`）
  - 字段与 HTTP SSE 基本一致：`tools` / `tool_choice` / `parallel_tool_calls` 同名同类型

**2) tools 的来源：把内部 ToolSpec 序列化为 JSON**

- `create_tools_json_for_responses_api`（`core/src/tools/spec.rs:1969`）
  - 逻辑非常直接：对每个 `ToolSpec` 做 `serde_json::to_value(tool)`，塞进 `Vec<Value>` 返回
- `ToolSpec` 定义（`core/src/client_common.rs:169`）
  - `#[serde(tag = "type")]`，因此序列化后每个 tool JSON 都有 `type` 字段
  - 常见变体：
    - `type="function"`：函数工具
    - `type="tool_search"`：工具搜索（给模型动态发现可用工具）
    - `type="web_search"` / `type="image_generation"` / `type="local_shell"`：内置工具
    - `type="custom"`：自由格式工具（freeform）

**3) function 工具的 schema（name/description/parameters）**

- `ResponsesApiTool`（`core/src/client_common.rs:271`）
  - `name: String`
  - `description: String`
  - `strict: bool`（当前大量工具是 `strict: false`）
  - `defer_loading: Option<bool>`（可选）
  - `parameters: JsonSchema`（关键：工具入参 JSON Schema）
  - `output_schema: Option<Value>`（`#[serde(skip)]`，即**不会随 tools 一起发到 API**，主要留作内部用途/未来扩展）

**4) parameters 的 JsonSchema 类型（subset）**

- `JsonSchema`（`core/src/tools/spec.rs:273`）
  - 是一个“足够覆盖工具入参”的 JSON Schema 子集
  - 典型对象 schema：
    - `type="object"`
    - `properties: BTreeMap<String, JsonSchema>`
    - `required: Option<Vec<String>>`
    - `additionalProperties`（注意：字段名是 `additionalProperties`，由 `#[serde(rename = "additionalProperties")]` 控制）

**5) tool_choice / parallel_tool_calls 的赋值位置**

- 构造请求：`ModelClientSession::build_responses_request`（`core/src/client.rs:492`）
  - `tools = create_tools_json_for_responses_api(&prompt.tools)?`
  - `tool_choice = "auto".to_string()`（当前固定值）
  - `parallel_tool_calls = prompt.parallel_tool_calls`

**补充：Azure Responses Endpoint 的 item id 回填**

- `attach_item_ids`（`codex-api/src/requests/responses.rs:11`）会把部分 `ResponseItem` 的 `id` 字段补回到最终 JSON（例如 `function_call` / `tool_search_call` 等）
- 调用点：`ResponsesClient::stream_request`（`codex-api/src/endpoint/responses.rs:45`）里，在 Azure responses endpoint 且 `request.store==true` 时触发


**二、响应：模型输出里的 function_call / tool_call 如何解析**

**1) SSE 事件解析：output_item.done → ResponseItem**

- 入口：`process_responses_event`（`codex-api/src/sse/responses.rs:231`）
  - 当 `event.kind == "response.output_item.done"`：
    - 取 `event.item`（JSON）
    - `serde_json::from_value::<ResponseItem>(item_val)` 反序列化成协议层类型

**2) Responses API 输出 item 的统一模型：ResponseItem（tag=type）**

- `ResponseItem`（`protocol/src/models.rs:287`）
  - `#[serde(tag = "type", rename_all = "snake_case")]`
  - 工具调用核心变体：
    - `FunctionCall`（`protocol/src/models.rs:325`）
      - `name: String`
      - `namespace: Option<String>`（可选，用于命名空间/兼容）
      - `arguments: String`（**注意：是字符串，里面装 JSON**）
      - `call_id: String`（Responses API 用于关联 tool output）
    - `ToolSearchCall`（用于 tool_search 工具）
    - `CustomToolCall`（用于 custom/freeform 工具）
    - `LocalShellCall`（兼容旧路径，含 `call_id: Option<String>` + `id: Option<String>`）

**3) 从 ResponseItem → 内部 ToolCall（决定跑哪个工具）**

- `ToolRouter::build_tool_call`（`core/src/tools/router.rs:86`）
  - `ResponseItem::FunctionCall { name, namespace, arguments, call_id }`
    - 先走 `Session::parse_mcp_tool_name` 判断是否是 MCP 工具（`core/src/codex.rs:3861`）
    - 是 MCP：`ToolPayload::Mcp { server, tool, raw_arguments }`
    - 否则：`ToolPayload::Function { arguments }`
  - `ResponseItem::ToolSearchCall`：当 `execution=="client"` 时解析 `arguments`（这里是 JSON object，不是 string）

**4) arguments 解析方式：serde_json::from_str**

- `parse_arguments<T>`（`core/src/tools/handlers/mod.rs:65`）
  - 对 `function_call.arguments` 做 `serde_json::from_str(arguments)`
  - 失败时用 `FunctionCallError::RespondToModel(...)` 把错误文本回写给模型（让模型修正参数格式）


**三、回传：工具输出如何回传（function_call_output / tool output item）**

**1) 工具执行结果的内部表示：ResponseInputItem**

- `ResponseInputItem`（`protocol/src/models.rs:226`）
  - 关键变体：`FunctionCallOutput`（`protocol/src/models.rs:231`）
    - `call_id: String`
    - `output: FunctionCallOutputPayload`
  - 还包含：
    - `CustomToolCallOutput`
    - `ToolSearchOutput`
    - `McpToolCallOutput`（内部用；后续会归一化成 `function_call_output`）

**2) 工具输出如何变成“发回模型的 item”**

- `ToolOutput` trait（`core/src/tools/context.rs:80`）定义 `to_response_item(&self, call_id, payload) -> ResponseInputItem`
- `function_tool_response`（`core/src/tools/context.rs:360`）
  - 根据 payload 类型选择：
    - 普通函数工具：`ResponseInputItem::FunctionCallOutput`
    - custom/freeform：`ResponseInputItem::CustomToolCallOutput`
  - 并对 output 做“字符串 vs content_items”两种编码优化

**3) MCP tool output 的归一化**

- `impl From<ResponseInputItem> for ResponseItem`（`protocol/src/models.rs:895`）
  - `ResponseInputItem::McpToolCallOutput { call_id, output }`
    - 会先把 MCP 的 `CallToolResult` 转成 `FunctionCallOutputPayload`
    - 然后产出 `ResponseItem::FunctionCallOutput { call_id, output }`
  - 结论：**最终发回 Responses API 的仍然是标准 `function_call_output`**

**4) function_call_output.output 的“线协议编码”**

- `FunctionCallOutputPayload`（`protocol/src/models.rs:1162`）
  - `body: FunctionCallOutputBody` + `success: Option<bool>`
  - 但：`success` 是内部元数据，**不会序列化到 wire**
- 自定义序列化：`impl Serialize for FunctionCallOutputPayload`（`protocol/src/models.rs:1242`）
  - 当 `body=Text(String)` → `output` 在线上是 **字符串**
  - 当 `body=ContentItems(Vec<...>)` → `output` 在线上是 **数组**
- content items 类型：`FunctionCallOutputContentItem`（`protocol/src/models.rs:1093`）
  - `type="input_text"` / `type="input_image"`（snake_case）

**5) 工具输出进入下一次请求：追加到会话历史 input**

- 工具调用检测与排队：
  - `handle_output_item_done`（`core/src/stream_events_utils.rs:158`）识别 `function_call`，并把工具执行 future 放入 `in_flight`
- 工具执行完成后写入历史：
  - `drain_in_flight`（`core/src/codex.rs:6889`）把 `ResponseInputItem` 转成 `ResponseItem`（`.into()`）并 `record_conversation_items(...)`
- 下一轮采样请求会读取更新后的 history，作为 `ResponsesApiRequest.input` 发送（`core/src/client.rs:492`）


**四、parallel_tool_calls：请求字段与运行时并行控制**

**1) 请求字段**

- `parallel_tool_calls: bool`（`codex-api/src/common.rs:144`）
- 构建位置：`core/src/client.rs:492`（来自 `prompt.parallel_tool_calls`）
- 模型能力标记：`supports_parallel_tool_calls: bool`（`protocol/src/openai_models.rs:259`，也会在 core 的 ModelInfo 中出现）

**2) 运行时并行控制（按工具粒度）**

- `ToolCallRuntime::handle_tool_call`（`core/src/tools/parallel.rs:50`）
  - 通过 `router.tool_supports_parallel(&call.tool_name)` 判断“这个工具能否并行”
  - 并用 `RwLock`：
    - 可并行工具拿 read lock（多个可同时运行）
    - 不可并行工具拿 write lock（与其他工具互斥）


**五、最小 JSON 形状示例（便于对照抓包/日志）**

**1) 请求：tools / tool_choice / parallel_tool_calls**

```json
{
  "model": "gpt-5.2-codex",
  "instructions": "...",
  "input": [],
  "tools": [
    {
      "type": "function",
      "name": "exec_command",
      "description": "Runs a command in a PTY, returning output or a session ID for ongoing interaction.",
      "strict": false,
      "parameters": {
        "type": "object",
        "properties": {
          "cmd": { "type": "string" }
        },
        "required": ["cmd"],
        "additionalProperties": false
      }
    }
  ],
  "tool_choice": "auto",
  "parallel_tool_calls": false
}
```

**2) 响应 item：function_call**

```json
{
  "type": "function_call",
  "name": "exec_command",
  "arguments": "{\"cmd\":\"ls -la\"}",
  "call_id": "call_abc123"
}
```

**3) 回传 input item：function_call_output**

```json
{
  "type": "function_call_output",
  "call_id": "call_abc123",
  "output": "Process exited with code 0\nOutput:\n..."
}
```


**六、m3652api 当前工具调用解决方案（2026-03-13 更新）**

> 这一节描述的是本仓库 `internal/provider/m365` 的实际落地方案，用于把只返回文本的 M365 Copilot Chat 输出，稳定转换为 Codex 可识别的 Responses 工具调用 item。

### 6.1 设计目标

1. 在不依赖上游原生 function calling 的前提下，稳定触发 `function_call` / `custom_tool_call` / `local_shell_call`。
2. 支持 `apply_patch` 这类大段多行输入，避免因 JSON 转义导致调用失败。
3. 降低“模型口头声称已完成、但未执行工具”的假成功场景。
4. 对 Codex 的 `tools` 入参保持更高兼容（Responses 风格与 ChatCompletions 风格）。

### 6.2 协议层：V2 定界符协议（主路径）+ V1 JSON（兜底）

当前实现采用 **V2 定界符协议**作为主路径，允许一次回复包含多个工具调用块：

- 块开始：`ꆈ龘ᐅ`
- 块结束：`ᐊ龘ꆈ`
- 工具名：`ꊰ▸<tool_name>◂ꊰ`
- function/local_shell 参数：`ꊰ▹{...}◃ꊰ`（必须是 JSON object）
- custom 原始输入：`ꊰ⟪<raw_input>⟫ꊰ`（保留多行原文）

仍保留 V1 JSON 兜底：

```json
{"type":"tool_call","tool":"<tool_name>","arguments":{...}}
{"type":"tool_call","tool":"<tool_name>","input":"..."}
```

### 6.3 解析与执行链路（m3652api）

1. 候选检测：
   - 若包含 V2 起始定界符，或文本以 `{` / markdown 代码围栏开头，则视为工具候选。
2. 解析顺序：
   - 先走 V2 多 block 解析（支持同一轮多个工具调用）。
   - 失败再走 V1 JSON 解析。
3. item 组装：
   - 对每个解析结果生成独立 `call_id`，构建对应 `function_call` / `custom_tool_call` / `local_shell_call` item。
4. 输出行为：
   - 流式：依次发多个 `response.output_item.done`，最后 `response.completed`。
   - 非流式：`output[]` 中可包含多个工具 item。
5. 文本清洗：
   - 给客户端展示 assistant 文本时会移除工具协议块，避免把协议原文当自然语言回显。

### 6.4 关键稳健性增强

1. **强制修复回合（force repair）**
   - 当用户请求明显属于“有副作用任务”（创建/修改/删除文件、执行命令、应用补丁等），即便模型未输出工具块，也会触发修复回合，要求模型补发工具调用块。
   - 用于修复“无报错但没有实际执行”的场景。

2. **repairToolCall 升级**
   - 修复请求提示改为 V2 定界符协议，不再只要求单 JSON。
   - 返回值升级为多 item（`[]any`），与主执行路径一致。

3. **assistant 消息过滤**
   - 抽取消息时优先识别 assistant-like role（`assistant/model/copilot`），减少误取 user/system 文本导致的误判。

4. **arguments 双重编码修复**
   - `function_call.arguments` 若本身是 JSON 字符串，校验有效后直接透传。
   - 非字符串参数才做序列化，避免 `"{\"cmd\":\"ls\"}"` 变成被再次转义的字符串。

### 6.5 tools 入参兼容策略（m3652api）

`parseOpenAITools` 同时兼容两类输入：

1. Responses 风格（顶层）：
   - `type` / `name` / `description` / `parameters`
2. ChatCompletions 风格（嵌套）：
   - `type="function"` 且 `function.name` / `function.description` / `function.parameters`

此外保留内置工具名回退逻辑（如 `local_shell`）。

### 6.6 当前已覆盖的回归测试点

1. V2 function/custom/multi-block 解析。
2. V1 JSON fallback 解析。
3. function arguments 为 JSON string 时不二次编码。
4. Responses 风格 tools 解析。
5. ChatCompletions 风格 tools 解析。
6. 有副作用任务触发强制修复策略。

### 6.7 现阶段边界说明

1. 上游 M365 本质仍是文本对话接口，工具调用能力来自代理协议与解析器，不是上游原生结构化 tool call。
2. V2 协议已显著降低失败率，但极端情况下仍可能受到上游文本输出质量影响。
3. 若要进一步提高可观测性，可在后续增加“工具协议调试日志”（仅记录协议片段与解析状态，不记录敏感业务数据）。
