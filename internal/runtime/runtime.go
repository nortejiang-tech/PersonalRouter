package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"personalrouter/internal/admin"
	"personalrouter/internal/gateway"
	"personalrouter/internal/security"
	"personalrouter/internal/store"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultIdleTimeout       = 2 * time.Minute
	maxHeaderBytes           = 1 << 20
	shutdownTimeout          = 20 * time.Second
)

const (
	runtimeNew int32 = iota
	runtimeRunning
	runtimeStopping
	runtimeStopped
	runtimeFailed
)

var errRuntimeHealth = errors.New("runtime health unavailable")
var errRuntimeFailure = errors.New("runtime listener failed")

type Runtime struct {
	cfg       Config
	store     *store.Store
	gateway   *gateway.Gateway
	admin     http.Handler
	servers   []*http.Server
	listeners []net.Listener
	mu        sync.Mutex
	state     atomic.Int32
	healthy   atomic.Bool
	errors    chan error
	errorOnce sync.Once
}

func New(config Config) (*Runtime, error) {
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateDir(config.DataDir); err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	if config.WebDir != "" && (pathWithin(config.DataDir, config.WebDir) || pathWithin(config.WebDir, config.DataDir)) {
		return nil, errors.New("web_dir must not be inside data_dir")
	}

	master, err := security.LoadOrCreateMasterKey(filepath.Join(config.DataDir, "master.key"))
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}
	vault, err := security.NewVault(master)
	if err != nil {
		return nil, errors.New("master key invalid")
	}
	adminKey, err := loadOrCreateAdminKey(filepath.Join(config.DataDir, "admin.key"))
	if err != nil {
		return nil, fmt.Errorf("admin key: %w", err)
	}
	managementKey, err := readPrivateSecret(config.AdapterManagementKeyFile)
	if err != nil {
		return nil, fmt.Errorf("adapter management key: %w", err)
	}
	adapterAPIKey, err := readPrivateSecret(config.AdapterAPIKeyFile)
	if err != nil {
		return nil, fmt.Errorf("adapter API key: %w", err)
	}

	database, err := store.Open(filepath.Join(config.DataDir, "personalrouter.db"))
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	gw := gateway.New(database, vault, gateway.Options{AdapterEndpoint: config.AdapterURL})
	adminHandler, err := admin.New(admin.Options{
		Store: database, Vault: vault, AdminKeyHash: security.HashKey(adminKey),
		AdapterURL: config.AdapterURL, AdapterManagementKey: managementKey, AdapterAPIKey: adapterAPIKey,
		LANBaseURL: config.LANBaseURL, PublicBaseURL: config.PublicBaseURL,
	})
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("admin: %w", err)
	}
	runtime := &Runtime{cfg: config, store: database, gateway: gw, admin: adminHandler, errors: make(chan error, 1)}
	runtime.state.Store(runtimeNew)
	runtime.servers = []*http.Server{
		runtime.newServer(runtime.inferenceHandler("lan")),
		runtime.newServer(runtime.adminHandler()),
		runtime.newServer(runtime.inferenceHandler("public")),
	}
	return runtime, nil
}

func (r *Runtime) Start() error {
	if r == nil {
		return errors.New("nil runtime")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Load() != runtimeNew {
		return errors.New("runtime cannot be reused")
	}
	addresses := []string{r.cfg.LANListen, r.cfg.AdminListen, r.cfg.PublicListen}
	listeners := make([]net.Listener, 0, len(addresses))
	for _, address := range addresses {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			r.healthy.Store(false)
			r.state.Store(runtimeFailed)
			return errors.New("listener unavailable")
		}
		listeners = append(listeners, listener)
	}
	r.listeners = listeners
	r.healthy.Store(true)
	r.state.Store(runtimeRunning)
	for i, listener := range listeners {
		server := r.servers[i]
		go r.serve(server, listener)
	}
	return nil
}

func (r *Runtime) serve(server *http.Server, listener net.Listener) {
	_ = server.Serve(listener)
	if r.state.Load() != runtimeRunning {
		return
	}
	r.healthy.Store(false)
	if r.state.CompareAndSwap(runtimeRunning, runtimeFailed) {
		r.errorOnce.Do(func() { r.errors <- errRuntimeFailure })
	}
}

// Errors returns a buffered one-shot notification channel for unexpected
// listener termination. Graceful shutdown does not publish a value.
func (r *Runtime) Errors() <-chan error {
	if r == nil {
		ch := make(chan error)
		close(ch)
		return ch
	}
	return r.errors
}

// CheckHealth verifies lifecycle state and performs a bounded read from the
// durable store. It never contacts an upstream model, adapter, or tunnel.
func (r *Runtime) CheckHealth(ctx context.Context) error {
	if r == nil || r.store == nil || r.state.Load() != runtimeRunning || !r.healthy.Load() {
		return errRuntimeHealth
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := r.store.CheckHealth(checkCtx); err != nil {
		return errRuntimeHealth
	}
	if r.state.Load() != runtimeRunning || !r.healthy.Load() {
		return errRuntimeHealth
	}
	return nil
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > shutdownTimeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()
	}
	r.mu.Lock()
	state := r.state.Load()
	if state == runtimeStopped {
		r.mu.Unlock()
		return nil
	}
	servers := append([]*http.Server(nil), r.servers...)
	r.healthy.Store(false)
	r.state.Store(runtimeStopping)
	r.mu.Unlock()
	var first error
	for _, server := range servers {
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
			if first == nil {
				first = err
			}
		}
	}
	if r.store != nil {
		if err := r.store.Close(); err != nil && first == nil {
			first = err
		}
	}
	r.state.Store(runtimeStopped)
	return first
}

func (r *Runtime) Config() Config {
	if r == nil {
		return Config{}
	}
	return r.cfg
}

