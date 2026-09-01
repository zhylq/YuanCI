package run

import (
	"context"

	"github.com/google/uuid"
)

func (m *MemoryStore) RecoverExpiredRunnerLeases(_ context.Context, limit int) (RecoveryResult, error) {
	if limit < 1 || limit > MaximumRecoveryBatch {
		return RecoveryResult{}, ErrInvalidRecoveryLimit
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := RecoveryResult{}
	processed := 0
	for _, job := range m.jobs {
		if processed >= limit || (job.status != JobAssigned && job.status != JobRunning) || m.leaseAlive(job) {
			continue
		}
		processed++
		runID := job.assignment.RunID
		if job.status == JobAssigned {
			job.status = JobQueued
			job.runnerID = uuid.Nil
			job.acceptedAt = nil
			job.leaseRenewed = nil
			job.assignment.LeaseToken = ""
			job.assignment.LeaseExpires = m.now().UTC()
			result.Requeued++
			m.resetRunAfterRequeue(runID)
			continue
		}
		job.status = JobFailed
		job.assignment.LeaseToken = ""
		job.leaseRenewed = nil
		for _, candidate := range m.jobs {
			if candidate.assignment.RunID == runID && (candidate.status == JobBlocked || candidate.status == JobQueued) {
				candidate.status = JobSkipped
			}
		}
		m.setRunStatus(runID, StatusFailed)
		now := m.now().UTC()
		for index := range m.records {
			if m.records[index].ID == runID {
				m.records[index].FinishedAt = &now
			}
		}
		result.Failed++
	}
	return result, nil
}

func (m *MemoryStore) resetRunAfterRequeue(runID uuid.UUID) {
	for _, job := range m.jobs {
		if job.assignment.RunID == runID && (job.status == JobRunning || job.status == JobSucceeded ||
			job.status == JobFailed || job.status == JobCanceled) {
			return
		}
	}
	for index := range m.records {
		if m.records[index].ID == runID {
			m.records[index].Status = StatusQueued
			m.records[index].StartedAt = nil
			m.records[index].FinishedAt = nil
			return
		}
	}
}

var _ LeaseRecoveryStore = (*MemoryStore)(nil)
