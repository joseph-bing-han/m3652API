package m365

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func RegisterModels(core *coreauth.Manager) {
	if core == nil {
		return
	}

	models := []*cliproxy.ModelInfo{
		{ID: "gpt-5.2-codex", Object: "model", Type: providerKey, DisplayName: "GPT-5.2 Codex (M365 Compatible Alias)"},
		{ID: "gpt-5.2", Object: "model", Type: providerKey, DisplayName: "GPT-5.2 (M365 Compatible Alias)"},
		{ID: "gpt-5.3-codex", Object: "model", Type: providerKey, DisplayName: "GPT-5.3 Codex (M365 Compatible Alias)"},
		{ID: "gpt-5.4", Object: "model", Type: providerKey, DisplayName: "GPT-5.4 (M365 Compatible Alias)"},
	}

	for _, a := range core.List() {
		if a == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(a.Provider), providerKey) {
			continue
		}
		if a.Disabled {
			continue
		}
		cliproxy.GlobalModelRegistry().RegisterClient(a.ID, providerKey, models)
		core.RefreshSchedulerEntry(a.ID)
	}
}
