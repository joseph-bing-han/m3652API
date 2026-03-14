package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"

	"github.com/joseph/m3652api/internal/oauthstate"
	"github.com/joseph/m3652api/internal/provider/m365"
)

const versionFileName = "version.txt"

var appVersion = loadVersion()

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// 当未提供子命令时，默认执行 `serve -c ./config.yaml`。
	// 这样 `go run cmd/m3652api/main.go` 可以开箱即用。
	if len(os.Args) < 2 {
		serve(nil)
		return
	}

	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "version", "-v", "--version":
		printVersion()
	case "help", "-h", "--help":
		printUsage()
	default:
		log.Printf("Unknown subcommand: %s", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	_, _ = fmt.Fprintf(os.Stderr, `m3652api - Microsoft 365 Copilot Chat proxy (OpenAI Responses compatible)
Version: %s

Usage:
  m3652api serve -c config.yaml
  m3652api version

Notes:
  - M365 credentials are loaded from config.yaml under m365.*.
  - The server exposes OpenAI-compatible endpoints under /v1 (Responses API).
`, currentVersion())
}

func printVersion() {
	_, _ = fmt.Fprintf(os.Stdout, "m3652api version: %s\n", currentVersion())
}

func currentVersion() string {
	return appVersion
}

func loadVersion() string {
	// 优先从当前工作目录读取，方便本地开发或容器挂载。
	if version, ok := readVersionFromPath(versionFileName); ok {
		return version
	}

	// 退回到可执行文件目录读取，方便发布时随二进制一同分发。
	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
		if version, ok := readVersionFromPath(filepath.Join(execDir, versionFileName)); ok {
			return version
		}
	}

	return "unknown"
}

