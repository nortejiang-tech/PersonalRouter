package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"personalrouter/internal/security"
	"personalrouter/internal/store"
)

func TestCallerKeyEndpointCreateReadIdempotentAndRotate(t *testing.T) {
	h, adminKey, db := testHandler(t, "", "")
	created := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/callers", adminKey, []byte(`{"id":"key-caller","name":"Key caller","allowed_models":["p1/model"],"access_scope":"lan","enabled":true}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("caller create status = %d", created.Code)
	}
	var createdBody map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	raw, ok := createdBody["key"].(string)
	if !ok || raw == "" {
		t.Fatal("created caller did not return a key")
	}

	read := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/callers/key-caller/key", adminKey, nil)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), raw) || !strings.Contains(read.Body.String(), `"available":true`) {
		t.Fatalf("caller key read = %d", read.Code)
	}
	idempotent := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/callers/key-caller/key", adminKey, []byte(`{"key":"`+raw+`"}`))
	if idempotent.Code != http.StatusOK || strings.Contains(idempotent.Body.String(), raw) {
		t.Fatal("idempotent key save returned credential")
	}
	list := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/callers", adminKey, nil)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), raw) || strings.Contains(list.Body.String(), "key_hash") || strings.Contains(list.Body.String(), "key_cipher") {
		t.Fatal("caller list exposed protected key data")
	}
	stored, err := db.Caller("key-caller")
	if err != nil || stored.KeyCipher == "" {
		t.Fatalf("caller cipher was not persisted: %v", err)
	}

	rotated := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/callers/key-caller/rotate", adminKey, nil)
	if rotated.Code != http.StatusOK {
		t.Fatalf("caller rotation status = %d", rotated.Code)
	}
	var rotatedBody map[string]any
	if err := json.Unmarshal(rotated.Body.Bytes(), &rotatedBody); err != nil {
		t.Fatal(err)
	}
	newRaw, ok := rotatedBody["key"].(string)
	if !ok || newRaw == "" || newRaw == raw {
		t.Fatal("caller rotation did not return a new key")
	}
	stored, err = db.Caller("key-caller")
	if err != nil || security.VerifyKey(raw, stored.KeyHash) || !security.VerifyKey(newRaw, stored.KeyHash) || stored.KeyCipher == "" {
		t.Fatal("caller rotation did not replace the hash and cipher")
	}
	oldSave := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/callers/key-caller/key", adminKey, []byte(`{"key":"`+raw+`"}`))
	if oldSave.Code != http.StatusBadRequest || strings.Contains(oldSave.Body.String(), raw) {
		t.Fatal("old caller key was accepted after rotation")
	}
	newRead := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/callers/key-caller/key", adminKey, nil)
	if newRead.Code != http.StatusOK || !strings.Contains(newRead.Body.String(), newRaw) {
		t.Fatal("new caller key was not readable after rotation")
	}
	if err := db.DeleteCaller("key-caller"); err != nil {
		t.Fatal(err)
	}
	missing := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/callers/key-caller/key", adminKey, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted caller key status = %d", missing.Code)
	}
}

