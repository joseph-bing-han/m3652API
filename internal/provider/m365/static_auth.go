package m365

import (
	"context"
	"errors"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func EnsureStaticAuth(ctx context.Context, core *coreauth.Manager, runtime RuntimeConfig, proxyURL string) (*coreauth.Auth, error) {
	if core == nil {
		return nil, errors.New("m365 auth bootstrap: auth manager is nil")
	}
	authID := strings.TrimSpace(runtime.AuthID)
	if authID == "" {
		return nil, errors.New("m365 auth bootstrap: auth-id is empty")
	}

	upsert := func(a *coreauth.Auth) *coreauth.Auth {
		if a == nil {
			a = &coreauth.Auth{}
		}
		cloned := a.Clone()
		if cloned == nil {
			cloned = &coreauth.Auth{}
		}

		cloned.ID = authID
		cloned.FileName = authID
		cloned.Provider = providerKey
		cloned.Status = coreauth.StatusActive
		cloned.Disabled = false
		cloned.ProxyURL = strings.TrimSpace(proxyURL)

		if cloned.Metadata == nil {
			cloned.Metadata = make(map[string]any)
		}
		// 注意：这里必须保留既有的 token/refresh_token 等动态字段，否则重启会丢登录态。
		cloned.Metadata["type"] = providerKey
		cloned.Metadata["tenant_id"] = runtime.TenantID
		cloned.Metadata["client_id"] = runtime.ClientID
		cloned.Metadata["client_secret"] = runtime.ClientSecret
		cloned.Metadata["timezone"] = runtime.TimeZone
		cloned.Metadata["scopes"] = runtime.Scopes
		cloned.Metadata["delegated_scopes"] = runtime.DelegatedScopes
		if strings.TrimSpace(runtime.OCRLangs) != "" {
			cloned.Metadata["ocr_langs"] = runtime.OCRLangs
		}
		return cloned
	}

	if existing, ok := core.GetByID(authID); ok && existing != nil {
		_, _ = core.Update(ctx, upsert(existing))
	} else {
		_, _ = core.Register(ctx, upsert(nil))
	}

	disableOtherM365Auths(ctx, core, authID)

	out, _ := core.GetByID(authID)
	if out == nil {
		return nil, errors.New("m365 auth bootstrap: auth not found after register/update")
	}
	return out, nil
}

func disableOtherM365Auths(ctx context.Context, core *coreauth.Manager, keepAuthID string) {
	if core == nil {
		return
	}
	for _, a := range core.List() {
		if a == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(a.Provider), providerKey) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(a.ID), strings.TrimSpace(keepAuthID)) {
			continue
		}
		if a.Disabled {
			continue
		}
		cloned := a.Clone()
		cloned.Disabled = true
		_, _ = core.Update(ctx, cloned)
	}
}
