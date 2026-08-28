package config

import (
	"encoding/base64"
	"os"
	"testing"
)

func TestManagedConfigurationGate(t *testing.T) {
	path := previewEnv(t)
	t.Setenv("YUANCI_AUTH_MANAGED_SETUP", "true")
	t.Setenv("YUANCI_GITHUB_CLIENT_ID", "")
	t.Setenv("YUANCI_GITHUB_CLIENT_SECRET_FILE", "")
	t.Setenv("YUANCI_BOOTSTRAP_GITHUB_USER_ID", "")
	t.Setenv("YUANCI_MASTER_KEY_FILE", path)
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServer()
	if err != nil || !cfg.ManagedSetup || len(cfg.MasterKey) != 32 {
		t.Fatal("valid managed configuration failed")
	}
	for key, value := range map[string]string{"YUANCI_AUTH_MANAGED_SETUP": "typo", "YUANCI_AUTHENTICATED_PREVIEW": "false", "YUANCI_GITHUB_CLIENT_ID": "mixed", "YUANCI_MASTER_KEY_FILE": "missing"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := LoadServer(); err == nil {
				t.Fatal("unsafe managed config accepted")
			}
		})
	}
	if err := os.WriteFile(path, []byte("invalid-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServer(); err == nil {
		t.Fatal("invalid key accepted")
	}
}
