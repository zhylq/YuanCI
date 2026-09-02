package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestServerFailsClosedWithoutIdentity(t *testing.T) {
	t.Setenv("YUANCI_AUTHENTICATED_PREVIEW", "false")
	t.Setenv("YUANCI_DATABASE_URL", "postgres://example")
	t.Setenv("YUANCI_DEV_IN_MEMORY", "false")
	t.Setenv("YUANCI_MILESTONE0_INSECURE_API", "false")
	_, err := LoadServer()
	if err == nil || !strings.Contains(err.Error(), "production authentication is not ready") {
		t.Fatalf("expected fail-closed error, got %v", err)
	}
}

func TestDevelopmentMemoryModeIsExplicit(t *testing.T) {
	t.Setenv("YUANCI_AUTHENTICATED_PREVIEW", "false")
	t.Setenv("YUANCI_DATABASE_URL", "")
	t.Setenv("YUANCI_DEV_IN_MEMORY", "true")
	cfg, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DevInMemory {
		t.Fatal("expected explicit development mode")
	}
}

func TestRunnerGRPCConfigurationIsAtomicAndRequiresPostgres(t *testing.T) {
	t.Setenv("YUANCI_RUNNER_GRPC_ADDR", ":9443")
	base := Server{AuthenticatedPreview: true, DatabaseURL: "postgres://example"}
	if err := loadRunnerGRPC(&base); err == nil || !strings.Contains(err.Error(), "all TLS") {
		t.Fatalf("partial Runner TLS configuration accepted: %v", err)
	}
	for key, value := range map[string]string{
		"YUANCI_RUNNER_SERVER_CERT_FILE": "server.pem", "YUANCI_RUNNER_SERVER_KEY_FILE": "server-key.pem",
		"YUANCI_RUNNER_CLIENT_CA_FILE": "root.pem", "YUANCI_RUNNER_ISSUER_CERT_FILE": "issuer.pem",
		"YUANCI_RUNNER_ISSUER_KEY_FILE": "issuer-key.pem",
	} {
		t.Setenv(key, value)
	}
	configured := Server{AuthenticatedPreview: true, DatabaseURL: "postgres://example"}
	err := loadRunnerGRPC(&configured)
	if err != nil || configured.RunnerGRPCAddress != ":9443" {
		t.Fatalf("complete Runner TLS configuration rejected: %+v %v", configured, err)
	}
	insecure := Server{DevInMemory: true}
	if err := loadRunnerGRPC(&insecure); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("in-memory Runner PKI configuration accepted: %v", err)
	}
	evaluation := Server{Milestone0InsecureAPI: true, DatabaseURL: "postgres://example"}
	if err := loadRunnerGRPC(&evaluation); err != nil || evaluation.RunnerGRPCAddress != ":9443" {
		t.Fatalf("explicit PostgreSQL evaluation gRPC configuration rejected: %v", err)
	}
}

func TestServerRejectsLegacyRunnerCredentials(t *testing.T) {
	t.Setenv("YUANCI_DEV_IN_MEMORY", "true")
	t.Setenv("YUANCI_RUNNER_SHARED_TOKEN", "obsolete")
	if _, err := LoadServer(); err == nil || !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("legacy Runner credential accepted: %v", err)
	}
}

func TestRunnerGRPCClientConfiguration(t *testing.T) {
	t.Setenv("YUANCI_RUNNER_GRPC_ADDRESS", "server:9443")
	t.Setenv("YUANCI_RUNNER_GRPC_SERVER_NAME", "server")
	t.Setenv("YUANCI_RUNNER_ROOT_CA_FILE", "root.pem")
	t.Setenv("YUANCI_RUNNER_STATE_DIR", filepath.Join(t.TempDir(), "runner"))
	t.Setenv("YUANCI_RUNNER_REGISTRATION_TOKEN_FILE", "token")
	t.Setenv("YUANCI_RUNNER_AVAILABLE_DISK_BYTES", "1073741824")

	cfg, err := LoadRunner()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCAddress != "server:9443" || cfg.AvailableDiskBytes != 1<<30 || cfg.Executor != "docker" {
		t.Fatalf("unexpected Runner configuration: %+v", cfg)
	}
}

func TestRunnerGRPCClientRejectsUnsafeConfiguration(t *testing.T) {
	setBase := func(t *testing.T) {
		t.Helper()
		t.Setenv("YUANCI_RUNNER_GRPC_ADDRESS", "server:9443")
		t.Setenv("YUANCI_RUNNER_GRPC_SERVER_NAME", "server")
		t.Setenv("YUANCI_RUNNER_ROOT_CA_FILE", "root.pem")
	}
	t.Run("relative state", func(t *testing.T) {
		setBase(t)
		t.Setenv("YUANCI_RUNNER_STATE_DIR", "relative")
		if _, err := LoadRunner(); err == nil {
			t.Fatal("relative credential state directory accepted")
		}
	})
	t.Run("two token sources", func(t *testing.T) {
		setBase(t)
		t.Setenv("YUANCI_RUNNER_STATE_DIR", filepath.Join(t.TempDir(), "runner"))
		t.Setenv("YUANCI_RUNNER_REGISTRATION_TOKEN", "secret")
		t.Setenv("YUANCI_RUNNER_REGISTRATION_TOKEN_FILE", "token")
		if _, err := LoadRunner(); err == nil {
			t.Fatal("multiple registration token sources accepted")
		}
	})
	t.Run("invalid disk", func(t *testing.T) {
		setBase(t)
		t.Setenv("YUANCI_RUNNER_STATE_DIR", filepath.Join(t.TempDir(), "runner"))
		t.Setenv("YUANCI_RUNNER_AVAILABLE_DISK_BYTES", "-1")
		if _, err := LoadRunner(); err == nil {
			t.Fatal("negative disk capacity accepted")
		}
	})
}
