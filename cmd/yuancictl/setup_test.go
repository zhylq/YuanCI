package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuanci/yuanci/internal/secrets"
)

func TestMasterKeyDoesNotOverwriteOrPrintSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master-key")
	var out bytes.Buffer
	if handled, err := adminCommand([]string{"master-key", "-file", path}, &out); !handled || err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.ParseMasterKey(strings.TrimSpace(string(value))); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), strings.TrimSpace(string(value))) {
		t.Fatal("key printed")
	}
	if _, err := adminCommand([]string{"master-key", "-file", path}, &out); err == nil {
		t.Fatal("existing key overwritten")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(value, after) {
		t.Fatal("key changed")
	}
}
