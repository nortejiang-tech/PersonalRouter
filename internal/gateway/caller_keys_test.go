package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"personalrouter/internal/security"
	"personalrouter/internal/store"
)

func TestGatewayCapturesLegacyCallerKeyForBearerAndAPIKeyAndSurvivesReopen(t *testing.T) {
	for _, header := range []string{"bearer", "x-api-key"} {
		t.Run(header, func(t *testing.T) {
			gateway, key, database, path, vault := callerKeyFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
			})
			request := httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil)
			if header == "bearer" {
				request.Header.Set("Authorization", "Bearer "+key)
			} else {
				request.Header.Set("x-api-key", key)
			}
			first := httptest.NewRecorder()
			gateway.Handler("lan").ServeHTTP(first, request)
			if first.Code != http.StatusOK || strings.Contains(first.Body.String(), key) {
				t.Fatalf("legacy models response was not successful and key-free, status = %d", first.Code)
			}
			caller, err := database.Caller("caller")
			if err != nil || caller.KeyCipher == "" {
				t.Fatalf("legacy caller cipher unavailable, err = %v", err)
			}
			captured := caller.KeyCipher
			plaintext, err := vault.Open(captured)
			if err != nil || string(plaintext) != key {
				t.Fatalf("captured key did not decrypt to the verified request key, err = %v", err)
			}

			second := httptest.NewRecorder()
			gateway.Handler("lan").ServeHTTP(second, request)
			caller, err = database.Caller("caller")
			if err != nil || caller.KeyCipher != captured {
				t.Fatalf("repeated capture changed the stored cipher, err = %v", err)
			}
			if marshaled, marshalErr := json.Marshal(caller); marshalErr != nil || strings.Contains(string(marshaled), key) {
				t.Fatalf("caller JSON was not key-free, err = %v", marshalErr)
			}

			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopenedCaller, err := reopened.Caller("caller")
			if err != nil || reopenedCaller.KeyCipher != captured {
				t.Fatalf("reopened caller cipher was not preserved, err = %v", err)
			}
			plaintext, err = vault.Open(reopenedCaller.KeyCipher)
			if err != nil || string(plaintext) != key {
				t.Fatalf("reopened key did not decrypt to the verified request key, err = %v", err)
			}
		})
	}
}

func TestGatewayLegacyCaptureIsBestEffortAndOnlyFollowsValidEnabledAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer upstream.Close()
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	defer database.Close()

	wrong := httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil)
	wrong.Header.Set("Authorization", "Bearer wrong-key")
	wrongResponse := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(wrongResponse, wrong)
	if wrongResponse.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key status = %d", wrongResponse.Code)
	}
	caller, err := database.Caller("caller")
	if err != nil || caller.KeyCipher != "" {
		t.Fatalf("wrong key unexpectedly captured a cipher, err = %v", err)
	}

	caller.Enabled = false
	if err := database.UpsertCaller(caller); err != nil {
		t.Fatal(err)
	}
	disabled := httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil)
	disabled.Header.Set("Authorization", "Bearer "+key)
	disabledResponse := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(disabledResponse, disabled)
	if disabledResponse.Code != http.StatusUnauthorized {
		t.Fatalf("disabled caller status = %d", disabledResponse.Code)
	}
	caller, err = database.Caller("caller")
	if err != nil || caller.KeyCipher != "" {
		t.Fatalf("disabled caller unexpectedly captured a cipher, err = %v", err)
	}

	caller.Enabled = true
	if err := database.UpsertCaller(caller); err != nil {
		t.Fatal(err)
	}
	gateway.vault = nil
	valid := httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil)
	valid.Header.Set("Authorization", "Bearer "+key)
	validResponse := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(validResponse, valid)
	if validResponse.Code != http.StatusOK {
		t.Fatalf("nil-vault valid auth status = %d", validResponse.Code)
	}
	caller, err = database.Caller("caller")
	if err != nil || caller.KeyCipher != "" {
		t.Fatalf("nil-vault request unexpectedly captured a cipher, err = %v", err)
	}
}

func TestGatewaySharedCallerKeySupportsTwoToolsAndAggregatesUsage(t *testing.T) {
	var started atomic.Int32
	bothStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if started.Add(1) == 2 {
			close(bothStarted)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"r1","model":"public-model","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}))
	t.Cleanup(func() { releaseAll(); upstream.Close() })
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	defer database.Close()

	call := func(userAgent string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", userAgent)
		response := httptest.NewRecorder()
		gateway.Handler("lan").ServeHTTP(response, request)
		return response
	}

	results := make(chan *httptest.ResponseRecorder, 3)
	go func() { results <- call("ZCode") }()
	go func() { results <- call("Codex") }()
	select {
	case <-bothStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("two shared-key requests did not enter concurrently")
	}

	thirdDone := make(chan struct{})
	go func() {
		third := call("Other-tool")
		if third.Code != http.StatusTooManyRequests {
			t.Errorf("above-capacity status = %d", third.Code)
		}
		close(thirdDone)
	}()
	select {
	case <-thirdDone:
	case <-time.After(2 * time.Second):
		t.Fatal("above-capacity request did not return")
	}
	releaseAll()

	for range 2 {
		select {
		case result := <-results:
			if result.Code != http.StatusOK {
				t.Fatalf("shared-key request status = %d", result.Code)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("shared-key request did not complete")
		}
	}
	rows, err := database.Requests(store.RequestFilter{CallerID: "caller", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var successful, denied int
	for _, row := range rows {
		if strings.Contains(row.Model, key) || strings.Contains(row.UpstreamModel, key) || strings.Contains(row.ResponseModel, key) {
			t.Fatal("caller key reached request ledger")
		}
		if row.Status == http.StatusOK && row.CallerID == "caller" {
			successful++
		}
		if row.Status == http.StatusTooManyRequests && row.ErrorCode == "concurrency_limited" {
			denied++
		}
	}
	if successful != 2 || denied != 1 {
		t.Fatalf("shared-key ledger successful/denied = %d/%d", successful, denied)
	}
	tokens, unknown, err := database.CallerUsageSince("caller", time.Now().Add(-time.Hour))
	if err != nil || unknown || tokens != 6 {
		t.Fatalf("aggregated usage = %d, unknown = %v, err = %v", tokens, unknown, err)
	}
	if marshaled, marshalErr := json.Marshal(rows); marshalErr != nil || strings.Contains(string(marshaled), key) {
		t.Fatalf("request ledger was not key-free, err = %v", marshalErr)
	}
}

func callerKeyFixture(t *testing.T, handler http.HandlerFunc) (*Gateway, string, *store.Store, string, *security.Vault) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "state.sqlite")
	database, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := security.NewVault(bytes32(9))
	if err != nil {
		t.Fatal(err)
	}
	providerCipher, err := vault.Seal([]byte("provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(handler)
	t.Cleanup(func() { upstream.Close(); database.Close() })
	if err := database.UpsertProvider(store.Provider{ID: "provider", Name: "provider", Kind: "api", AuthMode: "api_key", Protocol: "openai", Endpoint: upstream.URL, Enabled: true, SecretCipher: providerCipher}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertModel(store.Model{ID: "public-model", ProviderID: "provider", UpstreamModel: "upstream-model", Name: "public", Protocols: []string{"openai"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key, err := security.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertCaller(store.Caller{ID: "caller", Name: "caller", KeyHash: security.HashKey(key), AllowedModels: []string{"public-model"}, AccessScope: "lan", Enabled: true, MaxConcurrency: 2}); err != nil {
		t.Fatal(err)
	}
	return New(database, vault, Options{Timeout: time.Second}), key, database, path, vault
}