func readVersionFromPath(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	version := strings.TrimSpace(string(data))
	if version == "" {
		return "", false
	}
	return version, true
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("c", "config.yaml", "Path to config.yaml")
	_ = fs.Parse(args)

	absConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		log.Fatalf("Resolve config path failed: %v", err)
	}

	cfg, err := config.LoadConfig(absConfigPath)
	if err != nil {
		log.Fatalf("Load config failed: %v", err)
	}
	runtimeCfg, err := m365.LoadRuntimeConfig(absConfigPath)
	if err != nil {
		log.Fatalf("Load m365 runtime config failed: %v", err)
	}

	tokenStore := sdkAuth.GetTokenStore()
	if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok {
		dirSetter.SetBaseDir(cfg.AuthDir)
	}

	core := coreauth.NewManager(tokenStore, nil, nil)
	core.SetConfig(cfg)

	exec := m365.NewExecutor(cfg)
	core.RegisterExecutor(exec)

	// 先加载已存在的 auth，便于稳定地对旧条目做更新/禁用。
	if err := core.Load(context.Background()); err != nil {
		log.Printf("Load auth store failed (continuing): %v", err)
	}
	if _, err := m365.EnsureStaticAuth(context.Background(), core, runtimeCfg, cfg.ProxyURL); err != nil {
		log.Fatalf("Bootstrap m365 auth failed: %v", err)
	}
	m365.RegisterModels(core)

	oauthStates := oauthstate.NewStore(10 * time.Minute)

	hooks := cliproxy.Hooks{
		OnAfterStart: func(_ *cliproxy.Service) {
			m365.RegisterModels(core)
		},
	}

	svc, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(absConfigPath).
		WithCoreAuthManager(core).
		// 禁用文件监听：CLIProxyAPI 的 watcher 会把未知 provider 视为 OpenAI-compat，
		// 进而覆盖自定义 executor，并清空我们注册到全局模型表的模型映射（例如 m365）。
		WithWatcherFactory(func(string, string, func(*config.Config)) (*cliproxy.WatcherWrapper, error) {
			return nil, nil
		}).
		WithServerOptions(
			api.WithRouterConfigurator(func(e *gin.Engine, _ *handlers.BaseAPIHandler, _ *config.Config) {
				e.GET("/healthz", func(c *gin.Context) { c.String(200, "ok") })

				// OAuth 授权码流程端点（仅委派 delegated）。
				// 访问 /m365/oauth/start 会跳转到微软登录页；成功后回调 /m365/oauth/callback 并写入 refresh_token。
				e.GET("/m365/oauth/start", func(c *gin.Context) {
					redirectURI := strings.TrimSpace(runtimeCfg.RedirectURI)
					if redirectURI == "" {
						redirectURI = fmt.Sprintf("http://localhost:%d/m365/oauth/callback", cfg.Port)
					}
					prompt := strings.TrimSpace(c.Query("prompt"))
					if prompt == "" {
						prompt = strings.TrimSpace(runtimeCfg.AuthorizePrompt)
					}

					state := uuid.NewString()
					verifier, challenge, err := oauthstate.NewPKCE()
					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
						return
					}

					oauthStates.Put(state, oauthstate.Pending{
						AuthID:       runtimeCfg.AuthID,
						RedirectURI:  redirectURI,
						Scopes:       runtimeCfg.DelegatedScopes,
						CodeVerifier: verifier,
					})

					authURL, err := m365.BuildAuthorizeURL(runtimeCfg.TenantID, runtimeCfg.ClientID, redirectURI, runtimeCfg.DelegatedScopes, state, challenge, prompt)
					if err != nil {
						c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
						return
					}

					if c.Query("json") == "1" {
						c.JSON(http.StatusOK, gin.H{"authorize_url": authURL, "redirect_uri": redirectURI})
						return
					}
					c.Redirect(http.StatusFound, authURL)
				})

				e.GET("/m365/oauth/callback", func(c *gin.Context) {
					if errCode := strings.TrimSpace(c.Query("error")); errCode != "" {
						desc := strings.TrimSpace(c.Query("error_description"))
						if desc == "" {
							desc = errCode
						}
						c.String(http.StatusBadRequest, "OAuth error: %s", desc)
						return
					}

					code := strings.TrimSpace(c.Query("code"))
					state := strings.TrimSpace(c.Query("state"))
					if code == "" || state == "" {
						c.String(http.StatusBadRequest, "Missing code/state in callback.")
						return
					}

					pending, ok := oauthStates.Pop(state)
					if !ok {
						c.String(http.StatusBadRequest, "OAuth state is missing or expired. Please restart from /m365/oauth/start.")
						return
					}

					httpClient := m365.NewHTTPClientForProxyURL(cfg.ProxyURL)
					_, err := m365.ApplyAuthorizationCodeToAuth(
						c.Request.Context(),
						core,
						pending.AuthID,
						httpClient,
						runtimeCfg.TenantID,
						runtimeCfg.ClientID,
						runtimeCfg.ClientSecret,
						pending.RedirectURI,
						code,
						pending.CodeVerifier,
						pending.Scopes,
					)
					if err != nil {
						c.String(http.StatusInternalServerError, "OAuth token exchange failed: %v", err)
						return
					}

					c.String(http.StatusOK, "M365 OAuth completed. You can close this window.")
				})

				e.GET("/m365/oauth/status", func(c *gin.Context) {
					a, ok := core.GetByID(runtimeCfg.AuthID)
					if !ok || a == nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": "m365 auth not found"})
						return
					}

					redirectURIHint := strings.TrimSpace(runtimeCfg.RedirectURI)
					if redirectURIHint == "" {
						redirectURIHint = fmt.Sprintf("http://localhost:%d/m365/oauth/callback", cfg.Port)
					}

					var refreshToken string
					var expiresAt int64
					var obtainedAt int64
					var scope string
					imageUploadScopeConfigured := false
					imageUploadTokenScopeReady := false
					sharePointSiteConfigured := strings.EqualFold(strings.TrimSpace(runtimeCfg.ImageUpload.Target), "sharepoint") &&
						strings.TrimSpace(runtimeCfg.ImageUpload.SharePointHostname) != "" &&
						strings.TrimSpace(runtimeCfg.ImageUpload.SharePointSitePath) != ""

					for _, delegatedScope := range runtimeCfg.DelegatedScopes {
						if strings.EqualFold(strings.TrimSpace(delegatedScope), m365.ImageUploadRequiredScope) {
							imageUploadScopeConfigured = true
							break
						}
					}

					if md := a.Metadata; md != nil {
						if tokenMap, ok := md["token"].(map[string]any); ok && tokenMap != nil {
							if v, ok := tokenMap["refresh_token"].(string); ok {
								refreshToken = strings.TrimSpace(v)
							}
							if v, ok := tokenMap["expires_at"].(int64); ok {
								expiresAt = v
							} else if v, ok := tokenMap["expires_at"].(float64); ok {
								expiresAt = int64(v)
							}
							if v, ok := tokenMap["obtained_at"].(int64); ok {
								obtainedAt = v
							} else if v, ok := tokenMap["obtained_at"].(float64); ok {
								obtainedAt = int64(v)
							}
							if v, ok := tokenMap["scope"].(string); ok {
								scope = strings.TrimSpace(v)
							}
						}
					}

					if imageUploadScopeConfigured {
						for _, tokenScope := range strings.Fields(scope) {
							if strings.EqualFold(strings.TrimSpace(tokenScope), m365.ImageUploadRequiredScope) {
								imageUploadTokenScopeReady = true
								break
							}
						}
					}

					c.JSON(http.StatusOK, gin.H{
						"auth_id":                     a.ID,
						"provider":                    a.Provider,
						"has_refresh_token":           refreshToken != "",
						"token_expires_at":            expiresAt,
						"token_obtained_at":           obtainedAt,
						"token_scope":                 scope,
						"redirect_uri_hint":           redirectURIHint,
						"delegated_scopes":            runtimeCfg.DelegatedScopes,
						"app_scopes":                  runtimeCfg.Scopes,
						"authorize_endpoint":          fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/authorize", runtimeCfg.TenantID),
						"image_upload_enabled":        runtimeCfg.ImageUpload.Enabled,
						"image_upload_target":         runtimeCfg.ImageUpload.Target,
						"image_upload_required_scope": m365.ImageUploadRequiredScope,
						"image_upload_scope_ready":    runtimeCfg.ImageUpload.Enabled && sharePointSiteConfigured && imageUploadScopeConfigured && imageUploadTokenScopeReady,
						"sharepoint_site_configured":  sharePointSiteConfigured,
					})
				})

				// 上游连通性自检（可选）：验证 delegated token 与 Copilot Chat API 是否可用。
				e.GET("/m365/upstream/check", func(c *gin.Context) {
					a, ok := core.GetByID(runtimeCfg.AuthID)
					if !ok || a == nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": "m365 auth not found"})
						return
					}

					result := gin.H{}

					meReq, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, "https://graph.microsoft.com/v1.0/me", nil)
					meReq.Header.Set("Accept", "application/json")
					meResp, err := exec.HttpRequest(c.Request.Context(), a, meReq)
					if err != nil {
						c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
						return
					}
					meBody, _ := io.ReadAll(io.LimitReader(meResp.Body, 1<<20))
					_ = meResp.Body.Close()
					result["me_status"] = meResp.StatusCode
					if meResp.StatusCode < 200 || meResp.StatusCode >= 300 {
						result["me_error"] = strings.TrimSpace(string(meBody))
					}

					copilotEnabled := true
					if v := strings.TrimSpace(c.Query("copilot")); v != "" && (v == "0" || strings.EqualFold(v, "false")) {
						copilotEnabled = false
					}

					if copilotEnabled {
						body := []byte("{}")
						convReq, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, "https://graph.microsoft.com/beta/copilot/conversations", bytes.NewReader(body))
						convReq.Header.Set("Accept", "application/json")
						convReq.Header.Set("Content-Type", "application/json")
						convResp, err := exec.HttpRequest(c.Request.Context(), a, convReq)
						if err != nil {
							c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "me_status": result["me_status"]})
							return
						}
						convBody, _ := io.ReadAll(io.LimitReader(convResp.Body, 2<<20))
						_ = convResp.Body.Close()
						result["copilot_create_status"] = convResp.StatusCode
						if convResp.StatusCode >= 200 && convResp.StatusCode < 300 {
							var parsed struct {
								ID string `json:"id"`
							}
							if err := json.Unmarshal(convBody, &parsed); err == nil && strings.TrimSpace(parsed.ID) != "" {
								result["conversation_id"] = strings.TrimSpace(parsed.ID)
							}
						} else {
							result["copilot_create_error"] = strings.TrimSpace(string(convBody))
						}
					}

					c.JSON(http.StatusOK, result)
				})
			}),
		).
		WithHooks(hooks).
		Build()
	if err != nil {
		log.Fatalf("Build service failed: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("Starting server version=%s config=%s auth_dir=%s", currentVersion(), absConfigPath, cfg.AuthDir)
	if err := svc.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("Server exited with error: %v", err)
	}
	log.Printf("Server stopped")
}