func TestCallerKeyEndpointLegacyCaptureTamperAndPolicy(t *testing.T) {
	h, adminKey, db := testHandler(t, "", "")
	legacyRaw := "nr_legacy_fixture_key"
	if err := db.UpsertCaller(store.Caller{ID: "legacy-caller", Name: "Legacy", KeyHash: security.HashKey(legacyRaw), AccessScope: "both", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	legacy := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/callers/legacy-caller/key", adminKey, nil)
	if legacy.Code != http.StatusOK || !strings.Contains(legacy.Body.String(), `"available":false`) || !strings.Contains(legacy.Body.String(), "legacy_key_not_saved") {
		t.Fatalf("legacy key read = %d", legacy.Code)
	}
	wrong := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/callers/legacy-caller/key", adminKey, []byte(`{"key":"nr_wrong_fixture_key"}`))
	if wrong.Code != http.StatusBadRequest || strings.Contains(wrong.Body.String(), legacyRaw) {
		t.Fatal("wrong legacy key was not rejected safely")
	}
	captured := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/callers/legacy-caller/key", adminKey, []byte(`{"key":"`+legacyRaw+`"}`))
	if captured.Code != http.StatusOK {
		t.Fatalf("legacy capture status = %d", captured.Code)
	}
	read := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/callers/legacy-caller/key", adminKey, nil)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), legacyRaw) {
		t.Fatal("captured legacy key was not readable")
	}

	item, err := db.Caller("legacy-caller")
	if err != nil {
		t.Fatal(err)
	}
	item.KeyCipher = "tampered-cipher"
	if err := db.UpsertCaller(item); err != nil {
		t.Fatal(err)
	}
	tampered := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/callers/legacy-caller/key", adminKey, nil)
	if tampered.Code != http.StatusInternalServerError || strings.Contains(tampered.Body.String(), "tampered-cipher") || strings.Contains(tampered.Body.String(), legacyRaw) {
		t.Fatal("tampered caller key produced unsafe response")
	}
	wrongMethod := adminRequest(t, h, http.MethodPut, "http://admin.test/admin/api/callers/legacy-caller/key", adminKey, nil)
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("caller key wrong method = %d", wrongMethod.Code)
	}
	noOrigin := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://admin.test/admin/api/callers/legacy-caller/key", nil)
	req.Host = "admin.test"
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Origin", "http://other.test")
	h.ServeHTTP(noOrigin, req)
	if noOrigin.Code != http.StatusForbidden || noOrigin.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cross-origin key access = %d", noOrigin.Code)
	}
}

func TestCallerKeyEndpointAdminBoundaryAndHashCipherMismatch(t *testing.T) {
	h, adminKey, db := testHandler(t, "", "")
	created := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/callers", adminKey, []byte(`{"id":"boundary-caller","name":"Boundary","access_scope":"lan","enabled":true}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("boundary caller create status = %d", created.Code)
	}
	var createdBody map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	callerKey, ok := createdBody["key"].(string)
	if !ok || callerKey == "" {
		t.Fatal("boundary caller key missing")
	}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(method, "http://admin.test/admin/api/callers/boundary-caller/key", nil)
		req.Host = "admin.test"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("missing admin auth %s status = %d", method, rec.Code)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		body := []byte(nil)
		if method == http.MethodPost {
			body = []byte(`{"key":"` + callerKey + `"}`)
		}
		req := httptest.NewRequest(method, "http://admin.test/admin/api/callers/boundary-caller/key", bytes.NewReader(body))
		req.Host = "admin.test"
		req.Header.Set("Authorization", "Bearer "+callerKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), callerKey) {
			t.Fatalf("caller key used as admin auth %s status = %d", method, rec.Code)
		}
	}

	item, err := db.Caller("boundary-caller")
	if err != nil {
		t.Fatal(err)
	}
	differentKey := "nr_different_current_key"
	item.KeyHash = security.HashKey(differentKey)
	if err := db.UpsertCaller(item); err != nil {
		t.Fatal(err)
	}
	getMismatch := adminRequest(t, h, http.MethodGet, "http://admin.test/admin/api/callers/boundary-caller/key", adminKey, nil)
	if getMismatch.Code != http.StatusInternalServerError || strings.Contains(getMismatch.Body.String(), differentKey) {
		t.Fatalf("valid cipher/hash mismatch GET = %d", getMismatch.Code)
	}
	postMismatch := adminRequest(t, h, http.MethodPost, "http://admin.test/admin/api/callers/boundary-caller/key", adminKey, []byte(`{"key":"`+differentKey+`"}`))
	if postMismatch.Code != http.StatusInternalServerError || strings.Contains(postMismatch.Body.String(), differentKey) {
		t.Fatalf("valid cipher/hash mismatch POST = %d", postMismatch.Code)
	}
}
