package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func previewEnv(t *testing.T) string {
	t.Helper()
	for key, value := range map[string]string{
		"YUANCI_DATABASE_URL": "postgres://test", "YUANCI_AUTHENTICATED_PREVIEW": "true",
		"YUANCI_DEV_IN_MEMORY": "false", "YUANCI_MILESTONE0_INSECURE_API": "false", "YUANCI_RUNNER_SHARED_TOKEN": "",
		"YUANCI_PUBLIC_ORIGIN": "https://ci.example.test/", "YUANCI_GITHUB_CLIENT_ID": "Iv1.fixture",
		"YUANCI_BOOTSTRAP_GITHUB_USER_ID": "100", "YUANCI_REQUEST_BODY_LIMIT": "",
	} {
		t.Setenv(key, value)
	}
	secretPath := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretPath, []byte("fixture-secret-not-real-123456\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YUANCI_GITHUB_CLIENT_SECRET_FILE", secretPath)
	return secretPath
}

func TestAuthenticatedPreviewConfiguration(t *testing.T) {
	previewEnv(t)
	cfg, err := LoadServer()
	if err != nil || !cfg.AuthenticatedPreview || cfg.PublicOrigin != "https://ci.example.test" || cfg.GitHubClientSecret != "fixture-secret-not-real-123456" {
		t.Fatal("valid preview configuration rejected")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(encoded), cfg.GitHubClientSecret) {
		t.Fatal("client secret serialized")
	}
}

func TestAuthenticatedPreviewRejectsUnsafeConfiguration(t *testing.T) {
	for _, test := range []struct{ key, value string }{
		{"YUANCI_AUTHENTICATED_PREVIEW", "tru"}, {"YUANCI_DATABASE_URL", ""},
		{"YUANCI_DEV_IN_MEMORY", "true"}, {"YUANCI_MILESTONE0_INSECURE_API", "true"}, {"YUANCI_RUNNER_SHARED_TOKEN", "legacy"},
		{"YUANCI_PUBLIC_ORIGIN", "http://ci.example.test"}, {"YUANCI_PUBLIC_ORIGIN", "https://ci.example.test/path"},
		{"YUANCI_PUBLIC_ORIGIN", "https://user:password@ci.example.test"}, {"YUANCI_PUBLIC_ORIGIN", "https://ci.example.test?"},
		{"YUANCI_PUBLIC_ORIGIN", "https://ci.example.test/#fragment"}, {"YUANCI_GITHUB_CLIENT_ID", ""},
		{"YUANCI_BOOTSTRAP_GITHUB_USER_ID", "username"}, {"YUANCI_BOOTSTRAP_GITHUB_USER_ID", "0100"},
		{"YUANCI_GITHUB_CLIENT_SECRET_FILE", "missing-secret-file"},
	} {
		t.Run(test.key+"/"+test.value, func(t *testing.T) {
			previewEnv(t)
			t.Setenv(test.key, test.value)
			if _, err := LoadServer(); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	for _, content := range []string{"short", strings.Repeat("x", 4097), "secret-with\nembedded-newline-123456"} {
		t.Run("secret validation", func(t *testing.T) {
			path := previewEnv(t)
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadServer(); err == nil || strings.Contains(err.Error(), content) || strings.Contains(err.Error(), path) {
				t.Fatal("unsafe secret accepted or leaked")
			}
		})
	}
}
