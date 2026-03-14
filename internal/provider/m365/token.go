package m365

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type authInfo struct {
	TenantID        string
	ClientID        string
	ClientSecret    string
	TimeZone        string
	AppScopes       []string
	DelegatedScopes []string
}

type tokenInfo struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	ExpiresAt    int64
}

func parseAuthInfo(a *coreauth.Auth) (authInfo, tokenInfo, error) {
	if a == nil {
		return authInfo{}, tokenInfo{}, errors.New("auth is nil")
	}
	md := a.Metadata
	if md == nil {
		return authInfo{}, tokenInfo{}, errors.New("auth metadata is nil")
	}

	ai := authInfo{
		TenantID:     strings.TrimSpace(anyString(md["tenant_id"])),
		ClientID:     strings.TrimSpace(anyString(md["client_id"])),
		ClientSecret: strings.TrimSpace(anyString(md["client_secret"])),
		TimeZone:     strings.TrimSpace(anyString(md["timezone"])),
	}

	ai.AppScopes = anyStringSlice(md["scopes"])
	ai.DelegatedScopes = anyStringSlice(md["delegated_scopes"])

	ti := tokenInfo{}
	tokenMap, _ := md["token"].(map[string]any)
	if tokenMap != nil {
		ti.AccessToken = strings.TrimSpace(anyString(tokenMap["access_token"]))
		ti.RefreshToken = strings.TrimSpace(anyString(tokenMap["refresh_token"]))
		ti.TokenType = strings.TrimSpace(anyString(tokenMap["token_type"]))
		ti.Scope = strings.TrimSpace(anyString(tokenMap["scope"]))
		ti.ExpiresAt = anyInt64(tokenMap["expires_at"])
	}
	if ti.AccessToken == "" {
		ti.AccessToken = strings.TrimSpace(anyString(md["access_token"]))
	}
	if ti.RefreshToken == "" {
		ti.RefreshToken = strings.TrimSpace(anyString(md["refresh_token"]))
	}
	if ti.ExpiresAt == 0 {
		ti.ExpiresAt = anyInt64(md["expires_at"])
	}

	if ai.TenantID == "" || ai.ClientID == "" {
		return authInfo{}, tokenInfo{}, errors.New("missing tenant_id/client_id in auth metadata")
	}
	if ai.TimeZone == "" {
		ai.TimeZone = time.Now().Location().String()
	}

	return ai, ti, nil
}

func (e *Executor) ensureAccessToken(ctx context.Context, a *coreauth.Auth) (authInfo, string, error) {
	ai, ti, err := parseAuthInfo(a)
	if err != nil {
		return authInfo{}, "", err
	}

	now := time.Now().Unix()
	if ti.AccessToken != "" && (ti.ExpiresAt == 0 || now < ti.ExpiresAt-60) {
		return ai, ti.AccessToken, nil
	}

	httpClient := http.DefaultClient
	if e != nil {
		httpClient = e.httpClientForAuth(a)
	}

	// 优先使用委派令牌：Copilot Chat API 只接受 delegated。
	if strings.TrimSpace(ti.RefreshToken) != "" {
		scopes := ai.DelegatedScopes
		if len(scopes) == 0 {
			scopes = defaultDelegatedScopes()
		}
		refreshed, err := refreshWithRefreshToken(ctx, httpClient, ai.TenantID, ai.ClientID, ai.ClientSecret, ti.RefreshToken, scopes)
		if err != nil {
			return authInfo{}, "", err
		}
		expiresAt := time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second).Unix()
		persistTokenMetadata(a, refreshed, expiresAt)
		persistAuthBestEffort(ctx, a)
		return ai, strings.TrimSpace(refreshed.AccessToken), nil
	}

	return authInfo{}, "", errors.New("no delegated refresh_token found; open /m365/oauth/start in your browser to sign in")
}

func persistTokenMetadata(a *coreauth.Auth, token *tokenResponse, expiresAt int64) {
	if a == nil || token == nil {
		return
	}
	if a.Metadata == nil {
		a.Metadata = make(map[string]any)
	}
	tokenMap, _ := a.Metadata["token"].(map[string]any)
	if tokenMap == nil {
		tokenMap = make(map[string]any)
		a.Metadata["token"] = tokenMap
	}
	tokenMap["access_token"] = token.AccessToken
	if strings.TrimSpace(token.RefreshToken) != "" {
		tokenMap["refresh_token"] = token.RefreshToken
	}
	if strings.TrimSpace(token.TokenType) != "" {
		tokenMap["token_type"] = token.TokenType
	}
	if strings.TrimSpace(token.Scope) != "" {
		tokenMap["scope"] = token.Scope
	}
	tokenMap["expires_in"] = token.ExpiresIn
	tokenMap["expires_at"] = expiresAt
	tokenMap["obtained_at"] = time.Now().Unix()
}

func persistAuthBestEffort(ctx context.Context, a *coreauth.Auth) {
	if a == nil {
		return
	}
	store := sdkAuth.GetTokenStore()
	if store != nil {
		_, _ = store.Save(ctx, a)
	}
}

func tokenByClientSecret(ctx context.Context, httpClient *http.Client, tenantID, clientID, clientSecret string, scopes []string) (*tokenResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	tenantID = strings.TrimSpace(tenantID)
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if tenantID == "" || clientID == "" || clientSecret == "" {
		return nil, errors.New("client credentials token request: tenant_id, client_id, client_secret are required")
	}
	if len(scopes) == 0 {
		scopes = []string{"https://graph.microsoft.com/.default"}
	}

	u := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", url.PathEscape(tenantID))
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"scope":         {strings.Join(scopes, " ")},
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
		var tr tokenResponse
		_ = json.Unmarshal(body, &tr)
		errText := strings.TrimSpace(tr.ErrorDescription)
		if errText == "" {
			errText = strings.TrimSpace(string(body))
		}
		return nil, fmt.Errorf("client credentials token request failed: status=%d error=%s", resp.StatusCode, errText)
	}

	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("client credentials token response unmarshal failed: %w", err)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return nil, errors.New("client credentials token response missing access_token")
	}
	return &out, nil
}

func refreshWithRefreshToken(ctx context.Context, httpClient *http.Client, tenantID, clientID, clientSecret, refreshToken string, scopes []string) (*tokenResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("refresh_token is empty")
	}
	clientSecret = strings.TrimSpace(clientSecret)

	u := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", url.PathEscape(tenantID))
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
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
		var tr tokenResponse
		_ = json.Unmarshal(body, &tr)
		errText := strings.TrimSpace(tr.ErrorDescription)
		if errText == "" {
			errText = strings.TrimSpace(string(body))
		}
		return nil, fmt.Errorf("refresh token request failed: status=%d error=%s", resp.StatusCode, errText)
	}

	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("refresh token response unmarshal failed: %w", err)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return nil, errors.New("refresh token response missing access_token")
	}
	return &out, nil
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}

func anyInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case json.Number:
		i, _ := t.Int64()
		return i
	default:
		return 0
	}
}

func anyStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return sanitizeScopes(t)
	case []any:
		out := make([]string, 0, len(t))
		for _, it := range t {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return sanitizeScopes(out)
	default:
		return nil
	}
}

func anyBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	default:
		return false
	}
}
