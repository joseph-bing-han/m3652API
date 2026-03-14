package m365

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type DeviceCodeLoginOptions struct {
	TenantID string
	ClientID string
	TimeZone string
	Label    string
	Scopes   []string
}

func LoginDeviceCode(ctx context.Context, store coreauth.Store, opts DeviceCodeLoginOptions) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return "", errors.New("m365 login: store is nil")
	}
	opts.TenantID = strings.TrimSpace(opts.TenantID)
	opts.ClientID = strings.TrimSpace(opts.ClientID)
	opts.TimeZone = strings.TrimSpace(opts.TimeZone)
	opts.Label = strings.TrimSpace(opts.Label)

	if opts.TenantID == "" {
		return "", errors.New("m365 login: tenant_id is empty")
	}
	if opts.ClientID == "" {
		return "", errors.New("m365 login: client_id is empty")
	}
	if opts.TimeZone == "" {
		opts.TimeZone = time.Now().Location().String()
	}

	scopes := sanitizeScopes(opts.Scopes)
	if len(scopes) == 0 {
		scopes = defaultDelegatedScopes()
	}

	deviceCode, err := requestDeviceCode(ctx, http.DefaultClient, opts.TenantID, opts.ClientID, scopes)
	if err != nil {
		return "", err
	}

	// device code 的 message 是微软返回的用户指引文本。
	// 该文本保持原样（微软通常返回英文）。
	fmt.Fprintln(io.Discard) // 保留 fmt 依赖，便于未来把输出重定向给调用方
	fmt.Printf("%s\n", strings.TrimSpace(deviceCode.Message))

	token, err := pollDeviceCodeToken(ctx, http.DefaultClient, opts.TenantID, opts.ClientID, deviceCode, scopes)
	if err != nil {
		return "", err
	}

	now := time.Now()
	expiresAt := now.Add(time.Duration(token.ExpiresIn) * time.Second).Unix()

	fileName := fmt.Sprintf("m365-%s.json", uuid.NewString())
	metadata := map[string]any{
		"type":      providerKey,
		"label":     opts.Label,
		"tenant_id": opts.TenantID,
		"client_id": opts.ClientID,
		"timezone":  opts.TimeZone,
		"scopes":    scopes,
		"token": map[string]any{
			"access_token":  token.AccessToken,
			"refresh_token": token.RefreshToken,
			"token_type":    token.TokenType,
			"scope":         token.Scope,
			"expires_in":    token.ExpiresIn,
			"expires_at":    expiresAt,
			"obtained_at":   now.Unix(),
		},
	}

	auth := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: providerKey,
		Status:   coreauth.StatusActive,
		Disabled: false,
		Metadata: metadata,
	}

	path, err := store.Save(ctx, auth)
	if err != nil {
		return "", err
	}

	return path, nil
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Message         string `json:"message"`
}

type tokenResponse struct {
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func defaultDelegatedScopes() []string {
	return []string{
		"openid",
		"profile",
		"offline_access",
		"https://graph.microsoft.com/User.Read",
		"https://graph.microsoft.com/Sites.Read.All",
		"https://graph.microsoft.com/Mail.Read",
		"https://graph.microsoft.com/People.Read.All",
		"https://graph.microsoft.com/OnlineMeetingTranscript.Read.All",
		"https://graph.microsoft.com/Chat.Read",
		"https://graph.microsoft.com/ChannelMessage.Read.All",
		"https://graph.microsoft.com/ExternalItem.Read.All",
	}
}

func sanitizeScopes(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func requestDeviceCode(ctx context.Context, httpClient *http.Client, tenantID, clientID string, scopes []string) (*deviceCodeResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	u := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/devicecode", url.PathEscape(tenantID))
	form := url.Values{
		"client_id": {clientID},
		"scope":     {strings.Join(scopes, " ")},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("device code request failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	var out deviceCodeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("device code response unmarshal failed: %w", err)
	}
	if strings.TrimSpace(out.DeviceCode) == "" {
		return nil, fmt.Errorf("device code response missing device_code")
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return &out, nil
}

func pollDeviceCodeToken(ctx context.Context, httpClient *http.Client, tenantID, clientID string, dc *deviceCodeResponse, scopes []string) (*tokenResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if dc == nil {
		return nil, errors.New("token poll: device code is nil")
	}

	u := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", url.PathEscape(tenantID))
	interval := dc.Interval
	if interval <= 0 {
		interval = 5
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	for {
		if time.Now().After(deadline) {
			return nil, errors.New("token poll: device code expired")
		}

		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {clientID},
			"device_code": {dc.DeviceCode},
			"scope":       {strings.Join(scopes, " ")},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var tok tokenResponse
			if err := json.Unmarshal(body, &tok); err != nil {
				return nil, fmt.Errorf("token response unmarshal failed: %w", err)
			}
			if strings.TrimSpace(tok.AccessToken) == "" {
				return nil, fmt.Errorf("token response missing access_token")
			}
			return &tok, nil
		}

		var tok tokenResponse
		_ = json.Unmarshal(body, &tok)
		switch strings.TrimSpace(tok.Error) {
		case "authorization_pending":
			// 继续等待用户完成授权
		case "slow_down":
			interval += 5
		case "expired_token":
			return nil, errors.New("token poll: expired_token")
		case "access_denied":
			return nil, errors.New("token poll: access_denied")
		default:
			// 未知错误
			errText := strings.TrimSpace(tok.ErrorDescription)
			if errText == "" {
				errText = string(bytes.TrimSpace(body))
			}
			return nil, fmt.Errorf("token poll failed: status=%d error=%s", resp.StatusCode, errText)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
	}
}