func (r *Runtime) Addresses() []string {
	if r == nil {
		return nil
	}
	return []string{r.cfg.LANListen, r.cfg.AdminListen, r.cfg.PublicListen}
}

func (r *Runtime) newServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: defaultReadHeaderTimeout, ReadTimeout: 2 * time.Minute, IdleTimeout: defaultIdleTimeout, MaxHeaderBytes: maxHeaderBytes}
}

func (r *Runtime) inferenceHandler(entry string) http.Handler {
	return withHeaders(r.gateway.Handler(entry), false)
}

func (r *Runtime) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/admin/api", withHeaders(r.admin, true))
	mux.Handle("/admin/api/", withHeaders(r.admin, true))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		setSecurityHeaders(w, true)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if err := r.CheckHealth(req.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "unavailable", "accounting_failures": r.gateway.AccountingFailures()})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "accounting_failures": r.gateway.AccountingFailures()})
	})
	mux.HandleFunc("/", r.serveWeb)
	return mux
}

func withHeaders(next http.Handler, adminSurface bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		setSecurityHeaders(w, adminSurface)
		next.ServeHTTP(w, req)
	})
}

func setSecurityHeaders(w http.ResponseWriter, adminSurface bool) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	if adminSurface {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
	}
}

func (r *Runtime) serveWeb(w http.ResponseWriter, req *http.Request) {
	setSecurityHeaders(w, true)
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.cfg.WebDir == "" {
		http.Error(w, "web unavailable", http.StatusServiceUnavailable)
		return
	}
	root, err := filepath.EvalSymlinks(r.cfg.WebDir)
	if err != nil {
		http.Error(w, "web unavailable", http.StatusServiceUnavailable)
		return
	}
	dataRoot, dataErr := filepath.EvalSymlinks(r.cfg.DataDir)
	if dataErr != nil || pathWithin(dataRoot, root) || pathWithin(root, dataRoot) {
		http.Error(w, "web unavailable", http.StatusServiceUnavailable)
		return
	}
	pathPart, err := urlPath(req.URL.EscapedPath())
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if pathPart == "" {
		pathPart = "index.html"
	}
	if pathPart != "index.html" && !strings.HasPrefix(pathPart, "assets/") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if pathPart != "index.html" {
		ext := strings.ToLower(filepath.Ext(pathPart))
		allowed := map[string]bool{".css": true, ".js": true, ".map": true, ".png": true, ".jpg": true, ".jpeg": true, ".svg": true, ".ico": true, ".woff": true, ".woff2": true, ".ttf": true}
		if !allowed[ext] {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}
	candidate := filepath.Join(root, filepath.FromSlash(pathPart))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || pathWithin(root, resolved) == false {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	file, err := os.Open(resolved)
	if err != nil {
		http.Error(w, "web unavailable", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	http.ServeContent(w, req, filepath.Base(resolved), info.ModTime(), file)
}

func urlPath(escaped string) (string, error) {
	if !strings.HasPrefix(escaped, "/") || strings.Contains(escaped, "\\") {
		return "", errors.New("invalid path")
	}
	decoded, err := urlPathUnescape(escaped)
	if err != nil || strings.Contains(decoded, "\\") {
		return "", errors.New("invalid path")
	}
	trimmed := strings.TrimPrefix(decoded, "/")
	clean := filepath.Clean(filepath.FromSlash(trimmed))
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path traversal")
	}
	return filepath.ToSlash(clean), nil
}

func pathWithin(root, candidate string) bool {
	root, _ = filepath.Abs(filepath.Clean(root))
	candidate, _ = filepath.Abs(filepath.Clean(candidate))
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func ensurePrivateDir(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("directory path invalid")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("directory must be owner-only")
	}
	return nil
}

func loadOrCreateAdminKey(path string) (string, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return "", errors.New("key file permissions invalid")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", errors.New("key file unavailable")
		}
		key := strings.TrimSuffix(string(data), "\n")
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "nr_"))
		if len(key) != 46 || !strings.HasPrefix(key, "nr_") || decodeErr != nil || len(decoded) != 32 || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n") {
			return "", errors.New("key file invalid")
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("key file unavailable")
	}
	key, err := security.NewKey()
	if err != nil {
		return "", errors.New("key generation failed")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", errors.New("key creation failed")
	}
	if _, err := io.WriteString(file, key+"\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", errors.New("key creation failed")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", errors.New("key creation failed")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", errors.New("key creation failed")
	}
	return key, nil
}

func readPrivateSecret(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("secret file unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("secret file permissions invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("secret file unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() == 0 || opened.Size() > 4096 {
		return "", errors.New("secret file invalid")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) == 0 || len(data) > 4096 {
		return "", errors.New("secret file invalid")
	}
	value := strings.TrimSuffix(string(data), "\n")
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("secret file invalid")
	}
	return value, nil
}

func urlPathUnescape(value string) (string, error) {
	// Avoid importing URL parsing into static-file policy; this accepts only path escapes.
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			out.WriteByte(value[i])
			continue
		}
		if i+2 >= len(value) {
			return "", errors.New("bad escape")
		}
		nibble := func(b byte) (byte, bool) {
			switch {
			case b >= '0' && b <= '9':
				return b - '0', true
			case b >= 'a' && b <= 'f':
				return b - 'a' + 10, true
			case b >= 'A' && b <= 'F':
				return b - 'A' + 10, true
			default:
				return 0, false
			}
		}
		hi, ok1 := nibble(value[i+1])
		lo, ok2 := nibble(value[i+2])
		if !ok1 || !ok2 {
			return "", errors.New("bad escape")
		}
		out.WriteByte(hi<<4 | lo)
		i += 2
	}
	return out.String(), nil
}
