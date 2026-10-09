package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yuanci/yuanci/internal/pipeline"
)

func TestDeploymentPolicyStrictBoundedAndOutsideWorkspace(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "policy.json")
	write := func(body string) {
		t.Helper()
		_ = os.Chmod(filename, 0600)
		if err := os.WriteFile(filename, []byte(body), 0400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filename, 0400); err != nil {
			t.Fatal(err)
		}
	}
	valid := `{"repositories":[{"provider":"gitee","external_id":"70"}]}`
	write(valid)
	policy, err := LoadDeploymentPolicy(filename)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Labels()[pipeline.DeploymentRepositoryLabel("gitee", "70")] != "true" {
		t.Fatal("missing trusted grant")
	}
	for _, body := range []string{`{"repositories":[]}`, `{"repositories":[{"provider":"github","external_id":"070"}]}`, `{"repositories":[{"provider":"gitlab","external_id":"70"}]}`, valid + `{}`, `{"repositories":[{"provider":"gitee","external_id":"70"}],"secrets":{"token":"x"}}`} {
		write(body)
		if _, err := LoadDeploymentPolicy(filename); err == nil {
			t.Fatalf("invalid policy accepted: %s", body)
		}
	}
	for _, target := range []string{"/", "/workspace", "/workspace/project", "/proc", "/dev/socket", "/sys", "/tmp", "/deploy,readonly", "/deploy:rw", "/a/../workspace", "relative"} {
		body, _ := json.Marshal(map[string]any{"repositories": []map[string]string{{"provider": "gitee", "external_id": "70"}}, "host_mounts": []DeploymentMount{{Source: t.TempDir(), Target: target}}})
		write(string(body))
		if _, err := LoadDeploymentPolicy(filename); err == nil {
			t.Fatalf("unsafe mount target accepted: %s", target)
		}
	}
	write(valid)
	if _, err := LoadDeploymentPolicy("policy.json"); err == nil {
		t.Fatal("relative administrator policy accepted")
	}
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(working, "deployment-policy-test.json")
	if err := os.WriteFile(local, []byte(valid), 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(local)
	if _, err := LoadDeploymentPolicy(local); err == nil {
		t.Fatal("workspace policy accepted")
	}
}

func TestDeploymentPolicyLoadsWhenRunnerStartsAtFilesystemRoot(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(filename, []byte(`{"repositories":[{"provider":"gitee","external_id":"70"}]}`), 0400); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.VolumeName(filename) + string(filepath.Separator))
	if _, err := LoadDeploymentPolicy(filename); err != nil {
		t.Fatalf("nonrepository root was treated as checkout workspace: %v", err)
	}
}
