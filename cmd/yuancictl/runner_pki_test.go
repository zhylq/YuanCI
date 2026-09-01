package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerPKICommandCreatesBundleWithoutPrintingSecrets(t *testing.T) {
	output := filepath.Join(t.TempDir(), "pki")
	var stdout bytes.Buffer
	handled, err := runnerPKICommand([]string{
		"runner-pki", "init", "-dir", output,
		"-server-name", "server", "-server-name", "127.0.0.1",
	}, &stdout)
	if !handled || err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(output, "offline-root", "root-key.pem"),
		filepath.Join(output, "server", "server-chain.pem"),
		filepath.Join(output, "server", "manifest.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	text := stdout.String()
	if !strings.Contains(text, "Move") || !strings.Contains(text, "Mount only") || !strings.Contains(text, "Root fingerprint") {
		t.Fatalf("missing operator guidance: %s", text)
	}
	for _, secretMarker := range []string{"BEGIN PRIVATE KEY", "BEGIN CERTIFICATE", "intermediate-key.pem", "server-key.pem", "root-key.pem"} {
		if strings.Contains(text, secretMarker) {
			t.Fatalf("command output disclosed key material/path marker %q", secretMarker)
		}
	}
	if _, err := runnerPKICommand([]string{"runner-pki", "init", "-dir", output, "-server-name", "server"}, &stdout); err == nil {
		t.Fatal("existing PKI was overwritten")
	}
}

func TestRunnerPKICommandUsage(t *testing.T) {
	for _, arguments := range [][]string{
		{"runner-pki"},
		{"runner-pki", "unknown"},
		{"runner-pki", "init"},
		{"runner-pki", "init", "-dir", filepath.Join(t.TempDir(), "pki")},
	} {
		if handled, err := runnerPKICommand(arguments, &bytes.Buffer{}); !handled || err == nil {
			t.Fatalf("invalid arguments accepted: %v", arguments)
		}
	}
	if handled, err := runnerPKICommand([]string{"validate"}, &bytes.Buffer{}); handled || err != nil {
		t.Fatal("Runner PKI handler claimed another command")
	}
}
