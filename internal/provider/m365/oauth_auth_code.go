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

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func BuildAuthorizeURL(tenantID, clientID, redirectURI string, delegatedScopes []string, state, codeChallenge, prompt string) (string, error) {
	tenantID = strings.TrimSpace(tenantID)
	clientID = strings.TrimSpace(clientID)
	redirectURI = strings.TrimSpace(redirectURI)
	state = strings.TrimSpace(state)
	codeChallenge = strings.TrimSpace(codeChallenge)
	prompt = strings.TrimSpace(prompt)

	if tenantID == "" || clientID == "" || redirectURI == "" {
		return "", errors.New("authorize url: tenant_id, client_id, redirect_uri are required")
	}

	scopes := sanitizeScopes(delegatedScopes)
	if len(scopes) == 0 {
		scopes = defaultDelegatedScopes()
	}

	q := url.Values{
		"client_id":     {clientID},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"response_mode": {"query"},
		"scope":         {strings.Join(scopes, " ")},
	}
	if state != "" {
		q.Set("state", state)
	}
	if codeChallenge != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	if prompt != "" {
		q.Set("prompt", prompt)
	}

	return fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/authorize?%s", url.PathEscape(tenantID), q.Encode()), nil
}

func ApplyAuthorizationCodeToAuth(
	ctx context.Context,
	core *coreauth.Manager,
	authID string,
	httpClient *http.Client,
	tenantID, clientID, clientSecret, redirectURI, code, codeVerifier string,
	delegatedScopes []string,
) (*coreauth.Auth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if core == nil {
		return nil, errors.New("oauth callback: auth manager is nil")
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil, errors.New("oauth callback: authID is empty")
	}

	a, ok := core.GetByID(authID)
	if !ok || a == nil {
		return nil, fmt.Errorf("oauth callback: auth %s not found", authID)
	}

	scopes := sanitizeScopes(delegatedScopes)
	if len(scopes) == 0 {
		scopes = defaultDelegatedScopes()
	}

	token, err := tokenByAuthorizationCode(ctx, httpClient, tenantID, clientID, clientSecret, redirectURI, code, codeVerifier, scopes)
	if err != nil {
		return nil, err
	}

	expiresAt := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix()
	cloned := a.Clone()
	if cloned.Metadata == nil {
		cloned.Metadata = make(map[string]any)
	}
	cloned.Metadata["delegated_scopes"] = scopes
	persistTokenMetadata(cloned, token, expiresAt)

	out, err := core.Update(ctx, cloned)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func tokenByAuthorizationCode(ctx context.Context, httpClient *http.Client, tenantID, clientID, clientSecret, redirectURI, code, codeVerifier string, scopes []string) (*tokenResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	tenantID = strings.TrimSpace(tenantID)
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	redirectURI = strings.TrimSpace(redirectURI)
	code = strings.TrimSpace(code)
	codeVerifier = strings.TrimSpace(codeVerifier)
	if tenantID == "" || clientID == "" || clientSecret == "" || redirectURI == "" || code == "" {
		return nil, errors.New("authorization code token request: tenant_id, client_id, client_secret, redirect_uri, code are required")
	}
	scopes = sanitizeScopes(scopes)

	u := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", url.PathEscape(tenantID))
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
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
		return nil, fmt.Errorf("authorization code token request failed: status=%d error=%s", resp.StatusCode, errText)
	}

	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("authorization code token response unmarshal failed: %w", err)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return nil, errors.New("authorization code token response missing access_token")
	}
	return &out, nil
}
