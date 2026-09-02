package project

import (
	"context"
	"errors"
	"path"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

var (
	ErrAutomationConflict = errors.New("project automation settings changed")
	ErrAutomationInvalid  = errors.New("invalid project automation settings")
	ErrAutomationNotReady = errors.New("project automation cannot be enabled before validation")
)

const DefaultPipelinePath = ".yuanci.yml"

// AutomationSettings is the provider-neutral policy evaluated for an incoming
// SCM event. Revision zero represents the synthesized, never-written default.
type AutomationSettings struct {
	Enabled            bool   `json:"enabled"`
	PipelinePath       string `json:"pipeline_path"`
	TriggerPush        bool   `json:"trigger_push"`
	TriggerTag         bool   `json:"trigger_tag"`
	TriggerPullRequest bool   `json:"trigger_pull_request"`
	CancelOlderCommits bool   `json:"cancel_older_commits"`
	Revision           int64  `json:"revision"`
}

func DefaultAutomationSettings() AutomationSettings {
	return AutomationSettings{
		PipelinePath:       DefaultPipelinePath,
		TriggerPush:        true,
		TriggerTag:         true,
		TriggerPullRequest: true,
		CancelOlderCommits: true,
	}
}

type AutomationUpdate struct {
	Enabled            bool
	PipelinePath       string
	TriggerPush        bool
	TriggerTag         bool
	TriggerPullRequest bool
	CancelOlderCommits bool
	ExpectedRevision   int64
}

func (u AutomationUpdate) Validate() error {
	if u.ExpectedRevision < 0 || len(u.PipelinePath) < 1 || len(u.PipelinePath) > 256 ||
		strings.HasPrefix(u.PipelinePath, "/") || strings.Contains(u.PipelinePath, "\\") ||
		path.Clean(u.PipelinePath) != u.PipelinePath || unsafePathSegment(u.PipelinePath) ||
		(!strings.HasSuffix(u.PipelinePath, ".yml") && !strings.HasSuffix(u.PipelinePath, ".yaml")) ||
		strings.IndexFunc(u.PipelinePath, unicode.IsControl) >= 0 ||
		(u.Enabled && !u.TriggerPush && !u.TriggerTag && !u.TriggerPullRequest) {
		return ErrAutomationInvalid
	}
	return nil
}

func unsafePathSegment(value string) bool {
	for segment := range strings.SplitSeq(value, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

type AutomationStore interface {
	GetProjectAutomation(context.Context, string, uuid.UUID) (AutomationSettings, error)
	UpdateProjectAutomation(context.Context, string, uuid.UUID, AutomationUpdate) (AutomationSettings, error)
}
