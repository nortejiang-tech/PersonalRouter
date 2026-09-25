package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"personalrouter/internal/store"
)

const (
	connectivityTimeout = 15 * time.Second
	maxConnectivityBody = int64(1 << 20)
	messageDiscoveryOK  = "模型发现成功"
	messageTestOK       = "连通测试成功"
)

var errConnectivityInvalidResponse = errors.New("invalid upstream response")

// ProviderDiscovery is the deliberately small, stable response for the saved
// provider model discovery operation. It never contains upstream response
// content or headers.
type ProviderDiscovery struct {
	ProviderID      string             `json:"provider_id"`
	Status          string             `json:"status"`
	Models          []ProviderModelRef `json:"models"`
	Message         string             `json:"message"`
	CheckedAt       string             `json:"checked_at"`
	UpstreamStatus  int                `json:"upstream_status"`
	ProviderEnabled bool               `json:"provider_enabled"`
}

type ProviderModelRef struct {
	ID string `json:"id"`
}

// ProviderTest is the fixed response for a one-request connectivity probe.
// The model output itself is intentionally absent.
type ProviderTest struct {
	ProviderID      string `json:"provider_id"`
	Model           string `json:"model"`
	Protocol        string `json:"protocol"`
	Status          string `json:"status"`
	Message         string `json:"message"`
	CheckedAt       string `json:"checked_at"`
	LatencyMS       int64  `json:"latency_ms"`
	UpstreamStatus  int    `json:"upstream_status"`
	ProviderEnabled bool   `json:"provider_enabled"`
}

type providerConnectivityInput struct {
	Model    string          `json:"model"`
	Protocol json.RawMessage `json:"protocol"`
}

type connectivityClient struct {
	baseURL      *url.URL
	token        string
	protocol     string
	oauth        bool
	prefix       string
	anthropicKey bool
}

