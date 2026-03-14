package m365

import (
	"strings"
	"testing"
)

func TestBuildAdditionalContext_IgnoresReasoningEffort(t *testing.T) {
	out := buildAdditionalContext("system", "", nil, nil)
	for _, item := range out {
		if strings.Contains(item.Text, "Reasoning effort:") {
			t.Fatalf("unexpected reasoning hint in additional context: %#v", item)
		}
		if item.Description == "Reasoning/verbosity" {
			t.Fatalf("unexpected legacy context description: %#v", item)
		}
	}
}

func TestBuildAdditionalContext_UsesVerbosityOnly(t *testing.T) {
	out := buildAdditionalContext("system", "verbose", nil, nil)

	foundOutputStyle := false
	for _, item := range out {
		if item.Description != "Output style" {
			continue
		}
		foundOutputStyle = true
		if !strings.Contains(item.Text, "Verbosity: verbose.") {
			t.Fatalf("unexpected output style text: %q", item.Text)
		}
	}

	if !foundOutputStyle {
		t.Fatalf("expected Output style context block")
	}
}

func TestBuildUpstreamPayload_IgnoresModelAndReasoningInputs(t *testing.T) {
	tools := []openAITool{{ToolType: "function", Name: "exec_command"}}
	toolOutputs := []string{"ok"}

	got := buildUpstreamPayload(
		"Do work",
		"Pacific/Auckland",
		true,
		"instructions",
		"low",
		tools,
		toolOutputs,
	)

	if got.Message.Text != "Do work" {
		t.Fatalf("unexpected user task: %q", got.Message.Text)
	}
	if got.LocationHint.TimeZone != "Pacific/Auckland" {
		t.Fatalf("unexpected timezone: %q", got.LocationHint.TimeZone)
	}
	if got.ContextualResource == nil || got.ContextualResource.WebContext == nil || !got.ContextualResource.WebContext.IsWebEnabled {
		t.Fatalf("expected web context to be enabled")
	}
	for _, item := range got.AdditionalContext {
		if strings.Contains(item.Text, "Reasoning effort:") {
			t.Fatalf("unexpected reasoning hint in upstream payload: %#v", item)
		}
		if item.Description == "Image OCR results" {
			t.Fatalf("unexpected OCR context block: %#v", item)
		}
	}
}
