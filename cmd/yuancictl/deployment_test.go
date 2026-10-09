package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type recoverySpy struct {
	calls  int
	job    uuid.UUID
	reason string
	err    error
}

func (s *recoverySpy) ConfirmDeploymentStopped(_ context.Context, job uuid.UUID, reason string) error {
	s.calls++
	s.job = job
	s.reason = reason
	return s.err
}

func TestDeploymentInvalidOptionsDoNotOpenDatabase(t *testing.T) {
	id := uuid.NewString()
	for _, args := range [][]string{
		{"deployment"}, {"deployment", "other"},
		{"deployment", "confirm-stopped", "-job", id, "-reason", "verified"},
		{"deployment", "confirm-stopped", "-job", id, "-verified=false", "-reason", "verified"},
		{"deployment", "confirm-stopped", "-job", "bad", "-verified", "-reason", "verified"},
		{"deployment", "confirm-stopped", "-job", uuid.Nil.String(), "-verified", "-reason", "verified"},
		{"deployment", "confirm-stopped", "-job", id, "-verified", "-reason", "  "},
		{"deployment", "confirm-stopped", "-job", id, "-verified", "-reason", strings.Repeat("x", 1025)},
		{"deployment", "confirm-stopped", "-job", id, "-verified", "-reason", "verified", "extra"},
		{"deployment", "confirm-stopped", "-unknown"},
	} {
		handled, err := deploymentCommandWithStore(args, &bytes.Buffer{}, func(string) string { t.Fatal("read DB environment before validation"); return "" }, func(context.Context, string) (deploymentRecoveryStore, func(), error) {
			t.Fatal("opened database for invalid options")
			return nil, nil, nil
		})
		if !handled || err == nil {
			t.Fatalf("accepted invalid options: %v", args)
		}
	}
}

func TestDeploymentConfirmationDispatchAndSafeErrors(t *testing.T) {
	id := uuid.New()
	args := []string{"deployment", "confirm-stopped", "-job", id.String(), "-verified", "-reason", "  host inspected  "}
	for _, failure := range []error{nil, errors.New("postgres://secret@host")} {
		spy := &recoverySpy{err: failure}
		closed := false
		var out bytes.Buffer
		handled, err := deploymentCommandWithStore(args, &out, func(key string) string {
			if key != "YUANCI_DATABASE_URL" {
				t.Fatal(key)
			}
			return "postgres://secret@host"
		}, func(context.Context, string) (deploymentRecoveryStore, func(), error) {
			return spy, func() { closed = true }, nil
		})
		if !handled || spy.calls != 1 || spy.job != id || spy.reason != "host inspected" || !closed {
			t.Fatalf("dispatch: %#v %v", spy, err)
		}
		if (err != nil) != (failure != nil) {
			t.Fatalf("error: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Fatal("database error leaked")
		}
	}
	_, err := deploymentCommandWithStore(args, &bytes.Buffer{}, func(string) string { return "" }, func(context.Context, string) (deploymentRecoveryStore, func(), error) {
		t.Fatal("opened without DB URL")
		return nil, nil, nil
	})
	if err == nil {
		t.Fatal("missing database URL accepted")
	}
}
