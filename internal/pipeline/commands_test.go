package pipeline

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

const commandsPipeline = `version: v1
name: simple
triggers: [{event: push, branches: [main]}]
deployment: {environment: production}
stages:
  - name: deploy
    jobs:
      - name: release
        image: alpine:3.21
        environment: {TARGET: production}
        commands: ["printf 'hello\\n'"]
`

func TestCommandsCompileEquivalentSteps(t *testing.T) {
	explicit := strings.Replace(commandsPipeline, `        commands: ["printf 'hello\\n'"]`, "        steps:\n          - name: commands\n            commands: [\"printf 'hello\\\\n'\"]", 1)
	when := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	shorthand, err := Compile([]byte(commandsPipeline), when)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := Compile([]byte(explicit), when)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(shorthand, steps) {
		t.Fatalf("shorthand differs from explicit steps:\n%+v\n%+v", shorthand, steps)
	}
	job := shorthand.Stages[0].Jobs[0]
	if len(job.Steps) != 1 || job.Steps[0].Name != "commands" || !reflect.DeepEqual(job.Steps[0].Commands, []string{"printf 'hello\\n'"}) {
		t.Fatalf("unexpected commands step: %+v", job.Steps)
	}
	if shorthand.Deployment.Environment != "production" || job.Deployment != "production" || job.Image != "alpine:3.21" || job.Environment["TARGET"] != "production" || job.Timeout != 30*time.Minute || job.Resources != (Resources{}) || job.RunsOn.OS != "linux" || job.RunsOn.Executor != "docker" || job.RequiredDiskBytes != 0 {
		t.Fatalf("job metadata/defaults changed: %+v", job)
	}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["commands"]; exists {
		t.Fatalf("compiled job exposes raw commands: %s", data)
	}
}

func TestCommandsRejectInvalidForms(t *testing.T) {
	for name, form := range map[string]string{
		"both":                      "commands: [true]\n        steps: [{name: run, commands: [true]}]",
		"empty steps with commands": "commands: [true]\n        steps: []",
		"empty commands with steps": "commands: []\n        steps: [{name: run, commands: [true]}]",
		"both empty":                "commands: []\n        steps: []",
		"empty commands":            "commands: []",
		"missing execution":         "commands: null",
		"neither form":              "",
		"scalar commands":           "commands: true",
		"mapping commands":          "commands: {script: true}",
	} {
		t.Run(name, func(t *testing.T) {
			source := strings.Replace(commandsPipeline, `commands: ["printf 'hello\\n'"]`, form, 1)
			_, err := Compile([]byte(source), time.Time{})
			if err == nil {
				t.Fatal("invalid execution form accepted")
			}
			if strings.Contains(form, "steps:") && !strings.Contains(err.Error(), "mutually exclusive") {
				t.Fatalf("expected execution form conflict, got %v", err)
			}
		})
	}
	for name, source := range map[string]string{
		"missing image":             strings.Replace(commandsPipeline, "        image: alpine:3.21\n", "", 1),
		"unknown job field":         strings.Replace(commandsPipeline, "        image:", "        unknown: true\n        image:", 1),
		"unknown nested field":      strings.Replace(commandsPipeline, "        image:", "        runs_on: {unknown: true}\n        image:", 1),
		"unknown merged job field":  strings.Replace(commandsPipeline, "        image:", "        <<: {unknown: true}\n        image:", 1),
		"merged execution conflict": strings.Replace(commandsPipeline, "        image:", "        <<: {steps: []}\n        image:", 1),
		"trigger alias":             strings.Replace(commandsPipeline, "branches: [main]", "branches: [&branch main, *branch]", 1),
		"unsupported trigger":       strings.Replace(commandsPipeline, "branches: [main]", "branches: ['release/*']", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile([]byte(source), time.Time{}); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestCommandsNullAndValidationDoNotMutate(t *testing.T) {
	for _, source := range []string{
		commandsPipeline,
		strings.Replace(commandsPipeline, "        commands:", "        steps: null\n        commands:", 1),
		strings.Replace(commandsPipeline, `        commands: ["printf 'hello\\n'"]`, "        commands: null\n        steps: [{name: run, commands: [true]}]", 1),
	} {
		value, err := Parse([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		before, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(value); err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("Validate mutated pipeline: %s => %s", before, after)
		}
		if _, err := Compile([]byte(source), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommandsPreserveJobOptionsAndExecutableYAMLReuse(t *testing.T) {
	source := strings.Replace(commandsPipeline, "        image: alpine:3.21", `        <<: {image: alpine:3.21}
        timeout: 2m
        resources: {cpu: "2", memory: 512MiB, disk: 2GiB, pids: 64}
        runs_on: {architecture: amd64, labels: {region/cn: east}}
        secrets: [TOKEN]
        services: [{name: db, image: postgres:17}]
        matrix: {variant: [one, two]}`, 1)
	source = strings.Replace(source, `commands: ["printf 'hello\\n'"]`, `commands: &script ["printf 'hello\\n'"]
      - name: second
        <<: {image: alpine:3.21, commands: *script}`, 1)
	plan, err := Compile([]byte(source), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	job := plan.Stages[0].Jobs[0]
	if job.Timeout != 2*time.Minute || job.Resources.CPU != "2" || job.Resources.Memory != "512MiB" || job.Resources.PIDs != 64 || job.RequiredDiskBytes != 2*(1<<30) || job.RunsOn.Architecture != "amd64" || job.RunsOn.Labels["region/cn"] != "east" || len(job.Services) != 1 || job.Secrets[0] != "TOKEN" || len(job.Matrix["variant"]) != 2 {
		t.Fatalf("options lost: %+v", job)
	}
	if !reflect.DeepEqual(job.Steps, plan.Stages[0].Jobs[1].Steps) {
		t.Fatal("aliased merged commands changed")
	}
	legacy, err := Compile([]byte(validPipeline), time.Time{})
	if err != nil || legacy.Stages[0].Jobs[0].Steps[0].Name != "test" {
		t.Fatalf("existing explicit steps changed: %+v %v", legacy, err)
	}
}
