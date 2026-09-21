package admin

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"personalrouter/internal/security"
	"personalrouter/internal/store"
)

func testHandler(t *testing.T, adapterURL, adapterKey string) (http.Handler, string, *store.Store) {
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
	h, err := New(Options{Store: db, Vault: vault, AdminKeyHash: security.HashKey(key), AdapterURL: adapterURL, AdapterManagementKey: adapterKey})
	if err != nil {
		t.Fatal(err)
	}
	return h, key, db
}

func handlerForStore(t *testing.T, db *store.Store, adapterURL, adapterKey string) (http.Handler, string) {
	t.Helper()
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
	h, err := New(Options{Store: db, Vault: vault, AdminKeyHash: security.HashKey(key), AdapterURL: adapterURL, AdapterManagementKey: adapterKey})
	if err != nil {
		t.Fatal(err)
	}
	return h, key
}

func adminRequest(t *testing.T, h http.Handler, method, target, key string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Host = "admin.test"
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Origin", "http://admin.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAdminAuthOriginAndBodyLimit(t *testing.T) {
	h, key, _ := testHandler(t, "", "")
	unauthorized := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://admin.test/admin/api/settings", nil)
	req.Host = "admin.test"
	h.ServeHTTP(unauthorized, req)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	if unauthorized.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}

	badOrigin := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/settings", key, nil)
	badOrigin.Result().Header.Set("Origin", "http://wrong.test")
	// Recreate the request because headers are read from the request, not response.
	req = httptest.NewRequest(http.MethodGet, "http://admin.test/admin/api/settings", nil)
	req.Host = "admin.test"
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Origin", "http://wrong.test")
	badOrigin = httptest.NewRecorder()
	h.ServeHTTP(badOrigin, req)
	if badOrigin.Code != http.StatusForbidden {
		t.Fatalf("bad origin status = %d", badOrigin.Code)
	}

	over := bytes.Repeat([]byte("x"), 1<<20+1)
	oversize := adminRequest(t, h, http.MethodPut, "http://admin.test/admin/api/providers/p1", key, over)
	if oversize.Code != http.StatusBadRequest {
		t.Fatalf("oversize status = %d", oversize.Code)
	}
}

func TestProviderSecretRedactionAndPreservation(t *testing.T) {
	h, key, db := testHandler(t, "", "")
	body := []byte(`{"name":"OpenAI","kind":"api","auth_mode":"api_key","protocol":"openai","endpoint":"https://api.example.test","enabled":true,"secret":"sk-test-secret"}`)
	created := adminRequest(t, h, http.MethodPut, "http://admin.test/admin/api/providers/p1", key, body)
	if created.Code != http.StatusOK {
		t.Fatalf("create provider status = %d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "sk-test-secret") || strings.Contains(created.Body.String(), "secret_cipher") {
		t.Fatal("provider response leaked secret")
	}
	if !strings.Contains(created.Body.String(), `"has_secret":true`) {
		t.Fatal("provider did not report secret presence")
	}

	unchanged := adminRequest(t, h, http.MethodPut, "http://admin.test/admin/api/providers/p1", key, []byte(`{"name":"OpenAI 2","kind":"api","auth_mode":"api_key","protocol":"openai","endpoint":"https://api.example.test","enabled":true}`))
	if unchanged.Code != http.StatusOK || !strings.Contains(unchanged.Body.String(), `"has_secret":true`) {
		t.Fatalf("secret was not preserved: %d %s", unchanged.Code, unchanged.Body.String())
	}
	stored, err := db.Provider("p1")
	if err != nil || stored.SecretCipher == "" {
		t.Fatalf("stored secret missing: %v", err)
	}
	removed := adminRequest(t, h, http.MethodPut, "http://admin.test/admin/api/providers/p1", key, []byte(`{"name":"OpenAI 2","kind":"api","auth_mode":"api_key","protocol":"openai","endpoint":"https://api.example.test","enabled":true,"remove_secret":true}`))
	if removed.Code != http.StatusOK || strings.Contains(removed.Body.String(), `"has_secret":true`) {
		t.Fatalf("secret was not removed: %d %s", removed.Code, removed.Body.String())
	}
	unsafe := adminRequest(t, h, http.MethodPut, "http://admin.test/admin/api/providers/p2", key, []byte(`{"name":"bad","kind":"api","auth_mode":"api_key","protocol":"openai","endpoint":"https://user:pass@example.test/x?token=y"}`))
	if unsafe.Code != http.StatusBadRequest {
		t.Fatalf("unsafe endpoint status = %d", unsafe.Code)
	}
}

func TestCallerKeyRotationAndRedaction(t *testing.T) {
	h, key, db := testHandler(t, "", "")
	created := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/callers", key, []byte(`{"id":"caller-1","name":"CLI","allowed_models":["p1/model"],"access_scope":"lan","enabled":true}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("caller create status = %d %s", created.Code, created.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	rawKey, ok := response["key"].(string)
	if !ok || rawKey == "" {
		t.Fatal("caller key was not returned")
	}
	list := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/callers", key, nil)
	if strings.Contains(list.Body.String(), rawKey) || strings.Contains(list.Body.String(), "key_hash") {
		t.Fatal("caller secret leaked")
	}
	rotated := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/callers/caller-1/rotate", key, nil)
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate status = %d %s", rotated.Code, rotated.Body.String())
	}
	var rotatedResponse map[string]any
	if err := json.Unmarshal(rotated.Body.Bytes(), &rotatedResponse); err != nil {
		t.Fatal(err)
	}
	newKey, _ := rotatedResponse["key"].(string)
	item, err := db.Caller("caller-1")
	if err != nil || security.VerifyKey(rawKey, item.KeyHash) || !security.VerifyKey(newKey, item.KeyHash) {
		t.Fatal("key rotation did not replace hash")
	}
}

