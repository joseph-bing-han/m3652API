package m365

import (
	"context"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestDecodeDataURLImage_PNG(t *testing.T) {
	// 1x1 透明 PNG。
	b64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO6q7i0AAAAASUVORK5CYII="
	u := "data:image/png;base64," + b64
	mime, data, err := decodeDataURLImage(u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mime != "image/png" {
		t.Fatalf("unexpected mime: %q", mime)
	}
	if len(data) == 0 {
		t.Fatalf("expected non-empty bytes")
	}
}

func TestDecodeDataURLImage_Invalid(t *testing.T) {
	if _, _, err := decodeDataURLImage("data:text/plain;base64,AAAA"); err == nil {
		t.Fatalf("expected error for non-image mime")
	}
	if _, _, err := decodeDataURLImage("data:image/png,AAAA"); err == nil {
		t.Fatalf("expected error for missing base64 flag")
	}
}

func TestBuildOCRResults_RemoteURLRejected(t *testing.T) {
	e := &Executor{}
	a := &coreauth.Auth{Metadata: map[string]any{}}
	out := e.buildOCRResults(context.Background(), a, []string{"https://example.com/a.png"})
	if len(out) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out))
	}
	if !strings.Contains(out[0], "remote URLs are not allowed") {
		t.Fatalf("unexpected output: %q", out[0])
	}
}
