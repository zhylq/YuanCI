package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

type ValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e ValidationError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	parts := make([]string, 0, len(e))
	for _, item := range e {
		parts = append(parts, item.Error())
	}
	return strings.Join(parts, "; ")
}

func Parse(source []byte) (Pipeline, error) {
	var value Pipeline
	decoder := yaml.NewDecoder(strings.NewReader(string(source)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		return Pipeline{}, fmt.Errorf("decode pipeline: %w", err)
	}
	return value, nil
}

func Validate(value Pipeline) error {
	var problems ValidationErrors
	if value.Version != APIVersion {
		problems = append(problems, ValidationError{"version", "must be v1"})
	}
	if !namePattern.MatchString(value.Name) {
		problems = append(problems, ValidationError{"name", "must be 1-63 letters, numbers, dots, underscores or hyphens"})
	}
	if len(value.Stages) == 0 {
		problems = append(problems, ValidationError{"stages", "must contain at least one stage"})
	}
	if value.Concurrency != nil {
		if value.Concurrency.Group == "" {
			problems = append(problems, ValidationError{"concurrency.group", "is required"})
		}
		if value.Concurrency.Limit < 0 || value.Concurrency.Limit > 100 {
			problems = append(problems, ValidationError{"concurrency.limit", "must be between 0 and 100"})
		}
	}

	stageNames := make(map[string]struct{}, len(value.Stages))
	for i, stage := range value.Stages {
		path := fmt.Sprintf("stages[%d]", i)
		if !namePattern.MatchString(stage.Name) {
			problems = append(problems, ValidationError{path + ".name", "is invalid"})
		} else if _, exists := stageNames[stage.Name]; exists {
			problems = append(problems, ValidationError{path + ".name", "must be unique"})
		}
		stageNames[stage.Name] = struct{}{}
		problems = append(problems, validateJobs(path, stage.Jobs)...)
	}
	problems = append(problems, validateDependencies("stages", stageNames, stageDependencies(value.Stages))...)

	allowedEvents := map[string]struct{}{"push": {}, "pull_request": {}, "tag": {}, "manual": {}, "cron": {}, "api": {}}
	for i, trigger := range value.Triggers {
		if _, ok := allowedEvents[trigger.Event]; !ok {
			problems = append(problems, ValidationError{fmt.Sprintf("triggers[%d].event", i), "is not supported"})
		}
	}

	if len(problems) > 0 {
		return problems
	}
	return nil
}

func Compile(source []byte, now time.Time) (Plan, error) {
	value, err := Parse(source)
	if err != nil {
		return Plan{}, err
	}
	if err := Validate(value); err != nil {
		return Plan{}, err
	}

	canonical, err := json.Marshal(value)
	if err != nil {
		return Plan{}, fmt.Errorf("canonicalize pipeline: %w", err)
	}
	digest := sha256.Sum256(canonical)
	plan := Plan{
		Version:      value.Version,
		Name:         value.Name,
		ConfigSHA256: hex.EncodeToString(digest[:]),
		CompiledAt:   now.UTC(),
		Stages:       make([]PlanStage, 0, len(value.Stages)),
	}
	for _, stage := range value.Stages {
		compiledStage := PlanStage{Name: stage.Name, DependsOn: stage.DependsOn, Jobs: make([]PlanJob, 0, len(stage.Jobs))}
		for _, job := range stage.Jobs {
			timeout, err := parseTimeout(job.Timeout)
			if err != nil {
				return Plan{}, ValidationError{fmt.Sprintf("stage.%s.job.%s.timeout", stage.Name, job.Name), err.Error()}
			}
			compiledStage.Jobs = append(compiledStage.Jobs, PlanJob{
				Name: job.Name, Image: job.Image, DependsOn: job.DependsOn,
				Timeout: timeout, Retry: job.Retry, Matrix: job.Matrix,
				Environment: job.Environment, Services: job.Services, Resources: job.Resources,
				Secrets: job.Secrets, Steps: job.Steps,
			})
		}
		plan.Stages = append(plan.Stages, compiledStage)
	}
	return plan, nil
}

func validateJobs(path string, jobs []Job) ValidationErrors {
	var problems ValidationErrors
	if len(jobs) == 0 {
		return append(problems, ValidationError{path + ".jobs", "must contain at least one job"})
	}
	names := make(map[string]struct{}, len(jobs))
	deps := make(map[string][]string, len(jobs))
	for i, job := range jobs {
		jobPath := fmt.Sprintf("%s.jobs[%d]", path, i)
		if !namePattern.MatchString(job.Name) {
			problems = append(problems, ValidationError{jobPath + ".name", "is invalid"})
		} else if _, exists := names[job.Name]; exists {
			problems = append(problems, ValidationError{jobPath + ".name", "must be unique within its stage"})
		}
		names[job.Name] = struct{}{}
		deps[job.Name] = job.DependsOn
		if len(job.Steps) == 0 {
			problems = append(problems, ValidationError{jobPath + ".steps", "must contain at least one step"})
		}
		if job.Retry < 0 || job.Retry > 5 {
			problems = append(problems, ValidationError{jobPath + ".retry", "must be between 0 and 5"})
		}
		if job.Resources.Privileged {
			problems = append(problems, ValidationError{jobPath + ".resources.privileged", "is forbidden in pipeline v1; use an administrator-approved runner policy"})
		}
		if job.Timeout != "" {
			if _, err := parseTimeout(job.Timeout); err != nil {
				problems = append(problems, ValidationError{jobPath + ".timeout", err.Error()})
			}
		}
		for j, step := range job.Steps {
			stepPath := fmt.Sprintf("%s.steps[%d]", jobPath, j)
			if !namePattern.MatchString(step.Name) {
				problems = append(problems, ValidationError{stepPath + ".name", "is invalid"})
			}
			if step.Image == "" && job.Image == "" {
				problems = append(problems, ValidationError{stepPath + ".image", "is required when the job has no default image"})
			}
			if len(step.Commands) == 0 {
				problems = append(problems, ValidationError{stepPath + ".commands", "must not be empty"})
			}
		}
	}
	return append(problems, validateDependencies(path+".jobs", names, deps)...)
}

func validateDependencies(path string, names map[string]struct{}, dependencies map[string][]string) ValidationErrors {
	var problems ValidationErrors
	for name, values := range dependencies {
		for _, dependency := range values {
			if dependency == name {
				problems = append(problems, ValidationError{path + "." + name + ".depends_on", "cannot depend on itself"})
			} else if _, ok := names[dependency]; !ok {
				problems = append(problems, ValidationError{path + "." + name + ".depends_on", fmt.Sprintf("references unknown dependency %q", dependency)})
			}
		}
	}
	if hasCycle(dependencies) {
		problems = append(problems, ValidationError{path, "dependency graph contains a cycle"})
	}
	return problems
}

func hasCycle(graph map[string][]string) bool {
	const (
		unseen = iota
		visiting
		visited
	)
	state := make(map[string]int, len(graph))
	var visit func(string) bool
	visit = func(node string) bool {
		if state[node] == visiting {
			return true
		}
		if state[node] == visited {
			return false
		}
		state[node] = visiting
		for _, next := range graph[node] {
			if visit(next) {
				return true
			}
		}
		state[node] = visited
		return false
	}
	for node := range graph {
		if visit(node) {
			return true
		}
	}
	return false
}

func stageDependencies(stages []Stage) map[string][]string {
	result := make(map[string][]string, len(stages))
	for _, stage := range stages {
		result[stage.Name] = stage.DependsOn
	}
	return result
}

func parseTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return 30 * time.Minute, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, errors.New("must be a Go duration such as 10m or 1h")
	}
	if value < time.Second || value > 24*time.Hour {
		return 0, errors.New("must be between 1s and 24h")
	}
	return value, nil
}
