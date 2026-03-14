# m3652api

将 **Microsoft 365 Copilot Chat（Microsoft Graph beta）** 转换为 **OpenAI Responses API（`/v1/responses`）** 兼容接口，供 **Codex CLI** 作为“模型提供方”调用。

本项目面向“本地单用户代理”落地形态，重点支持：

- HTTP SSE 流式输出（不强依赖 WebSocket）
- 工具调用闭环（function/custom/local_shell）
- 纯文本请求转发到 Microsoft Graph Copilot Chat
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
4. Go toolchain
   - 由于依赖 `CLIProxyAPI/v6`，本仓库 `go.mod` 使用 `go 1.26.0`（建议使用支持 toolchain 的 Go 环境）


## 快速开始

1. 准备配置

```bash
cp config.example.yaml config.yaml
```

编辑 `config.yaml`，至少设置：

- `api-keys`：给 Codex CLI 使用的 API key
- `auth-dir`：token 存储目录
- `host/port`：本地裸跑建议监听 `localhost`；Docker 部署时应监听 `0.0.0.0`
- `m365.redirect-uri`：建议显式设置为 `http://localhost:8217/m365/oauth/callback`
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

## Docker 部署

仓库已提供 `Dockerfile` 与 `docker-compose.yml`，默认行为如下：

- 将当前项目编译进镜像
- 将宿主机 `./auth` 挂载到容器 `/app/auth`
- 将宿主机 `./config.yaml` 挂载到容器 `/app/config.yaml`
- 将容器 `8217` 端口仅映射到宿主机 `127.0.0.1:8217`

启动命令：

```bash
docker compose up -d --build
```

停止命令：

```bash
docker compose down
```

为了让 Docker 端口映射生效，同时继续满足 Microsoft 对 OAuth 回调地址的限制，`config.yaml` 里需要至少保证：

```yaml
host: "0.0.0.0"
port: 8217

m365:
  redirect-uri: "http://localhost:8217/m365/oauth/callback"
```

说明：

- 容器内服务需要监听 `0.0.0.0`，这样宿主机的端口转发才能连进容器
- 浏览器访问与 OAuth 回调地址仍然统一使用 `http://localhost:8217/...`
- 这是因为 Microsoft 对非 HTTPS 的 OAuth redirect URI 只接受 `http://localhost`
- `docker-compose.yml` 已将宿主机绑定限制为 `127.0.0.1`，不会默认暴露到局域网


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


## 图片输入

当前实现支持将 Codex CLI 传入的 `input_image` 转成 SharePoint app folder 文件，再通过 `contextualResources.files[].uri` 交给 Microsoft Graph Copilot Chat API。

限制与要求：

- 只支持 **`data:image/...;base64,...`** 形式的内联图片。
- **不支持** 远程 `http/https` 图片 URL，代理不会代替服务端抓取外部图片。
- 单张图片上限为 **10 MiB**。
- 需要在 `m365.image-upload` 中启用 SharePoint 上传配置：

```yaml
m365:
  image-upload:
    enabled: true
    target: "sharepoint"
    sharepoint-hostname: "contoso.sharepoint.com"
    sharepoint-site-path: "/sites/Engineering"
```

- 委派权限中必须包含：

```text
https://graph.microsoft.com/Files.ReadWrite.AppFolder
```

- 如果你是在已有配置上新增图片能力，更新 delegated scopes 后需要重新访问：

```text
http://localhost:8217/m365/oauth/start
```

图片处理流程：

1. 代理解析 `input_image`
2. 上传到配置好的 SharePoint site 默认 drive 的 `special/approot`
3. 取上传后文件的 `webUrl`
4. 注入到 `chatOverStream.contextualResources.files[]`
5. 请求完成后最佳努力删除临时上传文件

可通过以下状态接口检查是否具备图片上传条件：

- `image_upload_enabled`
- `image_upload_target`
- `image_upload_required_scope`
- `image_upload_scope_ready`
- `sharepoint_site_configured`

以上字段可在 `GET /m365/oauth/status` 中查看。


## 重要限制与安全提示

- 上游 M365 Chat API 为 beta，不承诺稳定；遇到字段/行为变更需要快速修复。
- 工具调用是“代理侧编排”：上游通过文本协议输出工具调用 JSON，Codex CLI 执行后把输出回填给上游继续推理。
- 请只在受信任的本机环境运行；本地裸跑建议绑定 `localhost`，Docker compose 默认仅绑定宿主机 `127.0.0.1`，避免把工具执行能力暴露到公网。
