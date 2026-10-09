package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	namePattern        = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
	runnerLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
)

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
	if _, err := decodePipelineDocument(source); err != nil {
		return Pipeline{}, err
	}
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
	if value.Deployment != nil {
		if !namePattern.MatchString(value.Deployment.Environment) {
			problems = append(problems, ValidationError{"deployment.environment", "must be a valid 1-63 character name"})
		}
		if value.Concurrency != nil && (value.Concurrency.CancelPrevious || value.Concurrency.Limit > 1) {
			problems = append(problems, ValidationError{"concurrency", "deployments require FIFO execution without cancellation or parallel runs"})
		}
		for _, stage := range value.Stages {
			for _, job := range stage.Jobs {
				if job.Retry != 0 {
					problems = append(problems, ValidationError{"retry", "deployments cannot retry"})
				}
			}
		}
	}
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

	problems = append(problems, validateTriggers(value.Triggers)...)

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
	// Normalize execution syntax before hashing and producing the persisted plan.
	for i := range value.Stages {
		for j := range value.Stages[i].Jobs {
			job := &value.Stages[i].Jobs[j]
			job.Steps = effectiveJobSteps(*job)
			job.Commands = nil
		}
	}

	canonical, err := json.Marshal(value)
	if err != nil {
		return Plan{}, fmt.Errorf("canonicalize pipeline: %w", err)
	}
	digest := sha256.Sum256(canonical)
	plan := Plan{
		Deployment:   value.Deployment,
		Version:      value.Version,
		Name:         value.Name,
		ConfigSHA256: hex.EncodeToString(digest[:]),
		CompiledAt:   now.UTC(),
		Triggers:     value.Triggers,
		Stages:       make([]PlanStage, 0, len(value.Stages)),
	}
	for _, stage := range value.Stages {
		compiledStage := PlanStage{Name: stage.Name, DependsOn: stage.DependsOn, Jobs: make([]PlanJob, 0, len(stage.Jobs))}
		for _, job := range stage.Jobs {
			timeout, err := parseTimeout(job.Timeout)
			if err != nil {
				return Plan{}, ValidationError{fmt.Sprintf("stage.%s.job.%s.timeout", stage.Name, job.Name), err.Error()}
			}
			diskBytes, err := parseByteSize(job.Resources.Disk)
			if err != nil {
				return Plan{}, ValidationError{fmt.Sprintf("stage.%s.job.%s.resources.disk", stage.Name, job.Name), err.Error()}
			}
			runsOn := job.RunsOn
			if runsOn.OS == "" {
				runsOn.OS = "linux"
			}
			if runsOn.Executor == "" {
				runsOn.Executor = "docker"
			}
			if runsOn.Labels == nil {
				runsOn.Labels = map[string]string{}
			}
			compiledStage.Jobs = append(compiledStage.Jobs, PlanJob{
				Name: job.Name, Image: job.Image, DependsOn: job.DependsOn,
				Timeout: timeout, Retry: job.Retry, Matrix: job.Matrix,
				Environment: job.Environment, Services: job.Services, Resources: job.Resources,
				Secrets: job.Secrets, Steps: job.Steps, RunsOn: runsOn, RequiredDiskBytes: diskBytes,
			})
			if value.Deployment != nil {
				compiledStage.Jobs[len(compiledStage.Jobs)-1].Deployment = value.Deployment.Environment
			}
		}
		plan.Stages = append(plan.Stages, compiledStage)
	}
	return plan, nil
}

