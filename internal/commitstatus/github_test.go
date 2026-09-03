package commitstatus

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/scm"
)

type recordingGitHubSender struct {
	repositoryID uuid.UUID
	externalID   string
	status       scm.CommitStatus
}

func (sender *recordingGitHubSender) DeliverCommitStatus(_ context.Context, repositoryID uuid.UUID, externalID string, status scm.CommitStatus) error {
	sender.repositoryID, sender.externalID, sender.status = repositoryID, externalID, status
	return nil
}

func TestGitHubProviderMapsOutboxState(t *testing.T) {
	for _, state := range []State{StatePending, StateSuccess, StateFailure, StateError} {
		sender := &recordingGitHubSender{}
		provider, err := NewGitHubProvider(sender)
		if err != nil {
			t.Fatal(err)
		}
		repositoryID := uuid.New()
		item := Item{RepositoryID: repositoryID, RepositoryExternalID: "70", Provider: "github",
			CommitSHA: "0123456789abcdef0123456789abcdef01234567", Context: "YuanCI", State: state,
			Description: "Run state"}
		if err := provider.Deliver(t.Context(), item); err != nil {
			t.Fatal(err)
		}
		if sender.repositoryID != repositoryID || sender.externalID != "70" || sender.status.State != string(state) || sender.status.SHA != item.CommitSHA {
			t.Fatalf("state %q mapped to %#v", state, sender)
		}
	}
}
