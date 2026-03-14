package m365

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	clipexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestDecodeDataURLImage_RejectsRemoteURL(t *testing.T) {
	_, _, err := decodeDataURLImage("https://example.com/image.png")
	if err == nil {
		t.Fatal("expected decode error")
	}

	var authErr *coreauth.Error
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *coreauth.Error, got %T", err)
	}
	if authErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", authErr.HTTPStatus)
	}
}

func TestExecute_RejectsImageInputWhenUploadDisabled(t *testing.T) {
	raw := []byte(`{
  "prompt_cache_key": "image-session",
  "model": "gpt-5.2",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [
        {"type": "input_text", "text": "describe this"},
        {"type": "input_image", "image_url": {"url": "data:image/png;base64,AAAA"}}
      ]
    }
  ]
}`)

	auth := testImageUploadAuth(false, true)
	e := NewExecutor(nil)
	_, err := e.Execute(context.Background(), auth, clipexec.Request{}, clipexec.Options{OriginalRequest: raw})
	if err == nil {
		t.Fatal("expected execute to fail")
	}

	var authErr *coreauth.Error
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *coreauth.Error, got %T", err)
	}
	if authErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", authErr.HTTPStatus)
	}
	if authErr.Code != "invalid_request_error" {
		t.Fatalf("unexpected code: %q", authErr.Code)
	}
}

func TestExecute_RejectsImageInputWhenUploadScopeMissing(t *testing.T) {
	raw := []byte(`{
  "prompt_cache_key": "image-session-scope",
  "model": "gpt-5.2",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [
        {"type": "input_text", "text": "describe this"},
        {"type": "input_image", "image_url": {"url": "data:image/png;base64,AAAA"}}
      ]
    }
  ]
}`)

	auth := testImageUploadAuth(true, false)
	e := NewExecutor(nil)
	_, err := e.Execute(context.Background(), auth, clipexec.Request{}, clipexec.Options{OriginalRequest: raw})
	if err == nil {
		t.Fatal("expected execute to fail")
	}

	var authErr *coreauth.Error
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *coreauth.Error, got %T", err)
	}
	if authErr.HTTPStatus != http.StatusForbidden {
		t.Fatalf("unexpected status: %d", authErr.HTTPStatus)
	}
}

func TestExecuteStream_RejectsImageInputBeforeStreamingStartsWhenUploadDisabled(t *testing.T) {
	raw := []byte(`{
  "prompt_cache_key": "image-session-stream",
  "model": "gpt-5.2",
  "stream": true,
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [
        {"type": "input_text", "text": "describe this"},
        {"type": "input_image", "image_url": {"url": "data:image/png;base64,AAAA"}}
      ]
    }
  ]
}`)

	auth := testImageUploadAuth(false, true)
	e := NewExecutor(nil)
	result, err := e.ExecuteStream(context.Background(), auth, clipexec.Request{}, clipexec.Options{OriginalRequest: raw})
	if err == nil {
		t.Fatal("expected execute stream to fail")
	}
	if result != nil {
		t.Fatal("expected no stream result on upload validation failure")
	}

	var authErr *coreauth.Error
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *coreauth.Error, got %T", err)
	}
	if authErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", authErr.HTTPStatus)
	}
}

func testImageUploadAuth(uploadEnabled, includeRequiredScope bool) *coreauth.Auth {
	delegatedScopes := []string{"openid", "profile"}
	scopeValue := "openid profile"
	if includeRequiredScope {
		delegatedScopes = append(delegatedScopes, ImageUploadRequiredScope)
		scopeValue += " " + ImageUploadRequiredScope
	}

	return &coreauth.Auth{
		Metadata: map[string]any{
			"tenant_id":        "tenant-id",
			"client_id":        "client-id",
			"client_secret":    "client-secret",
			"timezone":         "Pacific/Auckland",
			"delegated_scopes": delegatedScopes,
			"token": map[string]any{
				"access_token": "token",
				"scope":        scopeValue,
				"expires_at":   time.Now().Add(10 * time.Minute).Unix(),
			},
			"image_upload": map[string]any{
				"enabled":              uploadEnabled,
				"target":               imageUploadTargetSharePoint,
				"sharepoint_hostname":  "contoso.sharepoint.com",
				"sharepoint_site_path": "/sites/Engineering",
			},
		},
	}
}
