package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"personalrouter/internal/security"
	"personalrouter/internal/store"
)

func TestGatewayTransformsModelAndForwardsOnlyProviderCredential(t *testing.T) {
	var calls atomic.Int32
	var gotAuth string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotAuth = r.Header.Get("Authorization")
		if r.Header.Get("x-api-key") != "" || r.Header.Get("X-Caller-Key") != "" {
			t.Errorf("caller credential reached upstream")
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"r1","model":"upstream-model","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}))
	defer upstream.Close()

	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	defer database.Close()
	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"image_url":{"type":"string"},"photo":{"type":"image"}}}}}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Caller-Key", "caller-secret")
	response := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if calls.Load() != 1 || gotAuth != "Bearer provider-secret" {
		t.Fatalf("upstream calls/auth = %d/%q", calls.Load(), gotAuth)
	}
	if gotBody["model"] != "upstream-model" {
		t.Fatalf("upstream model = %#v", gotBody["model"])
	}
	if _, ok := gotBody["tools"]; !ok {
		t.Fatal("tool payload was not preserved")
	}
	if strings.Contains(response.Body.String(), "provider-secret") {
		t.Fatal("provider credential leaked in downstream response")
	}
}

func TestGatewayScopeAndLocalOnlyIsolation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"r1","model":"m","choices":[]}`)
	}))
	defer upstream.Close()
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai", Kind: "api"})
	defer database.Close()

	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	gateway.Handler("public").ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("public scope status = %d", response.Code)
	}
}

func TestGatewayRefusesRedirectAndDoesNotRetry(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		_, _ = io.WriteString(w, `{"id":"r1","model":"m","choices":[]}`)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	gateway, key, database := testGateway(t, redirect.URL, store.Provider{Protocol: "openai"})
	defer database.Close()

	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || targetCalls.Load() != 0 {
		t.Fatalf("redirect status/target calls = %d/%d", response.Code, targetCalls.Load())
	}
	rows, err := database.Requests(store.RequestFilter{CallerID: "caller", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Status != http.StatusBadGateway || rows[0].Attempts != 1 {
		t.Fatalf("redirect ledger = %#v, err = %v", rows, err)
	}
}

func TestEndpointRoutingUsesConfiguredBaseWithoutQuery(t *testing.T) {
	gateway := New(nil, nil, Options{AdapterEndpoint: "http://adapter.internal/api/coding/paas/v4"})
	provider := store.Provider{Protocol: "openai", Endpoint: "http://provider.internal/api/coding/paas/v4"}
	got, err := gateway.endpoint(provider, "/v1/chat/completions")
	if err != nil || got != "http://provider.internal/api/coding/paas/v4/chat/completions" {
		t.Fatalf("base endpoint = %q, err = %v", got, err)
	}
	provider.Endpoint = "http://provider.internal/v1/chat/completions"
	got, err = gateway.endpoint(provider, "/v1/chat/completions")
	if err != nil || got != provider.Endpoint {
		t.Fatalf("exact endpoint = %q, err = %v", got, err)
	}
	provider.Endpoint = "http://provider.internal/api/coding/paas/v4/chat/completions"
	got, err = gateway.endpoint(provider, "/v1/chat/completions")
	if err != nil || got != provider.Endpoint {
		t.Fatalf("exact non-v1 endpoint = %q, err = %v", got, err)
	}
	provider.Endpoint = "http://provider.internal/v1?token=secret"
	if _, err := gateway.endpoint(provider, "/v1/chat/completions"); err == nil {
		t.Fatal("endpoint query was accepted")
	}
}

func TestGatewayStreamsSSEAndPersistsCompletion(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"r1\",\"model\":\"upstream-model\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	defer database.Close()
	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model","stream":true}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("stream status/body = %d/%q", response.Code, response.Body.String())
	}
	rows, err := database.Requests(store.RequestFilter{CallerID: "caller", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Outcome != "success" || rows[0].Attempts != 1 {
		t.Fatalf("stream ledger = %#v, err = %v", rows, err)
	}
}

func TestGatewayNullUsageIsRecordedAsUnknownAndImagesAreRejected(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"id":"r1","model":"upstream-model","choices":[]}`)
	}))
	defer upstream.Close()
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	defer database.Close()

	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("null usage status = %d", response.Code)
	}
	rows, err := database.Requests(store.RequestFilter{CallerID: "caller", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].InputTokens != nil || rows[0].OutputTokens != nil {
		t.Fatalf("null usage ledger = %#v, err = %v", rows, err)
	}

	imageRequest := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/a.png"}}]}]}`))
	imageRequest.Header.Set("Authorization", "Bearer "+key)
	imageRequest.Header.Set("Content-Type", "application/json")
	imageResponse := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(imageResponse, imageRequest)
	if imageResponse.Code != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatalf("image status/upstream calls = %d/%d", imageResponse.Code, calls.Load())
	}
}

