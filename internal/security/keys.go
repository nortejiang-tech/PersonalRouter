package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	keyRandomBytes = 32
	masterKeyBytes = 32
)

// NewKey creates a caller key with enough entropy for use as a bearer token.
func NewKey() (string, error) {
	raw := make([]byte, keyRandomBytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return "nr_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashKey returns the stable hexadecimal SHA-256 representation of raw.
func HashKey(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

// VerifyKey compares a raw key with a stored hexadecimal SHA-256 hash.
func VerifyKey(raw, storedHash string) bool {
	if raw == "" || storedHash == "" {
		return false
	}
	decoded, err := hex.DecodeString(storedHash)
	if err != nil || len(decoded) != sha256.Size {
		return false
	}
	digest := sha256.Sum256([]byte(raw))
	return subtle.ConstantTimeCompare(digest[:], decoded) == 1
}

// RequestKey extracts a caller key from Authorization: Bearer or x-api-key.
// A request may provide both headers only when they carry the same value.
func RequestKey(r *http.Request) (string, error) {
	if r == nil {
		return "", errors.New("nil request")
	}

	authorization, hasAuthorization, err := authorizationKey(r.Header.Values("Authorization"))
	if err != nil {
		return "", err
	}
	xAPIKey, hasAPIKey, err := apiKey(r.Header.Values("x-api-key"))
	if err != nil {
		return "", err
	}
	if !hasAuthorization && !hasAPIKey {
		return "", errors.New("missing API key")
	}
	if hasAuthorization && hasAPIKey && authorization != xAPIKey {
		return "", errors.New("conflicting API keys")
	}
	if hasAuthorization {
		return authorization, nil
	}
	return xAPIKey, nil
}

func authorizationKey(values []string) (string, bool, error) {
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", false, errors.New("multiple Authorization headers")
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false, errors.New("malformed Authorization header")
	}
	return parts[1], true, nil
}

func apiKey(values []string) (string, bool, error) {
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", false, errors.New("multiple x-api-key headers")
	}
	value := strings.TrimSpace(values[0])
	if value == "" || strings.IndexFunc(value, func(r rune) bool {
		return r == ',' || r == '\r' || r == '\n' || r == ' ' || r == '\t'
	}) >= 0 {
		return "", false, errors.New("malformed x-api-key header")
	}
	return value, true, nil
}

// LoadOrCreateMasterKey loads a 32-byte owner-only key, creating it atomically
// when it does not exist. Existing files are never overwritten.
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("empty master-key path")
	}
	parent := filepath.Dir(path)
	if parentInfo, statErr := os.Lstat(parent); statErr == nil {
		if parentInfo.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("master-key directory must not be a symlink")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("stat master-key directory: %w", statErr)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create master-key directory: %w", err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return nil, fmt.Errorf("stat master-key directory: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("master-key directory must not be a symlink")
	}
	if !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("master-key directory must be owner-only")
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		key := make([]byte, masterKeyBytes)
		if _, readErr := io.ReadFull(rand.Reader, key); readErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return nil, fmt.Errorf("generate master key: %w", readErr)
		}
		if _, writeErr := file.Write(key); writeErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return nil, fmt.Errorf("write master key: %w", writeErr)
		}
		if syncErr := file.Sync(); syncErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return nil, fmt.Errorf("sync master key: %w", syncErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			_ = os.Remove(path)
			return nil, fmt.Errorf("close master key: %w", closeErr)
		}
		if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
			return nil, fmt.Errorf("protect master key: %w", chmodErr)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create master key: %w", err)
	}

	// A concurrent creator can expose the empty file briefly between O_EXCL
	// creation and its write. Retry only length failures; stable invalid files
	// are rejected without replacing them.
	var lastErr error
	for attempt := 0; attempt < 100; attempt++ {
		key, readErr := readMasterKey(path)
		if readErr == nil {
			return key, nil
		}
		lastErr = readErr
		if !errors.Is(readErr, errPartialMasterKey) {
			return nil, readErr
		}
		time.Sleep(time.Millisecond)
	}
	return nil, lastErr
}

var errPartialMasterKey = errors.New("master key is incomplete")

func readMasterKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat master key: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("master key must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("master key must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("master key must be owner-only")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open master key: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat opened master key: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("master key must be an owner-only regular file")
	}
	if !os.SameFile(info, openedInfo) {
		return nil, errors.New("master key changed during open")
	}
	key, err := io.ReadAll(io.LimitReader(file, masterKeyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if len(key) != masterKeyBytes {
		if len(key) < masterKeyBytes {
			return nil, errPartialMasterKey
		}
		return nil, errors.New("master key must contain exactly 32 bytes")
	}
	return key, nil
}
