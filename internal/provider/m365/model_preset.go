package m365

import "strings"

type modelPreset struct {
	ModelID                string
	DefaultReasoningEffort string
}

func resolveModelPreset(model string) modelPreset {
	model = strings.TrimSpace(model)
	if model == "" {
		return modelPreset{ModelID: "gpt-5.2-codex", DefaultReasoningEffort: "low"}
	}

	// 允许带 provider 前缀的模型名（例如 "m365/gpt-5.2"）。
	if i := strings.LastIndex(model, "/"); i >= 0 && i+1 < len(model) {
		model = model[i+1:]
	}

	switch strings.ToLower(strings.TrimSpace(model)) {
	case "m365-copilot-fast", "gpt-5.2-codex":
		return modelPreset{ModelID: "gpt-5.2-codex", DefaultReasoningEffort: "low"}
	case "m365-copilot-deep", "gpt-5.2":
		return modelPreset{ModelID: "gpt-5.2", DefaultReasoningEffort: "high"}
	case "gpt-5.3-codex":
		return modelPreset{ModelID: "gpt-5.3-codex", DefaultReasoningEffort: "low"}
	case "gpt-5.4":
		return modelPreset{ModelID: "gpt-5.4", DefaultReasoningEffort: "high"}
	case "m365-copilot":
		return modelPreset{ModelID: "gpt-5.2-codex", DefaultReasoningEffort: "low"}
	default:
		// 未知模型名：原样透传，不强制设置默认思考等级。
		return modelPreset{ModelID: model}
	}
}
