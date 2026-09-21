// Package gateway implements the authenticated inference boundary. It owns
// caller authorization and upstream request shaping; provider adapters remain
// separate processes with fixed, administrator-controlled endpoints.
package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"personalrouter/internal/security"
	"personalrouter/internal/store"
	"personalrouter/internal/usage"
)

var errSSEFraming = errors.New("upstream SSE framing exceeded limit")

const (
	maxBodyBytes       int64 = 16 << 20
	defaultTimeout           = 15 * time.Minute
	defaultAdapterURL        = ""
	statusClientClosed       = 499
	maxHeaderResponse        = 120 * time.Second
	maxSSEFilterBytes        = 256 << 10
)

// Options controls process-local gateway behavior. AdapterEndpoint is an
// administrator-supplied fixed internal endpoint; it is never read from a
// request. A zero RPM on Caller is unlimited. A zero Caller.MaxConcurrency is
// given the safe default of two; AdmissionConcurrency, when nonzero, is a
// process-wide ceiling.
type Options struct {
	HTTPClient           *http.Client
	Timeout              time.Duration
	AdmissionConcurrency int
	AdapterEndpoint      string
}

// Gateway is safe for concurrent use.
type Gateway struct {
	store              *store.Store
	vault              *security.Vault
	opt                Options
	client             *http.Client
	admit              *admission
	accountingFailures atomic.Uint64
}

// New constructs a gateway. It performs no network request and does not
// inspect or print secrets.
func New(s *store.Store, vault *security.Vault, options Options) *Gateway {
	if options.Timeout <= 0 || options.Timeout > defaultTimeout {
		options.Timeout = defaultTimeout
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	// Clone the client and force redirect refusal even for injected clients.
	// We intentionally do not mutate the caller's client.
	copyClient := *client
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	copyClient.Timeout = options.Timeout
	baseTransport := client.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	if transport, ok := baseTransport.(*http.Transport); ok {
		transportCopy := transport.Clone()
		// Enforce the gateway connect bound even when DefaultTransport already
		// carries its own dialer (which normally has a longer timeout).
		transportCopy.DialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
		if transportCopy.ResponseHeaderTimeout <= 0 || transportCopy.ResponseHeaderTimeout > maxHeaderResponse {
			transportCopy.ResponseHeaderTimeout = maxHeaderResponse
		}
		copyClient.Transport = transportCopy
	}
	return &Gateway{
		store:  s,
		vault:  vault,
		opt:    options,
		client: &copyClient,
		admit:  newAdmission(options.AdmissionConcurrency),
	}
}

// AccountingFailures reports durable-ledger writes that could not be
// completed. Runtime health can expose this counter without exposing request
// content or upstream errors.
func (g *Gateway) AccountingFailures() uint64 {
	if g == nil {
		return 0
	}
	return g.accountingFailures.Load()
}

// Handler returns a handler whose entry is fixed by the listener that owns it.
// Unknown entries are rejected rather than inferred from request headers.
func (g *Gateway) Handler(entry string) http.Handler {
	if entry != "lan" && entry != "public" {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requestID := newRequestID()
			w.Header().Set("X-Request-ID", requestID)
			writeError(w, http.StatusNotFound, "not_found", "not found")
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.serve(entry, w, r)
	})
}

type route struct {
	path     string
	protocol string
}

func routeFor(path string) (route, bool) {
	switch path {
	case "/v1/chat/completions":
		return route{path: path, protocol: "openai"}, true
	case "/v1/responses":
		return route{path: path, protocol: "responses"}, true
	case "/v1/messages":
		return route{path: path, protocol: "anthropic"}, true
	default:
		return route{}, false
	}
}

