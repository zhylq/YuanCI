package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/store/postgres"
)

type deploymentRecoveryStore interface {
	ConfirmDeploymentStopped(context.Context, uuid.UUID, string) error
}

type deploymentRecoveryOpener func(context.Context, string) (deploymentRecoveryStore, func(), error)

func deploymentCommand(args []string, out io.Writer) (bool, error) {
	return deploymentCommandWithStore(args, out, os.Getenv, func(ctx context.Context, url string) (deploymentRecoveryStore, func(), error) {
		s, err := postgres.Open(ctx, url)
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil
	})
}

func deploymentCommandWithStore(args []string, out io.Writer, getenv func(string) string, open deploymentRecoveryOpener) (bool, error) {
	if len(args) == 0 || args[0] != "deployment" {
		return false, nil
	}
	usage := errors.New("usage: yuancictl deployment confirm-stopped -job UUID -verified -reason TEXT (reason: 1-1024 bytes; first stop the Runner and verify exact job resources and external operations have stopped)")
	if len(args) < 2 || args[1] != "confirm-stopped" {
		return true, usage
	}
	flags := flag.NewFlagSet("deployment confirm-stopped", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	job := flags.String("job", "", "exact started deployment job UUID")
	verified := flags.Bool("verified", false, "operator physically verified execution stopped")
	reason := flags.String("reason", "", "operator verification record (no credentials)")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
		return true, usage
	}
	id, err := uuid.Parse(*job)
	why := strings.TrimSpace(*reason)
	if err != nil || id == uuid.Nil || !*verified || why == "" || len(why) > postgres.MaxDeploymentRecoveryReasonBytes || !utf8.ValidString(why) {
		return true, usage
	}
	url := getenv("YUANCI_DATABASE_URL")
	if url == "" {
		return true, errors.New("YUANCI_DATABASE_URL is required (administrator database credential)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, closeStore, err := open(ctx, url)
	if err != nil {
		return true, errors.New("deployment recovery database unavailable")
	}
	defer closeStore()
	if err := s.ConfirmDeploymentStopped(ctx, id, why); err != nil {
		if errors.Is(err, postgres.ErrDeploymentRecoveryDenied) {
			return true, postgres.ErrDeploymentRecoveryDenied
		}
		return true, errors.New("deployment cleanup confirmation failed; inspect job cleanup and audit state before repeating")
	}
	_, err = fmt.Fprintln(out, "Deployment job cleanup confirmed. The environment releases only when every job is terminal and all started jobs have confirmed cleanup.")
	return true, err
}
