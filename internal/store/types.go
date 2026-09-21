// Package store owns the durable, non-content state used by PersonalRouter.
package store

import (
	"errors"
	"time"
)

var (
	// ErrNotFound means that the requested object does not exist.
	ErrNotFound = errors.New("store object not found")
	// ErrConflict means that an operation would violate a durable reference or uniqueness rule.
	ErrConflict = errors.New("store conflict")
	// ErrInvalid means that an object or filter fails the store contract.
	ErrInvalid = errors.New("invalid store object")
)

// Provider describes an upstream provider configuration. SecretCipher is an
// encrypted opaque value and is never included in JSON responses.
type Provider struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	AuthMode      string    `json:"auth_mode"`
	Protocol      string    `json:"protocol"`
	Endpoint      string    `json:"endpoint"`
	Enabled       bool      `json:"enabled"`
	OAuthProvider string    `json:"oauth_provider"`
	SecretCipher  string    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
}

// Model describes an exposed model and its upstream mapping.
type Model struct {
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

// Caller describes caller authorization policy. KeyHash is never included in
// JSON responses; raw caller keys are not represented by this type.
type Caller struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	KeyHash         string    `json:"-"`
	KeyCipher       string    `json:"-"`
	AllowedModels   []string  `json:"allowed_models"`
	AccessScope     string    `json:"access_scope"`
	LocalOnly       bool      `json:"local_only"`
	Enabled         bool      `json:"enabled"`
	RPM             int       `json:"rpm"`
	MaxConcurrency  int       `json:"max_concurrency"`
	DailyTokenLimit int64     `json:"daily_token_limit"`
	CreatedAt       time.Time `json:"created_at"`
}

// RequestRecord is the durable request ledger entry. It deliberately has no
// request or response body and no sensitive headers.
type RequestRecord struct {
	ID              string    `json:"id"`
	CallerID        string    `json:"caller_id"`
	Entry           string    `json:"entry"`
	Model           string    `json:"model"`
	ProviderID      string    `json:"provider_id"`
	UpstreamModel   string    `json:"upstream_model"`
	ResponseModel   string    `json:"response_model"`
	Protocol        string    `json:"protocol"`
	Status          int       `json:"status"`
	ErrorCode       string    `json:"error_code"`
	StartedAt       time.Time `json:"started_at"`
	DurationMS      int64     `json:"duration_ms"`
	TTFTMS          *int64    `json:"ttft_ms"`
	InputTokens     *int64    `json:"input_tokens"`
	OutputTokens    *int64    `json:"output_tokens"`
	CacheTokens     *int64    `json:"cache_tokens"`
	ReasoningTokens *int64    `json:"reasoning_tokens"`
	EstimatedCost   *float64  `json:"estimated_cost"`
	Attempts        int       `json:"attempts"`
	Outcome         string    `json:"outcome"`
}

// RequestFilter selects request-ledger rows. A zero Status means all statuses;
// a zero Limit uses the default bounded page size.
type RequestFilter struct {
	CallerID string `json:"caller_id"`
	Model    string `json:"model"`
	Status   int    `json:"status"`
	Limit    int    `json:"limit"`
}

// Summary contains durable counts and known usage totals. UnknownUsageRequests
// counts records for which either input or output usage was unavailable.
type Summary struct {
	Requests             int64    `json:"requests"`
	Errors               int64    `json:"errors"`
	InputTokens          int64    `json:"input_tokens"`
	OutputTokens         int64    `json:"output_tokens"`
	UnknownUsageRequests int64    `json:"unknown_usage_requests"`
	EstimatedCost        *float64 `json:"estimated_cost"`
	Providers            int64    `json:"providers"`
	ReadyProviders       int64    `json:"ready_providers"`
}
