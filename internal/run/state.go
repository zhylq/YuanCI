package run

import "fmt"

type Status string

const (
	StatusQueued          Status = "queued"
	StatusRunning         Status = "running"
	StatusWaitingApproval Status = "waiting_approval"
	StatusSucceeded       Status = "succeeded"
	StatusFailed          Status = "failed"
	StatusCanceled        Status = "canceled"
)

var transitions = map[Status]map[Status]struct{}{
	StatusQueued:          {StatusRunning: {}, StatusCanceled: {}},
	StatusRunning:         {StatusWaitingApproval: {}, StatusSucceeded: {}, StatusFailed: {}, StatusCanceled: {}},
	StatusWaitingApproval: {StatusRunning: {}, StatusFailed: {}, StatusCanceled: {}},
}

func (s Status) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCanceled
}

func ValidateTransition(from, to Status) error {
	if _, ok := transitions[from][to]; !ok {
		return fmt.Errorf("illegal run transition %q -> %q", from, to)
	}
	return nil
}

type JobStatus string

const (
	JobBlocked   JobStatus = "blocked"
	JobQueued    JobStatus = "queued"
	JobAssigned  JobStatus = "assigned"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCanceled  JobStatus = "canceled"
	JobSkipped   JobStatus = "skipped"
)

func ValidateJobTransition(from, to JobStatus) error {
	valid := map[JobStatus]map[JobStatus]struct{}{
		JobBlocked:  {JobQueued: {}, JobCanceled: {}, JobSkipped: {}},
		JobQueued:   {JobAssigned: {}, JobCanceled: {}, JobSkipped: {}},
		JobAssigned: {JobRunning: {}, JobQueued: {}, JobCanceled: {}},
		JobRunning:  {JobSucceeded: {}, JobFailed: {}, JobCanceled: {}},
	}
	if _, ok := valid[from][to]; !ok {
		return fmt.Errorf("illegal job transition %q -> %q", from, to)
	}
	return nil
}
