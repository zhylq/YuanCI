package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/yuanci/yuanci/internal/config"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/secrets"
	"github.com/yuanci/yuanci/internal/store/postgres"
)

func adminCommand(args []string, out io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "master-key":
		flags := flag.NewFlagSet("master-key", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		path := flags.String("file", "", "new key file path")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *path == "" {
			return true, errors.New("usage: yuancictl master-key -file NEW_FILE (never overwrites)")
		}
		key, err := secrets.GenerateMasterKey()
		if err != nil {
			return true, errors.New("cannot generate master key")
		}
		file, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return true, errors.New("cannot create key file; parent must exist and target must not exist")
		}
		defer file.Close()
		if _, err := io.WriteString(file, key+"\n"); err != nil {
			return true, errors.New("key write failed; inspect the new file before retrying")
		}
		if err := file.Sync(); err != nil {
			return true, errors.New("key sync failed; inspect the new file before retrying")
		}
		_, err = fmt.Fprintln(out, "Master key created. Back up this file securely; do not replace it after initialization.")
		return true, err
	case "setup-code":
		if len(args) != 1 {
			return true, errors.New("usage: yuancictl setup-code")
		}
		cfg, err := config.LoadServer()
		if err != nil || !cfg.ManagedSetup {
			return true, errors.New("setup-code requires valid managed-preview configuration")
		}
		defer clear(cfg.MasterKey)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		store, err := postgres.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return true, errors.New("setup database unavailable")
		}
		defer store.Close()
		if err := store.BindManagedMasterKey(ctx, cfg.MasterKey); err != nil {
			return true, errors.New("master key mismatch")
		}
		code := identity.NewToken()
		if err := store.IssueSetupCode(ctx, code); err != nil {
			return true, errors.New("cannot issue setup code: initialization may already be complete or file-configured")
		}
		_, err = fmt.Fprintln(out, code)
		return true, err
	}
	return false, nil
}
