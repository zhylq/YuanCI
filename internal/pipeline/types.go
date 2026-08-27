package pipeline

import "time"

const APIVersion = "v1"

type Pipeline struct {
	Version     string            `yaml:"version" json:"version"`
	Name        string            `yaml:"name" json:"name"`
	Concurrency *Concurrency      `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	Triggers    []Trigger         `yaml:"triggers,omitempty" json:"triggers,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Stages      []Stage           `yaml:"stages" json:"stages"`
}

type Concurrency struct {
	Group          string `yaml:"group" json:"group"`
	Limit          int    `yaml:"limit,omitempty" json:"limit,omitempty"`
	CancelPrevious bool   `yaml:"cancel_previous,omitempty" json:"cancel_previous,omitempty"`
}

type Trigger struct {
	Event    string   `yaml:"event" json:"event"`
	Branches []string `yaml:"branches,omitempty" json:"branches,omitempty"`
	Paths    []string `yaml:"paths,omitempty" json:"paths,omitempty"`
}

type Stage struct {
	Name      string   `yaml:"name" json:"name"`
	DependsOn []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Jobs      []Job    `yaml:"jobs" json:"jobs"`
}

type Job struct {
	Name        string              `yaml:"name" json:"name"`
	Image       string              `yaml:"image,omitempty" json:"image,omitempty"`
	DependsOn   []string            `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Timeout     string              `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retry       int                 `yaml:"retry,omitempty" json:"retry,omitempty"`
	Environment map[string]string   `yaml:"environment,omitempty" json:"environment,omitempty"`
	Secrets     []string            `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Services    []Service           `yaml:"services,omitempty" json:"services,omitempty"`
	Matrix      map[string][]string `yaml:"matrix,omitempty" json:"matrix,omitempty"`
	Resources   Resources           `yaml:"resources,omitempty" json:"resources,omitempty"`
	Steps       []Step              `yaml:"steps" json:"steps"`
}

type Service struct {
	Name        string            `yaml:"name" json:"name"`
	Image       string            `yaml:"image" json:"image"`
	Environment map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
}

type Step struct {
	Name        string            `yaml:"name" json:"name"`
	Image       string            `yaml:"image,omitempty" json:"image,omitempty"`
	Commands    []string          `yaml:"commands" json:"commands"`
	WorkingDir  string            `yaml:"working_dir,omitempty" json:"working_dir,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Timeout     string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type Resources struct {
	CPU        string `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Memory     string `yaml:"memory,omitempty" json:"memory,omitempty"`
	PIDs       int    `yaml:"pids,omitempty" json:"pids,omitempty"`
	Disk       string `yaml:"disk,omitempty" json:"disk,omitempty"`
	Privileged bool   `yaml:"privileged,omitempty" json:"privileged,omitempty"`
}

type Plan struct {
	Version      string      `json:"version"`
	Name         string      `json:"name"`
	ConfigSHA256 string      `json:"config_sha256"`
	CompiledAt   time.Time   `json:"compiled_at"`
	Stages       []PlanStage `json:"stages"`
}

type PlanStage struct {
	Name      string    `json:"name"`
	DependsOn []string  `json:"depends_on,omitempty"`
	Jobs      []PlanJob `json:"jobs"`
}

type PlanJob struct {
	Name        string              `json:"name"`
	Image       string              `json:"image,omitempty"`
	DependsOn   []string            `json:"depends_on,omitempty"`
	Timeout     time.Duration       `json:"timeout"`
	Retry       int                 `json:"retry"`
	Matrix      map[string][]string `json:"matrix,omitempty"`
	Environment map[string]string   `json:"environment,omitempty"`
	Services    []Service           `json:"services,omitempty"`
	Resources   Resources           `json:"resources,omitempty"`
	Secrets     []string            `json:"secrets,omitempty"`
	Steps       []Step              `json:"steps"`
}