func (g *Gateway) serve(entry string, w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	w.Header().Set("X-Request-ID", requestID)
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		g.models(w, r, entry)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	route, ok := routeFor(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	started := time.Now()
	record := store.RequestRecord{ID: requestID, Entry: entry, Protocol: route.protocol, StartedAt: started, Outcome: "error"}
	initialPersisted := false
	ledgerAttempted := false
	defer func() {
		if initialPersisted || ledgerAttempted {
			return
		}
		// Pre-route denials receive exactly one best-effort ledger row. Once
		// the precharge itself has been attempted, an accounting failure must
		// not be retried as a second record after the response is sent.
		_ = g.addRecord(record)
	}()

	caller, err := g.authenticate(r)
	if err != nil {
		record.Status = http.StatusUnauthorized
		record.ErrorCode = "unauthorized"
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid API key")
		return
	}
	record.CallerID = caller.ID
	if !scopeAllows(caller.AccessScope, entry) {
		record.Status = http.StatusForbidden
		record.ErrorCode = "forbidden"
		writeError(w, http.StatusForbidden, "forbidden", "caller is not allowed on this entry")
		return
	}

	if mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaErr != nil || mediaType != "application/json" {
		record.Status = http.StatusUnsupportedMediaType
		record.ErrorCode = "invalid_content_type"
		writeError(w, http.StatusUnsupportedMediaType, "invalid_request", "content type must be application/json")
		return
	}
	body, err := readBody(r)
	if err != nil {
		record.Status = http.StatusRequestEntityTooLarge
		record.ErrorCode = "body_too_large"
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large")
		return
	}
	object, modelID, stream, hasImages, err := parseRequest(body)
	if err != nil {
		record.Status = http.StatusBadRequest
		record.ErrorCode = "invalid_request"
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	record.Model = ledgerValue(modelID)
	if modelID == "" {
		record.Status = http.StatusBadRequest
		record.ErrorCode = "invalid_request"
		writeError(w, http.StatusBadRequest, "invalid_request", "request model is required")
		return
	}

	model, provider, err := g.lookupModel(modelID)
	if err != nil || !callerModelAllowed(caller, modelID) || !model.Enabled || !provider.Enabled {
		record.Status = http.StatusForbidden
		record.ErrorCode = "forbidden"
		writeError(w, http.StatusForbidden, "forbidden", "model is not available to this caller")
		return
	}
	upstreamModel, oauthErr := routedUpstreamModel(provider, model.UpstreamModel)
	if oauthErr != nil {
		record.Status = http.StatusForbidden
		record.ErrorCode = "provider_unavailable"
		writeError(w, http.StatusForbidden, "provider_unavailable", "provider is unavailable")
		return
	}
	if caller.LocalOnly && provider.Kind != "local" {
		record.Status = http.StatusForbidden
		record.ErrorCode = "forbidden"
		writeError(w, http.StatusForbidden, "forbidden", "caller is restricted to local providers")
		return
	}
	if !contains(model.Protocols, route.protocol) {
		record.Status = http.StatusBadRequest
		record.ErrorCode = "unsupported_protocol"
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "model does not support this protocol")
		return
	}
	if provider.Protocol != "adapter" && provider.Protocol != route.protocol {
		record.Status = http.StatusBadRequest
		record.ErrorCode = "unsupported_protocol"
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "provider does not support this protocol")
		return
	}
	if hasImages && !model.InputImages {
		record.Status = http.StatusBadRequest
		record.ErrorCode = "images_not_supported"
		writeError(w, http.StatusBadRequest, "images_not_supported", "model does not support image input")
		return
	}
	if ok, budgetErr := budgetAvailable(g.store, caller, time.Now()); !ok {
		if errors.Is(budgetErr, errBudget) {
			record.Status = http.StatusTooManyRequests
			record.ErrorCode = "budget_exceeded"
			writeError(w, http.StatusTooManyRequests, "budget_exceeded", "daily token budget is unavailable or exhausted")
		} else {
			record.Status = http.StatusServiceUnavailable
			record.ErrorCode = "store_unavailable"
			writeError(w, http.StatusServiceUnavailable, "store_unavailable", "gateway state is unavailable")
		}
		return
	}
	if err := g.admit.acquire(caller, time.Now()); err != nil {
		if errors.Is(err, errConcurrency) {
			record.Status = http.StatusTooManyRequests
			record.ErrorCode = "concurrency_limited"
			writeError(w, http.StatusTooManyRequests, "concurrency_limited", "caller concurrency limit reached")
		} else {
			record.Status = http.StatusTooManyRequests
			record.ErrorCode = "rate_limited"
			writeError(w, http.StatusTooManyRequests, "rate_limited", "caller rate limit reached")
		}
		return
	}
	defer g.admit.release(caller.ID)

	object["model"] = json.RawMessage(strconvQuote(upstreamModel))
	forwardBody, err := json.Marshal(object)
	if err != nil {
		record.Status = http.StatusBadRequest
		record.ErrorCode = "invalid_request"
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}

	record.ProviderID = ledgerValue(provider.ID)
	record.UpstreamModel = ledgerValue(upstreamModel)
	secret, err := g.providerSecret(provider)
	if err != nil {
		record.Status = http.StatusServiceUnavailable
		record.ErrorCode = "provider_credentials"
		record.Outcome = "error"
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider is unavailable")
		return
	}

	upstreamURL, err := g.endpoint(provider, route.path)
	if err != nil {
		record.Status = http.StatusServiceUnavailable
		record.ErrorCode = "provider_endpoint"
		record.Outcome = "error"
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), g.opt.Timeout)
	defer cancel()
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(forwardBody))
	if err != nil {
		record.Status = http.StatusServiceUnavailable
		record.ErrorCode = "provider_endpoint"
		record.Outcome = "error"
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider is unavailable")
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	if secret != "" {
		if route.protocol == "anthropic" {
			upstreamReq.Header.Set("anthropic-version", "2023-06-01")
		}
		if route.protocol == "anthropic" && provider.Protocol != "adapter" {
			upstreamReq.Header.Set("x-api-key", secret)
		} else {
			upstreamReq.Header.Set("Authorization", "Bearer "+secret)
		}
	}
	// Commit the outbound attempt count before transport starts. A restart after
	// this durable precharge must not make a sent request look like a zero-attempt
	// unknown-usage row and bypass a budgeted caller's fail-closed rule.
	record.Status = 0
	record.ErrorCode = "pending"
	record.Outcome = "incomplete"
	record.Attempts = 1
	ledgerAttempted = true
	if err := g.addRecord(record); err != nil {
		record.Status = http.StatusServiceUnavailable
		record.ErrorCode = "accounting_unavailable"
		record.Outcome = "error"
		writeError(w, http.StatusServiceUnavailable, "gateway_unavailable", "gateway accounting is unavailable")
		return
	}
	initialPersisted = true
	resp, err := g.client.Do(upstreamReq)
	if err != nil {
		record.DurationMS = time.Since(started).Milliseconds()
		record.Status = statusForTransportError(err, r.Context())
		record.ErrorCode = errorCodeForTransport(err, r.Context())
		record.Outcome = outcomeForTransport(err, r.Context())
		g.updateRecord(record)
		if record.Status == statusClientClosed {
			writeError(w, statusClientClosed, "cancelled", "request cancelled")
		} else if record.Status == http.StatusGatewayTimeout {
			writeError(w, http.StatusGatewayTimeout, "timeout", "upstream provider timed out")
		} else {
			writeError(w, http.StatusBadGateway, "upstream_error", "upstream provider unavailable")
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		record.DurationMS = time.Since(started).Milliseconds()
		downstreamStatus := safeUpstreamStatus(resp.StatusCode)
		record.Status = downstreamStatus
		record.ErrorCode = "upstream_error"
		record.Outcome = "error"
		g.updateRecord(record)
		writeError(w, downstreamStatus, "upstream_error", "upstream provider returned an error")
		return
	}

	collector := usage.New(route.protocol, stream || isSSE(resp.Header.Get("Content-Type")))
	if stream || isSSE(resp.Header.Get("Content-Type")) {
		g.streamResponse(w, r, resp, collector, &record, &model, started)
		return
	}
	data, readErr, firstByteMS, tooLarge := readResponseBody(resp.Body, started)
	if tooLarge {
		record.DurationMS = time.Since(started).Milliseconds()
		record.Status = http.StatusBadGateway
		record.ErrorCode = "response_too_large"
		record.Outcome = "incomplete"
		g.updateRecord(record)
		writeError(w, http.StatusBadGateway, "upstream_error", "upstream response is invalid")
		return
	}
	if len(data) > 0 {
		collector.Feed(data)
	}
	usageResult := collector.Finish()
	applyUsage(&record, usageResult, model)
	record.DurationMS = time.Since(started).Milliseconds()
	if firstByteMS >= 0 {
		record.TTFTMS = &firstByteMS
	}
	record.Status = http.StatusOK
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		if r.Context().Err() != nil {
			record.Status = statusClientClosed
			record.ErrorCode = "cancelled"
			record.Outcome = "cancelled"
		} else if errors.Is(readErr, context.DeadlineExceeded) {
			record.Status = http.StatusGatewayTimeout
			record.ErrorCode = "timeout"
			record.Outcome = "incomplete"
		} else {
			record.Status = http.StatusBadGateway
			record.ErrorCode = "upstream_read"
			record.Outcome = "incomplete"
		}
		g.updateRecord(record)
		if r.Context().Err() != nil {
			writeError(w, statusClientClosed, "cancelled", "request cancelled")
		} else if errors.Is(readErr, context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "timeout", "upstream provider timed out")
		} else {
			writeError(w, http.StatusBadGateway, "upstream_error", "upstream response was incomplete")
		}
		return
	}
	if usageResult.Failed || usageResult.Truncated || !usageResult.Complete {
		record.Status = http.StatusBadGateway
		record.ErrorCode = "incomplete_response"
		record.Outcome = "incomplete"
		g.updateRecord(record)
		writeError(w, http.StatusBadGateway, "upstream_error", "upstream response was incomplete")
		return
	}
	record.Outcome = "success"
	if usageResult.Model != "" {
		record.ResponseModel = ledgerValue(usageResult.Model)
	}
	g.updateRecord(record)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func safeUpstreamStatus(status int) int {
	if status >= 400 && status <= 599 {
		return status
	}
	return http.StatusBadGateway
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request, entry string) {
	caller, err := g.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid API key")
		return
	}
	if !scopeAllows(caller.AccessScope, entry) {
		writeError(w, http.StatusForbidden, "forbidden", "caller is not allowed on this entry")
		return
	}
	if g.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "gateway state is unavailable")
		return
	}
	models, err := g.store.Models()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "gateway state is unavailable")
		return
	}
	type modelView struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	items := make([]modelView, 0, len(models))
	for _, model := range models {
		if !model.Enabled || !callerModelAllowed(caller, model.ID) {
			continue
		}
		provider, providerErr := g.store.Provider(model.ProviderID)
		if providerErr != nil || !provider.Enabled || (caller.LocalOnly && provider.Kind != "local") {
			continue
		}
		items = append(items, modelView{ID: model.ID, Object: "model", OwnedBy: provider.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": items})
}

func (g *Gateway) authenticate(r *http.Request) (store.Caller, error) {
	var empty store.Caller
	if g.store == nil {
		return empty, errors.New("store unavailable")
	}
	key, err := security.RequestKey(r)
	if err != nil {
		return empty, err
	}
	callers, err := g.store.Callers()
	if err != nil {
		return empty, err
	}
	for _, caller := range callers {
		if caller.Enabled && security.VerifyKey(key, caller.KeyHash) {
			// Legacy callers may have only the hash. Capture the already verified
			// request credential opportunistically so the admin surface can reveal it on
			// the next request. The conditional store update prevents a stale
			// authentication snapshot from overwriting a concurrent rotation.
			if caller.KeyCipher == "" && g.vault != nil && g.store != nil {
				if encrypted, sealErr := g.vault.Seal([]byte(key)); sealErr == nil && encrypted != "" {
					_, _ = g.store.CaptureCallerKey(caller.ID, caller.KeyHash, encrypted)
				}
			}
			return caller, nil
		}
	}
	return empty, errors.New("caller not found")
}

func (g *Gateway) lookupModel(id string) (store.Model, store.Provider, error) {
	var emptyModel store.Model
	var emptyProvider store.Provider
	if g.store == nil {
		return emptyModel, emptyProvider, errors.New("store unavailable")
	}
	model, err := g.store.Model(id)
	if err != nil {
		return emptyModel, emptyProvider, err
	}
	provider, err := g.store.Provider(model.ProviderID)
	if err != nil {
		return emptyModel, emptyProvider, err
	}
	return model, provider, nil
}

func routedUpstreamModel(provider store.Provider, model string) (string, error) {
	if provider.AuthMode != "oauth" {
		return model, nil
	}
	if provider.Protocol != "adapter" {
		return "", errors.New("oauth provider must use adapter protocol")
	}
	prefix := ""
	switch strings.ToLower(strings.TrimSpace(provider.OAuthProvider)) {
	case "codex":
		prefix = "nr-codex/"
	case "kimi":
		prefix = "nr-kimi/"
	default:
		return "", errors.New("unsupported oauth provider")
	}
	// The stored model is the original provider model ID. Any pre-existing
	// nr-* prefix would make account binding ambiguous or double-prefixed.
	if model == "" {
		return "", errors.New("oauth model is empty")
	}
	for _, segment := range strings.Split(model, "/") {
		if strings.HasPrefix(segment, "nr-") {
			return "", errors.New("oauth model already has an account prefix")
		}
	}
	return prefix + model, nil
}

func (g *Gateway) providerSecret(provider store.Provider) (string, error) {
	if provider.AuthMode == "none" || provider.SecretCipher == "" {
		if provider.AuthMode == "api_key" || provider.AuthMode == "oauth" {
			return "", errors.New("provider secret unavailable")
		}
		return "", nil
	}
	if g.vault == nil {
		return "", errors.New("vault unavailable")
	}
	secret, err := g.vault.Open(provider.SecretCipher)
	if err != nil {
		return "", errors.New("provider secret unavailable")
	}
	return string(secret), nil
}

func (g *Gateway) endpoint(provider store.Provider, path string) (string, error) {
	endpoint := provider.Endpoint
	if provider.Protocol == "adapter" {
		endpoint = g.opt.AdapterEndpoint
		if endpoint == defaultAdapterURL {
			return "", errors.New("adapter endpoint unavailable")
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return "", errors.New("invalid provider endpoint")
	}
	suffix := strings.TrimPrefix(strings.TrimPrefix(path, "/v1"), "/")
	suffix = "/" + suffix
	if strings.HasSuffix(u.Path, path) || strings.HasSuffix(u.Path, suffix) {
		return u.String(), nil
	}
	basePath := strings.TrimRight(u.Path, "/")
	if basePath == "" {
		u.Path = path
	} else {
		u.Path = basePath + suffix
	}
	return u.String(), nil
}

func (g *Gateway) streamResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, collector *usage.Collector, record *store.RequestRecord, model *store.Model, started time.Time) {
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/event-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	filter := sseFilter{}
	var firstByte bool
	var readErr error
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			collector.Feed(buf[:n])
			out, framingError := filter.feed(buf[:n], false)
			if !firstByte && len(out) > 0 {
				firstByte = true
				ttft := time.Since(started).Milliseconds()
				record.TTFTMS = &ttft
			}
			if _, writeErr := w.Write(out); writeErr != nil {
				readErr = writeErr
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
			if framingError {
				_ = resp.Body.Close()
				readErr = errSSEFraming
				break
			}
		}
		if err != nil {
			readErr = err
			break
		}
	}
	// An EOF without a blank-line terminator leaves the filter's pending event
	// undispatched. The usage collector records the response as incomplete.
	_, _ = filter.feed(nil, true)
	result := collector.Finish()
	applyUsage(record, result, *model)
	record.DurationMS = time.Since(started).Milliseconds()
	record.Status = http.StatusOK
	switch {
	case r.Context().Err() != nil:
		record.ErrorCode = "cancelled"
		record.Outcome = "cancelled"
	case errors.Is(readErr, context.DeadlineExceeded):
		record.ErrorCode = "timeout"
		record.Outcome = "incomplete"
	case readErr != nil && !errors.Is(readErr, io.EOF):
		record.ErrorCode = "upstream_read"
		record.Outcome = "incomplete"
	case result.Failed || result.Truncated || !result.Complete:
		record.ErrorCode = "incomplete_response"
		record.Outcome = "incomplete"
	default:
		record.Outcome = "success"
	}
	if result.Model != "" {
		record.ResponseModel = ledgerValue(result.Model)
	}
	g.updateRecord(*record)
}

// sseFilter keeps event framing intact while replacing provider error events
// with a fixed safe error. Normal model events pass through byte-for-byte.
type sseFilter struct {
	pending []byte
}

func (f *sseFilter) feed(chunk []byte, final bool) ([]byte, bool) {
	var output []byte
	for _, b := range chunk {
		if len(f.pending) >= maxSSEFilterBytes {
			f.pending = nil
			return append(output, []byte("event: error\ndata: {\"error\":{\"message\":\"upstream provider error\"}}\n\n")...), true
		}
		f.pending = append(f.pending, b)
		width := 0
		if len(f.pending) >= 2 && f.pending[len(f.pending)-2] == '\n' && f.pending[len(f.pending)-1] == '\n' {
			width = 2
		} else if len(f.pending) >= 4 && f.pending[len(f.pending)-4] == '\r' && f.pending[len(f.pending)-3] == '\n' && f.pending[len(f.pending)-2] == '\r' && f.pending[len(f.pending)-1] == '\n' {
			width = 4
		}
		if width == 0 {
			continue
		}
		event := append([]byte(nil), f.pending...)
		f.pending = nil
		if sseErrorEvent(event) {
			output = append(output, []byte("event: error\ndata: {\"error\":{\"message\":\"upstream provider error\"}}\n\n")...)
		} else {
			output = append(output, event...)
		}
	}
	if final {
		// An unterminated event is deliberately dropped rather than forwarded as
		// a valid terminal response.
		f.pending = nil
	}
	return output, false
}

func sseErrorEvent(event []byte) bool {
	var data strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(string(event), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "event: error") || strings.EqualFold(trimmed, "event: response.failed") {
			return true
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		data.WriteByte('\n')
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(data.String())), &object) == nil {
		if _, ok := object["error"]; ok {
			return true
		}
		if typ, ok := object["type"]; ok {
			var value string
			if json.Unmarshal(typ, &value) == nil && (value == "response.failed" || value == "error") {
				return true
			}
		}
	}
	return false
}

