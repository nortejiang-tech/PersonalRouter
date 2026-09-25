package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"personalrouter/internal/security"
	"personalrouter/internal/store"
)

func connectivityHandlerWithAdapterKeys(t *testing.T, adapterURL string) (http.Handler, string, *store.Store) {
	t.Helper()
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(runtimeDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key, err := security.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	master := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, master); err != nil {
		t.Fatal(err)
	}
	vault, err := security.NewVault(master)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Store: db, Vault: vault, AdminKeyHash: security.HashKey(key), AdapterURL: adapterURL, AdapterManagementKey: "adapter-secret", AdapterAPIKey: "adapter-secret"})
	if err != nil {
		t.Fatal(err)
	}
	return h, key, db
}

func saveConnectivityProvider(t *testing.T, h http.Handler, key, id, endpoint, protocol, secret string, enabled bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name": id, "kind": "api", "auth_mode": "api_key", "protocol": protocol,
		"endpoint": endpoint, "enabled": enabled, "secret": secret,
	})
	response := adminRequest(t, h, http.MethodPut, "http://admin.test/admin/api/providers/"+id, key, body)
	if response.Code != http.StatusOK {
		t.Fatalf("save provider %s: %d %s", id, response.Code, response.Body.String())
	}
}

func decodeConnectivityResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
}

func TestConnectivityProtocolsHeadersAndBodies(t *testing.T) {
	seen := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil || payload["stream"] != false || payload["model"] == nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer openai-secret" || r.Header.Get("x-api-key") != "" || payload["max_tokens"] != float64(16) {
				http.Error(w, "bad auth/body", http.StatusBadRequest)
				return
			}
			seen <- "openai"
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c","choices":[{"message":{"role":"assistant","content":"Reply OK"}}]}`)
		case "/v1/responses":
			if r.Header.Get("Authorization") != "Bearer responses-secret" || payload["max_output_tokens"] != float64(16) {
				http.Error(w, "bad auth/body", http.StatusBadRequest)
				return
			}
			seen <- "responses"
			_, _ = io.WriteString(w, `{"id":"r","object":"response","status":"completed","output":[{"type":"message"}]}`)
		case "/v1/messages":
			if r.Header.Get("x-api-key") != "anthropic-secret" || r.Header.Get("Authorization") != "" || r.Header.Get("anthropic-version") != "2023-06-01" {
				http.Error(w, "bad auth", http.StatusBadRequest)
				return
			}
			seen <- "anthropic"
			_, _ = io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"Reply OK"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	h, key, _ := testHandler(t, "", "")
	saveConnectivityProvider(t, h, key, "p-openai", server.URL, "openai", "openai-secret", true)
	saveConnectivityProvider(t, h, key, "p-responses", server.URL, "responses", "responses-secret", true)
	saveConnectivityProvider(t, h, key, "p-anthropic", server.URL, "anthropic", "anthropic-secret", true)
	for _, item := range []struct{ id, protocol string }{{"p-openai", "openai"}, {"p-responses", "responses"}, {"p-anthropic", "anthropic"}} {
		response := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/providers/"+item.id+"/test", key, []byte(`{"model":"real-upstream-model"}`))
		if response.Code != http.StatusOK {
			t.Fatalf("%s HTTP status = %d", item.protocol, response.Code)
		}
		var result ProviderTest
		decodeConnectivityResponse(t, response, &result)
		if result.Status != "ok" || result.Protocol != item.protocol || result.ProviderID != item.id || result.UpstreamStatus != 200 {
			t.Fatalf("%s result = %+v", item.protocol, result)
		}
		if strings.Contains(response.Body.String(), "Reply OK") {
			t.Fatalf("%s response leaked model output", item.protocol)
		}
	}
	close(seen)
	got := make([]string, 0, 3)
	for item := range seen {
		got = append(got, item)
	}
	if len(got) != 3 {
		t.Fatalf("upstream calls = %v", got)
	}
}

