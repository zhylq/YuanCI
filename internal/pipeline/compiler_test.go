package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCompilePreservesTriggers(t *testing.T) {
	source := strings.Replace(validPipeline, "  - event: push", "  - event: push\n    branches: [main, release/stable]", 1)
	plan, err := Compile([]byte(source), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		Triggers []Trigger `json:"triggers"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Triggers) != 1 || persisted.Triggers[0].Event != "push" || len(persisted.Triggers[0].Branches) != 2 || persisted.Triggers[0].Branches[1] != "release/stable" {
		t.Fatalf("compiled plan discarded trigger policy: %s", data)
	}
}

func TestValidateRejectsUnsupportedTriggerFilters(t *testing.T) {
	base, err := Parse([]byte(validPipeline))
	if err != nil {
		t.Fatal(err)
	}
	for name, trigger := range map[string]Trigger{
		"paths":                {Event: "push", Paths: []string{"src/**"}},
		"glob":                 {Event: "push", Branches: []string{"release/*"}},
		"qualified branch":     {Event: "push", Branches: []string{"refs/heads/main"}},
		"empty branch":         {Event: "push", Branches: []string{""}},
		"malformed branch":     {Event: "push", Branches: []string{"main//next"}},
		"lock branch":          {Event: "push", Branches: []string{"main.lock"}},
		"hidden component":     {Event: "push", Branches: []string{"release/.hidden"}},
		"tag branch filter":    {Event: "tag", Branches: []string{"main"}},
		"manual branch filter": {Event: "manual", Branches: []string{"main"}},
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			value.Triggers = []Trigger{trigger}
			if err := Validate(value); err == nil || !strings.Contains(err.Error(), "triggers[0]") {
				t.Fatalf("unsupported trigger filter accepted: %v", err)
			}
		})
	}
}

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

func TestCompileServices(t *testing.T) {
	base, err := Parse([]byte(validPipeline))
	if err != nil {
		t.Fatal(err)
	}
	for name, services := range map[string][]Service{
		"duplicate alias":     {{Name: "db", Image: "postgres:17"}, {Name: "DB", Image: "redis:7"}},
		"invalid alias":       {{Name: "bad alias", Image: "redis:7"}},
		"empty image":         {{Name: "db"}},
		"option image":        {{Name: "db", Image: "--privileged"}},
		"invalid environment": {{Name: "db", Image: "redis:7", Environment: map[string]string{"BAD=KEY": "value"}}},
		"nul environment":     {{Name: "db", Image: "redis:7", Environment: map[string]string{"KEY": "bad\x00value"}}},
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			value.Stages = append([]Stage(nil), base.Stages...)
			value.Stages[0].Jobs = append([]Job(nil), base.Stages[0].Jobs...)
			value.Stages[0].Jobs[0].Services = services
			if err := Validate(value); err == nil || !strings.Contains(err.Error(), "services") {
				t.Fatalf("unsafe services accepted: %v", err)
			}
		})
	}
	base.Stages[0].Jobs[0].Services = []Service{{Name: "db", Image: "postgres:17", Environment: map[string]string{"POSTGRES_DB": "test"}}}
	if err := Validate(base); err != nil {
		t.Fatal(err)
	}
	base.Stages[0].Jobs[0].Services = make([]Service, 17)
	if err := Validate(base); err == nil {
		t.Fatal("unbounded service list accepted")
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

func TestCompileRejectsAmbiguousTriggerYAML(t *testing.T) {
	for name, source := range map[string]string{
		"multiple documents": validPipeline + "\n---\ntriggers: [{event: tag}]\n",
		"root merge":         strings.Replace(validPipeline, "version: v1", "<<: {version: v1}", 1),
		"trigger aliases":    strings.Replace(validPipeline, "  - event: push", "  - event: push\n    branches: &branches [main]\n  - event: pull_request\n    branches: *branches", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile([]byte(source), time.Now()); err == nil {
				t.Fatal("ambiguous YAML compiled to an executable plan")
			}
		})
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
