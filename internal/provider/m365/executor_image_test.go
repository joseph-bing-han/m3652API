package m365

import (
	"context"
	"errors"
	"net/http"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	clipexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestValidateNoImageInputs_RejectsImageRequests(t *testing.T) {
	err := validateNoImageInputs([]string{"data:image/png;base64,AAAA"})
	if err == nil {
		t.Fatal("expected image validation error")
	}

	var authErr *coreauth.Error
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *coreauth.Error, got %T", err)
	}
	if authErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", authErr.HTTPStatus)
	}
	if authErr.Message != unsupportedImageInputMessage {
		t.Fatalf("unexpected message: %q", authErr.Message)
	}
}

func TestExecute_RejectsImageInputBeforeAuth(t *testing.T) {
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

	e := NewExecutor(nil)
	_, err := e.Execute(context.Background(), nil, clipexec.Request{}, clipexec.Options{OriginalRequest: raw})
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
	assertSessionNotCreatedAfterImageRejection(t, e, "image-session")
}

func TestExecuteStream_RejectsImageInputBeforeStreamingStarts(t *testing.T) {
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

	e := NewExecutor(nil)
	result, err := e.ExecuteStream(context.Background(), nil, clipexec.Request{}, clipexec.Options{OriginalRequest: raw})
	if err == nil {
		t.Fatal("expected execute stream to fail")
	}
	if result != nil {
		t.Fatal("expected no stream result on image validation failure")
	}

	var authErr *coreauth.Error
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *coreauth.Error, got %T", err)
	}
	if authErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", authErr.HTTPStatus)
	}
	assertSessionNotCreatedAfterImageRejection(t, e, "image-session-stream")
}

func assertSessionNotCreatedAfterImageRejection(t *testing.T, e *Executor, sessionKey string) {
	t.Helper()

	if e == nil || e.sessions == nil {
		t.Fatal("expected session store to exist")
	}

	e.sessions.mu.Lock()
	defer e.sessions.mu.Unlock()

	if _, ok := e.sessions.sessions[sessionKey]; ok {
		t.Fatalf("expected session %q to not be created", sessionKey)
	}
}
