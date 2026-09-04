package runner

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const checkoutHelperImage = "alpine/git:v2.47.2"

const checkoutScript = `set -eu
config=/run/yuanci/checkout.gitconfig
trap 'rm -f "$config"' EXIT HUP INT TERM
umask 077
cat >"$config"
git -c include.path="$config" init --quiet /workspace
git -C /workspace -c include.path="$config" remote add origin "$1"
git -C /workspace -c include.path="$config" fetch --quiet --no-tags --depth=1 origin "$2"
git -C /workspace -c include.path="$config" checkout --quiet --detach FETCH_HEAD
test "$(git -C /workspace -c include.path="$config" rev-parse HEAD)" = "$2"
`

type checkoutCommand struct {
	args        []string
	environment []string
}

func buildCheckoutCommand(volume, network, helperName string, source *localSource) (checkoutCommand, []byte, error) {
	if source == nil || volume == "" || network == "" || helperName == "" || source.cloneURL == "" || source.commitSHA == "" {
		return checkoutCommand{}, nil, errors.New("invalid checkout request")
	}
	if len(source.credential) == 0 || !utf8.Valid(source.credential) {
		return checkoutCommand{}, nil, errors.New("invalid checkout credential")
	}
	for _, value := range source.credential {
		if value < 0x20 || value > 0x7e {
			return checkoutCommand{}, nil, errors.New("invalid checkout credential")
		}
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(string(source.credential))
	var input bytes.Buffer
	_, _ = fmt.Fprintf(&input, `[http]
	extraHeader = Authorization: Bearer %s
	followRedirects = false
[credential]
	helper =
	useHttpPath = true
[core]
	hooksPath = /dev/null
[protocol]
	allow = never
	version = 0
[protocol "https"]
	allow = always
[submodule]
	recurse = false
[filter "lfs"]
	clean = cat
	smudge = cat
	process =
	required = true
`, escaped)
	environment := []string{"GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "GIT_CONFIG_NOSYSTEM=1"}
	args := []string{"run", "--rm", "--name", helperName, "--network", network, "--log-driver", "none",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128",
		"--read-only", "--tmpfs", "/run/yuanci:rw,nosuid,nodev,noexec,size=65536",
		"--volume", volume + ":/workspace", "--workdir", "/workspace"}
	for _, value := range environment {
		args = append(args, "--env", value)
	}
	args = append(args, checkoutHelperImage, "sh", "-euc", checkoutScript, "yuanci-checkout", source.cloneURL, source.commitSHA)
	return checkoutCommand{args: args, environment: environment}, input.Bytes(), nil
}
