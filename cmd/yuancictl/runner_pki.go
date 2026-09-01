package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/yuanci/yuanci/internal/runnerauth"
)

type repeatedString []string

func (values *repeatedString) String() string { return "" }

func (values *repeatedString) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runnerPKICommand(args []string, out io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "runner-pki" {
		return false, nil
	}
	if len(args) < 2 || args[1] != "init" {
		return true, errors.New("usage: yuancictl runner-pki init -dir NEW_DIR -server-name DNS_OR_IP [-server-name DNS_OR_IP]")
	}
	flags := flag.NewFlagSet("runner-pki init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outputDir := flags.String("dir", "", "new PKI output directory")
	var serverNames repeatedString
	flags.Var(&serverNames, "server-name", "Server gRPC DNS name or IP; repeatable")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *outputDir == "" || len(serverNames) == 0 {
		return true, errors.New("usage: yuancictl runner-pki init -dir NEW_DIR -server-name DNS_OR_IP [-server-name DNS_OR_IP]")
	}
	manifest, err := runnerauth.InitializePKI(runnerauth.PKIOptions{OutputDir: *outputDir, ServerNames: serverNames})
	if err != nil {
		return true, err
	}
	absolute, _ := filepath.Abs(*outputDir)
	if _, err := fmt.Fprintf(out,
		"Runner PKI created.\nMove %s offline after backing it up securely.\nMount only %s read-only in yuanci-server.\nRoot fingerprint (SHA-256): %s\n",
		filepath.Join(absolute, "offline-root"), filepath.Join(absolute, "server"), manifest.Root.SHA256Fingerprint); err != nil {
		return true, err
	}
	return true, nil
}
