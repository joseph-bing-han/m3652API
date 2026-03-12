# 仓库指南

## 项目结构与模块组织
- `cmd/m3652api/`：CLI 入口与启动逻辑（`serve` 子命令、HTTP 路由、认证管理器装配）。
- `internal/provider/m365/`：核心适配层，包含模型映射、请求转换、SSE 输出、工具调用编排、OCR 及上游 Graph 交互。
- `internal/oauthstate/`：可复用的 OAuth state 与 PKCE 辅助逻辑。
- `auth/`：运行期认证数据与错误日志目录，属于本地状态，不应作为业务数据源。
- `docs/`：设计说明与调研文档。
- 根目录关键文件：`config.example.yaml`（模板）、`config.yaml`（本地配置）、`go.mod`、`README.md`。

## 构建、测试与开发命令
- `go build -o m3652api ./cmd/m3652api`：构建本地可执行文件。
- `go run ./cmd/m3652api serve -c config.yaml`：使用本地配置启动服务。
- `go test ./...`：运行全量单元测试。
- `go test ./internal/provider/m365 -run TestToolCall`：仅运行工具调用相关测试。
- `go test -cover ./internal/provider/m365`：查看核心适配模块覆盖率。

## 代码风格与命名规范
- 提交前必须执行 `gofmt`，保持 Go 标准格式。
- 命名遵循 Go 约定：导出标识符使用 PascalCase，内部辅助函数使用 camelCase。
- 包名保持简短小写；按功能分组文件（如 `responses_*`、`oauth_*`、`tool_*`）。
- 面向外部协议的字符串与日志键名保持稳定并使用英文，确保 API 兼容性。

## 测试规范
- 测试文件与实现文件同目录放置，使用 `_test.go` 后缀。
- 优先使用表驱动测试覆盖解析/转换逻辑与工具调用边界场景。
- 同时覆盖成功路径与降级路径（重点关注 OCR 失败、Token 刷新与认证初始化）。
- 提交 PR 前确保 `go test ./...` 全部通过。

## 提交与 Pull Request 规范
- 当前目录快照未包含 `.git` 历史，默认采用 Conventional Commits（示例：`feat(m365): add oauth status endpoint`）。
- 每次提交聚焦单一变更点，保持原子性，避免混入无关修改。
- PR 需说明：变更目的、核心改动、配置影响、测试证据、回滚方案。
- 若修改 `/v1/*` 行为，请附最小可复现请求示例并关联对应 issue。

## 安全与配置建议
- 除非明确需要远程访问，服务应仅绑定 `localhost`。
- 严禁提交 `config.yaml` 与 `auth/` 中的密钥、Token 或其他敏感数据。
- 新增配置项时，同步更新 `config.example.yaml` 与 `README.md` 默认值说明。