func TestOAuthFixedPathsAndReplay(t *testing.T) {
	var callbackPath string
	var fieldsBody string
	accountPresent := false
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Management-Key") != "adapter-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		callbackPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/auth-files":
			if accountPresent {
				io.WriteString(w, `{"files":[{"name":"codex-account.json","provider":"codex","status":"active","prefix":"nr-codex/"}]}`)
			} else {
				io.WriteString(w, `{"files":[]}`)
			}
		case "/v0/management/codex-auth-url":
			io.WriteString(w, `{"url":"https://auth.openai.com/oauth/authorize?state=adapter","state":"state-1"}`)
		case "/v0/management/get-auth-status":
			io.WriteString(w, `{"status":"ok","error":"raw upstream detail"}`)
		case "/v0/management/oauth-callback":
			accountPresent = true
			io.WriteString(w, `{"status":"ok"}`)
		case "/v0/management/auth-files/fields":
			data, _ := io.ReadAll(r.Body)
			fieldsBody = string(data)
			io.WriteString(w, `{"status":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer adapter.Close()
	h, key, _ := testHandler(t, adapter.URL, "adapter-secret")
	start := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/oauth/codex/start", key, nil)
	if start.Code != http.StatusOK || !strings.Contains(start.Body.String(), "auth.openai.com") {
		t.Fatalf("oauth start: %d %s", start.Code, start.Body.String())
	}
	callback := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/oauth/callback", key, []byte(`{"provider":"codex","state":"state-1","code":"code-1"}`))
	if callback.Code != http.StatusOK || callbackPath != "/v0/management/oauth-callback" {
		t.Fatalf("callback: %d path=%s body=%s", callback.Code, callbackPath, callback.Body.String())
	}
	status := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/oauth/status?state=state-1", key, nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"status":"completed"`) || strings.Contains(status.Body.String(), "raw upstream detail") {
		t.Fatalf("oauth status leaked adapter detail: %d %s", status.Code, status.Body.String())
	}
	if fieldsBody != `{"name":"codex-account.json","prefix":"nr-codex","request_retry":0}` {
		t.Fatalf("prefix patch uses unsafe adapter prefix: %s", fieldsBody)
	}
	replay := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/oauth/callback", key, []byte(`{"provider":"codex","state":"state-1","code":"code-2"}`))
	if replay.Code != http.StatusConflict {
		t.Fatalf("callback replay status = %d", replay.Code)
	}
	badURL := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/oauth/callback", key, []byte(`{"provider":"codex","callback_url":"https://evil.test/auth/callback?state=state-1&code=bad"}`))
	if badURL.Code != http.StatusBadRequest {
		t.Fatalf("callback URL validation status = %d", badURL.Code)
	}
}

