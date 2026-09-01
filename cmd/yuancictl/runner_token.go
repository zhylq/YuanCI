package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/runnerauth"
	"github.com/yuanci/yuanci/internal/store/postgres"
)

type registrationTokenStore interface {
	CreateRegistrationToken(context.Context, runnerauth.RegistrationToken) error
}

func runnerTokenCommand(args []string, out io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "runner-token" {
		return false, nil
	}
	if len(args) < 2 || args[1] != "issue" {
		return true, errors.New("usage: yuancictl runner-token issue -pool NAME -file NEW_FILE [-ttl 10m] [-uses 1]")
	}
	flags := flag.NewFlagSet("runner-token issue", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	poolName := flags.String("pool", "", "Runner pool name")
	outputPath := flags.String("file", "", "new token file")
	ttl := flags.Duration("ttl", runnerauth.DefaultRegistrationTTL, "token lifetime")
	uses := flags.Int("uses", 1, "maximum registrations")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *poolName == "" || *outputPath == "" {
		return true, errors.New("usage: yuancictl runner-token issue -pool NAME -file NEW_FILE [-ttl 10m] [-uses 1]")
	}
	databaseURL := os.Getenv("YUANCI_DATABASE_URL")
	if databaseURL == "" {
		return true, errors.New("YUANCI_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return true, errors.New("Runner registration database unavailable")
	}
	defer store.Close()
	if err := issueRunnerToken(ctx, store, *poolName, *outputPath, *ttl, *uses); err != nil {
		return true, err
	}
	_, err = fmt.Fprintln(out, "Runner registration token created in the requested file. Transfer it securely and delete it after enrollment.")
	return true, err
}

func issueRunnerToken(ctx context.Context, store registrationTokenStore, poolName, outputPath string, ttl time.Duration, uses int) (err error) {
	if store == nil || poolName == "" || outputPath == "" || ttl < time.Minute || ttl > runnerauth.MaximumRegistrationTTL || uses < 1 || uses > 256 {
		return errors.New("invalid Runner registration token options")
	}
	token, digest, err := runnerauth.NewRegistrationToken()
	if err != nil {
		return err
	}
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create token file; parent must exist and target must not exist")
	}
	created := true
	defer func() {
		_ = file.Close()
		if err != nil && created {
			_ = os.Remove(outputPath)
		}
	}()
	if _, err = io.WriteString(file, token+"\n"); err != nil {
		return errors.New("cannot persist Runner registration token")
	}
	if err = file.Sync(); err != nil {
		return errors.New("cannot persist Runner registration token")
	}
	if err = store.CreateRegistrationToken(ctx, runnerauth.RegistrationToken{ID: uuid.New(), PoolName: poolName,
		Digest: digest, ExpiresAt: time.Now().UTC().Add(ttl), MaxUses: uses}); err != nil {
		return errors.New("cannot issue Runner registration token")
	}
	created = false
	return nil
}