func (g *Gateway) addRecord(record store.RequestRecord) error {
	if g.store == nil {
		g.accountingFailures.Add(1)
		return errors.New("store unavailable")
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now()
	}
	if err := g.store.AddRequest(record); err != nil {
		g.accountingFailures.Add(1)
		return err
	}
	return nil
}

func (g *Gateway) updateRecord(record store.RequestRecord) {
	if g.store == nil {
		g.accountingFailures.Add(1)
		return
	}
	if err := g.store.UpdateRequest(record); err != nil {
		g.accountingFailures.Add(1)
	}
}

func applyUsage(record *store.RequestRecord, result usage.Result, model store.Model) {
	record.InputTokens = result.InputTokens
	record.OutputTokens = result.OutputTokens
	record.CacheTokens = result.CacheTokens
	record.ReasoningTokens = result.ReasoningTokens
	var cost float64
	if model.InputPrice != nil && model.OutputPrice != nil && result.InputTokens != nil && result.OutputTokens != nil {
		cost = float64(*result.InputTokens)*(*model.InputPrice)/1_000_000 + float64(*result.OutputTokens)*(*model.OutputPrice)/1_000_000
	}
	if model.InputPrice != nil && model.OutputPrice != nil && result.InputTokens != nil && result.OutputTokens != nil && !math.IsNaN(cost) && !math.IsInf(cost, 0) {
		record.EstimatedCost = &cost
	}
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("missing body")
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || int64(len(data)) > maxBodyBytes {
		return nil, errors.New("body too large")
	}
	return data, nil
}

