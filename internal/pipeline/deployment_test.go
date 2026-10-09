package pipeline

import (
	"strings"
	"testing"
	"time"
)

func TestDeploymentCompilation(t *testing.T) {
	source := "version: v1\nname: deploy\ndeployment:\n  environment: production\nstages:\n  - name: deploy\n    jobs:\n      - name: commands\n        image: alpine\n        steps:\n          - name: deploy\n            commands: [echo deploy]\n"
	plan, err := Compile([]byte(source), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Deployment == nil || plan.Deployment.Environment != "production" || plan.Stages[0].Jobs[0].Deployment != "production" {
		t.Fatalf("missing deployment metadata: %#v", plan)
	}
	if err := ValidatePlanDeployment(plan); err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{"environment: ''", "environment: invalid/name"} {
		if _, err := Compile([]byte(strings.Replace(source, "environment: production", replacement, 1)), time.Now()); err == nil {
			t.Fatal("invalid environment accepted")
		}
	}
	for _, addition := range []string{"        retry: 1\n", ""} {
		bad := strings.Replace(source, "        image: alpine\n", "        image: alpine\n"+addition, 1)
		if addition == "" {
			bad += "concurrency:\n  group: deploy\n  cancel_previous: true\n"
		}
		if _, err := Compile([]byte(bad), time.Now()); err == nil {
			t.Fatal("unsafe deployment scheduling accepted")
		}
	}
	if _, err := Compile([]byte(source+"concurrency:\n  group: deploy\n  limit: 2\n"), time.Now()); err == nil {
		t.Fatal("parallel deployment limit accepted")
	}
	standard := strings.Replace(source, "deployment:\n  environment: production\n", "", 1)
	standard = strings.Replace(standard, "        image: alpine\n", "        image: alpine\n        retry: 1\n", 1)
	if _, err := Compile([]byte(standard+"concurrency:\n  group: tests\n  cancel_previous: true\n  limit: 2\n"), time.Now()); err != nil {
		t.Fatalf("ordinary scheduling changed: %v", err)
	}
	plan.Stages[0].Jobs[0].Deployment = "other"
	if ValidatePlanDeployment(plan) == nil {
		t.Fatal("inconsistent marker accepted")
	}
	plan.Deployment = nil
	if ValidatePlanDeployment(plan) == nil {
		t.Fatal("orphan marker accepted")
	}
	if DeploymentRepositoryLabel("gitee", "70") != "yuanci.deploy.gitee.70" {
		t.Fatal("wrong repository label")
	}
}
