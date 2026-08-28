package postgres

import (
	"context"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
)

// newTestDatabase never connects to or cleans the application's database.
// Only a database created by this invocation is eligible for cleanup.
func newTestDatabase(t *testing.T) string {
	t.Helper()
	rawURL := os.Getenv("YUANCI_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set YUANCI_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("integration configuration must be a PostgreSQL URL")
	}
	config, err := pgx.ParseConfig(rawURL)
	if err != nil {
		t.Fatal("invalid integration database configuration")
	}
	if config.Database != "yuanci_ci" {
		t.Fatal("integration URL must point to a dedicated yuanci_ci database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot connect to dedicated integration PostgreSQL")
	}
	name := "yuanci_test_" + uuid.New().String()
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create isolated database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, "DROP DATABASE "+identifier); err != nil {
			t.Errorf("drop owned test database: %v", err)
		}
	})
	parsed.Path = "/" + name
	parsed.RawPath = ""
	query := parsed.Query()
	query.Del("database")
	query.Del("dbname")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestPostgresStoreContract(t *testing.T) {
	storetest.Exercise(t, func(t *testing.T) runmodel.Store {
		url := newTestDatabase(t)
		store, err := Open(t.Context(), url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(store.Close)
		return store
	})
}

func TestConcurrentMigrationsAndReopen(t *testing.T) {
	url := newTestDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			store, err := Open(ctx, url)
			if err == nil {
				store.Close()
			}
			errors <- err
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	store, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	record := storetest.Record(t, 1, false)
	if _, err := store.Create(ctx, record); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	reopened, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err := reopened.List(ctx, 20)
	if err != nil || len(items) != 1 || items[0].ID != record.ID {
		t.Fatalf("reopen lost run: %v", err)
	}
}

func TestExpiredLeaseCannotChangeJob(t *testing.T) {
	store, err := Open(t.Context(), newTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Create(t.Context(), storetest.Record(t, 1, false)); err != nil {
		t.Fatal(err)
	}
	a, err := store.ClaimJob(t.Context(), runmodel.ClaimRequest{RunnerName: "contract"})
	if err != nil || a == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE jobs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, a.JobID); err != nil {
		t.Fatal(err)
	}
	if err := store.StartJob(t.Context(), a.JobID, a.LeaseToken); !errors.Is(err, runmodel.ErrLeaseInvalid) {
		t.Fatalf("expired start accepted: %v", err)
	}
	if err := store.CompleteJob(t.Context(), a.JobID, a.LeaseToken, runmodel.JobSucceeded); !errors.Is(err, runmodel.ErrLeaseInvalid) {
		t.Fatalf("expired completion accepted: %v", err)
	}
}