func checkedAt() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (h *Handler) providerModels(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		h.methodNotAllowed(w)
		return
	}
	if !safeID(id) {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	provider, err := h.store.Provider(id)
	if errors.Is(err, store.ErrNotFound) {
		h.writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
		return
	}
	result := ProviderDiscovery{ProviderID: id, Status: "error", Models: []ProviderModelRef{}, CheckedAt: checkedAt(), ProviderEnabled: provider.Enabled}
	client, message := h.connectivityClient(r.Context(), provider)
	if message != "" {
		result.Message = message
		h.writeJSON(w, http.StatusOK, result)
		return
	}

	endpoint, err := discoveryEndpoint(client.baseURL)
	if err != nil {
		result.Message = messageInvalidResponse
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	body, status, requestErr := h.connectivityRequest(r.Context(), http.MethodGet, endpoint, client.token, nil, client.anthropicKey)
	result.UpstreamStatus = status
	if requestErr != nil {
		result.Message = connectivityErrorMessage(requestErr, status, r.Context(), true)
		if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
			result.Status = "unsupported"
		}
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	models, parseErr := parseDiscoveryModels(body, client)
	if parseErr != nil {
		result.Message = messageInvalidResponse
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	result.Status = "ok"
	result.Message = messageDiscoveryOK
	result.Models = models
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) providerConnectivityTest(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		h.methodNotAllowed(w)
		return
	}
	if !safeID(id) {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	provider, err := h.store.Provider(id)
	if errors.Is(err, store.ErrNotFound) {
		h.writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
		return
	}
	var input providerConnectivityInput
	if !h.decodeJSON(w, r, &input) {
		return
	}
	input.Model = strings.TrimSpace(input.Model)
	if !safeID(input.Model) {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	var requestedProtocol *string
	if len(input.Protocol) > 0 {
		if strings.TrimSpace(string(input.Protocol)) == "null" {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "protocol is invalid")
			return
		}
		var requested string
		if json.Unmarshal(input.Protocol, &requested) != nil {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "protocol is invalid")
			return
		}
		requestedProtocol = &requested
	}
	protocol, err := selectedConnectivityProtocol(provider, requestedProtocol)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "protocol is not supported by this provider")
		return
	}
	result := ProviderTest{ProviderID: id, Model: input.Model, Protocol: protocol, Status: "error", CheckedAt: checkedAt(), ProviderEnabled: provider.Enabled}
	client, message := h.connectivityClient(r.Context(), provider)
	if message != "" {
		result.Message = message
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	if client.oauth && hasAccountPrefix(input.Model) {
		result.Message = messageInvalidRequest
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	model := input.Model
	if client.oauth {
		model = client.prefix + model
	}
	endpoint, err := protocolEndpoint(client.baseURL, protocol)
	if err != nil {
		result.Message = messageInvalidResponse
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	payload, err := connectivityProbeBody(protocol, model)
	if err != nil {
		result.Message = messageInvalidRequest
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	started := time.Now()
	body, status, requestErr := h.connectivityRequest(r.Context(), http.MethodPost, endpoint, client.token, payload, client.anthropicKey)
	result.LatencyMS = time.Since(started).Milliseconds()
	if result.LatencyMS < 0 {
		result.LatencyMS = 0
	}
	result.UpstreamStatus = status
	if requestErr != nil {
		result.Message = connectivityErrorMessage(requestErr, status, r.Context(), false)
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	if !validConnectivityResponse(protocol, body) {
		result.Message = messageInvalidResponse
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	result.Status = "ok"
	result.Message = messageTestOK
	h.writeJSON(w, http.StatusOK, result)
}

func selectedConnectivityProtocol(provider store.Provider, requested *string) (string, error) {
	protocol := provider.Protocol
	if protocol == "adapter" {
		protocol = "openai"
	} else if protocol != "openai" && protocol != "responses" && protocol != "anthropic" {
		return "", errors.New("unsupported protocol")
	}
	if requested != nil {
		if *requested != "openai" && *requested != "responses" && *requested != "anthropic" {
			return "", errors.New("unsupported protocol")
		}
		if provider.Protocol != "adapter" && *requested != provider.Protocol {
			return "", errors.New("protocol mismatch")
		}
		protocol = *requested
	}
	return protocol, nil
}

func (h *Handler) connectivityClient(ctx context.Context, provider store.Provider) (connectivityClient, string) {
	result := connectivityClient{protocol: provider.Protocol}
	if provider.AuthMode == "oauth" {
		if provider.Protocol != "adapter" || !supportedOAuthProvider(provider.OAuthProvider) || h.adapterURL == nil || h.adapterAPIKey == "" || h.adapterManagementKey == "" {
			return result, messageNoCredentials
		}
		snapshot, _, err := h.accountSnapshotForConnectivity(ctx, provider.OAuthProvider)
		if err != nil || snapshot.count != 1 || snapshot.status != "active" {
			if ctx.Err() != nil {
				return result, messageTimeout
			}
			return result, messageOAuthUnavailable
		}
		result.baseURL = h.adapterURL
		result.token = h.adapterAPIKey
		result.oauth = true
		result.prefix = fixedModelPrefix(provider.OAuthProvider)
		return result, ""
	}
	if provider.Protocol == "adapter" {
		if h.adapterURL == nil || h.adapterAPIKey == "" {
			return result, messageNoCredentials
		}
		result.baseURL = h.adapterURL
		result.token = h.adapterAPIKey
		return result, ""
	}
	if provider.Endpoint == "" {
		return result, messageNetwork
	}
	endpoint, err := parseConnectivityURL(provider.Endpoint)
	if err != nil {
		return result, messageInvalidResponse
	}
	result.baseURL = endpoint
	result.anthropicKey = provider.Protocol == "anthropic"
	if provider.AuthMode == "api_key" {
		if provider.SecretCipher == "" || h.vault == nil {
			return result, messageNoCredentials
		}
		secret, err := h.vault.Open(provider.SecretCipher)
		if err != nil || len(secret) == 0 {
			return result, messageNoCredentials
		}
		result.token = string(secret)
	} else if provider.AuthMode != "none" {
		return result, messageNoCredentials
	}
	return result, ""
}

// accountSnapshotForConnectivity is the read-only, request-context-aware
// counterpart of the management account lookup. Connectivity probes must
// stop promptly when the browser request disconnects; this path never calls
// refresh, bind, delete, or any other mutating adapter operation.
func (h *Handler) accountSnapshotForConnectivity(ctx context.Context, provider string) (accountSnapshot, int, error) {
	if !supportedOAuthProvider(provider) {
		return accountSnapshot{}, http.StatusConflict, errors.New("unsupported account")
	}
	result, status, err := h.adapterManagementRequestContext(ctx, http.MethodGet, "/v0/management/auth-files")
	if err != nil {
		return accountSnapshot{}, status, err
	}
	var payload adapterAuthFilesResponse
	if err := json.Unmarshal(result, &payload); err != nil || payload.Files == nil {
		return accountSnapshot{}, http.StatusBadGateway, errors.New("invalid account response")
	}
	snapshot := accountSnapshot{provider: provider, status: "missing"}
	for _, file := range payload.Files {
		if !sameAccountProvider(provider, file.Provider, file.Type) {
			continue
		}
		snapshot.count++
		if snapshot.count != 1 {
			snapshot.status = "unknown"
			snapshot.name = ""
			snapshot.prefix = ""
			continue
		}
		if !safeAuthBasename(file.Name) {
			return accountSnapshot{}, http.StatusBadGateway, errors.New("invalid account response")
		}
		snapshot.name = file.Name
		snapshot.status = normalizeAccountStatus(file.Status, file.Disabled)
		snapshot.prefix = strings.TrimSpace(file.Prefix)
		if snapshot.prefix == "" {
			snapshot.prefix = strings.TrimSpace(file.ModelPrefix)
		}
	}
	return snapshot, http.StatusOK, nil
}

func (h *Handler) adapterManagementRequestContext(ctx context.Context, method, suffix string) ([]byte, int, error) {
	if h.adapterURL == nil || h.adapterManagementKey == "" {
		return nil, http.StatusServiceUnavailable, errors.New("adapter unavailable")
	}
	u := *h.adapterURL
	u.Path = strings.TrimRight(u.Path, "/") + suffix
	u.RawPath = ""
	requestCtx, cancel := context.WithTimeout(ctx, connectivityTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, u.String(), nil)
	if err != nil {
		return nil, http.StatusServiceUnavailable, errors.New("adapter unavailable")
	}
	req.Header.Set("X-Management-Key", h.adapterManagementKey)
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, errors.New("adapter request failed")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody+1))
	if err != nil || int64(len(body)) > maxJSONBody {
		return nil, resp.StatusCode, errors.New("invalid account response")
	}
	return body, resp.StatusCode, nil
}

func parseConnectivityURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid endpoint")
	}
	if strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("invalid endpoint")
	}
	u.RawPath = ""
	return u, nil
}

