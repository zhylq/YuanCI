package run

import (
	"context"

	"github.com/google/uuid"
)

// AuthorizedStore revalidates session and scoped permission in the same
// transaction as the operation. Unscoped Store methods are evaluation/internal
// only and must never be called by authenticated browser run handlers.
type AuthorizedStore interface {
	CreateAuthorizedRun(context.Context, string, uuid.UUID, Record) (Record, error)
	ListAuthorizedRuns(context.Context, string, uuid.UUID, int) ([]Record, error)
}
