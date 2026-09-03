package commitstatus

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type State string

const (
	StatePending State = "pending"
	StateSuccess State = "success"
	StateFailure State = "failure"
	StateError   State = "error"
)

type DeliveryState string

const (
	DeliveryQueued     DeliveryState = "queued"
	DeliveryProcessing DeliveryState = "processing"
	DeliveryDelivered  DeliveryState = "delivered"
	DeliveryDead       DeliveryState = "dead"
)

var ErrInvalid = errors.New("invalid commit status outbox request")

type Item struct {
	ID                   uuid.UUID
	RepositoryID         uuid.UUID
	RepositoryExternalID string
	RunID                uuid.UUID
	Provider             string
	CommitSHA            string
	Context              string
	State                State
	Description          string
	TargetURL            string
	DeterministicKey     string
	DeliveryState        DeliveryState
	AttemptCount         int
	AvailableAt          time.Time
	ExpiresAt            time.Time
	LeaseOwner           uuid.UUID
	LeaseExpiresAt       time.Time
}

type RecoveryRepository interface {
	ClaimCommitStatus(context.Context, time.Duration) (*Item, error)
	RecoverCommitStatusLeases(context.Context, int) (int, error)
	FinishCommitStatus(context.Context, uuid.UUID, uuid.UUID) error
	RescheduleCommitStatus(context.Context, uuid.UUID, uuid.UUID, time.Time, string, bool) error
	ReplayCommitStatus(context.Context, uuid.UUID, uuid.UUID) error
}

func (state State) Valid() bool {
	return state == StatePending || state == StateSuccess || state == StateFailure || state == StateError
}
