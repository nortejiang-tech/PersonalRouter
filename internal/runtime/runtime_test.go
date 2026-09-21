package runtime

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freeListenAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func runtimeConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	webDir := filepath.Join(root, "web")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(webDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{DataDir: dataDir, WebDir: webDir, LANListen: freeListenAddress(t), AdminListen: freeListenAddress(t), PublicListen: freeListenAddress(t), LANBaseURL: "http://127.0.0.1:8787", PublicBaseURL: "http://127.0.0.1:8790"}
}

func TestNormalizeConfigRejectsUnsafeAndDuplicateListeners(t *testing.T) {
	base := runtimeConfig(t)
	base.PublicListen = "0.0.0.0:8790"
	if _, err := normalizeConfig(base); err == nil {
		t.Fatal("wildcard public listener accepted")
	}
	base = runtimeConfig(t)
	base.LANListen = "router.local:8787"
	if _, err := normalizeConfig(base); err == nil {
		t.Fatal("DNS listener accepted")
	}
	base = runtimeConfig(t)
	base.PublicListen = base.AdminListen
	if _, err := normalizeConfig(base); err == nil {
		t.Fatal("duplicate listener accepted")
	}
}

func TestRuntimeKeyPersistenceAndPermissionRejection(t *testing.T) {
	config := runtimeConfig(t)
	first, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	firstKey, err := os.ReadFile(filepath.Join(config.DataDir, "admin.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(config.DataDir, "admin.key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("admin key mode = %v, err=%v", info.Mode().Perm(), err)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := os.ReadFile(filepath.Join(config.DataDir, "admin.key"))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstKey) != string(secondKey) {
		t.Fatal("admin key changed across restart")
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(config.DataDir, "admin.key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config); err == nil {
		t.Fatal("insecure admin key accepted")
	}
}

func TestRuntimeListenerIsolationStaticContainmentAndShutdown(t *testing.T) {
	config := runtimeConfig(t)
	runtime, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	keyBytes, err := os.ReadFile(filepath.Join(config.DataDir, "admin.key"))
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(keyBytes))
	client := &http.Client{Timeout: 2 * time.Second}
	adminURL := "http://" + config.AdminListen
	publicURL := "http://" + config.PublicListen
	req, _ := http.NewRequest(http.MethodGet, adminURL+"/admin/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Host = strings.Split(config.AdminListen, ":")[0]
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin settings status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp, err = client.Get(publicURL + "/admin/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("public admin status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp, err = client.Get(adminURL + "/../AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("traversal status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(adminURL + "/healthz")
	if err == nil {
		t.Fatal("request succeeded after shutdown")
	}
}