func TestGatewayRoutesAllProtocolsAndStreamingForms(t *testing.T) {
	tests := []struct {
		name, protocol, path, body string
		stream                     bool
	}{
		{"openai-json", "openai", "/v1/chat/completions", `{"id":"r","model":"m","choices":[]}`, false},
		{"responses-json", "responses", "/v1/responses", `{"id":"r","model":"m","status":"completed"}`, false},
		{"anthropic-json", "anthropic", "/v1/messages", `{"id":"r","model":"m","type":"message","content":[]}`, false},
		{"openai-sse", "openai", "/v1/chat/completions", "data: {\"model\":\"m\",\"choices\":[]}\n\ndata: [DONE]\n\n", true},
		{"responses-sse", "responses", "/v1/responses", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", true},
		{"anthropic-sse", "anthropic", "/v1/messages", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.path {
					t.Errorf("upstream path = %s, want %s", r.URL.Path, test.path)
				}
				if test.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()
			gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: test.protocol})
			defer database.Close()
			model, err := database.Model("public-model")
			if err != nil {
				t.Fatal(err)
			}
			model.Protocols = []string{test.protocol}
			if err := database.UpsertModel(model); err != nil {
				t.Fatal(err)
			}
			payload := `{"model":"public-model"}`
			if test.stream {
				payload = `{"model":"public-model","stream":true}`
			}
			request := httptest.NewRequest(http.MethodPost, "http://gateway.test"+test.path, strings.NewReader(payload))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			gateway.Handler("lan").ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestGatewayDisabledCallerKeyAndLocalOnlyPolicy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"r1","model":"upstream-model","choices":[]}`)
	}))
	defer upstream.Close()
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai", Kind: "api"})
	defer database.Close()
	caller, err := database.Caller("caller")
	if err != nil {
		t.Fatal(err)
	}
	caller.Enabled = false
	if err := database.UpsertCaller(caller); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled caller status = %d", response.Code)
	}
	caller.Enabled = true
	caller.LocalOnly = true
	if err := database.UpsertCaller(caller); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("local-only status = %d", response.Code)
	}
}

func TestGatewayCallerIsolationAndConcurrencyLimit(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{"id":"r1","model":"upstream-model","choices":[]}`)
	}))
	defer upstream.Close()
	gateway, keyOne, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	defer database.Close()
	callerTwoKey, err := security.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertCaller(store.Caller{ID: "caller-two", Name: "caller-two", KeyHash: security.HashKey(callerTwoKey), AllowedModels: []string{"public-model"}, AccessScope: "lan", Enabled: true, MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	callerOne, err := database.Caller("caller")
	if err != nil {
		t.Fatal(err)
	}
	callerOne.MaxConcurrency = 1
	if err := database.UpsertCaller(callerOne); err != nil {
		t.Fatal(err)
	}

	call := func(key string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		gateway.Handler("lan").ServeHTTP(response, request)
		return response
	}
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- call(keyOne) }()
	<-started
	second := call(keyOne)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("same caller concurrency status = %d", second.Code)
	}
	close(release)
	if response := <-firstDone; response.Code != http.StatusOK {
		t.Fatalf("first caller status = %d", response.Code)
	}
	// The second caller has an independent admission bucket and can enter once
	// the provider is available again.
	release = make(chan struct{})
	thirdDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { thirdDone <- call(callerTwoKey) }()
	<-started
	close(release)
	if response := <-thirdDone; response.Code != http.StatusOK {
		t.Fatalf("second caller status = %d", response.Code)
	}
	rows, err := database.Requests(store.RequestFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	seenOne, seenTwo := false, false
	for _, row := range rows {
		seenOne = seenOne || row.CallerID == "caller"
		seenTwo = seenTwo || row.CallerID == "caller-two"
	}
	if !seenOne || !seenTwo {
		t.Fatalf("caller attribution = %#v", rows)
	}
}

