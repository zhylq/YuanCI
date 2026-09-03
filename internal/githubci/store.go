// Package githubci coordinates authenticated GitHub events into immutable CI runs.
package githubci

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/githubhook"
	"github.com/yuanci/yuanci/internal/pipeline"
	"github.com/yuanci/yuanci/internal/project"
)

var ErrInvalidCommit = errors.New("invalid GitHub CI run commit")
var ErrRepositoryUnavailable = errors.New("GitHub CI repository is not available")
var ErrRepositoryMismatch = errors.New("GitHub CI repository identity changed")

type RunCommit struct {
	Delivery       githubhook.WorkItem
	RepositoryID   uuid.UUID
	PipelinePath   string
	PipelineSource []byte
	Plan           pipeline.Plan
	CreatedAt      time.Time
}

type RunResult struct {
	ID      uuid.UUID
	Created bool
}

type Store interface {
	RuntimeAutomationForGitHub(context.Context, string) (uuid.UUID, project.AutomationSettings, error)
	CommitWebhookRun(context.Context, RunCommit) (RunResult, error)
	FinalizeWebhook(context.Context, githubhook.Finalize) error
}
