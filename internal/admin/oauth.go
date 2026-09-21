package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const oauthStateTTL = 10 * time.Minute

type oauthState struct {
	provider string
	expires  time.Time
	used     bool
}

type oauthStateStore struct {
	mu     sync.Mutex
	states map[string]oauthState
	busy   map[string]string
}

func newOAuthStateStore() oauthStateStore {
	return oauthStateStore{states: make(map[string]oauthState), busy: make(map[string]string)}
}

func (s *oauthStateStore) begin(provider string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if _, exists := s.busy[provider]; exists {
		return false
	}
	s.busy[provider] = ""
	return true
}

func (s *oauthStateStore) releaseProvider(provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, provider)
}

func (s *oauthStateStore) put(state, provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.states[state] = oauthState{provider: provider, expires: time.Now().Add(oauthStateTTL)}
	s.busy[provider] = state
}
func (s *oauthStateStore) get(state string) (oauthState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	item, ok := s.states[state]
	return item, ok
}
func (s *oauthStateStore) markUsed(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, ok := s.states[state]; ok {
		item.used = true
		s.states[state] = item
	}
}
func (s *oauthStateStore) claim(state, provider string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	item, ok := s.states[state]
	if !ok || item.provider != provider || item.used {
		return false
	}
	item.used = true
	s.states[state] = item
	return true
}

func (s *oauthStateStore) release(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.states[state]
	if ok {
		if s.busy[item.provider] == state {
			delete(s.busy, item.provider)
		}
		delete(s.states, state)
	}
}

func (s *oauthStateStore) releaseBusy(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.states[state]
	if ok && s.busy[item.provider] == state {
		delete(s.busy, item.provider)
	}
}

func (s *oauthStateStore) pruneLocked() {
	now := time.Now()
	for key, item := range s.states {
		if now.After(item.expires) {
			delete(s.states, key)
			if s.busy[item.provider] == key {
				delete(s.busy, item.provider)
			}
		}
	}
}

