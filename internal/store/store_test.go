package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreRoundTripRedactionAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "personalrouter.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	created := time.Date(2026, 9, 20, 1, 2, 3, 456000000, time.UTC)
	provider := Provider{
		ID:            "provider-a",
		Name:          "Provider A",
		Kind:          "api",
		AuthMode:      "api_key",
		Protocol:      "openai",
		Endpoint:      "https://provider.example/v1",
		Enabled:       true,
		OAuthProvider: "",
		SecretCipher:  "encrypted-secret",
		CreatedAt:     created,
	}
	if err := store.UpsertProvider(provider); err != nil {
		t.Fatal(err)
	}
	inputPrice := 1.25
	outputPrice := 2.5
	model := Model{
		ID:            "provider-a/model-one",
		ProviderID:    provider.ID,
		UpstreamModel: "model-one",
		Name:          "Model One",
		Protocols:     []string{"openai", "responses"},
		InputImages:   true,
		Enabled:       true,
		InputPrice:    &inputPrice,
		OutputPrice:   &outputPrice,
	}
	if err := store.UpsertModel(model); err != nil {
		t.Fatal(err)
	}
	caller := Caller{
		ID:              "caller-a",
		Name:            "Caller A",
		KeyHash:         "hash-of-key",
		AllowedModels:   []string{model.ID},
		AccessScope:     "both",
		LocalOnly:       false,
		Enabled:         true,
		RPM:             12,
		MaxConcurrency:  3,
		DailyTokenLimit: 1000,
		CreatedAt:       created,
	}
	if err := store.UpsertCaller(caller); err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || contains(string(encoded), "SecretCipher") || contains(string(encoded), "encrypted-secret") {
		t.Fatal("provider JSON exposed encrypted secret")
	}
	callerJSON, err := json.Marshal(caller)
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(callerJSON), "KeyHash") || contains(string(callerJSON), "hash-of-key") {
		t.Fatal("caller JSON exposed key hash")
	}

	zero := int64(0)
	five := int64(5)
	seven := int64(7)
	started := created.Add(time.Minute)
	if err := store.AddRequest(RequestRecord{
		ID:            "request-success",
		CallerID:      caller.ID,
		Entry:         "lan",
		Model:         model.ID,
		ProviderID:    provider.ID,
		UpstreamModel: model.UpstreamModel,
		ResponseModel: model.ID,
		Protocol:      "openai",
		Status:        200,
		StartedAt:     started,
		DurationMS:    100,
		InputTokens:   &five,
		OutputTokens:  &seven,
		Attempts:      1,
		Outcome:       "success",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRequest(RequestRecord{
		ID:            "request-error",
		CallerID:      caller.ID,
		Entry:         "public",
		Model:         model.ID,
		ProviderID:    provider.ID,
		UpstreamModel: model.UpstreamModel,
		ResponseModel: model.ID,
		Protocol:      "openai",
		Status:        500,
		ErrorCode:     "upstream_error",
		StartedAt:     started.Add(time.Minute),
		DurationMS:    120,
		TTFTMS:        &zero,
		Attempts:      1,
		Outcome:       "error",
	}); err != nil {
		t.Fatal(err)
	}

	requests, err := store.Requests(RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].ID != "request-error" || requests[1].InputTokens == nil || *requests[1].InputTokens != 5 || requests[0].InputTokens != nil || requests[0].TTFTMS == nil || *requests[0].TTFTMS != 0 {
		t.Fatalf("unexpected request round trip: %#v", requests)
	}
	filtered, err := store.Requests(RequestFilter{CallerID: caller.ID, Model: model.ID, Status: 500, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ID != "request-error" {
		t.Fatalf("unexpected filtered requests: %#v", filtered)
	}

	summary, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 2 || summary.Errors != 1 || summary.InputTokens != 5 || summary.OutputTokens != 7 || summary.UnknownUsageRequests != 1 || summary.Providers != 1 || summary.ReadyProviders != 1 || summary.EstimatedCost != nil {
		t.Fatalf("unexpected summary: %#v", summary)
	}

	if err := store.UpsertProvider(Provider{ID: provider.ID, Name: "renamed", Kind: provider.Kind, AuthMode: provider.AuthMode, Protocol: provider.Protocol, Endpoint: provider.Endpoint, Enabled: false, SecretCipher: "new-secret"}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Provider(provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.CreatedAt.Equal(created) || updated.Name != "renamed" || updated.Enabled {
		t.Fatalf("created timestamp was not preserved: %#v", updated)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	persistedProvider, err := store.Provider(provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedProvider.SecretCipher != "new-secret" || persistedProvider.Name != "renamed" {
		t.Fatal("encrypted provider secret or provider update did not persist")
	}
	persistedCaller, err := store.Caller(caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedCaller.KeyHash != caller.KeyHash || len(persistedCaller.AllowedModels) != 1 {
		t.Fatal("caller policy did not persist")
	}
}

func TestStoreReferenceGuardsAndNotFound(t *testing.T) {
	store := openTestStore(t)
	provider := Provider{ID: "provider", Name: "Provider", Kind: "local", AuthMode: "none", Protocol: "openai", Enabled: true}
	if err := store.UpsertProvider(provider); err != nil {
		t.Fatal(err)
	}
	model := Model{ID: "model", ProviderID: provider.ID, UpstreamModel: "upstream", Name: "Model", Protocols: []string{"openai"}, Enabled: true}
	if err := store.UpsertModel(model); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProvider(provider.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete provider error = %v, want ErrConflict", err)
	}
	if err := store.DeleteModel(model.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProvider(provider.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Provider(provider.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("provider lookup error = %v, want ErrNotFound", err)
	}
	if err := store.DeleteCaller("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("caller delete error = %v, want ErrNotFound", err)
	}
}

func TestStoreConcurrentRequests(t *testing.T) {
	store := openTestStore(t)
	const workers = 8
	var group sync.WaitGroup
	errorsByWorker := make([]error, workers)
	started := time.Now().UTC()
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			errorsByWorker[index] = store.AddRequest(RequestRecord{
				ID:         "request-" + string(rune('a'+index)),
				CallerID:   "caller",
				Model:      "model",
				ProviderID: "provider",
				Protocol:   "openai",
				Status:     200,
				StartedAt:  started.Add(time.Duration(index) * time.Second),
				Attempts:   1,
				Outcome:    "success",
			})
		}(i)
	}
	group.Wait()
	for _, err := range errorsByWorker {
		if err != nil {
			t.Fatal(err)
		}
	}
	requests, err := store.Requests(RequestFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != workers {
		t.Fatalf("got %d concurrent requests, want %d", len(requests), workers)
	}
}

func TestStoreUsageAggregateAndRequestLimit(t *testing.T) {
	store := openTestStore(t)
	started := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 125; i++ {
		input := int64(i)
		output := int64(1)
		attempts := 1
		if i == 123 {
			input = 0
		}
		if i == 124 {
			attempts = 0
		}
		record := RequestRecord{
			ID:           fmt.Sprintf("usage-%03d", i),
			CallerID:     "usage-caller",
			Model:        "usage-model",
			ProviderID:   "usage-provider",
			Protocol:     "openai",
			Status:       200,
			StartedAt:    started.Add(time.Duration(i) * time.Second),
			Attempts:     attempts,
			Outcome:      "success",
			OutputTokens: &output,
		}
		if i != 123 && i != 124 {
			record.InputTokens = &input
		}
		if i == 124 {
			record.OutputTokens = nil
		}
		if i == 123 {
			record.Outcome = "incomplete"
		}
		if err := store.AddRequest(record); err != nil {
			t.Fatal(err)
		}
	}
	defaultPage, err := store.Requests(RequestFilter{CallerID: "usage-caller"})
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultPage) != 100 {
		t.Fatalf("default request page length = %d, want 100", len(defaultPage))
	}
	fullPage, err := store.Requests(RequestFilter{CallerID: "usage-caller", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(fullPage) != 125 {
		t.Fatalf("bounded request page length = %d, want 125", len(fullPage))
	}
	summary, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 125 || summary.Errors != 1 || summary.UnknownUsageRequests != 1 {
		t.Fatalf("summary = %#v, want 125 requests, 1 error, 1 unknown usage", summary)
	}
	tokens, unknown, err := store.CallerUsageSince("usage-caller", started.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 7627 || !unknown {
		t.Fatalf("caller usage = (%d, %t), want (7627, true)", tokens, unknown)
	}
}

func TestStoreRequestPrechargeUpdatePreservesStart(t *testing.T) {
	store := openTestStore(t)
	started := time.Date(2026, 9, 20, 4, 0, 0, 0, time.UTC)
	initial := RequestRecord{
		ID:        "precharge",
		Protocol:  "openai",
		Status:    202,
		StartedAt: started,
		Attempts:  0,
		Outcome:   "incomplete",
	}
	if err := store.AddRequest(initial); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRequest(initial); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate precharge error = %v, want ErrConflict", err)
	}
	final := initial
	final.CallerID = "caller"
	final.Model = "model"
	final.ProviderID = "provider"
	final.UpstreamModel = "upstream"
	final.ResponseModel = "model"
	final.Status = 200
	final.DurationMS = 42
	final.Attempts = 1
	final.Outcome = "success"
	final.StartedAt = started.Add(time.Hour)
	if err := store.UpdateRequest(final); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Requests(RequestFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].StartedAt.Equal(started) || rows[0].DurationMS != 42 || rows[0].Attempts != 1 {
		t.Fatalf("updated precharge = %#v", rows)
	}
	if err := store.UpdateRequest(RequestRecord{ID: "missing", Protocol: "openai", StartedAt: started, Outcome: "error"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update error = %v, want ErrNotFound", err)
	}
}

func TestOpenRejectsUnsafeDatabasePath(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(directory, "database.db")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("permissive parent error = %v, want ErrInvalid", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(link, "database.db")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink parent error = %v, want ErrInvalid", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db", "store.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
