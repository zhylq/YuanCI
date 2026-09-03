package commitstatus

import (
	"context"

	"github.com/google/uuid"
)

type AdminService struct{ store RecoveryRepository }

func NewAdminService(store RecoveryRepository) (*AdminService, error) {
	if store == nil {
		return nil, ErrInvalid
	}
	return &AdminService{store: store}, nil
}

func (service *AdminService) Replay(ctx context.Context, actorID, itemID uuid.UUID) error {
	if actorID == uuid.Nil || itemID == uuid.Nil {
		return ErrInvalid
	}
	return service.store.ReplayCommitStatus(ctx, itemID, actorID)
}