func readResponseBody(body io.Reader, started time.Time) ([]byte, error, int64, bool) {
	var data bytes.Buffer
	data.Grow(32 * 1024)
	buffer := make([]byte, 32*1024)
	firstByteMS := int64(-1)
	for {
		n, err := body.Read(buffer)
		if n > 0 {
			if firstByteMS < 0 {
				firstByteMS = time.Since(started).Milliseconds()
			}
			if int64(data.Len()+n) > maxBodyBytes {
				return nil, errors.New("response too large"), firstByteMS, true
			}
			_, _ = data.Write(buffer[:n])
		}
		if err != nil {
			return data.Bytes(), err, firstByteMS, false
		}
	}
}

func parseRequest(data []byte) (map[string]json.RawMessage, string, bool, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, "", false, false, errors.New("request must be object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, "", false, false, errors.New("trailing JSON")
	}
	var model string
	if raw, ok := object["model"]; ok {
		if err := json.Unmarshal(raw, &model); err != nil {
			return nil, "", false, false, errors.New("model must be string")
		}
	}
	stream := false
	if raw, ok := object["stream"]; ok {
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, "", false, false, errors.New("stream must be boolean")
		}
	}
	return object, model, stream, containsImageValue(object), nil
}

func containsImageValue(value any) bool {
	switch v := value.(type) {
	case map[string]json.RawMessage:
		if raw, ok := v["type"]; ok {
			var kind string
			if json.Unmarshal(raw, &kind) == nil && isImageContent(v, kind) {
				return true
			}
		}
		for _, raw := range v {
			var nested any
			if json.Unmarshal(raw, &nested) == nil && containsImageValue(nested) {
				return true
			}
		}
	case map[string]any:
		if kind, ok := v["type"].(string); ok && isImageContentAny(v, kind) {
			return true
		}
		for _, nested := range v {
			if containsImageValue(nested) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if containsImageValue(item) {
				return true
			}
		}
	}
	return false
}

