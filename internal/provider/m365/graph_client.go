package m365

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"golang.org/x/net/proxy"
)

const graphBaseURL = "https://graph.microsoft.com"

// NewHTTPClientForProxyURL 根据 proxy URL 构建 HTTP 客户端。
// 支持 http/https/socks5；为空或非法时返回 http.DefaultClient。
func NewHTTPClientForProxyURL(proxyURL string) *http.Client {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return http.DefaultClient
	}
	pu, err := url.Parse(proxyURL)
	if err != nil {
		return http.DefaultClient
	}

	switch strings.ToLower(strings.TrimSpace(pu.Scheme)) {
	case "http", "https":
		tr := &http.Transport{Proxy: http.ProxyURL(pu)}
		return &http.Client{Transport: tr}
	case "socks5":
		dialer, err := proxy.FromURL(pu, proxy.Direct)
		if err != nil {
			return http.DefaultClient
		}
		tr := &http.Transport{
			DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}
		return &http.Client{Transport: tr}
	default:
		return http.DefaultClient
	}
}

func (e *Executor) httpClientForAuth(a *coreauth.Auth) *http.Client {
	if a == nil {
		return NewHTTPClientForProxyURL("")
	}
	return NewHTTPClientForProxyURL(a.ProxyURL)
}

func (e *Executor) createConversation(ctx context.Context, a *coreauth.Auth, accessToken string) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("create conversation: access token is empty")
	}
	u := graphBaseURL + "/beta/copilot/conversations"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := e.httpClientForAuth(a).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("create conversation failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	var out m365ConversationCreateResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("create conversation response unmarshal failed: %w", err)
	}
	if strings.TrimSpace(out.ID) == "" {
		return "", errors.New("create conversation response missing id")
	}
	return strings.TrimSpace(out.ID), nil
}

type sseEvent struct {
	Data []byte
	ID   string
}

func (e *Executor) chatOverStream(ctx context.Context, a *coreauth.Auth, accessToken, conversationID string, payload m365ChatOverStreamRequest) (*http.Response, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, errors.New("chatOverStream: conversationID is empty")
	}
	if strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("chatOverStream: access token is empty")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	u := graphBaseURL + "/beta/copilot/conversations/" + url.PathEscape(conversationID) + "/chatOverStream"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := e.httpClientForAuth(a).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("chatOverStream failed: status=%d body=%s", resp.StatusCode, string(body))
	}
	return resp, nil
}

func readSSEStream(ctx context.Context, r io.Reader, onEvent func(ev sseEvent) bool) error {
	if r == nil {
		return errors.New("sse reader is nil")
	}
	if onEvent == nil {
		return errors.New("sse onEvent is nil")
	}

	br := bufio.NewReaderSize(r, 64*1024)
	var dataBuf bytes.Buffer
	var id string

	flush := func() bool {
		if dataBuf.Len() == 0 {
			id = ""
			return true
		}
		ev := sseEvent{Data: bytes.TrimSpace(dataBuf.Bytes()), ID: strings.TrimSpace(id)}
		dataBuf.Reset()
		id = ""
		return onEvent(ev)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = flush()
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if !flush() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "id:") {
			id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if v != "" {
				if dataBuf.Len() > 0 {
					dataBuf.WriteByte('\n')
				}
				dataBuf.WriteString(v)
			}
			continue
		}
		// 忽略其他字段（event:、retry: 等）。
	}
}

func withTimeoutIfNone(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, has := ctx.Deadline(); has {
		return ctx, func() {}
	}
	if d <= 0 {
		d = 120 * time.Second
	}
	return context.WithTimeout(ctx, d)
}
