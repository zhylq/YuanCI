package postgres

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped DockerExecutor invokes this test binary at the daemon boundary.
// It performs real Git and shell work inside one owned temporary directory,
// while checking the credential, exact SHA and no-raw-log command contract.
// It does not claim to emulate Docker isolation or GitHub's network service.
func TestMain(m *testing.M) {
	if root := os.Getenv("YUANCI_E2E_DOCKER_ROOT"); root != "" && len(os.Args) > 1 {
		switch os.Args[1] {
		case "volume", "network", "container", "run":
			if err := e2eDockerCommand(root, os.Args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "isolated executor fixture failed")
				os.Exit(42)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func e2eDockerCommand(root string, args []string) error {
	absolute, err := filepath.Abs(root)
	if err != nil || absolute != filepath.Clean(root) {
		return fmt.Errorf("invalid fixture root")
	}
	workspace := filepath.Join(absolute, "workspace")
	if !strings.HasPrefix(workspace, absolute+string(os.PathSeparator)) {
		return fmt.Errorf("invalid workspace")
	}
	switch args[0] {
	case "network", "container":
		return nil
	case "volume":
		if len(args) < 2 {
			return fmt.Errorf("volume action missing")
		}
		if args[1] == "rm" {
			return os.RemoveAll(workspace)
		}
		return nil
	case "run":
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--log-driver none") || strings.Contains(joined, e2eCheckoutToken) {
			return fmt.Errorf("unsafe command")
		}
		if strings.Contains(joined, "yuanci-checkout") {
			input, err := io.ReadAll(io.LimitReader(os.Stdin, 16385))
			if err != nil || len(input) > 16384 {
				return fmt.Errorf("invalid checkout input")
			}
			defer clear(input)
			if !bytes.Contains(input, []byte("Authorization: Bearer "+e2eCheckoutToken)) || args[len(args)-2] != "https://github.com/team/safe.git" || args[len(args)-1] != os.Getenv("YUANCI_E2E_SOURCE_SHA") {
				return fmt.Errorf("private checkout denied")
			}
			if err := exec.Command("git", "-c", "core.hooksPath=/dev/null", "clone", "--quiet", "--no-hardlinks", filepath.Join(root, "repository"), workspace).Run(); err != nil {
				return err
			}
			sha := args[len(args)-1]
			if err := exec.Command("git", "-C", workspace, "-c", "core.hooksPath=/dev/null", "checkout", "--quiet", "--detach", sha).Run(); err != nil {
				return err
			}
			head, err := exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
			if err != nil || strings.TrimSpace(string(head)) != sha {
				return fmt.Errorf("checkout SHA mismatch")
			}
			return nil
		}
		command := exec.Command("sh", "-euc", args[len(args)-1])
		command.Dir = workspace
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			return err
		}
		// Intentional synthetic output proves the real log boundary redacts leaks.
		_, err := fmt.Fprintln(os.Stdout, e2eCheckoutToken)
		return err
	}
	return fmt.Errorf("unknown fixture command")
}