func TestGatewayCancellationAndUpstream429AreSafe(t *testing.T) {
	received := make(chan struct{})
	releaseUpstream := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(received)
		<-releaseUpstream
	}))
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	requestContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`)).WithContext(requestContext)
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		gateway.Handler("lan").ServeHTTP(response, request)
		close(done)
	}()
	<-received
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway did not return after cancellation")
	}
	if response.Code != statusClientClosed {
		t.Fatalf("cancel status = %d", response.Code)
	}
	rows, err := database.Requests(store.RequestFilter{CallerID: "caller", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Status != statusClientClosed || rows[0].Attempts != 1 || rows[0].Outcome != "cancelled" {
		t.Fatalf("cancel ledger = %#v, err = %v", rows, err)
	}
	close(releaseUpstream)
	database.Close()
	upstream.Close()

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"provider-secret"}}`)
	}))
	defer upstream.Close()
	gateway, key, database = testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	defer database.Close()
	request = httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || strings.Contains(response.Body.String(), "provider-secret") {
		t.Fatalf("upstream 429 status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestGatewayModelsFilterAndStableRPMDenialLedger(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"id":"r1","model":"m","choices":[]}`)
	}))
	defer upstream.Close()
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	defer database.Close()
	if err := database.UpsertModel(store.Model{ID: "hidden-model", ProviderID: "provider", UpstreamModel: "hidden", Name: "hidden", Protocols: []string{"openai"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+key)
	response := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("models status = %d", response.Code)
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	if len(models.Data) != 1 || models.Data[0].ID != "public-model" {
		t.Fatalf("models filter = %#v", models.Data)
	}

	caller, err := database.Caller("caller")
	if err != nil {
		t.Fatal(err)
	}
	caller.RPM = 1
	if err := database.UpsertCaller(caller); err != nil {
		t.Fatal(err)
	}
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		gateway.Handler("lan").ServeHTTP(rr, req)
		return rr
	}
	if first := call(); first.Code != http.StatusOK {
		t.Fatalf("first request status = %d", first.Code)
	}
	second := call()
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("RPM denial status = %d", second.Code)
	}
	var errorBody struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &errorBody); err != nil {
		t.Fatal(err)
	}
	if errorBody.Error.RequestID == "" || errorBody.Error.RequestID != second.Header().Get("X-Request-ID") {
		t.Fatalf("stable denial request ID header=%q body=%q", second.Header().Get("X-Request-ID"), errorBody.Error.RequestID)
	}
	rows, err := database.Requests(store.RequestFilter{CallerID: "caller", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var denied bool
	for _, row := range rows {
		if row.Status == http.StatusTooManyRequests && row.Attempts == 0 && row.ErrorCode == "rate_limited" {
			denied = true
		}
	}
	if !denied || calls.Load() != 1 {
		t.Fatalf("RPM ledger/calls = %#v/%d", rows, calls.Load())
	}
}

func TestGatewayBudgetUsesAllKnownUsageAndRejectsAfterThreshold(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"id":"r1","model":"m","choices":[],"usage":{"prompt_tokens":60,"completion_tokens":60}}`)
	}))
	defer upstream.Close()
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	defer database.Close()
	caller, err := database.Caller("caller")
	if err != nil {
		t.Fatal(err)
	}
	caller.DailyTokenLimit = 100
	if err := database.UpsertCaller(caller); err != nil {
		t.Fatal(err)
	}
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		gateway.Handler("lan").ServeHTTP(rr, req)
		return rr
	}
	if first := call(); first.Code != http.StatusOK {
		t.Fatalf("first budget request status = %d", first.Code)
	}
	second := call()
	if second.Code != http.StatusTooManyRequests || calls.Load() != 1 {
		t.Fatalf("budget status/calls = %d/%d", second.Code, calls.Load())
	}
}

func TestGatewayOAuthPrefixesBindAdapterAccounts(t *testing.T) {
	for _, providerName := range []string{"codex", "kimi"} {
		t.Run(providerName, func(t *testing.T) {
			var gotModel string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				gotModel, _ = body["model"].(string)
				_, _ = io.WriteString(w, `{"id":"r1","model":"m","choices":[]}`)
			}))
			defer upstream.Close()
			gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "adapter", AuthMode: "oauth", OAuthProvider: providerName})
			defer database.Close()
			model, err := database.Model("public-model")
			if err != nil {
				t.Fatal(err)
			}
			model.UpstreamModel = "same-model"
			if err := database.UpsertModel(model); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			gateway.Handler("lan").ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
			}
			want := "nr-" + providerName + "/same-model"
			if gotModel != want {
				t.Fatalf("adapter model = %q, want %q", gotModel, want)
			}
			rows, err := database.Requests(store.RequestFilter{CallerID: "caller", Limit: 10})
			if err != nil || len(rows) != 1 || rows[0].UpstreamModel != want {
				t.Fatalf("ledger = %#v, err = %v", rows, err)
			}
		})
	}
}

func TestGatewayRejectsOAuthPrefixAndDirectOAuthProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"r1","model":"m","choices":[]}`)
	}))
	defer upstream.Close()
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "adapter", AuthMode: "oauth", OAuthProvider: "codex"})
	defer database.Close()
	modelRecord, err := database.Model("public-model")
	if err != nil {
		t.Fatal(err)
	}
	for _, upstreamModel := range []string{"nr-kimi/same-model", "nr-codex/same-model"} {
		modelRecord.UpstreamModel = upstreamModel
		if err := database.UpsertModel(modelRecord); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		gateway.Handler("lan").ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("prefixed model %q status = %d", upstreamModel, response.Code)
		}
	}
	// A direct OAuth provider is refused before any upstream attempt.
	provider, err := database.Provider("provider")
	if err != nil {
		t.Fatal(err)
	}
	provider.Protocol = "openai"
	if err := database.UpsertProvider(provider); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("direct OAuth provider status = %d", response.Code)
	}
}

