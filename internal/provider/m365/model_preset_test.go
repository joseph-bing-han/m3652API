package m365

import "testing"

func TestNormalizeModelLabel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty uses default compatibility label", input: "", want: "gpt-5.2-codex"},
		{name: "canonical gpt-5.2-codex", input: "gpt-5.2-codex", want: "gpt-5.2-codex"},
		{name: "canonical gpt-5.2", input: "gpt-5.2", want: "gpt-5.2"},
		{name: "canonical gpt-5.3-codex", input: "gpt-5.3-codex", want: "gpt-5.3-codex"},
		{name: "canonical gpt-5.4", input: "gpt-5.4", want: "gpt-5.4"},
		{name: "legacy fast alias", input: "m365-copilot-fast", want: "gpt-5.2-codex"},
		{name: "legacy deep alias", input: "m365-copilot-deep", want: "gpt-5.2"},
		{name: "legacy generic alias", input: "m365-copilot", want: "gpt-5.2-codex"},
		{name: "provider prefix is stripped", input: "m365/gpt-5.2", want: "gpt-5.2"},
		{name: "unknown model is preserved", input: "custom-model", want: "custom-model"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeModelLabel(tc.input)
			if got != tc.want {
				t.Fatalf("normalizeModelLabel(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
