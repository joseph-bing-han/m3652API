package m365

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	ImageUploadRequiredScope    = "https://graph.microsoft.com/Files.ReadWrite.AppFolder"
	imageUploadTargetSharePoint = "sharepoint"
	maxInlineImageBytes         = 10 << 20
)

type imageUploadConfig struct {
	Enabled            bool
	Target             string
	SharePointHostname string
	SharePointSitePath string
}

type uploadedTurnFiles struct {
	Files    []m365File
	Uploaded []uploadedFileRef
}

type uploadedFileRef struct {
	SiteID string
	ItemID string
	URI    string
}

type graphSite struct {
	ID string `json:"id"`
}

type graphDriveItem struct {
	ID     string `json:"id"`
	WebURL string `json:"webUrl"`
}

func parseImageUploadConfig(a *coreauth.Auth) imageUploadConfig {
	if a == nil || a.Metadata == nil {
		return imageUploadConfig{}
	}
	cfgMap, _ := a.Metadata["image_upload"].(map[string]any)
	if cfgMap == nil {
		return imageUploadConfig{}
	}
	return imageUploadConfig{
		Enabled:            anyBool(cfgMap["enabled"]),
		Target:             strings.ToLower(strings.TrimSpace(anyString(cfgMap["target"]))),
		SharePointHostname: strings.TrimSpace(anyString(cfgMap["sharepoint_hostname"])),
		SharePointSitePath: normalizeSharePointSitePath(anyString(cfgMap["sharepoint_site_path"])),
	}
}

func validateImageUploadConfig(cfg imageUploadConfig) error {
	if !cfg.Enabled {
		return buildImageUploadConfigError("input_image requires m365.image-upload.enabled=true and a configured SharePoint target")
	}
	if cfg.Target != imageUploadTargetSharePoint {
		return buildImageUploadConfigError("input_image requires m365.image-upload.target=sharepoint")
	}
	if strings.TrimSpace(cfg.SharePointHostname) == "" || strings.TrimSpace(cfg.SharePointSitePath) == "" {
		return buildImageUploadConfigError("input_image requires m365.image-upload.sharepoint-hostname and sharepoint-site-path")
	}
	return nil
}

func buildImageUploadConfigError(message string) error {
	return &coreauth.Error{
		Code:       "invalid_request_error",
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
	}
}

func buildImageUploadScopeError(message string) error {
	return &coreauth.Error{
		Code:       "forbidden",
		Message:    message,
		HTTPStatus: http.StatusForbidden,
	}
}

func buildImageUploadUpstreamError(message string) error {
	return &coreauth.Error{
		Code:       "upstream_error",
		Message:    message,
		Retryable:  true,
		HTTPStatus: http.StatusBadGateway,
	}
}

func scopeListContains(scopes []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, scope := range scopes {
		if strings.EqualFold(strings.TrimSpace(scope), target) {
			return true
		}
	}
	return false
}

func tokenScopeContains(scopeString, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, scope := range strings.Fields(scopeString) {
		if strings.EqualFold(strings.TrimSpace(scope), target) {
			return true
		}
	}
	return false
}

func looksLikeConsentOrScopeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "consent_required") ||
		strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "invalid_scope") ||
		strings.Contains(msg, "insufficient") ||
		strings.Contains(msg, "interaction_required")
}

func decodeDataURLImage(dataURL string) (string, []byte, error) {
	dataURL = strings.TrimSpace(dataURL)
	if !strings.HasPrefix(strings.ToLower(dataURL), "data:") {
		return "", nil, buildImageUploadConfigError("input_image is only supported for data URLs; remote image URLs are not supported")
	}

	headerAndBody := strings.TrimPrefix(dataURL, "data:")
	meta, rawBody, ok := strings.Cut(headerAndBody, ",")
	if !ok {
		return "", nil, buildImageUploadConfigError("input_image must be a valid base64 data URL")
	}

	meta = strings.TrimSpace(meta)
	if meta == "" {
		return "", nil, buildImageUploadConfigError("input_image must declare an image MIME type")
	}

	parts := strings.Split(meta, ";")
	mimeType := strings.ToLower(strings.TrimSpace(parts[0]))
	if !strings.HasPrefix(mimeType, "image/") {
		return "", nil, buildImageUploadConfigError("input_image must be a base64-encoded image data URL")
	}

	hasBase64 := false
	for _, part := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			hasBase64 = true
			break
		}
	}
	if !hasBase64 {
		return "", nil, buildImageUploadConfigError("input_image must use base64 encoding")
	}

	rawBody = strings.TrimSpace(rawBody)
	rawBody = strings.ReplaceAll(rawBody, "\n", "")
	rawBody = strings.ReplaceAll(rawBody, "\r", "")
	if rawBody == "" {
		return "", nil, buildImageUploadConfigError("input_image is empty")
	}

	data, err := base64.StdEncoding.DecodeString(rawBody)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(rawBody)
	}
	if err != nil {
		return "", nil, buildImageUploadConfigError("input_image contains invalid base64 data")
	}
	if len(data) == 0 {
		return "", nil, buildImageUploadConfigError("input_image is empty")
	}
	if len(data) > maxInlineImageBytes {
		return "", nil, buildImageUploadConfigError("input_image exceeds the 10 MiB inline upload limit")
	}
	return mimeType, data, nil
}