func effectiveJobSteps(job Job) []Step {
	if job.Commands != nil {
		return []Step{{Name: "commands", Commands: job.Commands}}
	}
	return job.Steps
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
		if err := ValidateServices(job.Services); err != nil {
			for _, problem := range err.(ValidationErrors) {
				problems = append(problems, ValidationError{jobPath + "." + problem.Path, problem.Message})
			}
		}
		if job.Commands != nil && job.Steps != nil {
			problems = append(problems, ValidationError{jobPath, "commands and steps are mutually exclusive"})
		}
		steps := effectiveJobSteps(job)
		if len(steps) == 0 {
			problems = append(problems, ValidationError{jobPath + ".steps", "must contain at least one step"})
		}
		if job.Retry < 0 || job.Retry > 5 {
			problems = append(problems, ValidationError{jobPath + ".retry", "must be between 0 and 5"})
		}
		if job.Resources.Privileged {
			problems = append(problems, ValidationError{jobPath + ".resources.privileged", "is forbidden in pipeline v1; use an administrator-approved runner policy"})
		}
		if _, err := parseByteSize(job.Resources.Disk); err != nil {
			problems = append(problems, ValidationError{jobPath + ".resources.disk", err.Error()})
		}
		for field, value := range map[string]string{"os": job.RunsOn.OS, "architecture": job.RunsOn.Architecture, "executor": job.RunsOn.Executor} {
			if len(value) > 64 || (value != "" && !namePattern.MatchString(value)) {
				problems = append(problems, ValidationError{jobPath + ".runs_on." + field, "is invalid"})
			}
		}
		if len(job.RunsOn.Labels) > 64 {
			problems = append(problems, ValidationError{jobPath + ".runs_on.labels", "must contain at most 64 labels"})
		}
		for key, value := range job.RunsOn.Labels {
			if len(value) > 256 || !runnerLabelPattern.MatchString(key) {
				problems = append(problems, ValidationError{jobPath + ".runs_on.labels", "contains an invalid label"})
				break
			}
		}
		if job.Timeout != "" {
			if _, err := parseTimeout(job.Timeout); err != nil {
				problems = append(problems, ValidationError{jobPath + ".timeout", err.Error()})
			}
		}
		for j, step := range steps {
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

// ValidateServices is also used at the Runner boundary for persisted plans.
func ValidateServices(services []Service) error {
	var problems ValidationErrors
	if len(services) > 16 {
		return ValidationErrors{{"services", "must contain at most 16 services"}}
	}
	names := make(map[string]bool, len(services))
	for i, service := range services {
		path := fmt.Sprintf("services[%d]", i)
		alias := strings.ToLower(service.Name)
		if !namePattern.MatchString(service.Name) || names[alias] {
			problems = append(problems, ValidationError{path + ".name", "must be valid and unique within the job (case insensitive)"})
		}
		names[alias] = true
		if service.Image == "" || len(service.Image) > 512 || strings.HasPrefix(service.Image, "-") || strings.ContainsAny(service.Image, " \t\r\n\x00") {
			problems = append(problems, ValidationError{path + ".image", "must be a non-empty image reference, not an option"})
		}
		if len(service.Environment) > 128 {
			problems = append(problems, ValidationError{path + ".environment", "must contain at most 128 variables"})
		}
		for key, value := range service.Environment {
			if !environmentNamePattern.MatchString(key) || len(key) > 128 || len(value) > 32768 || strings.ContainsRune(value, 0) {
				problems = append(problems, ValidationError{path + ".environment", "contains an invalid variable"})
				break
			}
		}
	}
	if len(problems) > 0 {
		return problems
	}
	return nil
}

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func parseByteSize(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	upper := strings.ToUpper(strings.TrimSpace(raw))
	units := []struct {
		suffix     string
		multiplier int64
	}{{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"TB", 1_000_000_000_000}, {"GB", 1_000_000_000}, {"MB", 1_000_000}, {"KB", 1_000}}
	for _, unit := range units {
		if strings.HasSuffix(upper, unit.suffix) {
			value, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(upper, unit.suffix)), 10, 64)
			if err != nil || value < 0 || value > (1<<50)/unit.multiplier {
				return 0, errors.New("must be a non-negative size no larger than 1 PiB")
			}
			return value * unit.multiplier, nil
		}
	}
	value, err := strconv.ParseInt(upper, 10, 64)
	if err != nil || value < 0 || value > 1<<50 {
		return 0, errors.New("must use bytes or KB/MB/GB/TB/KiB/MiB/GiB/TiB")
	}
	return value, nil
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
