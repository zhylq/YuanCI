package run

import (
	"context"
	"crypto/subtle"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

func (m *MemoryStore) ClaimRunnerJob(_ context.Context, request RunnerClaim) (*Assignment, error) {
	if request.RunnerID == uuid.Nil {
		return nil, ErrInvalidRunnerRequest
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	runner, exists := m.runners[request.RunnerID]
	if !exists {
		return nil, ErrInvalidRunnerRequest
	}
	active := 0
	for _, job := range m.jobs {
		if job.runnerID == runner.ID && (job.status == JobAssigned || job.status == JobRunning) && m.leaseAlive(job) {
			active++
		}
	}
	if active >= runner.Capacity {
		return nil, nil
	}
	for _, job := range m.jobs {
		if job.status != JobQueued || !runnerMatches(job.assignment.Spec, runner) {
			continue
		}
		now := m.now().UTC()
		job.status = JobAssigned
		job.runnerID = runner.ID
		job.acceptedAt = nil
		job.leaseRenewed = &now
		job.assignment.LeaseToken = randomToken()
		job.assignment.LeaseExpires = now.Add(RunnerLeaseDuration)
		m.setRunStarted(job.assignment.RunID)
		m.setRunStatus(job.assignment.RunID, StatusRunning)
		assignment := cloneAssignment(job.assignment)
		return &assignment, nil
	}
	return nil, nil
}

func (m *MemoryStore) AcknowledgeRunnerJob(_ context.Context, request LeaseRequest) (LeaseState, error) {
	if err := validateLeaseRequest(request); err != nil {
		return LeaseState{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.findJob(request.JobID)
	if !m.validRunnerLease(job, request.RunnerID, request.LeaseToken) ||
		(job.status != JobAssigned && job.status != JobRunning) {
		return LeaseState{}, ErrLeaseInvalid
	}
	if job.acceptedAt == nil {
		now := m.now().UTC()
		job.acceptedAt = &now
	}
	return LeaseState{JobID: job.assignment.JobID, LeaseExpires: job.assignment.LeaseExpires}, nil
}

func (m *MemoryStore) StartRunnerJob(_ context.Context, request LeaseRequest) (LeaseState, error) {
	if err := validateLeaseRequest(request); err != nil {
		return LeaseState{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.findJob(request.JobID)
	if !m.validRunnerLease(job, request.RunnerID, request.LeaseToken) || job.acceptedAt == nil ||
		(job.status != JobAssigned && job.status != JobRunning) {
		return LeaseState{}, ErrLeaseInvalid
	}
	job.status = JobRunning
	return LeaseState{JobID: job.assignment.JobID, LeaseExpires: job.assignment.LeaseExpires}, nil
}

func (m *MemoryStore) RenewRunnerLeases(_ context.Context, request HeartbeatRequest) (HeartbeatResult, error) {
	if err := ValidateHeartbeatRequest(request); err != nil {
		return HeartbeatResult{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	request.Runner.Labels = cloneLabels(request.Runner.Labels)
	m.runners[request.Runner.ID] = request.Runner
	now := m.now().UTC()
	expires := now.Add(RunnerLeaseDuration)
	result := HeartbeatResult{LeaseExpires: expires, Jobs: make([]LeaseResult, 0, len(request.ActiveJobs))}
	for _, active := range request.ActiveJobs {
		lease := LeaseResult{JobID: active.JobID}
		job := m.findJob(active.JobID)
		if !m.validRunnerLease(job, request.Runner.ID, active.LeaseToken) ||
			(job.status != JobAssigned && job.status != JobRunning) {
			lease.CancelReason = "lease_invalid"
			result.Jobs = append(result.Jobs, lease)
			continue
		}
		job.assignment.LeaseExpires = expires
		job.leaseRenewed = &now
		lease.Renewed = true
		lease.LeaseExpires = expires
		result.Jobs = append(result.Jobs, lease)
	}
	return result, nil
}

func (m *MemoryStore) CompleteRunnerJob(_ context.Context, request RunnerCompletion) error {
	if request.RunnerID == uuid.Nil || request.JobID == uuid.Nil || request.LeaseToken == "" {
		return ErrInvalidRunnerRequest
	}
	if request.Status != JobSucceeded && request.Status != JobFailed && request.Status != JobCanceled {
		return errors.New("invalid terminal job status")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.findJob(request.JobID)
	if !m.validRunnerLease(job, request.RunnerID, request.LeaseToken) ||
		(job.status != JobRunning && job.status != JobAssigned) {
		return ErrLeaseInvalid
	}
	job.status = request.Status
	job.assignment.LeaseToken = ""
	job.runnerID = uuid.Nil
	if request.Status == JobSucceeded {
		m.unblock(job.assignment.RunID)
	} else {
		for _, candidate := range m.jobs {
			if candidate.assignment.RunID == job.assignment.RunID && (candidate.status == JobBlocked || candidate.status == JobQueued) {
				candidate.status = JobSkipped
			}
		}
	}
	m.finalizeRun(job.assignment.RunID)
	return nil
}

func validateLeaseRequest(request LeaseRequest) error {
	if request.RunnerID == uuid.Nil || request.JobID == uuid.Nil || request.LeaseToken == "" {
		return ErrInvalidRunnerRequest
	}
	return nil
}

func runnerMatches(spec pipeline.PlanJob, runner RunnerDescriptor) bool {
	if runner.PoolType != "standard" || spec.RunsOn.OS != runner.OS || spec.RunsOn.Executor != runner.Executor ||
		(spec.RunsOn.Architecture != "" && spec.RunsOn.Architecture != runner.Architecture) ||
		spec.RequiredDiskBytes > runner.AvailableDiskBytes {
		return false
	}
	for key, value := range spec.RunsOn.Labels {
		if runner.Labels[key] != value {
			return false
		}
	}
	return true
}

func (m *MemoryStore) validRunnerLease(job *memoryJob, runnerID uuid.UUID, token string) bool {
	return job != nil && job.runnerID == runnerID && m.leaseAlive(job) &&
		subtle.ConstantTimeCompare([]byte(job.assignment.LeaseToken), []byte(token)) == 1
}

func (m *MemoryStore) leaseAlive(job *memoryJob) bool {
	return job != nil && m.now().UTC().Before(job.assignment.LeaseExpires)
}

func cloneAssignment(value Assignment) Assignment {
	value.Spec.RunsOn.Labels = cloneLabels(value.Spec.RunsOn.Labels)
	if value.Source != nil {
		source := *value.Source
		value.Source = &source
	}
	return value
}

func cloneLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels))
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result[key] = labels[key]
	}
	return result
}

var _ RunnerJobStore = (*MemoryStore)(nil)
