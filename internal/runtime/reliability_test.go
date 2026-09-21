package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func healthResponse(t *testing.T, runtime *Runtime) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://admin.test/healthz", nil)
	runtime.adminHandler().ServeHTTP(recorder, request)
	return recorder
}

func TestRuntimeHealthLifecycleAndCancelledContext(t *testing.T) {
	runtime, err := New(runtimeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.CheckHealth(context.Background()); err == nil {
		t.Fatal("health succeeded before start")
	}
	if response := healthResponse(t, runtime); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("pre-start health status = %d", response.Code)
	}
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CheckHealth(context.Background()); err != nil {
		t.Fatalf("started runtime health failed: %v", err)
	}
	if response := healthResponse(t, runtime); response.Code != http.StatusOK {
		t.Fatalf("healthy status = %d", response.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runtime.CheckHealth(ctx); err == nil {
		t.Fatal("cancelled health succeeded")
	}
	if err := runtime.CheckHealth(context.Background()); err != nil {
		t.Fatalf("health did not recover after cancelled check: %v", err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CheckHealth(context.Background()); err == nil {
		t.Fatal("health succeeded after shutdown")
	}
	if err := runtime.Start(); err == nil {
		t.Fatal("shut-down runtime was reusable")
	}
}

func TestRuntimeHealthFailsWhenStoreCloses(t *testing.T) {
	runtime, err := New(runtimeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CheckHealth(context.Background()); err == nil {
		t.Fatal("health succeeded after store close")
	}
	response := healthResponse(t, runtime)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("closed-store health = %d", response.Code)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeUnexpectedListenerClosePublishesOneFailure(t *testing.T) {
	runtime, err := New(runtimeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	listener := runtime.listeners[0]
	runtime.mu.Unlock()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-runtime.Errors():
		if failure == nil {
			t.Fatal("listener failure notification was nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener failure notification timed out")
	}
	select {
	case failure := <-runtime.Errors():
		t.Fatalf("duplicate listener failure notification: %v", failure)
	case <-time.After(50 * time.Millisecond):
	}
	if err := runtime.CheckHealth(context.Background()); err == nil {
		t.Fatal("health succeeded after fatal listener close")
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeGracefulShutdownPublishesNoFailure(t *testing.T) {
	runtime, err := New(runtimeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-runtime.Errors():
		t.Fatalf("graceful shutdown published failure: %v", failure)
	case <-time.After(100 * time.Millisecond):
	}
}
