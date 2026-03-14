package m365

import (
	"context"
	"sync"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type memoryAuthStore struct {
	mu    sync.Mutex
	items map[string]*coreauth.Auth
}

func (s *memoryAuthStore) List(context.Context) ([]*coreauth.Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*coreauth.Auth, 0, len(s.items))
	for _, a := range s.items {
		if a == nil {
			continue
		}
		out = append(out, a.Clone())
	}
	return out, nil
}

func (s *memoryAuthStore) Save(_ context.Context, a *coreauth.Auth) (string, error) {
	if a == nil || a.ID == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[string]*coreauth.Auth)
	}
	s.items[a.ID] = a.Clone()
	return a.ID, nil
}

func (s *memoryAuthStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items != nil {
		delete(s.items, id)
	}
	return nil
}

func TestEnsureStaticAuth_PreservesTokenMetadata(t *testing.T) {
	store := &memoryAuthStore{}
	core := coreauth.NewManager(store, nil, nil)

	existing := &coreauth.Auth{
		ID:       "m365-static.json",
		FileName: "m365-static.json",
		Provider: providerKey,
		Status:   coreauth.StatusActive,
		Disabled: false,
		Metadata: map[string]any{
			"tenant_id": "t",
			"client_id": "c",
			"token": map[string]any{
				"refresh_token": "rt",
				"access_token":  "at",
			},
		},
	}
	_, _ = core.Register(context.Background(), existing)

	runtime := RuntimeConfig{
		AuthID:          "m365-static.json",
		TenantID:        "t2",
		ClientID:        "c2",
		ClientSecret:    "s2",
		TimeZone:        "Pacific/Auckland",
		Scopes:          []string{"https://graph.microsoft.com/.default"},
		DelegatedScopes: []string{"openid", "profile", "offline_access"},
	}

	out, err := EnsureStaticAuth(context.Background(), core, runtime, "")
	if err != nil {
		t.Fatalf("EnsureStaticAuth returned error: %v", err)
	}
	if out == nil || out.Metadata == nil {
		t.Fatalf("expected auth metadata to exist")
	}
	tokenMap, _ := out.Metadata["token"].(map[string]any)
	if tokenMap == nil {
		t.Fatalf("expected token map to be preserved")
	}
	if tokenMap["refresh_token"] != "rt" {
		t.Fatalf("expected refresh_token to be preserved, got %v", tokenMap["refresh_token"])
	}
	if out.Metadata["client_secret"] != "s2" {
		t.Fatalf("expected client_secret to be updated, got %v", out.Metadata["client_secret"])
	}
}
