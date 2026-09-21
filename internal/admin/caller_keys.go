package admin

import (
	"errors"
	"net/http"
	"strings"

	"personalrouter/internal/security"
	"personalrouter/internal/store"
)

type callerKeyInput struct {
	Key string `json:"key"`
}

func (h *Handler) callerKey(w http.ResponseWriter, r *http.Request, id string) {
	if !safeID(id) {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := h.store.Caller(id)
		if errors.Is(err, store.ErrNotFound) {
			h.writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
			return
		}
		if item.KeyCipher == "" {
			h.writeJSON(w, http.StatusOK, map[string]any{"caller_id": id, "available": false, "reason": "legacy_key_not_saved"})
			return
		}
		raw, err := h.vault.Open(item.KeyCipher)
		if err != nil || !security.VerifyKey(string(raw), item.KeyHash) {
			h.writeError(w, http.StatusInternalServerError, "internal_error", "caller key unavailable")
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]any{"caller_id": id, "available": true, "key": string(raw)})
	case http.MethodPost:
		var input callerKeyInput
		if !h.decodeJSON(w, r, &input) {
			return
		}
		raw := strings.TrimSpace(input.Key)
		if raw == "" || len(raw) > 1024 {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid caller key")
			return
		}
		item, err := h.store.Caller(id)
		if errors.Is(err, store.ErrNotFound) {
			h.writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
			return
		}
		if !security.VerifyKey(raw, item.KeyHash) {
			h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid caller key")
			return
		}
		if item.KeyCipher != "" {
			stored, openErr := h.vault.Open(item.KeyCipher)
			if openErr != nil || !security.VerifyKey(string(stored), item.KeyHash) {
				h.writeError(w, http.StatusInternalServerError, "internal_error", "caller key unavailable")
				return
			}
			h.writeJSON(w, http.StatusOK, map[string]any{"caller_id": id, "available": true})
			return
		}
		ciphertext, err := h.vault.Seal([]byte(raw))
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "internal_error", "caller key unavailable")
			return
		}
		captured, err := h.store.CaptureCallerKey(id, item.KeyHash, ciphertext)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "store_error", "store unavailable")
			return
		}
		if !captured {
			current, readErr := h.store.Caller(id)
			if readErr != nil || !security.VerifyKey(raw, current.KeyHash) {
				h.writeError(w, http.StatusBadRequest, "invalid_request", "invalid caller key")
				return
			}
			if current.KeyCipher == "" {
				h.writeError(w, http.StatusConflict, "conflict", "caller key changed")
				return
			}
			stored, openErr := h.vault.Open(current.KeyCipher)
			if openErr != nil || !security.VerifyKey(string(stored), current.KeyHash) || string(stored) != raw {
				h.writeError(w, http.StatusConflict, "conflict", "caller key changed")
				return
			}
		}
		h.writeJSON(w, http.StatusOK, map[string]any{"caller_id": id, "available": true})
	default:
		h.methodNotAllowed(w)
	}
}
