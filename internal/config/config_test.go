package config

import (
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
}
