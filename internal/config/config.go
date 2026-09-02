package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/secrets"
)

type Server struct {
	Address               string
	DatabaseURL           string
	DevInMemory           bool
	Milestone0InsecureAPI bool
	ShutdownTimeout       time.Duration
	RequestBodyLimit      int64
	AuthenticatedPreview  bool
	PublicOrigin          string
	GitHubClientID        string
	GitHubClientSecret    string `json:"-"`
	BootstrapGitHubUserID string
	ManagedSetup          bool
	MasterKey             []byte `json:"-"`
	RunnerGRPCAddress     string
	RunnerServerCertFile  string
	RunnerServerKeyFile   string
	RunnerClientCAFile    string
	RunnerIssuerCertFile  string
	RunnerIssuerKeyFile   string
}

func LoadServer() (Server, error) {
	cfg := Server{
		Address:               env("YUANCI_HTTP_ADDR", ":8080"),
		DatabaseURL:           os.Getenv("YUANCI_DATABASE_URL"),
		DevInMemory:           envBool("YUANCI_DEV_IN_MEMORY", false),
		Milestone0InsecureAPI: envBool("YUANCI_MILESTONE0_INSECURE_API", false),
		ShutdownTimeout:       15 * time.Second,
		RequestBodyLimit:      1 << 20,
	}
	if os.Getenv("YUANCI_RUNNER_SHARED_TOKEN") != "" || os.Getenv("YUANCI_RUNNER_TOKEN") != "" || os.Getenv("YUANCI_SERVER_URL") != "" {
		return Server{}, errors.New("legacy Runner shared-token settings are no longer supported; configure Runner gRPC mTLS")
	}

	if raw := os.Getenv("YUANCI_AUTHENTICATED_PREVIEW"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Server{}, errors.New("YUANCI_AUTHENTICATED_PREVIEW must be a boolean")
		}
		cfg.AuthenticatedPreview = value
	}
	if raw := os.Getenv("YUANCI_AUTH_MANAGED_SETUP"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Server{}, errors.New("YUANCI_AUTH_MANAGED_SETUP must be a boolean")
		}
		cfg.ManagedSetup = value
	}
	if cfg.ManagedSetup && !cfg.AuthenticatedPreview {
		return Server{}, errors.New("managed setup requires authenticated preview; evaluation is forbidden")
	}
	if cfg.DatabaseURL == "" && !cfg.DevInMemory {
		return Server{}, errors.New("YUANCI_DATABASE_URL is required unless YUANCI_DEV_IN_MEMORY=true")
	}
	if cfg.AuthenticatedPreview {
		if cfg.DevInMemory || cfg.Milestone0InsecureAPI {
			return Server{}, errors.New("authenticated preview cannot enable evaluation mode, memory storage or legacy Runner credentials")
		}
		loader := loadGitHub
		if cfg.ManagedSetup {
			loader = loadManaged
		}
		if err := loader(&cfg); err != nil {
			return Server{}, err
		}
	} else if !cfg.DevInMemory && !cfg.Milestone0InsecureAPI {
		return Server{}, errors.New("production authentication is not ready; explicitly select isolated evaluation or authenticated preview mode")
	}
	if raw := os.Getenv("YUANCI_REQUEST_BODY_LIMIT"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1024 {
			return Server{}, fmt.Errorf("invalid YUANCI_REQUEST_BODY_LIMIT %q", raw)
		}
		cfg.RequestBodyLimit = value
	}
	if err := loadRunnerGRPC(&cfg); err != nil {
		return Server{}, err
	}
	return cfg, nil
}

func loadRunnerGRPC(cfg *Server) error {
	values := []string{
		os.Getenv("YUANCI_RUNNER_GRPC_ADDR"), os.Getenv("YUANCI_RUNNER_SERVER_CERT_FILE"),
		os.Getenv("YUANCI_RUNNER_SERVER_KEY_FILE"), os.Getenv("YUANCI_RUNNER_CLIENT_CA_FILE"),
		os.Getenv("YUANCI_RUNNER_ISSUER_CERT_FILE"), os.Getenv("YUANCI_RUNNER_ISSUER_KEY_FILE"),
	}
	configured := 0
	for _, value := range values {
		if value != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil
	}
	if configured != len(values) || cfg.DevInMemory || (!cfg.AuthenticatedPreview && !cfg.Milestone0InsecureAPI) {
		return errors.New("Runner gRPC requires all TLS file settings and PostgreSQL in an explicitly selected control-plane mode")
	}
	cfg.RunnerGRPCAddress = values[0]
	cfg.RunnerServerCertFile = values[1]
	cfg.RunnerServerKeyFile = values[2]
	cfg.RunnerClientCAFile = values[3]
	cfg.RunnerIssuerCertFile = values[4]
	cfg.RunnerIssuerKeyFile = values[5]
	return nil
}

