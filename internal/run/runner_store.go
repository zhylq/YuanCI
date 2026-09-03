package run

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
)

const (
	RunnerLeaseDuration      = 30 * time.Second
	MaximumHeartbeatJobCount = 256
)

var ErrInvalidRunnerRequest = errors.New("invalid Runner request")

var runnerLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

type RunnerDescriptor struct {
	ID                 uuid.UUID
	PoolType           string
	OS                 string
	Architecture       string
	Executor           string
	Labels             map[string]string
	Capacity           int
	AvailableDiskBytes int64
	ProtocolVersion    int
}

type RunnerClaim struct {
	RunnerID uuid.UUID
}

type LeaseRequest struct {
	RunnerID   uuid.UUID
	JobID      uuid.UUID
	LeaseToken string
}

type LeaseState struct {
	JobID        uuid.UUID
	LeaseExpires time.Time
}

type ActiveLease struct {
	JobID      uuid.UUID
	LeaseToken string
	State      string
}

type HeartbeatRequest struct {
	Runner     RunnerDescriptor
	ActiveJobs []ActiveLease
}

type LeaseResult struct {
	JobID        uuid.UUID
	Renewed      bool
	LeaseExpires time.Time
	CancelReason string
}

type HeartbeatResult struct {
	LeaseExpires time.Time
	Jobs         []LeaseResult
}

type RunnerCompletion struct {
	RunnerID   uuid.UUID
	JobID      uuid.UUID
	LeaseToken string
	Status     JobStatus
}

type RunnerJobStore interface {
	ClaimRunnerJob(context.Context, RunnerClaim) (*Assignment, error)
	ReleaseRunnerJob(context.Context, LeaseRequest) error
	AcknowledgeRunnerJob(context.Context, LeaseRequest) (LeaseState, error)
	StartRunnerJob(context.Context, LeaseRequest) (LeaseState, error)
	RenewRunnerLeases(context.Context, HeartbeatRequest) (HeartbeatResult, error)
	CompleteRunnerJob(context.Context, RunnerCompletion) error
}

func ValidateHeartbeatRequest(request HeartbeatRequest) error {
	runner := request.Runner
	if runner.ID == uuid.Nil || (runner.PoolType != "standard" && runner.PoolType != "privileged" && runner.PoolType != "deployment") ||
		runner.OS == "" || len(runner.OS) > 64 || runner.Architecture == "" || len(runner.Architecture) > 64 ||
		runner.Executor == "" || len(runner.Executor) > 64 || runner.Capacity < 1 || runner.Capacity > 256 ||
		runner.AvailableDiskBytes < 0 || runner.ProtocolVersion < 1 || runner.ProtocolVersion > 2 ||
		len(runner.Labels) > 128 || len(request.ActiveJobs) > MaximumHeartbeatJobCount ||
		len(request.ActiveJobs) > runner.Capacity {
		return ErrInvalidRunnerRequest
	}
	seen := make(map[uuid.UUID]struct{}, len(request.ActiveJobs))
	for key, value := range runner.Labels {
		if !runnerLabelPattern.MatchString(key) || len(value) > 256 {
			return ErrInvalidRunnerRequest
		}
	}
	for _, active := range request.ActiveJobs {
		if active.JobID == uuid.Nil || active.LeaseToken == "" || (active.State != "received" && active.State != "running") {
			return ErrInvalidRunnerRequest
		}
		if _, exists := seen[active.JobID]; exists {
			return ErrInvalidRunnerRequest
		}
		seen[active.JobID] = struct{}{}
	}
	return nil
}
