package m365

import "strings"

func normalizeModelLabel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "gpt-5.2-codex"
	}

	// 允许带 provider 前缀的模型名（例如 "m365/gpt-5.2"）。
	if i := strings.LastIndex(model, "/"); i >= 0 && i+1 < len(model) {
		model = model[i+1:]
	}

	switch strings.ToLower(strings.TrimSpace(model)) {
	case "m365-copilot-fast", "gpt-5.2-codex":
		return "gpt-5.2-codex"
	case "m365-copilot-deep", "gpt-5.2":
		return "gpt-5.2"
	case "gpt-5.3-codex":
		return "gpt-5.3-codex"
	case "gpt-5.4":
		return "gpt-5.4"
	case "m365-copilot":
		return "gpt-5.2-codex"
	default:
		// 未知模型名：原样透传，仅作为兼容层标签使用。
		return model
	}
}