func TestConnectivityDiscoveryDedupesAndPreservesContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer discovery-secret" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"model-a","object":"model"},{"id":"model-a","created":1},{"id":"model-b","owned_by":"fixture"}]}`)
	}))
	defer server.Close()
	h, key, _ := testHandler(t, "", "")
	saveConnectivityProvider(t, h, key, "discover", server.URL, "openai", "discovery-secret", true)
	response := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/providers/discover/models", key, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("discovery HTTP status = %d", response.Code)
	}
	var result ProviderDiscovery
	decodeConnectivityResponse(t, response, &result)
	if result.Status != "ok" || result.Message != messageDiscoveryOK || !result.ProviderEnabled || len(result.Models) != 2 || result.Models[0].ID != "model-a" || result.Models[1].ID != "model-b" || result.UpstreamStatus != 200 {
		t.Fatalf("discovery result = %+v", result)
	}
	if result.CheckedAt == "" {
		t.Fatal("discovery missing checked_at")
	}
}

func TestConnectivityErrorsRedirectionOversizeAndCancellation(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/models", http.StatusFound)
	}))
	defer redirect.Close()
	oversize := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"`+strings.Repeat("x", 1<<20)+`"}]}`)
	}))
	defer oversize.Close()
	h, key, db := testHandler(t, "", "")
	saveConnectivityProvider(t, h, key, "redirect", redirect.URL, "openai", "redirect-secret", true)
	saveConnectivityProvider(t, h, key, "oversize", oversize.URL, "openai", "oversize-secret", true)
	redirectResult := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/providers/redirect/models", key, nil)
	if redirectResult.Code != http.StatusOK || strings.Contains(redirectResult.Body.String(), target.URL) || !strings.Contains(redirectResult.Body.String(), messageRedirect) {
		t.Fatalf("redirect result = %d %s", redirectResult.Code, redirectResult.Body.String())
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect was followed")
	}
	overResult := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/providers/oversize/models", key, nil)
	if overResult.Code != http.StatusOK || !strings.Contains(overResult.Body.String(), messageInvalidResponse) || strings.Contains(overResult.Body.String(), strings.Repeat("x", 100)) {
		t.Fatalf("oversize result = %d %s", overResult.Code, overResult.Body.String())
	}
	if _, err := db.Provider("missing"); err == nil {
		t.Fatal("fixture lookup unexpectedly succeeded")
	}
	// A canceled caller context reaches the request before transport and is
	// represented by the fixed timeout/cancellation message.
	cancelReq := httptest.NewRequest(http.MethodPost, "http://admin.test/admin/api/providers/redirect/test", bytes.NewReader([]byte(`{"model":"model-a"}`)))
	cancelCtx, cancel := context.WithCancel(cancelReq.Context())
	cancel()
	cancelReq = cancelReq.WithContext(cancelCtx)
	cancelReq.Host = "admin.test"
	cancelReq.Header.Set("Authorization", "Bearer "+key)
	cancelReq.Header.Set("Origin", "http://admin.test")
	cancelResponse := httptest.NewRecorder()
	h.ServeHTTP(cancelResponse, cancelReq)
	if cancelResponse.Code != http.StatusOK || !strings.Contains(cancelResponse.Body.String(), messageTimeout) {
		t.Fatalf("cancel result = %d %s", cancelResponse.Code, cancelResponse.Body.String())
	}
}

func TestConnectivityDisabledProviderAndOAuthPrefixIsolation(t *testing.T) {
	var oauthPosts atomic.Int32
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/auth-files":
			if r.Header.Get("X-Management-Key") != "adapter-secret" {
				http.Error(w, "bad management key", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"files":[{"name":"codex.json","provider":"codex","status":"active"}]}`)
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer adapter-secret" {
				http.Error(w, "management key leak", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"nr-codex/model-a"},{"id":"nr-kimi/model-b"},{"id":"nr-codex/model-a"}]}`)
		case "/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer adapter-secret" || r.Header.Get("X-Management-Key") != "" {
				http.Error(w, "bad auth", http.StatusUnauthorized)
				return
			}
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["model"] != "nr-codex/model-a" {
				http.Error(w, "bad prefix", http.StatusBadRequest)
				return
			}
			oauthPosts.Add(1)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer adapter.Close()
	h, key, db := connectivityHandlerWithAdapterKeys(t, adapter.URL)
	if err := db.UpsertProvider(store.Provider{ID: "oauth", Name: "OAuth", Kind: "subscription", AuthMode: "oauth", Protocol: "adapter", OAuthProvider: "codex", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	discovery := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/providers/oauth/models", key, nil)
	if discovery.Code != http.StatusOK || !strings.Contains(discovery.Body.String(), `"id":"model-a"`) || strings.Contains(discovery.Body.String(), "model-b") || strings.Contains(discovery.Body.String(), "nr-codex") {
		t.Fatalf("oauth discovery = %d %s", discovery.Code, discovery.Body.String())
	}
	otherPrefix := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/providers/oauth/test", key, []byte(`{"model":"nr-kimi/model-b"}`))
	if otherPrefix.Code != http.StatusOK || !strings.Contains(otherPrefix.Body.String(), messageInvalidRequest) || oauthPosts.Load() != 0 {
		t.Fatalf("oauth prefix isolation = %d %s", otherPrefix.Code, otherPrefix.Body.String())
	}
	ok := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/providers/oauth/test", key, []byte(`{"model":"model-a"}`))
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"status":"ok"`) || oauthPosts.Load() != 1 {
		t.Fatalf("oauth test = %d %s", ok.Code, ok.Body.String())
	}

	// Disabled providers remain testable and the persisted row is untouched.
	disabledServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer disabledServer.Close()
	saveConnectivityProvider(t, h, key, "disabled", disabledServer.URL, "openai", "disabled-secret", false)
	before, _ := db.Provider("disabled")
	disabled := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/providers/disabled/test", key, []byte(`{"model":"model-a"}`))
	after, _ := db.Provider("disabled")
	if disabled.Code != http.StatusOK || !strings.Contains(disabled.Body.String(), `"status":"ok"`) || !strings.Contains(disabled.Body.String(), `"provider_enabled":false`) || !reflect.DeepEqual(before, after) {
		t.Fatalf("disabled test = %d %s before=%+v after=%+v", disabled.Code, disabled.Body.String(), before, after)
	}
}

