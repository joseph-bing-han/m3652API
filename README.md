# m3652api

将 **Microsoft 365 Copilot Chat（Microsoft Graph beta）** 转换为 **OpenAI Responses API（`/v1/responses`）** 兼容接口，供 **Codex CLI** 作为“模型提供方”调用。

本项目面向“本地单用户代理”落地形态，重点支持：

- HTTP SSE 流式输出（不强依赖 WebSocket）
- 工具调用闭环（function/custom/local_shell）
- 图片输入降级（OCR 识别 → 文本注入给上游）
- `GET /v1/models` 兼容模型别名列表（用于客户端探测与配置，不会切换微软上游真实模型）


## 前置条件

1. 账号与许可
   - 仅支持 **工作/学校账号**（Delegated 权限）
   - 调用用户需要具备 **Microsoft 365 Copilot 许可证**
2. Graph API
   - 上游使用 `https://graph.microsoft.com/beta/copilot/...`（beta，随时可能变更）
3. M365 凭据
   - 需要在 `config.yaml` 的 `m365` 段提供：
     - `tenant-id`
     - `client-id`
     - `client-secret`
   - 由于 Copilot Chat API **仅支持 Delegated**，首次使用需要进行一次浏览器授权（见“快速开始”）
4. OCR（必需）
   - 需要本机安装 `tesseract`
   - 本项目只支持 `data:` 形式的图片（例如 `data:image/png;base64,...`），不默认下载远程 URL
5. Go toolchain
   - 由于依赖 `CLIProxyAPI/v6`，本仓库 `go.mod` 使用 `go 1.26.0`（建议使用支持 toolchain 的 Go 环境）


## 快速开始

1. 准备配置

```bash
cp config.example.yaml config.yaml
```

编辑 `config.yaml`，至少设置：

- `api-keys`：给 Codex CLI 使用的 API key
- `auth-dir`：token 存储目录
- `host/port`：建议仅监听 `localhost`
- `m365.tenant-id` / `m365.client-id` / `m365.client-secret`

2. 编译

```bash
go build -o m3652api ./cmd/m3652api
```

3. 启动服务

```bash
./m3652api serve -c config.yaml
```

服务会暴露 OpenAI 兼容接口（`/v1/...`）以及健康检查：

- `GET /healthz`

4. 首次授权（必须）

Copilot Chat API 只接受 **Delegated 用户令牌**。本项目不使用 device code，也不在命令行交互登录，而是通过本机浏览器完成授权码登录，并把 `refresh_token` 写入 `auth-dir`。

1) 在 Entra ID 应用中配置 Redirect URI（Web 平台）：

- `http://localhost:8217/m365/oauth/callback`

2) 启动服务后，用浏览器打开：

- `http://localhost:8217/m365/oauth/start`

完成登录后可检查状态：

- `http://localhost:8217/m365/oauth/status`

当 `has_refresh_token=true` 时，说明已具备持续调用上游的能力。

如需快速验证上游 Graph 权限与 Copilot Chat 可用性（会创建一个 conversation）：

- `http://localhost:8217/m365/upstream/check`


## 配置 Codex CLI

最简单方式是使用环境变量：

```bash
export OPENAI_BASE_URL="http://localhost:8217/v1"
export OPENAI_API_KEY="change-me"
```

然后正常运行 `codex`，并指定一个**兼容模型别名**，例如：

- `gpt-5.2-codex`
- `gpt-5.2`
- `gpt-5.3-codex`
- `gpt-5.4`

注意：

- 这些 `model` 值是**代理层兼容别名**，主要用于适配 Codex CLI / OpenAI 兼容生态。
- 当前实现里，所有兼容模型别名都会映射到**相同的 Microsoft Graph Copilot Chat API** 调用路径。
- `reasoning.effort` 会被兼容接收，但**不会影响微软上游请求**，当前实现会直接忽略它。
- `response.model` 仍会返回兼容层模型标签，用于维持 OpenAI 兼容接口行为；它不代表微软上游真实模型。

提示：当 `OPENAI_BASE_URL` 指向非官方 OpenAI 地址时，Codex CLI 的“读取模型列表”行为可能依赖服务端对 `/v1/models` 的实现，本项目提供的是**兼容性模型别名列表**，以提高客户端接入兼容性。


## OCR 说明

- 代理会把 `input_image` 的 `image_url` 解码并执行 OCR，再把结果注入到上游请求的 `additionalContext` 中。
- 如果 OCR 失败（例如未安装 `tesseract`、缺少语言包、图片过大），也会注入明确错误文本，避免 silent drop。
- 如需中文识别，请安装对应语言包（例如 `chi_sim`），并确保 `tesseract --list-langs` 能看到它，然后在 `config.yaml` 中设置 `m365.ocr-langs: "eng+chi_sim"`。


## 重要限制与安全提示

- 上游 M365 Chat API 为 beta，不承诺稳定；遇到字段/行为变更需要快速修复。
- 工具调用是“代理侧编排”：上游通过文本协议输出工具调用 JSON，Codex CLI 执行后把输出回填给上游继续推理。
- 请只在受信任的本机环境运行，并将服务绑定到 `localhost`，避免把工具执行能力暴露到公网。
