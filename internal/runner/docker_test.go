package runner

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

func TestDockerArgsApplySecurityDefaults(t *testing.T) {
	args := buildDockerArgs("workspace", uuid.MustParse("a120348a-b47b-4e92-91a4-5d2e266dc680"), 0, "alpine:3.21",
		pipeline.PlanJob{Resources: pipeline.Resources{CPU: "2", Memory: "1Gi"}},
		pipeline.Step{Name: "test", Commands: []string{"echo ok"}})
	joined := strings.Join(args, " ")
	for _, expected := range []string{"--cap-drop ALL", "no-new-privileges", "--read-only", "--pids-limit 256", "--cpus 2", "--memory 1Gi"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("expected %q in %q", expected, joined)
		}
	}
}
