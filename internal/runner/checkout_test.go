package runner

import (
	"bytes"
	"strings"
	"testing"
)

func TestCheckoutCommandKeepsCredentialOutOfArgsAndEnvironment(t *testing.T) {
	token := []byte("github_pat_secret-value")
	command, input, err := buildCheckoutCommand("workspace", "network", "helper", &localSource{
		cloneURL:  "https://github.com/example/repository.git",
		commitSHA: strings.Repeat("a", 40), credential: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(append(command.args, command.environment...), "\x00")
	if strings.Contains(joined, string(token)) {
		t.Fatal("checkout credential entered argv or environment")
	}
	if !bytes.Contains(input, token) {
		t.Fatal("checkout credential was not provided on stdin")
	}
	for _, expected := range []string{
		"--read-only", "--tmpfs", "/run/yuanci:rw,nosuid,nodev,noexec,size=65536",
		"--env", "GIT_TERMINAL_PROMPT=0", "--env", "GIT_LFS_SKIP_SMUDGE=1",
		"--volume", "workspace:/workspace", "--network", "network",
	} {
		if !strings.Contains(strings.Join(command.args, " "), expected) {
			t.Fatalf("checkout command is missing %q: %q", expected, command.args)
		}
	}
	config := string(input)
	for _, expected := range []string{
		"extraHeader = Authorization: Bearer github_pat_secret-value",
		"hooksPath = /dev/null", "followRedirects = false", "allow = never",
		"helper =", "required = true", "recurse = false", "smudge = cat",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("checkout input is missing %q", expected)
		}
	}
}

func TestCheckoutCommandRejectsUnsafeCredentialBytes(t *testing.T) {
	for _, token := range [][]byte{nil, []byte("line\nbreak"), []byte{0xff}} {
		_, _, err := buildCheckoutCommand("workspace", "network", "helper", &localSource{
			cloneURL:  "https://github.com/example/repository.git",
			commitSHA: strings.Repeat("a", 40), credential: token,
		})
		if err == nil {
			t.Fatalf("credential %q was accepted", token)
		}
	}
}
