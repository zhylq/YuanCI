package commitstatus

import (
	"context"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/scm"
)

type GitHubSender interface {
	DeliverCommitStatus(context.Context, uuid.UUID, string, scm.CommitStatus) error
}

type GitHubProvider struct{ sender GitHubSender }

func NewGitHubProvider(sender GitHubSender) (*GitHubProvider, error) {
	if sender == nil {
		return nil, ErrInvalid
	}
	return &GitHubProvider{sender: sender}, nil
}

func (provider *GitHubProvider) Deliver(ctx context.Context, item Item) error {
	if item.Provider != "github" || item.RepositoryID == uuid.Nil || item.RepositoryExternalID == "" ||
		len(item.CommitSHA) != 40 || item.Context == "" || !item.State.Valid() {
		return ErrInvalid
	}
	return provider.sender.DeliverCommitStatus(ctx, item.RepositoryID, item.RepositoryExternalID, scm.CommitStatus{
		SHA: item.CommitSHA, Context: item.Context, State: string(item.State),
		Description: item.Description, TargetURL: item.TargetURL,
	})
}
