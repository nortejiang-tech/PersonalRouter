package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

const (
	defaultRequestLimit = 100
	maxRequestLimit     = 500
	busyTimeoutMS       = 5000
)

// Store is the single-process durable state store. Its connection pool is
// deliberately limited to one connection so schema and write ordering remain
// deterministic while SQLite serializes access.
type Store struct {
	db *sql.DB
}

// Open opens or creates a private SQLite database, enables WAL and foreign-key
// enforcement, and initializes the store schema.
func Open(path string) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("%w: database path", ErrInvalid)
	}
	if err := prepareDatabasePath(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	if err := store.configure(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the SQLite database resources.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) configure() error {
	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMS),
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return nil
}

func (s *Store) initSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS providers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    auth_mode TEXT NOT NULL,
    protocol TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    oauth_provider TEXT NOT NULL,
    secret_cipher TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS models (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE RESTRICT,
    upstream_model TEXT NOT NULL,
    name TEXT NOT NULL,
    protocols TEXT NOT NULL,
    input_images INTEGER NOT NULL CHECK (input_images IN (0, 1)),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    input_price REAL,
    output_price REAL
);
CREATE TABLE IF NOT EXISTS callers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    key_hash TEXT NOT NULL,
    key_cipher TEXT NOT NULL DEFAULT '',
    allowed_models TEXT NOT NULL,
    access_scope TEXT NOT NULL,
    local_only INTEGER NOT NULL CHECK (local_only IN (0, 1)),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    rpm INTEGER NOT NULL CHECK (rpm >= 0),
    max_concurrency INTEGER NOT NULL CHECK (max_concurrency >= 0),
    daily_token_limit INTEGER NOT NULL CHECK (daily_token_limit >= 0),
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS requests (
    id TEXT PRIMARY KEY,
    caller_id TEXT NOT NULL,
    entry TEXT NOT NULL,
    model TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    upstream_model TEXT NOT NULL,
    response_model TEXT NOT NULL,
    protocol TEXT NOT NULL,
    status INTEGER NOT NULL,
    error_code TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    ttft_ms INTEGER,
    input_tokens INTEGER,
    output_tokens INTEGER,
    cache_tokens INTEGER,
    reasoning_tokens INTEGER,
    estimated_cost REAL,
    attempts INTEGER NOT NULL CHECK (attempts >= 0 AND attempts <= 1),
    outcome TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_models_provider_id ON models(provider_id);
CREATE INDEX IF NOT EXISTS idx_requests_started_at ON requests(started_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_requests_caller_id ON requests(caller_id);
CREATE INDEX IF NOT EXISTS idx_requests_model ON requests(model);
CREATE INDEX IF NOT EXISTS idx_requests_status ON requests(status);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	if err := s.migrateCallerKeyCipher(); err != nil {
		return err
	}
	return nil
}

// migrateCallerKeyCipher adds the caller secret column to databases created
// before caller keys could be retained. It intentionally performs no other
// schema or data migration.
func (s *Store) migrateCallerKeyCipher() error {
	rows, err := s.db.Query(`PRAGMA table_info(callers)`)
	if err != nil {
		return fmt.Errorf("inspect callers schema: %w", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan callers schema: %w", err)
		}
		if name == "key_cipher" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect callers schema: %w", err)
	}
	if found {
		return nil
	}
	if _, err := s.db.Exec(`ALTER TABLE callers ADD COLUMN key_cipher TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrate callers key cipher: %w", err)
	}
	return nil
}

// Providers returns all providers in stable ID order.
func (s *Store) Providers() ([]Provider, error) {
	rows, err := s.db.Query(`SELECT id, name, kind, auth_mode, protocol, endpoint, enabled, oauth_provider, secret_cipher, created_at FROM providers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()
	items := make([]Provider, 0)
	for rows.Next() {
		item, scanErr := scanProvider(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan provider: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	return items, nil
}

// Provider returns one provider or ErrNotFound.
func (s *Store) Provider(id string) (Provider, error) {
	if err := validateID(id); err != nil {
		return Provider{}, err
	}
	row := s.db.QueryRow(`SELECT id, name, kind, auth_mode, protocol, endpoint, enabled, oauth_provider, secret_cipher, created_at FROM providers WHERE id = ?`, id)
	item, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err != nil {
		return Provider{}, fmt.Errorf("get provider: %w", err)
	}
	return item, nil
}

// UpsertProvider inserts or updates a provider while preserving its original
// CreatedAt value on updates.
func (s *Store) UpsertProvider(item Provider) error {
	if err := validateProvider(item); err != nil {
		return err
	}
	createdAt := item.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
INSERT INTO providers (id, name, kind, auth_mode, protocol, endpoint, enabled, oauth_provider, secret_cipher, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    kind = excluded.kind,
    auth_mode = excluded.auth_mode,
    protocol = excluded.protocol,
    endpoint = excluded.endpoint,
    enabled = excluded.enabled,
    oauth_provider = excluded.oauth_provider,
    secret_cipher = excluded.secret_cipher`,
		item.ID, item.Name, item.Kind, item.AuthMode, item.Protocol, item.Endpoint, boolInt(item.Enabled), item.OAuthProvider, item.SecretCipher, unixNano(createdAt))
	if err != nil {
		return fmt.Errorf("upsert provider: %w", err)
	}
	return nil
}

// DeleteProvider deletes a provider only when no model refers to it.
func (s *Store) DeleteProvider(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin provider delete: %w", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM models WHERE provider_id = ?`, id).Scan(&count); err != nil {
		return fmt.Errorf("check provider references: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("%w: provider has models", ErrConflict)
	}
	result, err := tx.Exec(`DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider delete: %w", err)
	}
	return nil
}

// Models returns all models in stable ID order.
func (s *Store) Models() ([]Model, error) {
	rows, err := s.db.Query(`SELECT id, provider_id, upstream_model, name, protocols, input_images, enabled, input_price, output_price FROM models ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()
	items := make([]Model, 0)
	for rows.Next() {
		item, scanErr := scanModel(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan model: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	return items, nil
}

// Model returns one model or ErrNotFound.
func (s *Store) Model(id string) (Model, error) {
	if err := validateID(id); err != nil {
		return Model{}, err
	}
	row := s.db.QueryRow(`SELECT id, provider_id, upstream_model, name, protocols, input_images, enabled, input_price, output_price FROM models WHERE id = ?`, id)
	item, err := scanModel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	if err != nil {
		return Model{}, fmt.Errorf("get model: %w", err)
	}
	return item, nil
}

// UpsertModel inserts or updates a model. The referenced provider must exist.
func (s *Store) UpsertModel(item Model) error {
	if err := validateModel(item); err != nil {
		return err
	}
	protocols, err := json.Marshal(item.Protocols)
	if err != nil {
		return fmt.Errorf("encode model protocols: %w", err)
	}
	_, err = s.db.Exec(`
INSERT INTO models (id, provider_id, upstream_model, name, protocols, input_images, enabled, input_price, output_price)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    provider_id = excluded.provider_id,
    upstream_model = excluded.upstream_model,
    name = excluded.name,
    protocols = excluded.protocols,
    input_images = excluded.input_images,
    enabled = excluded.enabled,
    input_price = excluded.input_price,
    output_price = excluded.output_price`,
		item.ID, item.ProviderID, item.UpstreamModel, item.Name, string(protocols), boolInt(item.InputImages), boolInt(item.Enabled), nullableFloat(item.InputPrice), nullableFloat(item.OutputPrice))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "foreign key") {
			return ErrNotFound
		}
		return fmt.Errorf("upsert model: %w", err)
	}
	return nil
}

// DeleteModel deletes one model or returns ErrNotFound.
func (s *Store) DeleteModel(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	result, err := s.db.Exec(`DELETE FROM models WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete model: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Callers returns all callers in stable ID order.
func (s *Store) Callers() ([]Caller, error) {
	rows, err := s.db.Query(`SELECT id, name, key_hash, key_cipher, allowed_models, access_scope, local_only, enabled, rpm, max_concurrency, daily_token_limit, created_at FROM callers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list callers: %w", err)
	}
	defer rows.Close()
	items := make([]Caller, 0)
	for rows.Next() {
		item, scanErr := scanCaller(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan caller: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list callers: %w", err)
	}
	return items, nil
}

// Caller returns one caller or ErrNotFound.
func (s *Store) Caller(id string) (Caller, error) {
	if err := validateID(id); err != nil {
		return Caller{}, err
	}
	row := s.db.QueryRow(`SELECT id, name, key_hash, key_cipher, allowed_models, access_scope, local_only, enabled, rpm, max_concurrency, daily_token_limit, created_at FROM callers WHERE id = ?`, id)
	item, err := scanCaller(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Caller{}, ErrNotFound
	}
	if err != nil {
		return Caller{}, fmt.Errorf("get caller: %w", err)
	}
	return item, nil
}

// UpsertCaller atomically saves caller identity and authorization policy.
func (s *Store) UpsertCaller(item Caller) error {
	if err := validateCaller(item); err != nil {
		return err
	}
	allowed, err := json.Marshal(item.AllowedModels)
	if err != nil {
		return fmt.Errorf("encode caller models: %w", err)
	}
	createdAt := item.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = s.db.Exec(`
INSERT INTO callers (id, name, key_hash, key_cipher, allowed_models, access_scope, local_only, enabled, rpm, max_concurrency, daily_token_limit, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    key_hash = excluded.key_hash,
    key_cipher = excluded.key_cipher,
    allowed_models = excluded.allowed_models,
    access_scope = excluded.access_scope,
    local_only = excluded.local_only,
    enabled = excluded.enabled,
    rpm = excluded.rpm,
    max_concurrency = excluded.max_concurrency,
    daily_token_limit = excluded.daily_token_limit`,
		item.ID, item.Name, item.KeyHash, item.KeyCipher, string(allowed), item.AccessScope, boolInt(item.LocalOnly), boolInt(item.Enabled), item.RPM, item.MaxConcurrency, item.DailyTokenLimit, unixNano(createdAt))
	if err != nil {
		return fmt.Errorf("upsert caller: %w", err)
	}
	return nil
}

// DeleteCaller deletes a caller while leaving historical request records intact.
func (s *Store) DeleteCaller(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	result, err := s.db.Exec(`DELETE FROM callers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete caller: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// AddRequest appends one request record. IDs are deterministic caller inputs;
// duplicate IDs are rejected by the primary key.
func (s *Store) AddRequest(item RequestRecord) error {
	if err := validateRequest(item); err != nil {
		return err
	}
	_, err := s.db.Exec(`
INSERT INTO requests (id, caller_id, entry, model, provider_id, upstream_model, response_model, protocol, status, error_code, started_at, duration_ms, ttft_ms, input_tokens, output_tokens, cache_tokens, reasoning_tokens, estimated_cost, attempts, outcome)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.CallerID, item.Entry, item.Model, item.ProviderID, item.UpstreamModel, item.ResponseModel, item.Protocol, item.Status, item.ErrorCode, unixNano(item.StartedAt), item.DurationMS, nullableInt(item.TTFTMS), nullableInt(item.InputTokens), nullableInt(item.OutputTokens), nullableInt(item.CacheTokens), nullableInt(item.ReasoningTokens), nullableFloat(item.EstimatedCost), item.Attempts, item.Outcome)
	if err != nil {
		if isConstraintError(err) {
			return fmt.Errorf("%w: request ID already exists", ErrConflict)
		}
		return fmt.Errorf("add request: %w", err)
	}
	return nil
}

// UpdateRequest finalizes an existing precharge record. The original
// StartedAt value is immutable so a restart or cancellation cannot rewrite the
// beginning of a request's audit trail.
func (s *Store) UpdateRequest(item RequestRecord) error {
	if err := validateRequest(item); err != nil {
		return err
	}
	result, err := s.db.Exec(`
UPDATE requests SET
    caller_id = ?,
    entry = ?,
    model = ?,
    provider_id = ?,
    upstream_model = ?,
    response_model = ?,
    protocol = ?,
    status = ?,
    error_code = ?,
    duration_ms = ?,
    ttft_ms = ?,
    input_tokens = ?,
    output_tokens = ?,
    cache_tokens = ?,
    reasoning_tokens = ?,
    estimated_cost = ?,
    attempts = ?,
    outcome = ?
WHERE id = ?`,
		item.CallerID, item.Entry, item.Model, item.ProviderID, item.UpstreamModel, item.ResponseModel, item.Protocol, item.Status, item.ErrorCode, item.DurationMS, nullableInt(item.TTFTMS), nullableInt(item.InputTokens), nullableInt(item.OutputTokens), nullableInt(item.CacheTokens), nullableInt(item.ReasoningTokens), nullableFloat(item.EstimatedCost), item.Attempts, item.Outcome, item.ID)
	if err != nil {
		return fmt.Errorf("update request: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Requests returns newest records first, with a default limit of 100 and a
// hard maximum of 500. A zero status means all statuses.
func (s *Store) Requests(filter RequestFilter) ([]RequestRecord, error) {
	if filter.Limit < 0 {
		return nil, fmt.Errorf("%w: negative request limit", ErrInvalid)
	}
	limit := filter.Limit
	if limit == 0 {
		limit = defaultRequestLimit
	}
	if limit > maxRequestLimit {
		limit = maxRequestLimit
	}
	where := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if filter.CallerID != "" {
		if err := validateID(filter.CallerID); err != nil {
			return nil, err
		}
		where = append(where, "caller_id = ?")
		args = append(args, filter.CallerID)
	}
	if filter.Model != "" {
		if err := validateID(filter.Model); err != nil {
			return nil, err
		}
		where = append(where, "model = ?")
		args = append(args, filter.Model)
	}
	if filter.Status != 0 {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	query := `SELECT id, caller_id, entry, model, provider_id, upstream_model, response_model, protocol, status, error_code, started_at, duration_ms, ttft_ms, input_tokens, output_tokens, cache_tokens, reasoning_tokens, estimated_cost, attempts, outcome FROM requests`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY started_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list requests: %w", err)
	}
	defer rows.Close()
	items := make([]RequestRecord, 0, limit)
	for rows.Next() {
		item, scanErr := scanRequest(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan request: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list requests: %w", err)
	}
	return items, nil
}

// Summary returns durable request totals and counts of configured providers.
// ReadyProviders means enabled provider configuration, not observed health.
func (s *Store) Summary() (Summary, error) {
	var summary Summary
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status >= 400 OR outcome <> 'success' THEN 1 ELSE 0 END), 0), COALESCE(SUM(CASE WHEN attempts > 0 AND (input_tokens IS NULL OR output_tokens IS NULL) THEN 1 ELSE 0 END), 0), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), SUM(estimated_cost) FROM requests`).Scan(&summary.Requests, &summary.Errors, &summary.UnknownUsageRequests, &summary.InputTokens, &summary.OutputTokens, nullableFloatScan(&summary.EstimatedCost)); err != nil {
		return Summary{}, fmt.Errorf("summarize requests: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN enabled = 1 THEN 1 ELSE 0 END), 0) FROM providers`).Scan(&summary.Providers, &summary.ReadyProviders); err != nil {
		return Summary{}, fmt.Errorf("summarize providers: %w", err)
	}
	return summary, nil
}

// CallerUsageSince aggregates input and output tokens for one caller since
// since. Unknown is true only when a real attempt is missing either usage
// component; pre-route records with Attempts == 0 do not consume unknown usage.
func (s *Store) CallerUsageSince(callerID string, since time.Time) (tokens int64, unknown bool, err error) {
	if err := validateID(callerID); err != nil {
		return 0, false, err
	}
	var unknownCount int64
	err = s.db.QueryRow(`
SELECT COALESCE(SUM(COALESCE(input_tokens, 0) + COALESCE(output_tokens, 0)), 0),
       COALESCE(SUM(CASE WHEN attempts > 0 AND (input_tokens IS NULL OR output_tokens IS NULL) THEN 1 ELSE 0 END), 0)
FROM requests
WHERE caller_id = ? AND started_at >= ?`, callerID, unixNano(since)).Scan(&tokens, &unknownCount)
	if err != nil {
		return 0, false, fmt.Errorf("aggregate caller usage: %w", err)
	}
	return tokens, unknownCount > 0, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProvider(row scanner) (Provider, error) {
	var item Provider
	var enabled int
	var created int64
	err := row.Scan(&item.ID, &item.Name, &item.Kind, &item.AuthMode, &item.Protocol, &item.Endpoint, &enabled, &item.OAuthProvider, &item.SecretCipher, &created)
	item.Enabled = enabled != 0
	item.CreatedAt = fromUnixNano(created)
	return item, err
}

func scanModel(row scanner) (Model, error) {
	var item Model
	var protocols string
	var inputImages, enabled int
	var inputPrice, outputPrice sql.NullFloat64
	err := row.Scan(&item.ID, &item.ProviderID, &item.UpstreamModel, &item.Name, &protocols, &inputImages, &enabled, &inputPrice, &outputPrice)
	if err != nil {
		return item, err
	}
	if err := json.Unmarshal([]byte(protocols), &item.Protocols); err != nil {
		return item, err
	}
	item.InputImages = inputImages != 0
	item.Enabled = enabled != 0
	item.InputPrice = nullableFloatValue(inputPrice)
	item.OutputPrice = nullableFloatValue(outputPrice)
	return item, nil
}

func scanCaller(row scanner) (Caller, error) {
	var item Caller
	var allowed string
	var localOnly, enabled int
	var created int64
	err := row.Scan(&item.ID, &item.Name, &item.KeyHash, &item.KeyCipher, &allowed, &item.AccessScope, &localOnly, &enabled, &item.RPM, &item.MaxConcurrency, &item.DailyTokenLimit, &created)
	if err != nil {
		return item, err
	}
	if err := json.Unmarshal([]byte(allowed), &item.AllowedModels); err != nil {
		return item, err
	}
	item.LocalOnly = localOnly != 0
	item.Enabled = enabled != 0
	item.CreatedAt = fromUnixNano(created)
	return item, nil
}

func scanRequest(row scanner) (RequestRecord, error) {
	var item RequestRecord
	var started int64
	var ttft, input, output, cache, reasoning sql.NullInt64
	var cost sql.NullFloat64
	err := row.Scan(&item.ID, &item.CallerID, &item.Entry, &item.Model, &item.ProviderID, &item.UpstreamModel, &item.ResponseModel, &item.Protocol, &item.Status, &item.ErrorCode, &started, &item.DurationMS, &ttft, &input, &output, &cache, &reasoning, &cost, &item.Attempts, &item.Outcome)
	if err != nil {
		return item, err
	}
	item.StartedAt = fromUnixNano(started)
	item.TTFTMS = nullableIntValue(ttft)
	item.InputTokens = nullableIntValue(input)
	item.OutputTokens = nullableIntValue(output)
	item.CacheTokens = nullableIntValue(cache)
	item.ReasoningTokens = nullableIntValue(reasoning)
	item.EstimatedCost = nullableFloatValue(cost)
	return item, nil
}

func prepareDatabasePath(path string) error {
	parent := filepath.Dir(path)
	if info, err := os.Lstat(parent); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: database parent is symlink", ErrInvalid)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat database parent: %w", err)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create database parent: %w", err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("stat database parent: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: database parent must be owner-only directory", ErrInvalid)
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil && !errors.Is(createErr, os.ErrExist) {
			return fmt.Errorf("create database: %w", createErr)
		}
		if createErr == nil {
			if closeErr := file.Close(); closeErr != nil {
				return fmt.Errorf("close database: %w", closeErr)
			}
			return nil
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("stat database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: database must be owner-only regular file", ErrInvalid)
	}
	return nil
}

func validateProvider(item Provider) error {
	if err := validateID(item.ID); err != nil {
		return err
	}
	if item.Name == "" || item.Kind == "" || item.AuthMode == "" || item.Protocol == "" {
		return fmt.Errorf("%w: provider fields", ErrInvalid)
	}
	if item.Kind != "api" && item.Kind != "subscription" && item.Kind != "local" {
		return fmt.Errorf("%w: provider kind", ErrInvalid)
	}
	if item.AuthMode != "api_key" && item.AuthMode != "oauth" && item.AuthMode != "none" {
		return fmt.Errorf("%w: provider auth mode", ErrInvalid)
	}
	if item.Protocol != "openai" && item.Protocol != "responses" && item.Protocol != "anthropic" && item.Protocol != "adapter" {
		return fmt.Errorf("%w: provider protocol", ErrInvalid)
	}
	return nil
}

func validateModel(item Model) error {
	if err := validateID(item.ID); err != nil {
		return err
	}
	if err := validateID(item.ProviderID); err != nil {
		return err
	}
	if item.UpstreamModel == "" || item.Name == "" || len(item.Protocols) == 0 {
		return fmt.Errorf("%w: model fields", ErrInvalid)
	}
	if item.InputPrice != nil && *item.InputPrice < 0 || item.OutputPrice != nil && *item.OutputPrice < 0 {
		return fmt.Errorf("%w: model prices", ErrInvalid)
	}
	for _, protocol := range item.Protocols {
		if protocol != "openai" && protocol != "responses" && protocol != "anthropic" {
			return fmt.Errorf("%w: model protocol", ErrInvalid)
		}
	}
	return nil
}

func validateCaller(item Caller) error {
	if err := validateID(item.ID); err != nil {
		return err
	}
	if item.Name == "" || item.KeyHash == "" || item.AccessScope == "" || item.RPM < 0 || item.MaxConcurrency < 0 || item.DailyTokenLimit < 0 {
		return fmt.Errorf("%w: caller fields", ErrInvalid)
	}
	if item.AccessScope != "lan" && item.AccessScope != "public" && item.AccessScope != "both" {
		return fmt.Errorf("%w: caller access scope", ErrInvalid)
	}
	for _, modelID := range item.AllowedModels {
		if err := validateID(modelID); err != nil {
			return err
		}
	}
	return nil
}

func validateRequest(item RequestRecord) error {
	if err := validateID(item.ID); err != nil {
		return err
	}
	for _, value := range []string{item.CallerID, item.Model, item.ProviderID, item.UpstreamModel, item.ResponseModel} {
		if value == "" {
			continue
		}
		if err := validateID(value); err != nil {
			return err
		}
	}
	if item.Protocol != "openai" && item.Protocol != "responses" && item.Protocol != "anthropic" {
		return fmt.Errorf("%w: request protocol", ErrInvalid)
	}
	if item.Outcome != "success" && item.Outcome != "error" && item.Outcome != "cancelled" && item.Outcome != "incomplete" {
		return fmt.Errorf("%w: request outcome", ErrInvalid)
	}
	if item.StartedAt.IsZero() || item.DurationMS < 0 || item.Attempts < 0 || item.Attempts > 1 {
		return fmt.Errorf("%w: request fields", ErrInvalid)
	}
	for _, value := range []*int64{item.TTFTMS, item.InputTokens, item.OutputTokens, item.CacheTokens, item.ReasoningTokens} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: request counters", ErrInvalid)
		}
	}
	if item.EstimatedCost != nil && *item.EstimatedCost < 0 {
		return fmt.Errorf("%w: request cost", ErrInvalid)
	}
	return nil
}

func validateID(value string) error {
	if value == "" || len(value) > 200 || strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: unsafe identifier", ErrInvalid)
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%w: unsafe identifier", ErrInvalid)
		}
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func unixNano(value time.Time) int64 {
	return value.UTC().UnixNano()
}

func fromUnixNano(value int64) time.Time {
	return time.Unix(0, value).UTC()
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableIntValue(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func nullableFloatValue(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func nullableFloatScan(destination **float64) any {
	return &nullableFloatScanner{destination: destination}
}

func isConstraintError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "primary key")
}

type nullableFloatScanner struct {
	destination **float64
}

func (s *nullableFloatScanner) Scan(src any) error {
	var value sql.NullFloat64
	if err := value.Scan(src); err != nil {
		return err
	}
	*s.destination = nullableFloatValue(value)
	return nil
}
