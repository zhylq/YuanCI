package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yuanci/yuanci/internal/identity"
)

type Server struct {
	Address               string
	DatabaseURL           string
	DevInMemory           bool
	Milestone0InsecureAPI bool
	ShutdownTimeout       time.Duration
	RequestBodyLimit      int64
	RunnerSharedToken     string
	AuthenticatedPreview  bool
	PublicOrigin          string
	GitHubClientID        string
	GitHubClientSecret    string `json:"-"`
	BootstrapGitHubUserID string
}

func LoadServer() (Server, error) {
	cfg := Server{
		Address:               env("YUANCI_HTTP_ADDR", ":8080"),
		DatabaseURL:           os.Getenv("YUANCI_DATABASE_URL"),
		DevInMemory:           envBool("YUANCI_DEV_IN_MEMORY", false),
		Milestone0InsecureAPI: envBool("YUANCI_MILESTONE0_INSECURE_API", false),
		ShutdownTimeout:       15 * time.Second,
		RequestBodyLimit:      1 << 20,
		RunnerSharedToken:     os.Getenv("YUANCI_RUNNER_SHARED_TOKEN"),
	}

	if raw := os.Getenv("YUANCI_AUTHENTICATED_PREVIEW"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Server{}, errors.New("YUANCI_AUTHENTICATED_PREVIEW must be a boolean")
		}
		cfg.AuthenticatedPreview = value
	}
	if cfg.DatabaseURL == "" && !cfg.DevInMemory {
		return Server{}, errors.New("YUANCI_DATABASE_URL is required unless YUANCI_DEV_IN_MEMORY=true")
	}
	if cfg.AuthenticatedPreview {
		if cfg.DevInMemory || cfg.Milestone0InsecureAPI || cfg.RunnerSharedToken != "" {
			return Server{}, errors.New("authenticated preview cannot enable evaluation mode, memory storage or legacy Runner credentials")
		}
		if err := loadGitHub(&cfg); err != nil {
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
	return cfg, nil
}

func loadGitHub(cfg *Server) error {
	origin, err := url.Parse(os.Getenv("YUANCI_PUBLIC_ORIGIN"))
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return errors.New("YUANCI_PUBLIC_ORIGIN must be a canonical HTTPS origin")
	}
	cfg.PublicOrigin = origin.Scheme + "://" + origin.Host
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
	Name       string
	ServerURL  string
	Token      string
	Capacity   int
	Labels     map[string]string
	PollPeriod time.Duration
}

func LoadRunner() (Runner, error) {
	capacity, err := strconv.Atoi(env("YUANCI_RUNNER_CAPACITY", "2"))
	if err != nil || capacity < 1 || capacity > 100 {
		return Runner{}, errors.New("YUANCI_RUNNER_CAPACITY must be between 1 and 100")
	}
	cfg := Runner{
		Name:       env("YUANCI_RUNNER_NAME", hostname()),
		ServerURL:  os.Getenv("YUANCI_SERVER_URL"),
		Token:      os.Getenv("YUANCI_RUNNER_TOKEN"),
		Capacity:   capacity,
		Labels:     map[string]string{"executor": "docker"},
		PollPeriod: 3 * time.Second,
	}
	if cfg.ServerURL == "" || cfg.Token == "" {
		return Runner{}, errors.New("YUANCI_SERVER_URL and YUANCI_RUNNER_TOKEN are required")
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
