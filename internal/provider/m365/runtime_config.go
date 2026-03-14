package m365

import (
	"errors"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type RuntimeConfig struct {
	AuthID          string   `yaml:"auth-id"`
	TenantID        string   `yaml:"tenant-id"`
	ClientID        string   `yaml:"client-id"`
	ClientSecret    string   `yaml:"client-secret"`
	TimeZone        string   `yaml:"timezone"`
	Scopes          []string `yaml:"scopes"`
	DelegatedScopes []string `yaml:"delegated-scopes"`
	RedirectURI     string   `yaml:"redirect-uri"`
	AuthorizePrompt string   `yaml:"authorize-prompt"`
}

type runtimeConfigFile struct {
	M365 RuntimeConfig `yaml:"m365"`
}

func LoadRuntimeConfig(configPath string) (RuntimeConfig, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return RuntimeConfig{}, errors.New("m365 config: config path is empty")
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return RuntimeConfig{}, err
	}

	var f runtimeConfigFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return RuntimeConfig{}, err
	}

	cfg := f.M365
	cfg.AuthID = strings.TrimSpace(cfg.AuthID)
	cfg.TenantID = strings.TrimSpace(cfg.TenantID)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.ClientSecret = strings.TrimSpace(cfg.ClientSecret)
	cfg.TimeZone = strings.TrimSpace(cfg.TimeZone)
	cfg.Scopes = sanitizeScopes(cfg.Scopes)
	cfg.DelegatedScopes = sanitizeScopes(cfg.DelegatedScopes)
	cfg.RedirectURI = strings.TrimSpace(cfg.RedirectURI)
	cfg.AuthorizePrompt = strings.TrimSpace(cfg.AuthorizePrompt)

	if cfg.AuthID == "" {
		cfg.AuthID = "m365-static"
	}
	if !strings.HasSuffix(strings.ToLower(cfg.AuthID), ".json") {
		cfg.AuthID += ".json"
	}
	if cfg.TimeZone == "" {
		cfg.TimeZone = time.Now().Location().String()
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"https://graph.microsoft.com/.default"}
	}
	if len(cfg.DelegatedScopes) == 0 {
		cfg.DelegatedScopes = defaultDelegatedScopes()
	}
	if cfg.TenantID == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return RuntimeConfig{}, errors.New("m365 config: tenant-id, client-id, client-secret are required")
	}

	return cfg, nil
}