func TestOAuthAccountLifecycleIsSingleAndRedacted(t *testing.T) {
	accountPresent := true
	duplicate := false
	refreshOK := true
	var seenPaths []string
	var refreshBody string
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Management-Key") != "adapter-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		seenPaths = append(seenPaths, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/auth-files":
			if r.Method == http.MethodDelete {
				accountPresent = false
				io.WriteString(w, `{"status":"ok","token":"SENTINEL"}`)
				return
			}
			if !accountPresent {
				io.WriteString(w, `{"files":[]}`)
				return
			}
			if duplicate {
				io.WriteString(w, `{"files":[{"name":"one.json","provider":"codex","status":"active"},{"name":"two.json","provider":"codex","status":"active"}]}`)
				return
			}
			io.WriteString(w, `{"files":[{"name":"account.json","provider":"codex","status":"active","prefix":"nr-codex/","email":"private@example.test","id_token":"sentinel"}]}`)
		case "/v0/management/auth-files/refresh":
			data, _ := io.ReadAll(r.Body)
			refreshBody = string(data)
			if !refreshOK {
				io.WriteString(w, `{"ok":false,"error":"raw refresh failure","token":"SENTINEL"}`)
				return
			}
			io.WriteString(w, `{"ok":true,"auth":{"token":"SENTINEL"}}`)
		case "/v0/management/auth-files/fields":
			_, _ = io.ReadAll(r.Body)
			io.WriteString(w, `{"status":"ok","token":"SENTINEL"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer adapter.Close()
	h, key, db := testHandler(t, adapter.URL, "adapter-secret")
	if err := db.UpsertProvider(store.Provider{ID: "codex-account", Name: "Codex", Kind: "subscription", AuthMode: "oauth", Protocol: "adapter", OAuthProvider: "codex", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	account := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/oauth/codex/account", key, nil)
	if account.Code != http.StatusOK || !strings.Contains(account.Body.String(), `"status":"active"`) || !strings.Contains(account.Body.String(), `"account_count":1`) || strings.Contains(account.Body.String(), "account.json") || strings.Contains(account.Body.String(), "private@example.test") {
		t.Fatalf("account response unsafe: %d %s", account.Code, account.Body.String())
	}
	restarted, restartedKey := handlerForStore(t, db, adapter.URL, "adapter-secret")
	recovered := adminRequest(t, restarted, http.MethodGet, "http://admin.test/admin/api/oauth/codex/account", restartedKey, nil)
	if recovered.Code != http.StatusOK || !strings.Contains(recovered.Body.String(), `"status":"active"`) {
		t.Fatalf("account status did not survive handler restart: %d %s", recovered.Code, recovered.Body.String())
	}

	refreshed := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/oauth/codex/refresh", key, nil)
	if refreshed.Code != http.StatusOK || strings.Contains(refreshed.Body.String(), "SENTINEL") || refreshBody != `{"name":"account.json"}` {
		t.Fatalf("refresh response/request unsafe: %d %s body=%s", refreshed.Code, refreshed.Body.String(), refreshBody)
	}

	duplicate = true
	conflict := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/oauth/codex/account", key, nil)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("duplicate account status = %d body=%s", conflict.Code, conflict.Body.String())
	}
	duplicate = false
	refreshOK = false
	failed := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/oauth/codex/refresh", key, nil)
	if failed.Code != http.StatusBadGateway || strings.Contains(failed.Body.String(), "SENTINEL") || strings.Contains(failed.Body.String(), "raw refresh") {
		t.Fatalf("refresh failure was not fixed/redacted: %d %s", failed.Code, failed.Body.String())
	}
	refreshOK = true

	deleted := adminRequest(t, h, http.MethodDelete, "http://admin.test/admin/api/oauth/codex/account", key, nil)
	if deleted.Code != http.StatusOK || strings.Contains(deleted.Body.String(), "account.json") || strings.Contains(deleted.Body.String(), "SENTINEL") {
		t.Fatalf("delete response unsafe: %d %s", deleted.Code, deleted.Body.String())
	}
	provider, err := db.Provider("codex-account")
	if err != nil || provider.Enabled {
		t.Fatalf("provider was not disabled before delete: err=%v enabled=%v", err, provider.Enabled)
	}
	if accountPresent || len(seenPaths) == 0 {
		t.Fatalf("delete route was not called: %v", seenPaths)
	}
	missing := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/oauth/codex/account", key, nil)
	if missing.Code != http.StatusOK || !strings.Contains(missing.Body.String(), `"status":"missing"`) {
		t.Fatalf("missing account status: %d %s", missing.Code, missing.Body.String())
	}
}

func TestOAuthStartRejectsParallelLogin(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Management-Key") != "adapter-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/auth-files":
			io.WriteString(w, `{"files":[]}`)
		case "/v0/management/codex-auth-url":
			once.Do(func() { close(started) })
			<-release
			io.WriteString(w, `{"url":"https://auth.openai.com/oauth/authorize","state":"parallel-state"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer adapter.Close()
	h, key, _ := testHandler(t, adapter.URL, "adapter-secret")
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		first <- adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/oauth/codex/start", key, nil)
	}()
	<-started
	parallel := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/oauth/codex/start", key, nil)
	if parallel.Code != http.StatusConflict || !strings.Contains(parallel.Body.String(), "oauth_in_progress") {
		t.Fatalf("parallel login was not rejected: %d %s", parallel.Code, parallel.Body.String())
	}
	close(release)
	if result := <-first; result.Code != http.StatusOK {
		t.Fatalf("first login failed after releasing mock: %d %s", result.Code, result.Body.String())
	}
}
