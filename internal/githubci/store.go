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
	RuntimeAutomation(context.Context, uuid.UUID) (project.AutomationSettings, error)
	CommitWebhookRun(context.Context, RunCommit) (RunResult, error)
}