func isImageContent(value map[string]json.RawMessage, kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "image":
		// Anthropic image blocks carry source; data is accepted for the
		// equivalent inline form. A bare JSON-schema type=image is not content.
		_, source := value["source"]
		_, data := value["data"]
		return source || data
	case "image_url":
		_, imageURL := value["image_url"]
		return imageURL
	case "input_image", "input_image_url":
		_, imageURL := value["image_url"]
		_, fileID := value["file_id"]
		return imageURL || fileID
	default:
		return false
	}
}

func isImageContentAny(value map[string]any, kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "image":
		_, source := value["source"]
		_, data := value["data"]
		return source || data
	case "image_url":
		_, imageURL := value["image_url"]
		return imageURL
	case "input_image", "input_image_url":
		_, imageURL := value["image_url"]
		_, fileID := value["file_id"]
		return imageURL || fileID
	default:
		return false
	}
}

func scopeAllows(scope, entry string) bool {
	return scope == "both" || scope == entry
}

func callerModelAllowed(c store.Caller, model string) bool {
	for _, allowed := range c.AllowedModels {
		if allowed == model {
			return true
		}
	}
	return false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func ledgerValue(value string) string {
	if value == "" || len(value) > 200 || strings.TrimSpace(value) != value {
		return ""
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ""
		}
	}
	return value
}

func isSSE(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])), "text/event-stream")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	requestID := w.Header().Get("X-Request-ID")
	if requestID == "" {
		requestID = newRequestID()
		w.Header().Set("X-Request-ID", requestID)
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "request_id": requestID}})
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

func strconvQuote(value string) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func statusForTransportError(err error, requestCtx context.Context) int {
	if requestCtx.Err() != nil || errors.Is(err, context.Canceled) {
		return statusClientClosed
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

func errorCodeForTransport(err error, requestCtx context.Context) string {
	if requestCtx.Err() != nil || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "upstream_error"
}

func outcomeForTransport(err error, requestCtx context.Context) string {
	if requestCtx.Err() != nil || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "incomplete"
	}
	return "error"
}
