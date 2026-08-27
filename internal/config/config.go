package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Server struct {
	Address               string
	DatabaseURL           string
	DevInMemory           bool
	Milestone0InsecureAPI bool
	ShutdownTimeout       time.Duration
	RequestBodyLimit      int64
	RunnerSharedToken     string
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

	if cfg.DatabaseURL == "" && !cfg.DevInMemory {
		return Server{}, errors.New("YUANCI_DATABASE_URL is required unless YUANCI_DEV_IN_MEMORY=true")
	}
	if !cfg.DevInMemory && !cfg.Milestone0InsecureAPI {
		return Server{}, errors.New("identity is not implemented in milestone 0; set YUANCI_MILESTONE0_INSECURE_API=true only for an isolated evaluation environment")
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
