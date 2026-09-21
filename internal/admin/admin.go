// Package admin implements the authenticated, LAN-only management API.
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"personalrouter/internal/security"
	"personalrouter/internal/store"
)

const maxJSONBody int64 = 1 << 20

var requestSequence uint64

type Options struct {
	Store                *store.Store
	Vault                *security.Vault
	AdminKeyHash         string
	AdapterURL           string
	AdapterManagementKey string
	AdapterAPIKey        string
	HTTPClient           *http.Client
	LANBaseURL           string
	PublicBaseURL        string
}

type Handler struct {
	store                *store.Store
	vault                *security.Vault
	adminKeyHash         string
	adapterURL           *url.URL
	adapterManagementKey string
	adapterAPIKey        string
	httpClient           *http.Client
	lanBaseURL           string
	publicBaseURL        string
	oauth                oauthStateStore
	providerMu           sync.Mutex
}

// New constructs the management handler. AdapterURL is optional; when absent
// OAuth endpoints return a fixed unavailable error instead of fabricating an
// authorization flow. AdapterManagementKey is retained only in memory.
func New(options Options) (http.Handler, error) {
	if options.Store == nil {
		return nil, errors.New("admin store is required")
	}
	if options.Vault == nil {
		return nil, errors.New("admin vault is required")
	}
	if !validHash(options.AdminKeyHash) {
		return nil, errors.New("admin key hash is invalid")
	}
	adapter, err := parseBaseURL(options.AdapterURL)
	if err != nil {
		return nil, fmt.Errorf("adapter URL: %w", err)
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	if options.HTTPClient != nil {
		copy := *options.HTTPClient
		if copy.Timeout <= 0 || copy.Timeout > 15*time.Second {
			copy.Timeout = 15 * time.Second
		}
		copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		client = &copy
	}
	return &Handler{
		store:                options.Store,
		vault:                options.Vault,
		adminKeyHash:         options.AdminKeyHash,
		adapterURL:           adapter,
		adapterManagementKey: options.AdapterManagementKey,
		adapterAPIKey:        options.AdapterAPIKey,
		httpClient:           client,
		lanBaseURL:           options.LANBaseURL,
		publicBaseURL:        options.PublicBaseURL,
		oauth:                newOAuthStateStore(),
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if r.Method == http.MethodOptions {
		h.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !h.authorized(r) {
		h.writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}
	if !sameOrigin(r) {
		h.writeError(w, http.StatusForbidden, "origin_forbidden", "origin rejected")
		return
	}

	path := strings.TrimPrefix(r.URL.EscapedPath(), "/admin/api")
	if path == "" || path[0] != '/' {
		h.writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	rawParts := strings.Split(strings.Trim(path, "/"), "/")
	parts := make([]string, len(rawParts))
	for i, rawPart := range rawParts {
		part, err := url.PathUnescape(rawPart)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
			return
		}
		parts[i] = part
	}
	if len(parts) == 1 && parts[0] == "overview" {
		h.overview(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "settings" {
		h.settings(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "providers" {
		h.providers(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "models" {
		h.models(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "callers" {
		h.callers(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "requests" {
		h.requests(w, r)
		return
	}
	if len(parts) == 3 && parts[0] == "providers" && parts[2] == "models" {
		h.providerModels(w, r, parts[1])
		return
	}
	if len(parts) == 3 && parts[0] == "providers" && parts[2] == "test" {
		h.providerConnectivityTest(w, r, parts[1])
		return
	}
	if len(parts) == 2 && parts[0] == "providers" {
		h.provider(w, r, parts[1])
		return
	}
	if len(parts) == 2 && parts[0] == "models" {
		h.model(w, r, parts[1])
		return
	}
	if len(parts) == 2 && parts[0] == "callers" {
		h.caller(w, r, parts[1])
		return
	}
	if len(parts) == 3 && parts[0] == "callers" && parts[2] == "key" {
		h.callerKey(w, r, parts[1])
		return
	}
	if len(parts) == 3 && parts[0] == "callers" && parts[2] == "rotate" {
		h.rotateCaller(w, r, parts[1])
		return
	}
	if parts[0] == "oauth" {
		h.oauthRoute(w, r, parts[1:])
		return
	}
	h.writeError(w, http.StatusNotFound, "not_found", "not found")
}

func (h *Handler) authorized(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	fields := strings.Fields(values[0])
	return len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") && security.VerifyKey(fields[1], h.adminKeyHash)
}

func sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) > 1 {
		return false
	}
	if len(origins) == 0 || strings.TrimSpace(origins[0]) == "" {
		return true
	}
	origin := strings.TrimSpace(origins[0])
	if r.Host == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && u.Host == r.Host
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.methodNotAllowed(w)
		return
	}
	summary, err := h.store.Summary()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
		return
	}
	h.writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.methodNotAllowed(w)
		return
	}
	configured := h.adapterURL != nil && h.adapterManagementKey != ""
	supported := []string(nil)
	if configured {
		supported = []string{"codex", "kimi"}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"adapter_configured": configured, "supported_oauth": supported, "lan_base_url": h.lanBaseURL, "public_base_url": h.publicBaseURL})
}

func (h *Handler) providers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.methodNotAllowed(w)
		return
	}
	items, err := h.store.Providers()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
		return
	}
	views := make([]providerView, 0, len(items))
	for _, item := range items {
		views = append(views, redactProvider(item))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.methodNotAllowed(w)
		return
	}
	items, err := h.store.Models()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) callers(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		h.createCaller(w, r)
		return
	}
	if r.Method != http.MethodGet {
		h.methodNotAllowed(w)
		return
	}
	items, err := h.store.Callers()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
		return
	}
	views := make([]callerView, 0, len(items))
	for _, item := range items {
		views = append(views, redactCaller(item))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

func (h *Handler) requests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	filter := store.RequestFilter{CallerID: q.Get("caller_id"), Model: q.Get("model")}
	var err error
	if raw := q.Get("status"); raw != "" {
		filter.Status, err = strconv.Atoi(raw)
		if err != nil || filter.Status < 0 {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
			return
		}
	}
	if raw := q.Get("limit"); raw != "" {
		filter.Limit, err = strconv.Atoi(raw)
		if err != nil || filter.Limit < 0 {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
			return
		}
	}
	items, err := h.store.Requests(filter)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type providerInput struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	AuthMode      string  `json:"auth_mode"`
	Protocol      string  `json:"protocol"`
	Endpoint      string  `json:"endpoint"`
	Enabled       bool    `json:"enabled"`
	OAuthProvider string  `json:"oauth_provider"`
	Secret        *string `json:"secret"`
	APIKey        *string `json:"api_key"`
	RemoveSecret  bool    `json:"remove_secret"`
}

type providerView struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	AuthMode      string    `json:"auth_mode"`
	Protocol      string    `json:"protocol"`
	Endpoint      string    `json:"endpoint"`
	Enabled       bool      `json:"enabled"`
	OAuthProvider string    `json:"oauth_provider"`
	HasSecret     bool      `json:"has_secret"`
	CreatedAt     time.Time `json:"created_at"`
}

func (h *Handler) provider(w http.ResponseWriter, r *http.Request, id string) {
	if !safeID(id) {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := h.store.Provider(id)
		if errors.Is(err, store.ErrNotFound) {
			h.writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
			return
		}
		h.writeJSON(w, http.StatusOK, redactProvider(item))
	case http.MethodPut:
		var input providerInput
		if !h.decodeJSON(w, r, &input) {
			return
		}
		if input.ID != "" && input.ID != id {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
			return
		}
		if err := h.saveProvider(id, input); err != nil {
			h.writeDomainError(w, err)
			return
		}
		item, _ := h.store.Provider(id)
		h.writeJSON(w, http.StatusOK, redactProvider(item))
	case http.MethodDelete:
		if err := h.store.DeleteProvider(id); err != nil {
			h.writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		h.methodNotAllowed(w)
	}
}

func (h *Handler) saveProvider(id string, input providerInput) error {
	if !safeID(id) || strings.TrimSpace(input.Name) == "" || !validEnum(input.Kind, "api", "subscription", "local") || !validEnum(input.AuthMode, "api_key", "oauth", "none") || !validEnum(input.Protocol, "openai", "responses", "anthropic", "adapter") {
		return fmt.Errorf("%w: provider policy", store.ErrInvalid)
	}
	if input.AuthMode == "oauth" {
		h.providerMu.Lock()
		defer h.providerMu.Unlock()
		providers, err := h.store.Providers()
		if err != nil {
			return err
		}
		for _, existing := range providers {
			if existing.ID != id && existing.AuthMode == "oauth" && existing.OAuthProvider == input.OAuthProvider {
				return fmt.Errorf("%w: one OAuth account provider record is allowed", store.ErrConflict)
			}
		}
	}
	if err := validateProviderEndpoint(input.Endpoint); err != nil {
		return err
	}
	if input.AuthMode == "oauth" && (!validEnum(input.OAuthProvider, "codex", "kimi") || input.Protocol != "adapter") {
		return fmt.Errorf("%w: oauth provider", store.ErrInvalid)
	}
	if input.AuthMode != "oauth" && input.OAuthProvider != "" {
		return fmt.Errorf("%w: oauth provider", store.ErrInvalid)
	}
	if input.Kind == "local" && input.AuthMode == "oauth" {
		return fmt.Errorf("%w: local auth mode", store.ErrInvalid)
	}
	if input.AuthMode == "oauth" && input.RemoveSecret && input.Enabled {
		return fmt.Errorf("%w: OAuth secret removal disables provider", store.ErrInvalid)
	}
	if input.AuthMode == "oauth" && input.Enabled && (h.adapterManagementKey == "" || h.adapterAPIKey == "") {
		return errors.New("OAuth adapter unavailable")
	}
	if input.AuthMode == "oauth" && input.Enabled {
		snapshot, _, err := h.accountSnapshot(input.OAuthProvider)
		if err != nil || snapshot.count != 1 || snapshot.status != "active" {
			return fmt.Errorf("%w: OAuth account unavailable", store.ErrConflict)
		}
		if err := h.bindAccountPrefix(input.OAuthProvider, snapshot.name); err != nil {
			return err
		}
	}
	if input.Secret != nil && input.APIKey != nil {
		return fmt.Errorf("%w: secret fields", store.ErrInvalid)
	}
	if input.AuthMode == "oauth" && (input.Secret != nil || input.APIKey != nil) {
		return fmt.Errorf("%w: OAuth secret must come from the adapter key file", store.ErrInvalid)
	}
	if input.Secret == nil {
		input.Secret = input.APIKey
	}
	previous, err := h.store.Provider(id)
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if input.AuthMode == "oauth" && !input.RemoveSecret && input.Secret == nil && h.adapterAPIKey != "" {
		adapterKey := h.adapterAPIKey
		input.Secret = &adapterKey
	}
	ciphertext := ""
	if exists {
		ciphertext = previous.SecretCipher
	}
	if input.RemoveSecret {
		if input.Secret != nil {
			return fmt.Errorf("%w: secret fields", store.ErrInvalid)
		}
		ciphertext = ""
	} else if input.Secret != nil {
		if *input.Secret == "" {
			return fmt.Errorf("%w: secret fields", store.ErrInvalid)
		}
		ciphertext, err = h.vault.Seal([]byte(*input.Secret))
		if err != nil {
			return err
		}
	}
	return h.store.UpsertProvider(store.Provider{ID: id, Name: strings.TrimSpace(input.Name), Kind: input.Kind, AuthMode: input.AuthMode, Protocol: input.Protocol, Endpoint: input.Endpoint, Enabled: input.Enabled, OAuthProvider: input.OAuthProvider, SecretCipher: ciphertext, CreatedAt: previous.CreatedAt})
}

func redactProvider(item store.Provider) providerView {
	return providerView{ID: item.ID, Name: item.Name, Kind: item.Kind, AuthMode: item.AuthMode, Protocol: item.Protocol, Endpoint: item.Endpoint, Enabled: item.Enabled, OAuthProvider: item.OAuthProvider, HasSecret: item.SecretCipher != "", CreatedAt: item.CreatedAt}
}

type modelInput struct {
	ID            string   `json:"id"`
	ProviderID    string   `json:"provider_id"`
	UpstreamModel string   `json:"upstream_model"`
	Name          string   `json:"name"`
	Protocols     []string `json:"protocols"`
	InputImages   bool     `json:"input_images"`
	Enabled       bool     `json:"enabled"`
	InputPrice    *float64 `json:"input_price"`
	OutputPrice   *float64 `json:"output_price"`
}

func (h *Handler) model(w http.ResponseWriter, r *http.Request, id string) {
	if !safeID(id) {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := h.store.Model(id)
		if errors.Is(err, store.ErrNotFound) {
			h.writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
			return
		}
		h.writeJSON(w, http.StatusOK, item)
	case http.MethodPut:
		var input modelInput
		if !h.decodeJSON(w, r, &input) {
			return
		}
		if input.ID != "" && input.ID != id {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
			return
		}
		if err := validateModelInput(id, input); err != nil {
			h.writeDomainError(w, err)
			return
		}
		item := store.Model{ID: id, ProviderID: input.ProviderID, UpstreamModel: input.UpstreamModel, Name: input.Name, Protocols: input.Protocols, InputImages: input.InputImages, Enabled: input.Enabled, InputPrice: input.InputPrice, OutputPrice: input.OutputPrice}
		if err := h.store.UpsertModel(item); err != nil {
			h.writeDomainError(w, err)
			return
		}
		h.writeJSON(w, http.StatusOK, item)
	case http.MethodDelete:
		if err := h.store.DeleteModel(id); err != nil {
			h.writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		h.methodNotAllowed(w)
	}
}

func validateModelInput(id string, input modelInput) error {
	if !safeID(id) || !safeID(input.ProviderID) || strings.TrimSpace(input.UpstreamModel) == "" || strings.TrimSpace(input.Name) == "" || len(input.Protocols) == 0 {
		return fmt.Errorf("%w: model policy", store.ErrInvalid)
	}
	for _, protocol := range input.Protocols {
		if !validEnum(protocol, "openai", "responses", "anthropic") {
			return fmt.Errorf("%w: model protocol", store.ErrInvalid)
		}
	}
	if input.InputPrice != nil && *input.InputPrice < 0 || input.OutputPrice != nil && *input.OutputPrice < 0 {
		return fmt.Errorf("%w: model price", store.ErrInvalid)
	}
	return nil
}

type callerCreateInput struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	AllowedModels   []string `json:"allowed_models"`
	AccessScope     string   `json:"access_scope"`
	LocalOnly       bool     `json:"local_only"`
	Enabled         bool     `json:"enabled"`
	RPM             int      `json:"rpm"`
	MaxConcurrency  int      `json:"max_concurrency"`
	DailyTokenLimit int64    `json:"daily_token_limit"`
}

type callerUpdateInput struct {
	Name            *string   `json:"name"`
	AllowedModels   *[]string `json:"allowed_models"`
	AccessScope     *string   `json:"access_scope"`
	LocalOnly       *bool     `json:"local_only"`
	Enabled         *bool     `json:"enabled"`
	RPM             *int      `json:"rpm"`
	MaxConcurrency  *int      `json:"max_concurrency"`
	DailyTokenLimit *int64    `json:"daily_token_limit"`
}

type callerView struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	AllowedModels   []string  `json:"allowed_models"`
	AccessScope     string    `json:"access_scope"`
	LocalOnly       bool      `json:"local_only"`
	Enabled         bool      `json:"enabled"`
	RPM             int       `json:"rpm"`
	MaxConcurrency  int       `json:"max_concurrency"`
	DailyTokenLimit int64     `json:"daily_token_limit"`
	CreatedAt       time.Time `json:"created_at"`
}

func (h *Handler) createCaller(w http.ResponseWriter, r *http.Request) {
	var input callerCreateInput
	if !h.decodeJSON(w, r, &input) {
		return
	}
	if input.ID == "" {
		input.ID = "caller-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	if err := validateCallerPolicy(input.ID, input.Name, input.AllowedModels, input.AccessScope, input.RPM, input.MaxConcurrency, input.DailyTokenLimit); err != nil {
		h.writeDomainError(w, err)
		return
	}
	raw, err := security.NewKey()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	ciphertext, err := h.vault.Seal([]byte(raw))
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	item := store.Caller{ID: input.ID, Name: strings.TrimSpace(input.Name), KeyHash: security.HashKey(raw), KeyCipher: ciphertext, AllowedModels: input.AllowedModels, AccessScope: input.AccessScope, LocalOnly: input.LocalOnly, Enabled: input.Enabled, RPM: input.RPM, MaxConcurrency: input.MaxConcurrency, DailyTokenLimit: input.DailyTokenLimit}
	if err := h.store.UpsertCaller(item); err != nil {
		h.writeDomainError(w, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]any{"caller": redactCaller(item), "key": raw})
}

func (h *Handler) caller(w http.ResponseWriter, r *http.Request, id string) {
	if !safeID(id) {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var input callerUpdateInput
		if !h.decodeJSON(w, r, &input) {
			return
		}
		item, err := h.store.Caller(id)
		if errors.Is(err, store.ErrNotFound) {
			h.writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
			return
		}
		if input.Name != nil {
			item.Name = *input.Name
		}
		if input.AllowedModels != nil {
			item.AllowedModels = *input.AllowedModels
		}
		if input.AccessScope != nil {
			item.AccessScope = *input.AccessScope
		}
		if input.LocalOnly != nil {
			item.LocalOnly = *input.LocalOnly
		}
		if input.Enabled != nil {
			item.Enabled = *input.Enabled
		}
		if input.RPM != nil {
			item.RPM = *input.RPM
		}
		if input.MaxConcurrency != nil {
			item.MaxConcurrency = *input.MaxConcurrency
		}
		if input.DailyTokenLimit != nil {
			item.DailyTokenLimit = *input.DailyTokenLimit
		}
		if err := validateCallerPolicy(item.ID, item.Name, item.AllowedModels, item.AccessScope, item.RPM, item.MaxConcurrency, item.DailyTokenLimit); err != nil {
			h.writeDomainError(w, err)
			return
		}
		if err := h.store.UpsertCaller(item); err != nil {
			h.writeDomainError(w, err)
			return
		}
		h.writeJSON(w, http.StatusOK, redactCaller(item))
	case http.MethodGet:
		item, err := h.store.Caller(id)
		if errors.Is(err, store.ErrNotFound) {
			h.writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
			return
		}
		h.writeJSON(w, http.StatusOK, redactCaller(item))
	case http.MethodDelete:
		if err := h.store.DeleteCaller(id); err != nil {
			h.writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		h.methodNotAllowed(w)
	}
}

func (h *Handler) rotateCaller(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost || !safeID(id) {
		h.methodNotAllowed(w)
		return
	}
	item, err := h.store.Caller(id)
	if errors.Is(err, store.ErrNotFound) {
		h.writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
		return
	}
	raw, err := security.NewKey()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	item.KeyHash = security.HashKey(raw)
	ciphertext, err := h.vault.Seal([]byte(raw))
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	item.KeyCipher = ciphertext
	if err := h.store.UpsertCaller(item); err != nil {
		h.writeDomainError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"caller": redactCaller(item), "key": raw})
}

func validateCallerPolicy(id, name string, models []string, scope string, rpm, concurrency int, daily int64) error {
	if !safeID(id) || strings.TrimSpace(name) == "" || !validEnum(scope, "lan", "public", "both") || rpm < 0 || concurrency < 0 || daily < 0 {
		return fmt.Errorf("%w: caller policy", store.ErrInvalid)
	}
	for _, model := range models {
		if !safeID(model) {
			return fmt.Errorf("%w: caller model", store.ErrInvalid)
		}
	}
	return nil
}

func redactCaller(item store.Caller) callerView {
	return callerView{ID: item.ID, Name: item.Name, AllowedModels: item.AllowedModels, AccessScope: item.AccessScope, LocalOnly: item.LocalOnly, Enabled: item.Enabled, RPM: item.RPM, MaxConcurrency: item.MaxConcurrency, DailyTokenLimit: item.DailyTokenLimit, CreatedAt: item.CreatedAt}
}

func validEnum(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func safeID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || strings.Contains(value, "..") || strings.Contains(value, "//") || strings.HasSuffix(value, "/") {
		return false
	}
	segmentStart := true
	for _, r := range value {
		if segmentStart && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
		if r == '/' {
			segmentStart = true
			continue
		}
		segmentStart = false
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func validateProviderEndpoint(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: provider endpoint", store.ErrInvalid)
	}
	return nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must be an http or https URL without userinfo, query, or fragment")
	}
	return u, nil
}

func (h *Handler) decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		h.writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON")
		return false
	}
	return true
}

func (h *Handler) writeDomainError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusBadRequest, "invalid_request", "invalid request"
	if errors.Is(err, store.ErrNotFound) {
		status, code, message = http.StatusNotFound, "not_found", "not found"
	}
	if errors.Is(err, store.ErrConflict) {
		status, code, message = http.StatusConflict, "conflict", "conflict"
	}
	h.writeError(w, status, code, message)
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter) {
	h.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func (h *Handler) writeError(w http.ResponseWriter, status int, code, message string) {
	h.writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "request_id": nextRequestID()}})
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	setNoStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func setNoStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func nextRequestID() string { return fmt.Sprintf("req_%d", atomic.AddUint64(&requestSequence, 1)) }

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
