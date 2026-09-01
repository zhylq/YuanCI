package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yuanci/yuanci/internal/runnerauth"
)

type tokenStoreStub struct {
	record runnerauth.RegistrationToken
	err    error
}

func (store *tokenStoreStub) CreateRegistrationToken(_ context.Context, record runnerauth.RegistrationToken) error {
	store.record = record
	return store.err
}

func TestIssueRunnerTokenWritesExclusiveFileAndPersistsOnlyDigest(t *testing.T) {
	store := &tokenStoreStub{}
	path := filepath.Join(t.TempDir(), "runner-token")
	if err := issueRunnerToken(t.Context(), store, "standard", path, 10*time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(content))
	digest, err := runnerauth.TokenDigest(token)
	if err != nil || digest != store.record.Digest || store.record.PoolName != "standard" || store.record.MaxUses != 1 {
		t.Fatal("plaintext token and persisted digest do not match")
	}
	if bytes.Contains(store.record.Digest[:], []byte(token)) {
		t.Fatal("plaintext persisted")
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		t.Fatalf("token file permissions are not private: %v %v", info, err)
	}
	before := append([]byte(nil), content...)
	if err := issueRunnerToken(t.Context(), store, "standard", path, 10*time.Minute, 1); err == nil {
		t.Fatal("existing token file overwritten")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("existing token file changed")
	}
}

func TestIssueRunnerTokenRemovesFileWhenDatabaseCommitFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner-token")
	store := &tokenStoreStub{err: errors.New("injected database failure")}
	if err := issueRunnerToken(t.Context(), store, "standard", path, 10*time.Minute, 1); err == nil {
		t.Fatal("database failure ignored")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unusable plaintext token file retained")
	}
}

func TestRunnerTokenCommandUsageDoesNotClaimOtherCommands(t *testing.T) {
	if handled, err := runnerTokenCommand([]string{"validate"}, &bytes.Buffer{}); handled || err != nil {
		t.Fatal("Runner token handler claimed another command")
	}
	for _, args := range [][]string{{"runner-token"}, {"runner-token", "unknown"}, {"runner-token", "issue"}} {
		if handled, err := runnerTokenCommand(args, &bytes.Buffer{}); !handled || err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}
