package security

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNewKeyUniquenessAndVerification(t *testing.T) {
	first, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != len("nr_")+43 || first[:3] != "nr_" {
		t.Fatal("generated keys are not distinct, prefixed, and 32-byte encoded")
	}
	hash := HashKey(first)
	if !VerifyKey(first, hash) || VerifyKey(second, hash) || VerifyKey("", hash) {
		t.Fatal("key hash verification failed")
	}
	if VerifyKey(first, "") || VerifyKey(first, "not-a-hash") || VerifyKey(first, hex.EncodeToString(make([]byte, sha256.Size-1))) {
		t.Fatal("invalid stored hashes must be rejected")
	}
}

func TestRequestKeyHeaders(t *testing.T) {
	tests := []struct {
		name      string
		request   *http.Request
		want      string
		wantError bool
	}{
		{name: "bearer", request: requestWithHeaders("Authorization", "Bearer token"), want: "token"},
		{name: "api key", request: requestWithHeaders("x-api-key", "token"), want: "token"},
		{name: "matching headers", request: requestWithHeaders("Authorization", "Bearer token", "x-api-key", "token"), want: "token"},
		{name: "conflict", request: requestWithHeaders("Authorization", "Bearer one", "x-api-key", "two"), wantError: true},
		{name: "wrong scheme", request: requestWithHeaders("Authorization", "Basic token"), wantError: true},
		{name: "malformed bearer", request: requestWithHeaders("Authorization", "Bearer"), wantError: true},
		{name: "duplicate authorization", request: requestWithHeaders("Authorization", "Bearer one", "Authorization", "Bearer one"), wantError: true},
		{name: "duplicate api key", request: requestWithHeaders("x-api-key", "one", "x-api-key", "one"), wantError: true},
		{name: "missing", request: newRequest(), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := RequestKey(test.request)
			if test.wantError {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("request key = %q, err = %v", got, err)
			}
		})
	}
}

func TestVaultRoundTripAndCorruption(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	vault, err := NewVault(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal([]byte("private value"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := vault.Open(sealed)
	if err != nil || string(opened) != "private value" {
		t.Fatalf("vault round trip = %q, err = %v", opened, err)
	}
	second, err := vault.Seal([]byte("private value"))
	if err != nil || sealed == second {
		t.Fatal("vault must use a fresh nonce")
	}
	wrongKey := make([]byte, 32)
	wrongKey[0] = 1
	wrongVault, err := NewVault(wrongKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongVault.Open(sealed); err == nil {
		t.Fatal("wrong key must fail authentication")
	}
	for _, malformed := range []string{"", "v2.x", "v1.", "v1.!", "v1.a"} {
		if _, err := vault.Open(malformed); err == nil {
			t.Fatalf("malformed token %q was accepted", malformed)
		}
	}
	payload := strings.TrimPrefix(sealed, vaultVersion+".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	corrupted := vaultVersion + "." + base64.RawURLEncoding.EncodeToString(raw)
	if _, err := vault.Open(corrupted); err == nil {
		t.Fatal("corrupted token was accepted")
	}
	const rawURLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	lastIndex := strings.IndexByte(rawURLAlphabet, payload[len(payload)-1])
	if lastIndex < 0 {
		t.Fatal("sealed payload used an invalid base64 character")
	}
	paddingBits := 2
	if len(raw)%3 == 1 {
		paddingBits = 4
	}
	paddingMask := (1 << paddingBits) - 1
	alternateIndex := (lastIndex &^ paddingMask) | ((lastIndex + 1) & paddingMask)
	if alternateIndex == lastIndex {
		alternateIndex = (lastIndex &^ paddingMask) | ((lastIndex + 2) & paddingMask)
	}
	nonCanonical := vaultVersion + "." + payload[:len(payload)-1] + string(rawURLAlphabet[alternateIndex])
	if _, err := vault.Open(nonCanonical); err == nil {
		t.Fatal("non-canonical base64 token was accepted")
	}
	if _, err := NewVault(make([]byte, 31)); err == nil {
		t.Fatal("short vault key was accepted")
	}
}

func TestLoadOrCreateMasterKeyPersistencePermissionsAndConcurrency(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "runtime", "master.key")
	first, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 {
		t.Fatal("master key has the wrong length")
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected permissions: directory %o, file %o", dirInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
	second, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("master key did not persist")
	}

	concurrentPath := filepath.Join(directory, "concurrent", "master.key")
	const workers = 16
	results := make([][]byte, workers)
	errors := make([]error, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index], errors[index] = LoadOrCreateMasterKey(concurrentPath)
		}(i)
	}
	group.Wait()
	for i := range results {
		if errors[i] != nil || string(results[i]) != string(results[0]) {
			t.Fatalf("concurrent load %d failed: %v", i, errors[i])
		}
	}
}

func TestLoadOrCreateMasterKeyRejectsInvalidFiles(t *testing.T) {
	directory := t.TempDir()
	tooShort := filepath.Join(directory, "short.key")
	if err := os.WriteFile(tooShort, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateMasterKey(tooShort); err == nil {
		t.Fatal("short existing key was accepted")
	}
	tooBroad := filepath.Join(directory, "broad.key")
	if err := os.WriteFile(tooBroad, make([]byte, 32), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateMasterKey(tooBroad); err == nil {
		t.Fatal("group-readable key was accepted")
	}
	link := filepath.Join(directory, "link.key")
	validTarget := filepath.Join(directory, "target.key")
	if err := os.WriteFile(validTarget, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(validTarget, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateMasterKey(link); err == nil {
		t.Fatal("symlink key was accepted")
	}
	parentTarget := filepath.Join(directory, "parent-target")
	if err := os.Mkdir(parentTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(directory, "parent-link")
	if err := os.Symlink(parentTarget, parentLink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateMasterKey(filepath.Join(parentLink, "master.key")); err == nil {
		t.Fatal("symlink master-key directory was accepted")
	}
}

func newRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "http://example.test", nil)
}

func requestWithHeaders(values ...string) *http.Request {
	r := newRequest()
	for i := 0; i < len(values); i += 2 {
		r.Header.Add(values[i], values[i+1])
	}
	return r
}
