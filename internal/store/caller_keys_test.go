package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func callerKeyFixture() Caller {
	return Caller{
		ID:              "caller-key",
		Name:            "Key caller",
		KeyHash:         "hash-a",
		AllowedModels:   []string{"provider/model"},
		AccessScope:     "both",
		LocalOnly:       true,
		Enabled:         true,
		RPM:             17,
		MaxConcurrency:  3,
		DailyTokenLimit: 4096,
		CreatedAt:       time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC),
	}
}

func TestCallerKeyCipherMigrationPreservesDataAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "state.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `CREATE TABLE callers (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        key_hash TEXT NOT NULL,
        allowed_models TEXT NOT NULL,
        access_scope TEXT NOT NULL,
        local_only INTEGER NOT NULL,
        enabled INTEGER NOT NULL,
        rpm INTEGER NOT NULL,
        max_concurrency INTEGER NOT NULL,
        daily_token_limit INTEGER NOT NULL,
        created_at INTEGER NOT NULL
    )`
	if _, err := legacy.Exec(legacySchema); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	fixture := callerKeyFixture()
	if _, err := legacy.Exec(`INSERT INTO callers (id,name,key_hash,allowed_models,access_scope,local_only,enabled,rpm,max_concurrency,daily_token_limit,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, fixture.ID, fixture.Name, fixture.KeyHash, `["provider/model"]`, fixture.AccessScope, 1, 1, fixture.RPM, fixture.MaxConcurrency, fixture.DailyTokenLimit, fixture.CreatedAt.UnixNano()); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Caller(fixture.ID)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if got.KeyCipher != "" || got.ID != fixture.ID || got.Name != fixture.Name || got.KeyHash != fixture.KeyHash || got.AccessScope != fixture.AccessScope || !got.LocalOnly || !got.Enabled || got.RPM != fixture.RPM || got.MaxConcurrency != fixture.MaxConcurrency || got.DailyTokenLimit != fixture.DailyTokenLimit || len(got.AllowedModels) != 1 || got.AllowedModels[0] != fixture.AllowedModels[0] {
		store.Close()
		t.Fatalf("legacy caller changed during migration: %#v", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err = store.Caller(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyCipher != "" || got.KeyHash != fixture.KeyHash || got.Name != fixture.Name {
		t.Fatalf("legacy caller did not survive reopen: %#v", got)
	}
}

func TestCallerKeyCipherPersistenceRotationAndUpdate(t *testing.T) {
	store := openTestStore(t)
	fixture := callerKeyFixture()
	fixture.KeyCipher = "cipher-a"
	if err := store.UpsertCaller(fixture); err != nil {
		t.Fatal(err)
	}
	got, err := store.Caller(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyHash != "hash-a" || got.KeyCipher != "cipher-a" {
		t.Fatalf("initial key state was not persisted: %#v", got)
	}

	got.Name = "Edited caller"
	got.RPM = 31
	if err := store.UpsertCaller(got); err != nil {
		t.Fatal(err)
	}
	got, err = store.Caller(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Edited caller" || got.RPM != 31 || got.KeyHash != "hash-a" || got.KeyCipher != "cipher-a" {
		t.Fatalf("read-edit-save changed key state: %#v", got)
	}

	got.KeyHash = "hash-b"
	got.KeyCipher = "cipher-b"
	if err := store.UpsertCaller(got); err != nil {
		t.Fatal(err)
	}
	rotated, err := store.Caller(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.KeyHash != "hash-b" || rotated.KeyCipher != "cipher-b" || rotated.Name != "Edited caller" || rotated.RPM != 31 {
		t.Fatalf("hash and cipher rotation was not atomic: %#v", rotated)
	}
}

func TestCaptureCallerKeyCompareAndSet(t *testing.T) {
	store := openTestStore(t)
	fixture := callerKeyFixture()
	if err := store.UpsertCaller(fixture); err != nil {
		t.Fatal(err)
	}
	if captured, err := store.CaptureCallerKey(fixture.ID, "stale-hash", "cipher-a"); err != nil || captured {
		t.Fatalf("stale hash capture = %v, %v", captured, err)
	}
	if captured, err := store.CaptureCallerKey(fixture.ID, fixture.KeyHash, "cipher-a"); err != nil || !captured {
		t.Fatalf("first capture = %v, %v", captured, err)
	}
	if captured, err := store.CaptureCallerKey(fixture.ID, fixture.KeyHash, "cipher-b"); err != nil || captured {
		t.Fatalf("filled cipher capture = %v, %v", captured, err)
	}
	if captured, err := store.CaptureCallerKey("missing-caller", fixture.KeyHash, "cipher-c"); err != nil || captured {
		t.Fatalf("missing caller capture = %v, %v", captured, err)
	}
	for _, input := range [][3]string{{"", fixture.KeyHash, "cipher"}, {fixture.ID, "", "cipher"}, {fixture.ID, fixture.KeyHash, ""}} {
		if captured, err := store.CaptureCallerKey(input[0], input[1], input[2]); !errors.Is(err, ErrInvalid) || captured {
			t.Fatalf("empty capture input = %v, %v", captured, err)
		}
	}
	got, err := store.Caller(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyHash != fixture.KeyHash || got.KeyCipher != "cipher-a" {
		t.Fatalf("compare-and-set overwrote key state: %#v", got)
	}
}

func TestCallerJSONRedactsHashAndCipherAndDelete(t *testing.T) {
	store := openTestStore(t)
	fixture := callerKeyFixture()
	fixture.KeyCipher = "opaque-cipher"
	if err := store.UpsertCaller(fixture); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || string(encoded) == "null" {
		t.Fatal("caller JSON unexpectedly empty")
	}
	for _, forbidden := range []string{"KeyHash", "KeyCipher", "hash-a", "opaque-cipher"} {
		if contains(string(encoded), forbidden) {
			t.Fatalf("caller JSON exposed protected field %q", forbidden)
		}
	}
	if err := store.DeleteCaller(fixture.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Caller(fixture.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted caller lookup error = %v", err)
	}
}