func extForMime(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/bmp":
		return "bmp"
	case "image/tiff":
		return "tiff"
	case "image/heic":
		return "heic"
	case "image/heif":
		return "heif"
	case "image/svg+xml":
		return "svg"
	default:
		mediaType, _, err := mime.ParseMediaType(mimeType)
		if err == nil {
			mimeType = mediaType
		}
		if idx := strings.IndexByte(mimeType, '/'); idx >= 0 && idx+1 < len(mimeType) {
			ext := mimeType[idx+1:]
			ext = strings.ReplaceAll(ext, "+", "-")
			ext = strings.TrimSpace(ext)
			if ext != "" {
				return ext
			}
		}
		return "bin"
	}
}

func buildUploadedImageName(mimeType string) string {
	now := time.Now().UTC()
	return fmt.Sprintf("uploads/%04d/%02d/%02d/%s.%s",
		now.Year(),
		now.Month(),
		now.Day(),
		uuid.NewString(),
		extForMime(mimeType),
	)
}

func graphPathEscapePreservingSlash(raw string) string {
	parts := strings.Split(strings.TrimSpace(raw), "/")
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		escaped = append(escaped, url.PathEscape(part))
	}
	if len(escaped) == 0 {
		return ""
	}
	return "/" + strings.Join(escaped, "/")
}

func sharePointSiteLookupURL(hostname, sitePath string) string {
	return graphBaseURL + "/v1.0/sites/" + url.PathEscape(strings.TrimSpace(hostname)) + ":" + graphPathEscapePreservingSlash(sitePath)
}

