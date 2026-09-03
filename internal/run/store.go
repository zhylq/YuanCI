package run

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/pipeline"
)

type Record struct {
	ID                uuid.UUID       `json:"id"`
	ProjectID         *uuid.UUID      `json:"project_id,omitempty"`
	CreatedBy         *uuid.UUID      `json:"created_by,omitempty"`
	PipelineVersionID *uuid.UUID      `json:"-"`
	IdempotencyKey    string          `json:"-"`
	PipelineName      string          `json:"pipeline_name"`
	Event             string          `json:"event"`
	Ref               string          `json:"ref,omitempty"`
	CommitSHA         string          `json:"commit_sha,omitempty"`
	Status            Status          `json:"status"`
	ConfigSHA256      string          `json:"config_sha256"`
	Plan              json.RawMessage `json:"plan,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
}

type Store interface {
	Ping(context.Context) error
	Create(context.Context, Record) (Record, error)
	List(context.Context, int) ([]Record, error)
	ClaimJob(context.Context, ClaimRequest) (*Assignment, error)
	StartJob(context.Context, uuid.UUID, string) error
	CompleteJob(context.Context, uuid.UUID, string, JobStatus) error
	Close()
}

var (
	ErrLeaseInvalid = errors.New("job lease is invalid or expired")
	ErrJobNotFound  = errors.New("job not found")
)

type ClaimRequest struct {
	RunnerName string            `json:"runner_name"`
	Labels     map[string]string `json:"labels,omitempty"`
}

type Assignment struct {
	JobID        uuid.UUID        `json:"job_id"`
	RunID        uuid.UUID        `json:"run_id"`
	StageName    string           `json:"stage_name"`
	JobName      string           `json:"job_name"`
	Attempt      int              `json:"attempt"`
	LeaseToken   string           `json:"lease_token"`
	LeaseExpires time.Time        `json:"lease_expires_at"`
	Spec         pipeline.PlanJob `json:"spec"`
	Source       *SourceCheckout  `json:"-"`
}

// SourceCheckout is trusted control-plane metadata for an immutable checkout.
// Credentials are deliberately not part of this persistent assignment model.
type SourceCheckout struct {
	Provider     string
	RepositoryID string
	CloneURL     string
	CommitSHA    string
}

type memoryJob struct {
	assignment   Assignment
	key          string
	dependencies []string
	status       JobStatus
	runnerID     uuid.UUID
	acceptedAt   *time.Time
	leaseRenewed *time.Time
}

type MemoryStore struct {
	mu      sync.RWMutex
	records []Record
	jobs    []*memoryJob
	runners map[uuid.UUID]RunnerDescriptor
	now     func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runners: make(map[uuid.UUID]RunnerDescriptor), now: time.Now}
}
func (m *MemoryStore) Ping(context.Context) error { return nil }
func (m *MemoryStore) Close()                     {}

func (m *MemoryStore) Create(_ context.Context, record Record) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var plan pipeline.Plan
	if err := json.Unmarshal(record.Plan, &plan); err != nil {
		return Record{}, fmt.Errorf("decode plan: %w", err)
	}
	for _, existing := range m.records {
		if existing.ID == record.ID {
			return Record{}, errors.New("run already exists")
		}
	}
	m.records = append([]Record{cloneRecord(record)}, m.records...)
	stageJobs := make(map[string][]string, len(plan.Stages))
	for _, stage := range plan.Stages {
		for _, job := range stage.Jobs {
			stageJobs[stage.Name] = append(stageJobs[stage.Name], stage.Name+"/"+job.Name)
		}
	}
	for _, stage := range plan.Stages {
		for _, job := range stage.Jobs {
			dependencies := make([]string, 0)
			for _, stageDependency := range stage.DependsOn {
				dependencies = append(dependencies, stageJobs[stageDependency]...)
			}
			for _, jobDependency := range job.DependsOn {
				dependencies = append(dependencies, stage.Name+"/"+jobDependency)
			}
			status := JobBlocked
			if len(dependencies) == 0 {
				status = JobQueued
			}
			m.jobs = append(m.jobs, &memoryJob{
				assignment: Assignment{JobID: uuid.New(), RunID: record.ID, StageName: stage.Name, JobName: job.Name, Attempt: 1, Spec: job},
				key:        stage.Name + "/" + job.Name, dependencies: dependencies, status: status,
			})
		}
	}
	return record, nil
}

func (m *MemoryStore) List(_ context.Context, limit int) ([]Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 || limit > len(m.records) {
		limit = len(m.records)
	}
	result := make([]Record, limit)
	for index := range result {
		result[index] = cloneRecord(m.records[index])
	}
	return result, nil
}

func (m *MemoryStore) ClaimJob(_ context.Context, request ClaimRequest) (*Assignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.status != JobQueued {
			continue
		}
		job.status = JobAssigned
		job.assignment.LeaseToken = randomToken()
		job.assignment.LeaseExpires = m.now().UTC().Add(leaseDuration(job.assignment.Spec.Timeout))
		m.setRunStarted(job.assignment.RunID)
		m.setRunStatus(job.assignment.RunID, StatusRunning)
		copy := job.assignment
		return &copy, nil
	}
	return nil, nil
}

func (m *MemoryStore) StartJob(_ context.Context, id uuid.UUID, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.findJob(id)
	if job == nil {
		return ErrJobNotFound
	}
	if !m.validLease(job, token) || job.status != JobAssigned {
		return ErrLeaseInvalid
	}
	job.status = JobRunning
	return nil
}

func (m *MemoryStore) CompleteJob(_ context.Context, id uuid.UUID, token string, status JobStatus) error {
	if status != JobSucceeded && status != JobFailed && status != JobCanceled {
		return errors.New("invalid terminal job status")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.findJob(id)
	if job == nil {
		return ErrJobNotFound
	}
	if !m.validLease(job, token) || (job.status != JobRunning && job.status != JobAssigned) {
		return ErrLeaseInvalid
	}
	job.status = status
	job.assignment.LeaseToken = ""
	if status == JobSucceeded {
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

func (m *MemoryStore) findJob(id uuid.UUID) *memoryJob {
	for _, job := range m.jobs {
		if job.assignment.JobID == id {
			return job
		}
	}
	return nil
}

func (m *MemoryStore) unblock(runID uuid.UUID) {
	states := make(map[string]JobStatus)
	for _, job := range m.jobs {
		if job.assignment.RunID == runID {
			states[job.key] = job.status
		}
	}
	for _, job := range m.jobs {
		if job.assignment.RunID != runID || job.status != JobBlocked {
			continue
		}
		ready := true
		for _, dependency := range job.dependencies {
			if states[dependency] != JobSucceeded {
				ready = false
				break
			}
		}
		if ready {
			job.status = JobQueued
		}
	}
}

func (m *MemoryStore) setRunStatus(runID uuid.UUID, status Status) {
	for index := range m.records {
		if m.records[index].ID == runID {
			m.records[index].Status = status
			return
		}
	}
}

func (m *MemoryStore) setRunStarted(runID uuid.UUID) {
	now := m.now().UTC()
	for index := range m.records {
		if m.records[index].ID == runID && m.records[index].StartedAt == nil {
			m.records[index].StartedAt = &now
			return
		}
	}
}

func (m *MemoryStore) finalizeRun(runID uuid.UUID) {
	failed := false
	canceled := false
	for _, job := range m.jobs {
		if job.assignment.RunID != runID {
			continue
		}
		if job.status == JobBlocked || job.status == JobQueued || job.status == JobAssigned || job.status == JobRunning {
			return
		}
		if job.status == JobFailed {
			failed = true
		}
		if job.status == JobCanceled {
			canceled = true
		}
	}
	if failed {
		m.setRunStatus(runID, StatusFailed)
	} else if canceled {
		m.setRunStatus(runID, StatusCanceled)
	} else {
		m.setRunStatus(runID, StatusSucceeded)
	}
	now := m.now().UTC()
	for index := range m.records {
		if m.records[index].ID == runID {
			m.records[index].FinishedAt = &now
			return
		}
	}
}

func cloneRecord(record Record) Record {
	if record.ProjectID != nil {
		value := *record.ProjectID
		record.ProjectID = &value
	}
	if record.CreatedBy != nil {
		value := *record.CreatedBy
		record.CreatedBy = &value
	}
	if record.PipelineVersionID != nil {
		value := *record.PipelineVersionID
		record.PipelineVersionID = &value
	}
	record.Plan = append(json.RawMessage(nil), record.Plan...)
	if record.StartedAt != nil {
		value := *record.StartedAt
		record.StartedAt = &value
	}
	if record.FinishedAt != nil {
		value := *record.FinishedAt
		record.FinishedAt = &value
	}
	return record
}

func (m *MemoryStore) validLease(job *memoryJob, token string) bool {
	if !m.now().UTC().Before(job.assignment.LeaseExpires) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(job.assignment.LeaseToken), []byte(token)) == 1
}

func randomToken() string {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", value[:])
}

func leaseDuration(jobTimeout time.Duration) time.Duration {
	if jobTimeout <= 0 {
		jobTimeout = 30 * time.Minute
	}
	return jobTimeout + 5*time.Minute
}
