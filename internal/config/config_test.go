package config

import (
	"strings"
	"testing"
)

func TestServerFailsClosedWithoutIdentity(t *testing.T) {
	t.Setenv("YUANCI_DATABASE_URL", "postgres://example")
	t.Setenv("YUANCI_DEV_IN_MEMORY", "false")
	t.Setenv("YUANCI_MILESTONE0_INSECURE_API", "false")
	_, err := LoadServer()
	if err == nil || !strings.Contains(err.Error(), "identity is not implemented") {
		t.Fatalf("expected fail-closed error, got %v", err)
	}
}

func TestDevelopmentMemoryModeIsExplicit(t *testing.T) {
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