func TestConnectivityUnauthenticatedAndRequestValidation(t *testing.T) {
	h, key, _ := testHandler(t, "", "")
	unauthorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://admin.test/admin/api/providers/missing/models", nil)
	request.Host = "admin.test"
	h.ServeHTTP(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	unknown := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/providers/missing/test", key, []byte(`{"model":"m","unknown":true}`))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("missing provider precedence = %d %s", unknown.Code, unknown.Body.String())
	}
	// Existing provider makes the strict body and direct protocol mismatch
	// checks observable without contacting an upstream.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected upstream request")
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()
	saveConnectivityProvider(t, h, key, "strict", server.URL, "openai", "secret", true)
	badBody := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/providers/strict/test", key, []byte(`{"model":"m","unknown":true}`))
	if badBody.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", badBody.Code)
	}
	mismatch := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/providers/strict/test", key, []byte(fmt.Sprintf(`{"model":"m","protocol":"%s"}`, "anthropic")))
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("protocol mismatch status = %d", mismatch.Code)
	}
}

func TestConnectivityResponseValidationRejectsErrorsAndWrongShapes(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		body     string
	}{
		{"chat message string", "openai", `{"choices":[{"message":"ok"}]}`},
		{"chat error", "openai", `{"error":{"message":"upstream secret"}}`},
		{"responses failed", "responses", `{"object":"response","status":"failed","output":[]}`},
		{"responses cancelled", "responses", `{"object":"response","status":"cancelled","output":[]}`},
		{"responses wrong object", "responses", `{"object":"chat.completion","status":"completed","output":[]}`},
		{"responses error", "responses", `{"object":"response","status":"completed","output":[],"error":{"message":"bad"}}`},
		{"messages content string", "anthropic", `{"type":"message","content":"ok"}`},
		{"messages error", "anthropic", `{"type":"error","content":[],"error":{"message":"bad"}}`},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if validConnectivityResponse(item.protocol, []byte(item.body)) {
				t.Fatalf("accepted invalid %s response", item.protocol)
			}
		})
	}
	if !validConnectivityResponse("responses", []byte(`{"object":"response","status":"incomplete","output":[]}`)) {
		t.Fatal("rejected a structurally valid incomplete response")
	}
	if !validConnectivityResponse("responses", []byte(`{"object":"response","status":"completed","error":null,"output":[]}`)) {
		t.Fatal("rejected a valid Responses response with null error")
	}
}