func (e *Executor) resolveSharePointSite(ctx context.Context, a *coreauth.Auth, accessToken, hostname, sitePath string) (graphSite, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sharePointSiteLookupURL(hostname, sitePath), nil)
	if err != nil {
		return graphSite{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := e.httpClientForAuth(a).Do(req)
	if err != nil {
		return graphSite{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return graphSite{}, fmt.Errorf("resolve sharepoint site failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out graphSite
	if err := json.Unmarshal(body, &out); err != nil {
		return graphSite{}, fmt.Errorf("resolve sharepoint site response unmarshal failed: %w", err)
	}
	if strings.TrimSpace(out.ID) == "" {
		return graphSite{}, fmt.Errorf("resolve sharepoint site response missing id")
	}
	return out, nil
}

func (e *Executor) getSharePointAppRoot(ctx context.Context, a *coreauth.Auth, accessToken, siteID string) (graphDriveItem, error) {
	u := graphBaseURL + "/v1.0/sites/" + url.PathEscape(strings.TrimSpace(siteID)) + "/drive/special/approot"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return graphDriveItem{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := e.httpClientForAuth(a).Do(req)
	if err != nil {
		return graphDriveItem{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return graphDriveItem{}, fmt.Errorf("resolve sharepoint app root failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out graphDriveItem
	if err := json.Unmarshal(body, &out); err != nil {
		return graphDriveItem{}, fmt.Errorf("resolve sharepoint app root response unmarshal failed: %w", err)
	}
	if strings.TrimSpace(out.ID) == "" {
		return graphDriveItem{}, fmt.Errorf("resolve sharepoint app root response missing id")
	}
	return out, nil
}

func (e *Executor) uploadImageToSharePointAppRoot(ctx context.Context, a *coreauth.Auth, accessToken, siteID, objectName, mimeType string, data []byte) (graphDriveItem, error) {
	u := graphBaseURL + "/v1.0/sites/" + url.PathEscape(strings.TrimSpace(siteID)) + "/drive/special/approot:" + graphPathEscapePreservingSlash(objectName) + ":/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(data))
	if err != nil {
		return graphDriveItem{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Accept", "application/json")
	req.ContentLength = int64(len(data))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}

	resp, err := e.httpClientForAuth(a).Do(req)
	if err != nil {
		return graphDriveItem{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return graphDriveItem{}, fmt.Errorf("sharepoint image upload failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out graphDriveItem
	if err := json.Unmarshal(body, &out); err != nil {
		return graphDriveItem{}, fmt.Errorf("sharepoint image upload response unmarshal failed: %w", err)
	}
	if strings.TrimSpace(out.ID) == "" {
		return graphDriveItem{}, fmt.Errorf("sharepoint image upload response missing id")
	}
	return out, nil
}

func (e *Executor) getDriveItemWebURL(ctx context.Context, a *coreauth.Auth, accessToken, siteID, itemID string) (graphDriveItem, error) {
	u := graphBaseURL + "/v1.0/sites/" + url.PathEscape(strings.TrimSpace(siteID)) + "/drive/items/" + url.PathEscape(strings.TrimSpace(itemID)) + "?$select=id,webUrl"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return graphDriveItem{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := e.httpClientForAuth(a).Do(req)
	if err != nil {
		return graphDriveItem{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return graphDriveItem{}, fmt.Errorf("get drive item webUrl failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out graphDriveItem
	if err := json.Unmarshal(body, &out); err != nil {
		return graphDriveItem{}, fmt.Errorf("get drive item webUrl response unmarshal failed: %w", err)
	}
	if strings.TrimSpace(out.ID) == "" {
		out.ID = strings.TrimSpace(itemID)
	}
	return out, nil
}

func (e *Executor) deleteDriveItem(ctx context.Context, a *coreauth.Auth, accessToken, siteID, itemID string) error {
	u := graphBaseURL + "/v1.0/sites/" + url.PathEscape(strings.TrimSpace(siteID)) + "/drive/items/" + url.PathEscape(strings.TrimSpace(itemID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := e.httpClientForAuth(a).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return fmt.Errorf("delete drive item failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (e *Executor) ensureImageUploadAccessToken(ctx context.Context, a *coreauth.Auth) (authInfo, string, error) {
	cfg := parseImageUploadConfig(a)
	if err := validateImageUploadConfig(cfg); err != nil {
		return authInfo{}, "", err
	}

	ai, ti, err := parseAuthInfo(a)
	if err != nil {
		return authInfo{}, "", err
	}
	if !scopeListContains(ai.DelegatedScopes, ImageUploadRequiredScope) {
		return authInfo{}, "", buildImageUploadScopeError("input_image upload requires delegated scope https://graph.microsoft.com/Files.ReadWrite.AppFolder; re-authorize via /m365/oauth/start")
	}

	now := time.Now().Unix()
	if ti.AccessToken != "" && (ti.ExpiresAt == 0 || now < ti.ExpiresAt-60) && tokenScopeContains(ti.Scope, ImageUploadRequiredScope) {
		return ai, ti.AccessToken, nil
	}
	if strings.TrimSpace(ti.RefreshToken) == "" {
		return authInfo{}, "", buildImageUploadScopeError("input_image upload requires a delegated refresh token; re-authorize via /m365/oauth/start")
	}

	httpClient := http.DefaultClient
	if e != nil {
		httpClient = e.httpClientForAuth(a)
	}

	scopes := ai.DelegatedScopes
	if len(scopes) == 0 {
		scopes = defaultDelegatedScopes()
	}
	refreshed, err := refreshWithRefreshToken(ctx, httpClient, ai.TenantID, ai.ClientID, ai.ClientSecret, ti.RefreshToken, scopes)
	if err != nil {
		if looksLikeConsentOrScopeError(err) {
			return authInfo{}, "", buildImageUploadScopeError("input_image upload requires delegated scope https://graph.microsoft.com/Files.ReadWrite.AppFolder; re-authorize via /m365/oauth/start")
		}
		return authInfo{}, "", err
	}
	if !tokenScopeContains(refreshed.Scope, ImageUploadRequiredScope) {
		return authInfo{}, "", buildImageUploadScopeError("input_image upload requires delegated scope https://graph.microsoft.com/Files.ReadWrite.AppFolder; re-authorize via /m365/oauth/start")
	}

	expiresAt := time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second).Unix()
	persistTokenMetadata(a, refreshed, expiresAt)
	persistAuthBestEffort(ctx, a)
	return ai, strings.TrimSpace(refreshed.AccessToken), nil
}

func (e *Executor) prepareUploadedTurnFiles(ctx context.Context, a *coreauth.Auth, accessToken string, imageURLs []string) (uploadedTurnFiles, error) {
	if len(imageURLs) == 0 {
		return uploadedTurnFiles{}, nil
	}

	cfg := parseImageUploadConfig(a)
	if err := validateImageUploadConfig(cfg); err != nil {
		return uploadedTurnFiles{}, err
	}

	type decodedImage struct {
		MIMEType string
		Data     []byte
	}
	decodedImages := make([]decodedImage, 0, len(imageURLs))
	for _, imageURL := range imageURLs {
		mimeType, data, err := decodeDataURLImage(imageURL)
		if err != nil {
			return uploadedTurnFiles{}, err
		}
		decodedImages = append(decodedImages, decodedImage{
			MIMEType: mimeType,
			Data:     data,
		})
	}

	site, err := e.resolveSharePointSite(ctx, a, accessToken, cfg.SharePointHostname, cfg.SharePointSitePath)
	if err != nil {
		return uploadedTurnFiles{}, buildImageUploadUpstreamError("image upload failed while resolving the SharePoint site")
	}
	if _, err := e.getSharePointAppRoot(ctx, a, accessToken, site.ID); err != nil {
		return uploadedTurnFiles{}, buildImageUploadUpstreamError("image upload failed while resolving the SharePoint app folder")
	}

	out := uploadedTurnFiles{
		Files:    make([]m365File, 0, len(imageURLs)),
		Uploaded: make([]uploadedFileRef, 0, len(imageURLs)),
	}

	for _, decoded := range decodedImages {
		item, err := e.uploadImageToSharePointAppRoot(ctx, a, accessToken, site.ID, buildUploadedImageName(decoded.MIMEType), decoded.MIMEType, decoded.Data)
		if err != nil {
			e.rollbackUploadedTurnFiles(a, accessToken, out)
			return uploadedTurnFiles{}, buildImageUploadUpstreamError("image upload failed while writing to SharePoint")
		}

		webURL := strings.TrimSpace(item.WebURL)
		if webURL == "" {
			item, err = e.getDriveItemWebURL(ctx, a, accessToken, site.ID, item.ID)
			if err != nil {
				out.Uploaded = append(out.Uploaded, uploadedFileRef{SiteID: site.ID, ItemID: item.ID})
				e.rollbackUploadedTurnFiles(a, accessToken, out)
				return uploadedTurnFiles{}, buildImageUploadUpstreamError("image upload failed while resolving the uploaded file URL")
			}
			webURL = strings.TrimSpace(item.WebURL)
		}
		if webURL == "" {
			out.Uploaded = append(out.Uploaded, uploadedFileRef{SiteID: site.ID, ItemID: item.ID})
			e.rollbackUploadedTurnFiles(a, accessToken, out)
			return uploadedTurnFiles{}, buildImageUploadUpstreamError("image upload failed because SharePoint did not return a file URL")
		}

		out.Files = append(out.Files, m365File{URI: webURL})
		out.Uploaded = append(out.Uploaded, uploadedFileRef{
			SiteID: site.ID,
			ItemID: item.ID,
			URI:    webURL,
		})
	}

	return out, nil
}

func (e *Executor) rollbackUploadedTurnFiles(a *coreauth.Auth, accessToken string, uploaded uploadedTurnFiles) {
	if len(uploaded.Uploaded) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.cleanupUploadedTurnFiles(ctx, a, accessToken, uploaded)
}

func (e *Executor) cleanupUploadedTurnFiles(ctx context.Context, a *coreauth.Auth, accessToken string, uploaded uploadedTurnFiles) {
	if len(uploaded.Uploaded) == 0 {
		return
	}

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
	} else if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	for i := len(uploaded.Uploaded) - 1; i >= 0; i-- {
		item := uploaded.Uploaded[i]
		if strings.TrimSpace(item.SiteID) == "" || strings.TrimSpace(item.ItemID) == "" {
			continue
		}
		if err := e.deleteDriveItem(ctx, a, accessToken, item.SiteID, item.ItemID); err != nil {
			log.Printf("m365 image cleanup failed: site=%s item=%s err=%v", item.SiteID, item.ItemID, err)
		}
	}
}