func loadOrigin(cfg *Server) error {
	origin, err := url.Parse(os.Getenv("YUANCI_PUBLIC_ORIGIN"))
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return errors.New("YUANCI_PUBLIC_ORIGIN must be a canonical HTTPS origin")
	}
	cfg.PublicOrigin = origin.Scheme + "://" + origin.Host
	return nil
}
func loadManaged(cfg *Server) error {
	if err := loadOrigin(cfg); err != nil {
		return err
	}
	for _, key := range []string{"YUANCI_GITHUB_CLIENT_ID", "YUANCI_GITHUB_CLIENT_SECRET_FILE", "YUANCI_BOOTSTRAP_GITHUB_USER_ID"} {
		if os.Getenv(key) != "" {
			return errors.New("managed setup cannot mix file-configured GitHub settings")
		}
	}
	file, err := os.Open(os.Getenv("YUANCI_MASTER_KEY_FILE"))
	if err != nil {
		return errors.New("cannot open master key file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128 {
		return errors.New("invalid master key file")
	}
	value, err := io.ReadAll(io.LimitReader(file, 129))
	if err != nil || len(value) > 128 {
		return errors.New("cannot read master key file")
	}
	defer clear(value)
	cfg.MasterKey, err = secrets.ParseMasterKey(strings.TrimRight(string(value), "\r\n"))
	if err != nil {
		return errors.New("master key file must contain a base64-encoded 32-byte key")
	}
	return nil
}
func loadGitHub(cfg *Server) error {
	if err := loadOrigin(cfg); err != nil {
		return err
	}
	cfg.GitHubClientID = os.Getenv("YUANCI_GITHUB_CLIENT_ID")
	cfg.BootstrapGitHubUserID = os.Getenv("YUANCI_BOOTSTRAP_GITHUB_USER_ID")
	if !identity.ValidGitHubSubject(cfg.BootstrapGitHubUserID) {
		return errors.New("YUANCI_BOOTSTRAP_GITHUB_USER_ID must be a positive canonical numeric GitHub user ID")
	}
	file, err := os.Open(os.Getenv("YUANCI_GITHUB_CLIENT_SECRET_FILE"))
	if err != nil {
		return errors.New("cannot open GitHub client secret file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return errors.New("GitHub client secret file must be a regular file of at most 4096 bytes")
	}
	value, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(value) > 4096 {
		return errors.New("cannot read GitHub client secret file")
	}
	cfg.GitHubClientSecret = strings.TrimRight(string(value), "\r\n")
	if _, err := identity.NewGitHub(cfg.GitHubClientID, cfg.GitHubClientSecret, cfg.PublicOrigin+"/api/v1/auth/github/callback"); err != nil {
		return errors.New("invalid GitHub client configuration")
	}
	return nil
}

type Runner struct {
	Name                  string
	Capacity              int
	Labels                map[string]string
	GRPCAddress           string
	GRPCServerName        string
	RootCAFile            string
	StateDir              string
	RegistrationToken     string `json:"-"`
	RegistrationTokenFile string
	OS                    string
	Architecture          string
	Executor              string
	IsolationLevel        string
	AvailableDiskBytes    int64
}

func LoadRunner() (Runner, error) {
	capacity, err := strconv.Atoi(env("YUANCI_RUNNER_CAPACITY", "2"))
	if err != nil || capacity < 1 || capacity > 100 {
		return Runner{}, errors.New("YUANCI_RUNNER_CAPACITY must be between 1 and 100")
	}
	cfg := Runner{
		Name:                  env("YUANCI_RUNNER_NAME", hostname()),
		Capacity:              capacity,
		Labels:                map[string]string{"executor": "docker"},
		GRPCAddress:           os.Getenv("YUANCI_RUNNER_GRPC_ADDRESS"),
		GRPCServerName:        os.Getenv("YUANCI_RUNNER_GRPC_SERVER_NAME"),
		RootCAFile:            os.Getenv("YUANCI_RUNNER_ROOT_CA_FILE"),
		StateDir:              os.Getenv("YUANCI_RUNNER_STATE_DIR"),
		RegistrationToken:     os.Getenv("YUANCI_RUNNER_REGISTRATION_TOKEN"),
		RegistrationTokenFile: os.Getenv("YUANCI_RUNNER_REGISTRATION_TOKEN_FILE"),
		OS:                    runtime.GOOS,
		Architecture:          runtime.GOARCH,
		Executor:              "docker",
		IsolationLevel:        "standard",
	}
	if raw := os.Getenv("YUANCI_RUNNER_AVAILABLE_DISK_BYTES"); raw != "" {
		value, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || value < 0 {
			return Runner{}, errors.New("YUANCI_RUNNER_AVAILABLE_DISK_BYTES must be a non-negative integer")
		}
		cfg.AvailableDiskBytes = value
	}
	if cfg.GRPCAddress == "" || cfg.GRPCServerName == "" || cfg.RootCAFile == "" || cfg.StateDir == "" || !filepath.IsAbs(cfg.StateDir) ||
		(cfg.RegistrationToken != "" && cfg.RegistrationTokenFile != "") {
		return Runner{}, errors.New("Runner requires gRPC address, server name, root CA, absolute state directory and at most one registration token source")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	return err == nil && value
}

func hostname() string {
	value, err := os.Hostname()
	if err != nil || value == "" {
		return "yuanci-runner"
	}
	return value
}
