package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/gitee"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/integration"
)

func checkGiteeGrant(ctx context.Context, tx pgx.Tx, snap gitee.Snapshot, id, revision uuid.UUID) error {
	if snap.Grant == nil || snap.Grant.ID != id || snap.Grant.Revision != revision || snap.Grant.Status != "active" || snap.Grant.LoginID != snap.Config.ID {
		return gitee.ErrStale
	}
	if err := flowLive(ctx, tx, snap.Grant.ExpiresAt); err != nil {
		return gitee.ErrStale
	}
	return nil
}
func (s *Store) CheckGiteeContext(ctx context.Context, session string, id, revision uuid.UUID) error {
	return s.giteeTx(ctx, session, false, func(tx pgx.Tx, _ settingsActor, snap gitee.Snapshot) error {
		return checkGiteeGrant(ctx, tx, snap, id, revision)
	})
}
func (s *Store) ImportGiteeRepositories(ctx context.Context, session string, id, revision uuid.UUID, repos []gitee.Repository) ([]integration.Imported, error) {
	if len(repos) == 0 || len(repos) > 20 {
		return nil, gitee.ErrStale
	}
	result := []integration.Imported{}
	err := s.giteeTx(ctx, session, false, func(tx pgx.Tx, actor settingsActor, snap gitee.Snapshot) error {
		if err := checkGiteeGrant(ctx, tx, snap, id, revision); err != nil {
			return err
		}
		for _, repo := range repos {
			if !identity.ValidGitHubSubject(repo.ID) || !identity.ValidGitHubSubject(repo.AccountID) || !gitee.ValidComponent(repo.Owner) || !gitee.ValidComponent(repo.Name) || repo.DefaultBranch == "" {
				return gitee.ErrStale
			}
			var org uuid.UUID
			err := tx.QueryRow(ctx, `SELECT organization_id FROM gitee_accounts WHERE account_id=$1`, repo.AccountID).Scan(&org)
			if errors.Is(err, pgx.ErrNoRows) {
				org = uuid.New()
				if _, err := tx.Exec(ctx, `INSERT INTO organizations(id,slug,display_name) VALUES($1,$2,$3)`, org, "gitee-"+repo.AccountID, "Gitee / "+repo.Owner); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO gitee_accounts(account_id,organization_id) VALUES($1,$2)`, repo.AccountID, org); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			var existing, existingOrg uuid.UUID
			var authorization *uuid.UUID
			var owner, name string
			err = tx.QueryRow(ctx, `SELECT id,organization_id,gitee_authorization_id,owner,name FROM repositories WHERE provider='gitee' AND provider_instance=$1 AND external_id=$2 FOR UPDATE`, identity.GiteeInstance, repo.ID).Scan(&existing, &existingOrg, &authorization, &owner, &name)
			if err == nil {
				if existingOrg != org || authorization == nil || *authorization != id || owner != repo.Owner || name != repo.Name {
					return gitee.ErrStale
				}
				result = append(result, integration.Imported{ID: existing, Name: owner + "/" + name})
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			project := uuid.New()
			_, err = tx.Exec(ctx, `INSERT INTO repositories(id,organization_id,provider,provider_instance,external_id,owner,name,clone_url,default_branch,gitee_authorization_id) VALUES($1,$2,'gitee',$3,$4,$5,$6,$7,$8,$9)`, project, org, identity.GiteeInstance, repo.ID, repo.Owner, repo.Name, identity.GiteeInstance+"/"+repo.Owner+"/"+repo.Name+".git", repo.DefaultBranch, id)
			if err != nil {
				return err
			}
			if err := appendAudit(ctx, tx, actor.session.UserID, "repository.imported", "repository", project); err != nil {
				return err
			}
			result = append(result, integration.Imported{ID: project, Name: repo.Owner + "/" + repo.Name, Created: true})
		}
		return checkGiteeGrant(ctx, tx, snap, id, revision)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

var _ gitee.RepositoryStore = (*Store)(nil)
