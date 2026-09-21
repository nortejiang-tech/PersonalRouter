package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	DataDir                  string `json:"data_dir"`
	LANListen                string `json:"lan_listen"`
	AdminListen              string `json:"admin_listen"`
	PublicListen             string `json:"public_listen"`
	WebDir                   string `json:"web_dir"`
	LANBaseURL               string `json:"lan_base_url"`
	PublicBaseURL            string `json:"public_base_url"`
	AdapterURL               string `json:"adapter_url"`
	AdapterManagementKeyFile string `json:"adapter_management_key_file"`
	AdapterAPIKeyFile        string `json:"adapter_api_key_file"`
}

func LoadConfig(path string) (Config, error) {
	if path == "" || !filepath.IsAbs(path) {
		return Config{}, errors.New("config path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("config unavailable")
	}
	defer file.Close()
	decoder := json.NewDecoder(ioLimit(file, 1<<20))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, errors.New("config invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("config invalid")
	}
	return normalizeConfig(config)
}

func normalizeConfig(config Config) (Config, error) {
	if config.LANListen == "" {
		config.LANListen = "127.0.0.1:8787"
	}
	if config.AdminListen == "" {
		config.AdminListen = "127.0.0.1:8788"
	}
	if config.PublicListen == "" {
		config.PublicListen = "127.0.0.1:8790"
	}
	if config.DataDir == "" || !filepath.IsAbs(config.DataDir) {
		return Config{}, errors.New("data_dir must be absolute")
	}
	if config.WebDir != "" && !filepath.IsAbs(config.WebDir) {
		return Config{}, errors.New("web_dir must be absolute")
	}
	for _, path := range []string{config.AdapterManagementKeyFile, config.AdapterAPIKeyFile} {
		if path != "" && !filepath.IsAbs(path) {
			return Config{}, errors.New("secret file path must be absolute")
		}
	}
	if config.AdapterURL != "" {
		if err := validateURL(config.AdapterURL); err != nil {
			return Config{}, err
		}
	}
	lan, err := validateListen(config.LANListen, false)
	if err != nil {
		return Config{}, fmt.Errorf("lan_listen: %w", err)
	}
	admin, err := validateListen(config.AdminListen, false)
	if err != nil {
		return Config{}, fmt.Errorf("admin_listen: %w", err)
	}
	public, err := validateListen(config.PublicListen, true)
	if err != nil {
		return Config{}, fmt.Errorf("public_listen: %w", err)
	}
	if lan == admin || lan == public || admin == public {
		return Config{}, errors.New("listener addresses must be distinct")
	}
	return config, nil
}

func validateURL(raw string) error {
	if strings.ContainsAny(raw, "\r\n") {
		return errors.New("URL invalid")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("URL invalid")
	}
	return nil
}

func validateListen(raw string, public bool) (string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil || host == "" {
		return "", errors.New("must contain an explicit IP and port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", errors.New("port invalid")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		return "", errors.New("hostname, wildcard, or DNS address is not allowed")
	}
	if public {
		if !ip.IsLoopback() {
			return "", errors.New("public listener must be loopback")
		}
	} else if !ip.IsLoopback() && !ip.IsPrivate() {
		return "", errors.New("listener must be loopback or private")
	}
	return net.JoinHostPort(host, strconv.Itoa(portNumber)), nil
}

type limitedReader struct {
	r         *os.File
	remaining int64
}

func ioLimit(file *os.File, max int64) *limitedReader { return &limitedReader{r: file, remaining: max} }
func (r *limitedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("input too large")
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	return n, err
}