func (h *Handler) oauthRoute(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 2 && parts[1] == "account" {
		if r.Method == http.MethodGet {
			h.oauthAccount(w, r, parts[0])
			return
		}
		if r.Method == http.MethodDelete {
			h.oauthDeleteAccount(w, r, parts[0])
			return
		}
		h.methodNotAllowed(w)
		return
	}
	if len(parts) == 2 && parts[1] == "refresh" {
		h.oauthRefreshAccount(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "start" {
		h.oauthStart(w, r, parts[0])
		return
	}
	if len(parts) == 1 && parts[0] == "status" {
		h.oauthStatus(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "callback" {
		h.oauthCallback(w, r)
		return
	}
	h.writeError(w, http.StatusNotFound, "not_found", "not found")
}

func supportedOAuthProvider(provider string) bool { return provider == "codex" || provider == "kimi" }

func (h *Handler) oauthStart(w http.ResponseWriter, r *http.Request, provider string) {
	if r.Method != http.MethodPost || !supportedOAuthProvider(provider) {
		h.writeError(w, http.StatusConflict, "oauth_unsupported", "OAuth provider unsupported")
		return
	}
	snapshot, status, err := h.accountSnapshot(provider)
	if err != nil {
		h.writeAdapterError(w, status, err)
		return
	}
	if snapshot.count > 0 {
		h.writeError(w, http.StatusConflict, "account_exists", "account already exists; disconnect it first")
		return
	}
	if !h.oauth.begin(provider) {
		h.writeError(w, http.StatusConflict, "oauth_in_progress", "OAuth authorization already in progress")
		return
	}
	keepState := false
	defer func() {
		if !keepState {
			h.oauth.releaseProvider(provider)
		}
	}()
	result, status, err := h.adapterRequest(http.MethodGet, adapterAuthPath(provider), nil)
	if err != nil {
		h.writeAdapterError(w, status, err)
		return
	}
	var payload struct {
		URL       string `json:"url"`
		State     string `json:"state"`
		UserCode  string `json:"user_code"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(result, &payload); err != nil || !validOAuthState(payload.State) || !validAuthURL(provider, payload.URL) {
		h.writeError(w, http.StatusBadGateway, "adapter_invalid_response", "adapter response invalid")
		return
	}
	response := map[string]any{"provider": provider, "state": payload.State, "url": payload.URL}
	if provider == "kimi" {
		if payload.UserCode == "" || payload.ExpiresIn <= 0 || payload.ExpiresIn > 900 || len(payload.UserCode) > 128 {
			h.writeError(w, http.StatusBadGateway, "adapter_invalid_response", "adapter response invalid")
			return
		}
		response["user_code"] = payload.UserCode
		response["expires_in"] = payload.ExpiresIn
	}
	h.oauth.put(payload.State, provider)
	keepState = true
	h.writeJSON(w, http.StatusOK, response)
}

func (h *Handler) oauthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.methodNotAllowed(w)
		return
	}
	state := r.URL.Query().Get("state")
	item, ok := h.oauth.get(state)
	if !ok || !validOAuthState(state) {
		h.writeError(w, http.StatusNotFound, "oauth_state_unknown", "OAuth state unknown")
		return
	}
	result, status, err := h.adapterRequest(http.MethodGet, "/v0/management/get-auth-status?state="+url.QueryEscape(state), nil)
	if err != nil {
		h.writeAdapterError(w, status, err)
		return
	}
	var raw map[string]any
	if json.Unmarshal(result, &raw) != nil {
		h.writeError(w, http.StatusBadGateway, "adapter_invalid_response", "adapter response invalid")
		return
	}
	response := map[string]any{"provider": item.provider, "state": state, "used": item.used}
	normalizedStatus, valid := normalizeOAuthStatus(raw["status"])
	if !valid {
		h.writeError(w, http.StatusBadGateway, "adapter_invalid_response", "adapter response invalid")
		return
	}
	response["status"] = normalizedStatus
	if normalizedStatus == "completed" {
		snapshot, snapshotStatus, snapshotErr := h.accountSnapshot(item.provider)
		if snapshotErr != nil {
			h.writeAdapterError(w, snapshotStatus, snapshotErr)
			return
		}
		if snapshot.count > 1 {
			h.writeError(w, http.StatusConflict, "account_conflict", "multiple accounts require manual cleanup")
			return
		}
		if snapshot.count != 1 || snapshot.status != "active" {
			h.writeError(w, http.StatusBadGateway, "account_unavailable", "authorized account unavailable")
			return
		}
		if err := h.bindAccountPrefix(item.provider, snapshot.name); err != nil {
			h.writeError(w, http.StatusBadGateway, "account_binding_failed", "account prefix binding failed")
			return
		}
		h.oauth.releaseBusy(state)
	}
	h.writeJSON(w, http.StatusOK, response)
}

type callbackInput struct {
	Provider    string `json:"provider"`
	State       string `json:"state"`
	Code        string `json:"code"`
	Error       string `json:"error"`
	CallbackURL string `json:"callback_url"`
}

func (h *Handler) oauthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.methodNotAllowed(w)
		return
	}
	var input callbackInput
	if !h.decodeJSON(w, r, &input) {
		return
	}
	if input.CallbackURL != "" {
		parsed, err := parseCodexCallbackURL(input.CallbackURL)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
			return
		}
		if input.State != "" && input.State != parsed.State {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
			return
		}
		input.State, input.Code, input.Error = parsed.State, parsed.Code, parsed.Error
	}
	if len(input.Code) > 4096 || len(input.Error) > 4096 || (input.Code != "" && input.Error != "") {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	item, ok := h.oauth.get(input.State)
	if !ok || item.provider != input.Provider || !supportedOAuthProvider(input.Provider) {
		h.writeError(w, http.StatusBadRequest, "oauth_state_unknown", "OAuth state unknown")
		return
	}
	// Kimi is a device flow at the audited adapter commit and has no browser
	// callback route. Refuse rather than inventing a relay endpoint.
	if input.Provider != "codex" {
		h.writeError(w, http.StatusConflict, "oauth_callback_unsupported", "OAuth callback unsupported")
		return
	}
	if input.Code == "" && input.Error == "" {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	if !h.oauth.claim(input.State, input.Provider) {
		h.writeError(w, http.StatusConflict, "oauth_state_used", "OAuth state already used")
		return
	}
	body, _ := json.Marshal(map[string]string{"provider": "codex", "state": input.State, "code": input.Code, "error": input.Error})
	result, status, err := h.adapterRequest(http.MethodPost, "/v0/management/oauth-callback", body)
	if err != nil {
		h.writeAdapterError(w, status, err)
		return
	}
	var raw map[string]any
	if json.Unmarshal(result, &raw) != nil {
		h.writeError(w, http.StatusBadGateway, "adapter_invalid_response", "adapter response invalid")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"provider": "codex", "state": input.State, "status": "accepted"})
}

func adapterAuthPath(provider string) string {
	if provider == "codex" {
		return "/v0/management/codex-auth-url"
	}
	return "/v0/management/kimi-auth-url"
}

func validOAuthState(state string) bool {
	if state == "" || len(state) > 256 || strings.TrimSpace(state) != state {
		return false
	}
	for _, r := range state {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func validOAuthStatus(status string) bool {
	switch status {
	case "pending", "completed", "failed", "cancelled", "expired":
		return true
	default:
		return false
	}
}

func normalizeOAuthStatus(value any) (string, bool) {
	status, ok := value.(string)
	if !ok {
		return "", false
	}
	switch status {
	case "ok", "completed":
		return "completed", true
	case "wait", "pending":
		return "pending", true
	case "error", "failed":
		return "failed", true
	case "cancelled":
		return "cancelled", true
	case "expired":
		return "expired", true
	default:
		return "", false
	}
}

func validAuthURL(provider, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return false
	}
	want := "auth.openai.com"
	if provider == "kimi" {
		want = "auth.kimi.com"
	}
	return (u.Port() == "" || u.Port() == "443") && u.Hostname() == want && u.Fragment == ""
}

func (h *Handler) adapterRequest(method, suffix string, body []byte) ([]byte, int, error) {
	if h.adapterURL == nil || h.adapterManagementKey == "" {
		return nil, http.StatusServiceUnavailable, errors.New("adapter unavailable")
	}
	u := *h.adapterURL
	route, err := url.Parse(suffix)
	if err != nil || route.IsAbs() || route.Host != "" || !strings.HasPrefix(route.Path, "/") {
		return nil, http.StatusBadGateway, errors.New("adapter request failed")
	}
	u.Path = strings.TrimRight(u.Path, "/") + route.Path
	u.RawQuery = route.RawQuery
	u.Fragment = ""
	request, err := http.NewRequest(method, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return nil, http.StatusBadGateway, errors.New("adapter request failed")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Management-Key", h.adapterManagementKey)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.httpClient.Do(request)
	if err != nil {
		return nil, http.StatusBadGateway, errors.New("adapter request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBody+1))
	if err != nil || int64(len(data)) > maxJSONBody {
		return nil, http.StatusBadGateway, errors.New("adapter response invalid")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, errors.New("adapter request rejected")
	}
	return data, http.StatusOK, nil
}

type codexCallback struct {
	State string
	Code  string
	Error string
}

func parseCodexCallbackURL(raw string) (codexCallback, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "localhost" || (u.Port() != "" && u.Port() != "1455") || u.Path != "/auth/callback" || u.User != nil || u.Fragment != "" {
		return codexCallback{}, errors.New("invalid callback URL")
	}
	query := u.Query()
	return codexCallback{State: query.Get("state"), Code: query.Get("code"), Error: query.Get("error")}, nil
}

func (h *Handler) writeAdapterError(w http.ResponseWriter, status int, err error) {
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		status = http.StatusBadGateway
	}
	h.writeError(w, status, "adapter_error", "adapter request failed")
}