func discoveryEndpoint(base *url.URL) (string, error) {
	u := *base
	path := strings.TrimRight(u.Path, "/")
	methodSuffix := false
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
			methodSuffix = true
			break
		}
	}
	if path == "" {
		if methodSuffix {
			u.Path = "/models"
		} else {
			u.Path = "/v1/models"
		}
	} else {
		u.Path = path + "/models"
	}
	u.RawPath = ""
	return u.String(), nil
}

func protocolEndpoint(base *url.URL, protocol string) (string, error) {
	u := *base
	suffix := map[string]string{"openai": "/chat/completions", "responses": "/responses", "anthropic": "/messages"}[protocol]
	if suffix == "" {
		return "", errors.New("unsupported protocol")
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, suffix) || strings.HasSuffix(path, "/v1"+suffix) {
		u.Path = path
	} else if path == "" {
		u.Path = "/v1" + suffix
	} else {
		u.Path = path + suffix
	}
	u.RawPath = ""
	return u.String(), nil
}

func (h *Handler) connectivityRequest(ctx context.Context, method, endpoint, token string, payload []byte, anthropicKey bool) ([]byte, int, error) {
	requestCtx, cancel := context.WithTimeout(ctx, connectivityTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		if anthropicKey {
			req.Header.Set("x-api-key", token)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, upstreamStatusError(resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxConnectivityBody+1))
	if err != nil {
		return nil, resp.StatusCode, errConnectivityInvalidResponse
	}
	if int64(len(body)) > maxConnectivityBody {
		return nil, resp.StatusCode, errConnectivityInvalidResponse
	}
	return body, resp.StatusCode, nil
}

type upstreamStatusError int

func (e upstreamStatusError) Error() string { return fmt.Sprintf("upstream status %d", int(e)) }

func connectivityErrorMessage(err error, status int, ctx context.Context, discovery bool) string {
	if errors.Is(err, errConnectivityInvalidResponse) {
		return messageInvalidResponse
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return messageTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "deadline exceeded") || strings.Contains(strings.ToLower(err.Error()), "client.timeout") {
		return messageTimeout
	}
	switch status {
	case http.StatusUnauthorized:
		return messageAuth
	case http.StatusForbidden:
		return messagePermission
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		if discovery {
			return messageUnsupported
		}
		return messageUnsupportedCall
	case http.StatusTooManyRequests:
		return messageRateLimit
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return messageRedirect
	default:
		if status >= 400 && status < 500 {
			return messageNetwork
		}
		return messageNetwork
	}
}

const (
	messageNoCredentials    = "未配置凭据"
	messageOAuthUnavailable = "OAuth账户不可用"
	messageAuth             = "上游认证失败"
	messagePermission       = "上游权限不足"
	messageUnsupported      = "上游不支持模型列表"
	messageUnsupportedCall  = "上游不支持该模型或调用路径"
	messageRateLimit        = "上游限流或额度不足"
	messageTimeout          = "上游请求超时"
	messageNetwork          = "上游网络请求失败"
	messageInvalidResponse  = "上游响应无效"
	messageRedirect         = "上游请求被重定向"
	messageInvalidRequest   = "请求模型无效"
)

func parseDiscoveryModels(body []byte, client connectivityClient) ([]ProviderModelRef, error) {
	var payload struct {
		Data json.RawMessage `json:"data"`
	}
	if err := decodeConnectivityJSON(body, &payload); err != nil {
		return nil, err
	}
	if len(payload.Data) == 0 || string(payload.Data) == "null" {
		return nil, errors.New("missing model data")
	}
	var entries []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload.Data, &entries); err != nil {
		return nil, err
	}
	models := make([]ProviderModelRef, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, item := range entries {
		id := item.ID
		if client.oauth {
			if !strings.HasPrefix(id, client.prefix) {
				continue
			}
			id = strings.TrimPrefix(id, client.prefix)
		}
		if !safeID(id) {
			return nil, errors.New("invalid model id")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, ProviderModelRef{ID: id})
		if len(models) > 500 {
			return nil, errors.New("too many models")
		}
	}
	return models, nil
}

func decodeConnectivityJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func connectivityProbeBody(protocol, model string) ([]byte, error) {
	var payload any
	switch protocol {
	case "openai":
		payload = map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply OK"}}, "stream": false, "max_tokens": 16}
	case "responses":
		payload = map[string]any{"model": model, "input": "Reply OK", "stream": false, "max_output_tokens": 16}
	case "anthropic":
		payload = map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply OK"}}, "stream": false, "max_tokens": 16}
	default:
		return nil, errors.New("unsupported protocol")
	}
	return json.Marshal(payload)
}

func validConnectivityResponse(protocol string, body []byte) bool {
	var object map[string]json.RawMessage
	if decodeConnectivityJSON(body, &object) != nil || object == nil {
		return false
	}
	if rawError, hasError := object["error"]; hasError && string(bytes.TrimSpace(rawError)) != "null" {
		return false
	}
	switch protocol {
	case "openai":
		var choices []struct {
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(object["choices"], &choices) != nil || len(choices) == 0 || len(choices[0].Message) == 0 || string(choices[0].Message) == "null" {
			return false
		}
		var message map[string]json.RawMessage
		if json.Unmarshal(choices[0].Message, &message) != nil || message == nil {
			return false
		}
		_, hasContent := message["content"]
		_, hasReasoning := message["reasoning_content"]
		return hasContent || hasReasoning
	case "responses":
		var objectType string
		if json.Unmarshal(object["object"], &objectType) != nil || objectType != "response" {
			return false
		}
		var status string
		if json.Unmarshal(object["status"], &status) != nil || (status != "completed" && status != "incomplete") {
			return false
		}
		rawOutput, ok := object["output"]
		if !ok || string(bytes.TrimSpace(rawOutput)) == "null" {
			return false
		}
		var output []json.RawMessage
		return json.Unmarshal(rawOutput, &output) == nil
	case "anthropic":
		var typ string
		if json.Unmarshal(object["type"], &typ) != nil || typ != "message" {
			return false
		}
		rawContent, ok := object["content"]
		if !ok || string(bytes.TrimSpace(rawContent)) == "null" {
			return false
		}
		var content []json.RawMessage
		return json.Unmarshal(rawContent, &content) == nil
	default:
		return false
	}
}

func hasAccountPrefix(model string) bool {
	for _, segment := range strings.Split(model, "/") {
		if strings.HasPrefix(segment, "nr-") {
			return true
		}
	}
	return false
}