func TestGatewayTimeoutAndNonstreamReadErrorsUseLedgerStatus(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	gateway.opt.Timeout = 10 * time.Millisecond
	gateway.Handler("lan").ServeHTTP(response, request)
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout status = %d, body=%s", response.Code, response.Body.String())
	}
	rows, err := database.Requests(store.RequestFilter{CallerID: "caller", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Status != http.StatusGatewayTimeout {
		t.Fatalf("timeout ledger = %#v, err = %v", rows, err)
	}
	close(release)
	database.Close()
	upstream.Close()
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("fixture read failure") }
func (failingBody) Close() error             { return nil }

func TestGatewayNonstreamUpstreamReadFailureIs502InLedger(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"r1","model":"m","choices":[]}`)
	}))
	defer upstream.Close()
	gateway, key, database := testGateway(t, upstream.URL, store.Provider{Protocol: "openai"})
	defer database.Close()
	gateway.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}, Header: make(http.Header)}, nil
	})}
	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	gateway.Handler("lan").ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("read failure status = %d", response.Code)
	}
	rows, err := database.Requests(store.RequestFilter{CallerID: "caller", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Status != http.StatusBadGateway {
		t.Fatalf("read failure ledger = %#v, err = %v", rows, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testGateway(t *testing.T, endpoint string, providerOverrides store.Provider) (*Gateway, string, *store.Store) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(directory, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := security.NewVault(bytes32(7))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := vault.Seal([]byte("provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	provider := store.Provider{ID: "provider", Name: "provider", Kind: "api", AuthMode: "api_key", Protocol: "openai", Endpoint: endpoint, Enabled: true, SecretCipher: ciphertext}
	if providerOverrides.ID != "" {
		provider.ID = providerOverrides.ID
	}
	if providerOverrides.Kind != "" {
		provider.Kind = providerOverrides.Kind
	}
	if providerOverrides.Protocol != "" {
		provider.Protocol = providerOverrides.Protocol
	}
	if providerOverrides.AuthMode != "" {
		provider.AuthMode = providerOverrides.AuthMode
	}
	if providerOverrides.OAuthProvider != "" {
		provider.OAuthProvider = providerOverrides.OAuthProvider
	}
	if providerOverrides.Endpoint != "" {
		provider.Endpoint = providerOverrides.Endpoint
	}
	if err := database.UpsertProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertModel(store.Model{ID: "public-model", ProviderID: provider.ID, UpstreamModel: "upstream-model", Name: "public", Protocols: []string{"openai"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key, err := security.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertCaller(store.Caller{ID: "caller", Name: "caller", KeyHash: security.HashKey(key), AllowedModels: []string{"public-model"}, AccessScope: "lan", Enabled: true, MaxConcurrency: 2}); err != nil {
		t.Fatal(err)
	}
	options := Options{Timeout: time.Second}
	if provider.Protocol == "adapter" {
		options.AdapterEndpoint = endpoint
	}
	return New(database, vault, options), key, database
}

func bytes32(value byte) []byte {
	result := make([]byte, 32)
	for i := range result {
		result[i] = value
	}
	return result
}
