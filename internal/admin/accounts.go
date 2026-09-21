package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
)

// accountSnapshot is deliberately smaller than the adapter auth-file entry.
// The adapter response can contain identity, token and quota data; none of it
// crosses the PersonalRouter management boundary.
type accountSnapshot struct {
	provider string
	status   string
	count    int
	name     string
	prefix   string
}

type adapterAuthFilesResponse struct {
	Files []adapterAuthFile `json:"files"`
}

type adapterAuthFile struct {
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Disabled    bool   `json:"disabled"`
	Prefix      string `json:"prefix"`
	ModelPrefix string `json:"model_prefix"`
}

func fixedModelPrefix(provider string) string {
	if provider == "codex" {
		return "nr-codex/"
	}
	return "nr-kimi/"
}

func adapterModelPrefix(provider string) string {
	return strings.TrimSuffix(fixedModelPrefix(provider), "/")
}

func (h *Handler) accountSnapshot(provider string) (accountSnapshot, int, error) {
	if !supportedOAuthProvider(provider) {
		return accountSnapshot{}, http.StatusConflict, errors.New("unsupported account")
	}
	result, status, err := h.adapterRequest(http.MethodGet, "/v0/management/auth-files", nil)
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
	if snapshot.count == 0 {
		snapshot.status = "missing"
	}
	return snapshot, http.StatusOK, nil
}

func sameAccountProvider(want string, values ...string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

func normalizeAccountStatus(raw string, disabled bool) string {
	if disabled {
		return "disabled"
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "active":
		return "active"
	case "disabled":
		return "disabled"
	case "expired":
		return "expired"
	case "error", "failed":
		return "error"
	case "missing":
		return "missing"
	default:
		return "unknown"
	}
}

func safeAuthBasename(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 255 || filepath.Base(name) != name || name == "." || name == ".." || !strings.HasSuffix(strings.ToLower(name), ".json") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func accountView(snapshot accountSnapshot) map[string]any {
	return map[string]any{
		"provider":      snapshot.provider,
		"status":        snapshot.status,
		"account_count": snapshot.count,
		"model_prefix":  fixedModelPrefix(snapshot.provider),
	}
}

func (h *Handler) oauthAccount(w http.ResponseWriter, r *http.Request, provider string) {
	if !supportedOAuthProvider(provider) {
		h.writeError(w, http.StatusConflict, "oauth_unsupported", "OAuth provider unsupported")
		return
	}
	snapshot, status, err := h.accountSnapshot(provider)
	if err != nil {
		h.writeAdapterError(w, status, err)
		return
	}
	if snapshot.count > 1 {
		h.writeError(w, http.StatusConflict, "account_conflict", "multiple accounts require manual cleanup")
		return
	}
	h.writeJSON(w, http.StatusOK, accountView(snapshot))
}

func (h *Handler) oauthRefreshAccount(w http.ResponseWriter, r *http.Request, provider string) {
	if r.Method != http.MethodPost || !supportedOAuthProvider(provider) {
		h.methodNotAllowed(w)
		return
	}
	snapshot, status, err := h.accountSnapshot(provider)
	if err != nil {
		h.writeAdapterError(w, status, err)
		return
	}
	if snapshot.count == 0 {
		h.writeError(w, http.StatusNotFound, "account_missing", "account not found")
		return
	}
	if snapshot.count > 1 {
		h.writeError(w, http.StatusConflict, "account_conflict", "multiple accounts require manual cleanup")
		return
	}
	body, _ := json.Marshal(map[string]string{"name": snapshot.name})
	result, status, err := h.adapterRequest(http.MethodPost, "/v0/management/auth-files/refresh", body)
	if err != nil {
		h.writeAdapterError(w, status, err)
		return
	}
	var response struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(result, &response) != nil || !response.OK {
		h.writeError(w, http.StatusBadGateway, "refresh_failed", "account refresh failed")
		return
	}
	updated, status, err := h.accountSnapshot(provider)
	if err != nil {
		h.writeAdapterError(w, status, err)
		return
	}
	if updated.count != 1 {
		h.writeError(w, http.StatusBadGateway, "refresh_failed", "account refresh failed")
		return
	}
	h.writeJSON(w, http.StatusOK, accountView(updated))
}

func (h *Handler) oauthDeleteAccount(w http.ResponseWriter, r *http.Request, provider string) {
	if r.Method != http.MethodDelete || !supportedOAuthProvider(provider) {
		h.methodNotAllowed(w)
		return
	}
	snapshot, status, err := h.accountSnapshot(provider)
	if err != nil {
		h.writeAdapterError(w, status, err)
		return
	}
	if snapshot.count == 0 {
		h.writeError(w, http.StatusNotFound, "account_missing", "account not found")
		return
	}
	if snapshot.count > 1 {
		h.writeError(w, http.StatusConflict, "account_conflict", "multiple accounts require manual cleanup")
		return
	}
	if err := h.disableOAuthProviders(provider); err != nil {
		h.writeDomainError(w, err)
		return
	}
	result, status, err := h.adapterRequest(http.MethodDelete, "/v0/management/auth-files?name="+url.QueryEscape(snapshot.name), nil)
	_ = result // DELETE response may contain adapter auth material; discard it.
	if err != nil {
		h.writeAdapterError(w, status, err)
		return
	}
	remaining, status, err := h.accountSnapshot(provider)
	if err != nil || remaining.count != 0 {
		h.writeError(w, http.StatusBadGateway, "account_delete_failed", "account disconnect failed")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"provider": provider, "status": "missing", "account_count": 0, "model_prefix": fixedModelPrefix(provider), "disconnected": true})
}

func (h *Handler) disableOAuthProviders(provider string) error {
	items, err := h.store.Providers()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.AuthMode != "oauth" || item.OAuthProvider != provider || !item.Enabled {
			continue
		}
		item.Enabled = false
		if err := h.store.UpsertProvider(item); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) bindAccountPrefix(provider, name string) error {
	if !safeAuthBasename(name) {
		return errors.New("invalid account response")
	}
	body, _ := json.Marshal(map[string]any{"name": name, "prefix": adapterModelPrefix(provider), "request_retry": 0})
	result, status, err := h.adapterRequest(http.MethodPatch, "/v0/management/auth-files/fields", body)
	_ = result // The adapter response is not trusted or returned.
	if err != nil {
		return errors.New("account prefix binding failed")
	}
	if status < 200 || status >= 300 {
		return errors.New("account prefix binding failed")
	}
	return nil
}
