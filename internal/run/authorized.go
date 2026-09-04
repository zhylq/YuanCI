package run

import (
	"context"
	"errors"
	"github.com/yuanci/yuanci/internal/pipeline"
	"time"

	"github.com/google/uuid"
)

// AuthorizedStore revalidates session and scoped permission in the same
// transaction as the operation. Unscoped Store methods are evaluation/internal
// only and must never be called by authenticated browser run handlers.
type AuthorizedStore interface {
	CreateAuthorizedRun(context.Context, string, uuid.UUID, Record) (Record, error)
	ListAuthorizedRuns(context.Context, string, uuid.UUID, int) ([]Record, error)
}

type CancellationStore interface {
	CancelAuthorizedRun(context.Context, string, uuid.UUID, uuid.UUID) (Status, error)
}

var ErrRunConflict = errors.New("Run cannot be rerun in this state")

type RerunStore interface {
	RerunAuthorizedRun(context.Context, string, uuid.UUID, uuid.UUID, string, uuid.UUID) (Record, error)
}

type JobDetail struct {
	ID         uuid.UUID        `json:"id"`
	StageName  string           `json:"stage_name"`
	JobName    string           `json:"job_name"`
	Status     JobStatus        `json:"status"`
	Spec       pipeline.PlanJob `json:"spec"`
	StartedAt  *time.Time       `json:"started_at,omitempty"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
	ReusedFrom *uuid.UUID       `json:"reused_from_job_id,omitempty"`
}
type Detail struct {
	Run  Record      `json:"run"`
	Jobs []JobDetail `json:"jobs"`
}
type LogPage struct {
	Items        []LogChunk `json:"items"`
	NextSequence int64      `json:"next_sequence"`
	Expired      bool       `json:"expired"`
}
type DetailStore interface {
	GetAuthorizedRun(context.Context, string, uuid.UUID, uuid.UUID) (Detail, error)
	ReadAuthorizedLogs(context.Context, string, uuid.UUID, uuid.UUID, uuid.UUID, int64) (LogPage, error)
}
