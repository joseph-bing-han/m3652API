package m365

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	maxImageBytes      = 10 << 20
	ocrTimeoutPerImage = 30 * time.Second
)

func (e *Executor) buildOCRResults(ctx context.Context, a *coreauth.Auth, imageURLs []string) []string {
	if len(imageURLs) == 0 {
		return nil
	}
	langs := strings.TrimSpace(anyStringFromMap(a, "ocr_langs"))
	if langs == "" {
		langs = "eng"
	}

	results := make([]string, 0, len(imageURLs))
	for i, u := range imageURLs {
		idx := i + 1
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, "data:") {
			results = append(results, fmt.Sprintf("Image #%d OCR failed: remote URLs are not allowed", idx))
			continue
		}
		mime, data, err := decodeDataURLImage(u)
		if err != nil {
			results = append(results, fmt.Sprintf("Image #%d OCR failed: %s", idx, err.Error()))
			continue
		}
		ext := extForMime(mime)
		if ext == "" {
			ext = ".img"
		}
		text, err := runTesseractOCR(ctx, data, ext, langs)
		if err != nil {
			results = append(results, fmt.Sprintf("Image #%d OCR failed: %s", idx, err.Error()))
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			results = append(results, fmt.Sprintf("Image #%d OCR text:\n", idx))
			continue
		}
		results = append(results, fmt.Sprintf("Image #%d OCR text:\n%s", idx, text))
	}
	return results
}

func anyStringFromMap(a *coreauth.Auth, key string) string {
	if a == nil || a.Metadata == nil {
		return ""
	}
	if v, ok := a.Metadata[key].(string); ok {
		return v
	}
	return ""
}

func decodeDataURLImage(dataURL string) (mime string, data []byte, err error) {
	// 期望格式：data:image/png;base64,AAAA...
	dataURL = strings.TrimSpace(dataURL)
	if !strings.HasPrefix(dataURL, "data:") {
		return "", nil, errors.New("not a data url")
	}
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return "", nil, errors.New("invalid data url: missing comma")
	}
	meta := dataURL[len("data:"):comma]
	payload := dataURL[comma+1:]

	if !strings.Contains(meta, ";base64") {
		return "", nil, errors.New("invalid data url: base64 is required")
	}
	semi := strings.Index(meta, ";")
	if semi < 0 {
		return "", nil, errors.New("invalid data url: missing mime type")
	}
	mime = strings.TrimSpace(meta[:semi])
	if !strings.HasPrefix(mime, "image/") {
		return "", nil, errors.New("unsupported data url mime type")
	}

	// 解码 base64（同时兼容带 padding 与不带 padding 的形式）。
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return "", nil, errors.New("empty base64 payload")
	}
	var decoded []byte
	if b, err := base64.StdEncoding.DecodeString(payload); err == nil {
		decoded = b
	} else if b, err := base64.RawStdEncoding.DecodeString(payload); err == nil {
		decoded = b
	} else {
		return "", nil, errors.New("base64 decode failed")
	}
	if len(decoded) > maxImageBytes {
		return "", nil, errors.New("image too large")
	}
	return mime, decoded, nil
}

func extForMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/tiff":
		return ".tiff"
	default:
		return ""
	}
}

func runTesseractOCR(ctx context.Context, img []byte, ext, langs string) (string, error) {
	if len(img) == 0 {
		return "", errors.New("empty image bytes")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	imgCtx, cancel := context.WithTimeout(ctx, ocrTimeoutPerImage)
	defer cancel()

	tmp, err := os.CreateTemp("", "m3652api-ocr-*"+ext)
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(path)
	}()
	if _, err := tmp.Write(img); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	// 确保工作目录安全。
	wd := filepath.Dir(path)

	args := []string{path, "stdout"}
	if strings.TrimSpace(langs) != "" {
		args = append(args, "-l", langs)
	}
	cmd := exec.CommandContext(imgCtx, "tesseract", args...)
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return "", fmt.Errorf("tesseract failed: %s", truncateForError(text, 512))
	}
	return text, nil
}

func truncateForError(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
