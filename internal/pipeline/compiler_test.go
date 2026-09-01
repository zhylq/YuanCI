package pipeline

import (
	"strings"
	"testing"
	"time"
)

const validPipeline = `version: v1
name: verify
triggers:
  - event: push
stages:
  - name: test
    jobs:
      - name: unit
        image: golang:1.27
        timeout: 10m
        steps:
          - name: test
            commands: ["go test ./..."]
  - name: package
    depends_on: [test]
    jobs:
      - name: image
        image: docker:28-cli
        steps:
          - name: build
            commands: ["docker build ."]
`

func TestCompileValidPipeline(t *testing.T) {
	when := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	plan, err := Compile([]byte(validPipeline), when)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if plan.Name != "verify" || len(plan.Stages) != 2 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if len(plan.ConfigSHA256) != 64 {
		t.Fatalf("expected sha256, got %q", plan.ConfigSHA256)
	}
	if plan.Stages[0].Jobs[0].Timeout != 10*time.Minute {
		t.Fatalf("unexpected timeout")
	}
}

func TestCompileNormalizesRunnerRequirementsAndDisk(t *testing.T) {
	source := strings.Replace(validPipeline, "        timeout: 10m", `        timeout: 10m
        runs_on:
          architecture: amd64
          labels: {region/cn: east}
        resources:
          disk: 2GiB`, 1)
	plan, err := Compile([]byte(source), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	job := plan.Stages[0].Jobs[0]
	if job.RunsOn.OS != "linux" || job.RunsOn.Executor != "docker" || job.RunsOn.Architecture != "amd64" ||
		job.RunsOn.Labels["region/cn"] != "east" || job.RequiredDiskBytes != 2*(1<<30) {
		t.Fatalf("unexpected Runner requirements: %#v", job)
	}
}

func TestCompileRejectsInvalidRunnerRequirements(t *testing.T) {
	for name, fragment := range map[string]string{
		"disk":  "resources: {disk: 12XB}",
		"label": "runs_on: {labels: {'bad label': value}}",
	} {
		t.Run(name, func(t *testing.T) {
			source := strings.Replace(validPipeline, "        timeout: 10m", "        timeout: 10m\n        "+fragment, 1)
			if _, err := Compile([]byte(source), time.Now()); err == nil {
				t.Fatal("invalid Runner requirement accepted")
			}
		})
	}
}

func TestCompileRejectsUnknownFields(t *testing.T) {
	_, err := Compile([]byte(validPipeline+"unknown: true\n"), time.Now())
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestValidateRejectsDependencyCycle(t *testing.T) {
	source := `version: v1
name: cycle
stages:
  - name: first
    depends_on: [second]
    jobs:
      - name: one
        image: alpine
        steps: [{name: run, commands: ["true"]}]
  - name: second
    depends_on: [first]
    jobs:
      - name: two
        image: alpine
        steps: [{name: run, commands: ["true"]}]
`
	_, err := Compile([]byte(source), time.Now())
	if err == nil || !strings.Contains(err.Error(), "contains a cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}
