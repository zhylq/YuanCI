package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/githubapp"
	"github.com/yuanci/yuanci/internal/identity"
)

func (s *Store) ResolveGitHubRepository(ctx context.Context, externalID string) (githubapp.Repository, error) {
	if !identity.ValidGitHubSubject(externalID) {
		return githubapp.Repository{}, githubapp.ErrRepositoryUnavailable
	}
	var repository githubapp.Repository
	var encrypted []byte
	err := s.pool.QueryRow(ctx, `SELECT r.id,r.external_id,r.owner,r.name,r.clone_url,r.default_branch,
        i.id::text,a.id,a.client_id,a.encrypted_key
      FROM repositories r
      JOIN github_installations i ON i.id=r.github_installation_id
      JOIN github_app_configs a ON a.app_id=i.app_id
      JOIN login_configs l ON l.id=a.login_config_id AND l.status='active'
      WHERE r.provider='github' AND r.provider_instance=$1 AND r.external_id=$2 AND r.active`,
		identity.GitHubInstance, externalID).Scan(&repository.ID, &repository.ExternalID, &repository.Owner,
		&repository.Name, &repository.CloneURL, &repository.DefaultBranch, &repository.InstallationID,
		&repository.AppID, &repository.AppClientID, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return githubapp.Repository{}, githubapp.ErrRepositoryUnavailable
	}
	if err != nil {
		return githubapp.Repository{}, err
	}
	if json.Unmarshal(encrypted, &repository.EncryptedKey) != nil {
		return githubapp.Repository{}, githubapp.ErrCredentialUnavailable
	}
	return repository, nil
}

var _ githubapp.Store = (*Store)(nil)
