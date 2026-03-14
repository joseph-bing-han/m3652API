package m365

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestPrepareUploadedTurnFiles_SharePointFlowAndCleanup(t *testing.T) {
	auth := testImageUploadAuth(true, true)
	dataURL := "data:image/png;base64,AAAA"

	var mu sync.Mutex
	uploadCount := 0
	var deletedIDs []string
	var authHeader string
	var contentType string
	var uploadBodyLen int
	var unexpectedRequest string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.0/sites/contoso.sharepoint.com:/sites/Engineering":
			authHeader = strings.TrimSpace(r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "site-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1.0/sites/site-1/drive/special/approot":
			authHeader = strings.TrimSpace(r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "approot-1"})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1.0/sites/site-1/drive/special/approot:/uploads/") && strings.HasSuffix(r.URL.Path, ":/content"):
			authHeader = strings.TrimSpace(r.Header.Get("Authorization"))
			contentType = strings.TrimSpace(r.Header.Get("Content-Type"))
			body, _ := io.ReadAll(r.Body)
			uploadBodyLen = len(body)
			uploadCount++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     "item-" + string(rune('0'+uploadCount)),
				"webUrl": "https://contoso.sharepoint.com/sites/Engineering/Shared%20Documents/item",
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1.0/sites/site-1/drive/items/"):
			authHeader = strings.TrimSpace(r.Header.Get("Authorization"))
			deletedIDs = append(deletedIDs, strings.TrimPrefix(r.URL.Path, "/v1.0/sites/site-1/drive/items/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			unexpectedRequest = r.Method + " " + r.URL.String()
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() {
		graphBaseURL = previousBaseURL
	}()

	e := NewExecutor(nil)
	uploaded, err := e.prepareUploadedTurnFiles(context.Background(), auth, "token", []string{dataURL, dataURL})
	if err != nil {
		t.Fatalf("prepareUploadedTurnFiles returned error: %v", err)
	}

	e.cleanupUploadedTurnFiles(nil, auth, "token", uploaded)

	mu.Lock()
	defer mu.Unlock()

	if unexpectedRequest != "" {
		t.Fatalf("unexpected request: %s", unexpectedRequest)
	}
	if authHeader != "Bearer token" {
		t.Fatalf("unexpected authorization header: %q", authHeader)
	}
	if contentType != "image/png" {
		t.Fatalf("unexpected content type: %q", contentType)
	}
	if uploadBodyLen == 0 {
		t.Fatal("expected upload body to be present")
	}
	if uploadCount != 2 {
		t.Fatalf("expected 2 uploads, got %d", uploadCount)
	}
	if len(uploaded.Files) != 2 {
		t.Fatalf("expected 2 uploaded file URIs, got %d", len(uploaded.Files))
	}
	if len(uploaded.Uploaded) != 2 {
		t.Fatalf("expected 2 uploaded file refs, got %d", len(uploaded.Uploaded))
	}
	if len(deletedIDs) != 2 {
		t.Fatalf("expected 2 deletes during cleanup, got %d", len(deletedIDs))
	}
}

func TestPrepareUploadedTurnFiles_RollsBackOnUploadFailure(t *testing.T) {
	auth := testImageUploadAuth(true, true)
	dataURL := "data:image/png;base64,AAAA"

	var mu sync.Mutex
	uploadCount := 0
	var deletedIDs []string
	var unexpectedRequest string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.0/sites/contoso.sharepoint.com:/sites/Engineering":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "site-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1.0/sites/site-1/drive/special/approot":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "approot-1"})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1.0/sites/site-1/drive/special/approot:/uploads/") && strings.HasSuffix(r.URL.Path, ":/content"):
			uploadCount++
			if uploadCount == 1 {
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":     "item-1",
					"webUrl": "https://contoso.sharepoint.com/sites/Engineering/Shared%20Documents/item-1",
				})
				return
			}
			http.Error(w, "upload failed", http.StatusInternalServerError)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1.0/sites/site-1/drive/items/item-1":
			deletedIDs = append(deletedIDs, "item-1")
			w.WriteHeader(http.StatusNoContent)
		default:
			unexpectedRequest = r.Method + " " + r.URL.String()
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() {
		graphBaseURL = previousBaseURL
	}()

	e := NewExecutor(nil)
	_, err := e.prepareUploadedTurnFiles(context.Background(), auth, "token", []string{dataURL, dataURL})
	if err == nil {
		t.Fatal("expected prepareUploadedTurnFiles to fail")
	}

	var authErr *coreauth.Error
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *coreauth.Error, got %T", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if unexpectedRequest != "" {
		t.Fatalf("unexpected request: %s", unexpectedRequest)
	}
	if authErr.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("unexpected status: %d", authErr.HTTPStatus)
	}
	if authErr.Code != "upstream_error" {
		t.Fatalf("unexpected code: %q", authErr.Code)
	}
	if uploadCount != 2 {
		t.Fatalf("expected 2 upload attempts, got %d", uploadCount)
	}
	if len(deletedIDs) != 1 {
		t.Fatalf("expected rollback delete to run once, got %d", len(deletedIDs))
	}
}
