package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/integration"
	"github.com/yuanci/yuanci/internal/provisioning"
)

// All local checks and mutations share the membership/configuration lock. No
// external I/O is allowed inside this transaction or its callback.
func (s *Store) integrationTx(ctx context.Context, token string, recent bool, fn func(pgx.Tx, settingsActor, integration.Snapshot) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return err
	}
	actor, err := settingsAccess(ctx, tx, provisioning.Access{SessionToken: token}, recent)
	if err != nil {
		return err
	}
	snap, err := integrationSnapshot(ctx, tx, actor)
	if err != nil {
		return err
	}
	if err := fn(tx, actor, snap); err != nil {
		return err
	}
	if err := sessionLive(ctx, tx, actor.session); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func integrationSnapshot(ctx context.Context, tx pgx.Tx, actor settingsActor) (integration.Snapshot, error) {
	var snap integration.Snapshot
	var secret []byte
	err := tx.QueryRow(ctx, `SELECT id,client_id,encrypted_secret FROM login_configs WHERE status='active'`).Scan(&snap.LoginID, &snap.ClientID, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return snap, integration.ErrStale
	}
	if err != nil {
		return snap, err
	}
	if json.Unmarshal(secret, &snap.Secret) != nil {
		return snap, integration.ErrConfig
	}
	err = tx.QueryRow(ctx, `SELECT external_id FROM external_identities WHERE user_id=$1 AND provider='github' AND provider_instance=$2`, actor.session.UserID, identity.GitHubInstance).Scan(&snap.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return snap, integration.ErrAccess
	}
	if err != nil {
		return snap, err
	}
	var app integration.App
	var key []byte
	err = tx.QueryRow(ctx, `SELECT id,login_config_id,app_id::text,client_id,slug,encrypted_key FROM github_app_configs`).Scan(&app.ID, &app.LoginID, &app.AppID, &app.ClientID, &app.Slug, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	if json.Unmarshal(key, &app.Key) != nil {
		return snap, integration.ErrConfig
	}
	snap.App = &app
	var proof integration.Proof
	var encrypted []byte
	err = tx.QueryRow(ctx, `SELECT id,encrypted_token,expires_at FROM github_import_proofs
 WHERE session_id=$1 AND login_id=$2 AND app_revision=$3 AND expires_at>clock_timestamp()`, actor.session.ID, snap.LoginID, app.ID).Scan(&proof.ID, &encrypted, &proof.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	if json.Unmarshal(encrypted, &proof.Token) != nil {
		return snap, integration.ErrConfig
	}
	snap.Proof = &proof
	return snap, nil
}
func (s *Store) IntegrationContext(ctx context.Context, token string, recent bool) (integration.Snapshot, error) {
	var result integration.Snapshot
	err := s.integrationTx(ctx, token, recent, func(_ pgx.Tx, _ settingsActor, snap integration.Snapshot) error { result = snap; return nil })
	return result, err
}
func sameIntegration(a, b integration.Snapshot, proof bool) error {
	if a.LoginID != b.LoginID || a.Subject != b.Subject || (a.App == nil) != (b.App == nil) {
		return integration.ErrStale
	}
	if a.App != nil && a.App.ID != b.App.ID {
		return integration.ErrStale
	}
	if proof && (a.App == nil || a.App.LoginID != a.LoginID || a.Proof == nil || b.Proof == nil || a.Proof.ID != b.Proof.ID) {
		return integration.ErrStale
	}
	return nil
}
func (s *Store) SaveIntegrationApp(ctx context.Context, token string, expected integration.Snapshot, app integration.App) error {
	if app.ID == uuid.Nil || app.LoginID != expected.LoginID || app.ClientID != expected.ClientID || !identity.ValidGitHubSubject(app.AppID) {
		return integration.ErrConfig
	}
	return s.integrationTx(ctx, token, true, func(tx pgx.Tx, actor settingsActor, current integration.Snapshot) error {
		if err := sameIntegration(current, expected, false); err != nil {
			return err
		}
		key, err := json.Marshal(app.Key)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO github_app_configs(id,login_config_id,app_id,client_id,slug,encrypted_key) VALUES($1,$2,$3,$4,$5,$6)
  ON CONFLICT(singleton) DO UPDATE SET id=EXCLUDED.id,login_config_id=EXCLUDED.login_config_id,app_id=EXCLUDED.app_id,
  client_id=EXCLUDED.client_id,slug=EXCLUDED.slug,encrypted_key=EXCLUDED.encrypted_key,updated_at=clock_timestamp()`, app.ID, app.LoginID, app.AppID, app.ClientID, app.Slug, key)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM github_import_flows`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM github_import_proofs`); err != nil {
			return err
		}
		return appendAudit(ctx, tx, actor.session.UserID, "github_app.configured", "github_app", app.ID)
	})
}
func (s *Store) BeginIntegrationFlow(ctx context.Context, token string, expected integration.Snapshot, state, nonce string) error {
	stateHash, err := identity.TokenDigest(state)
	if err != nil {
		return identity.ErrOAuthFlow
	}
	nonceHash, err := identity.TokenDigest(nonce)
	if err != nil {
		return identity.ErrOAuthFlow
	}
	return s.integrationTx(ctx, token, true, func(tx pgx.Tx, actor settingsActor, current integration.Snapshot) error {
		if err := sameIntegration(current, expected, false); err != nil {
			return err
		}
		if current.App == nil || current.App.LoginID != current.LoginID {
			return integration.ErrStale
		}
		if _, err := tx.Exec(ctx, `DELETE FROM github_import_flows WHERE expires_at<=clock_timestamp() OR session_id=$1`, actor.session.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM github_import_proofs WHERE expires_at<=clock_timestamp() OR session_id=$1`, actor.session.ID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM github_import_flows`).Scan(&count); err != nil {
			return err
		}
		if count >= 1000 {
			return identity.ErrFlowCapacity
		}
		_, err := tx.Exec(ctx, `INSERT INTO github_import_flows(id,session_id,state_hash,nonce_hash,login_id,app_revision) VALUES($1,$2,$3,$4,$5,$6)`, uuid.New(), actor.session.ID, stateHash[:], nonceHash[:], current.LoginID, current.App.ID)
		return err
	})
}
func (s *Store) ConsumeIntegrationFlow(ctx context.Context, token, state, nonce string) (integration.Snapshot, error) {
	var result integration.Snapshot
	stateHash, err := identity.TokenDigest(state)
	if err != nil {
		return result, identity.ErrOAuthFlow
	}
	nonceHash, err := identity.TokenDigest(nonce)
	if err != nil {
		return result, identity.ErrOAuthFlow
	}
	err = s.integrationTx(ctx, token, false, func(tx pgx.Tx, actor settingsActor, current integration.Snapshot) error {
		if current.App == nil || current.App.LoginID != current.LoginID {
			return integration.ErrStale
		}
		var id uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE github_import_flows SET consumed=true WHERE session_id=$1 AND state_hash=$2 AND nonce_hash=$3
  AND NOT consumed AND login_id=$4 AND app_revision=$5 AND expires_at>clock_timestamp() RETURNING id`, actor.session.ID, stateHash[:], nonceHash[:], current.LoginID, current.App.ID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.ErrOAuthFlow
		}
		if err != nil {
			return err
		}
		current.FlowID = id
		result = current
		return nil
	})
	return result, err
}
func (s *Store) SaveIntegrationProof(ctx context.Context, token string, expected integration.Snapshot, proof integration.Proof) error {
	if proof.ID == uuid.Nil || proof.ExpiresAt.After(time.Now().Add(10*time.Minute)) {
		return integration.ErrConfig
	}
	return s.integrationTx(ctx, token, false, func(tx pgx.Tx, actor settingsActor, current integration.Snapshot) error {
		if err := sameIntegration(current, expected, false); err != nil {
			return err
		}
		if current.App == nil {
			return integration.ErrStale
		}
		result, err := tx.Exec(ctx, `DELETE FROM github_import_flows WHERE id=$1 AND session_id=$2 AND consumed AND expires_at>clock_timestamp()`, expected.FlowID, actor.session.ID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return integration.ErrStale
		}
		if err := flowLive(ctx, tx, proof.ExpiresAt); err != nil {
			return integration.ErrStale
		}
		encrypted, err := json.Marshal(proof.Token)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO github_import_proofs(id,session_id,login_id,app_revision,encrypted_token,expires_at) VALUES($1,$2,$3,$4,$5,$6)
  ON CONFLICT(session_id) DO UPDATE SET id=EXCLUDED.id,login_id=EXCLUDED.login_id,app_revision=EXCLUDED.app_revision,encrypted_token=EXCLUDED.encrypted_token,expires_at=EXCLUDED.expires_at`, proof.ID, actor.session.ID, current.LoginID, current.App.ID, encrypted, proof.ExpiresAt)
		if err != nil {
			return err
		}
		return appendAudit(ctx, tx, actor.session.UserID, "github_import.authorized", "github_app", current.App.ID)
	})
}
func (s *Store) CheckIntegration(ctx context.Context, token string, expected integration.Snapshot, proof bool) error {
	return s.integrationTx(ctx, token, false, func(tx pgx.Tx, _ settingsActor, current integration.Snapshot) error {
		if err := sameIntegration(current, expected, proof); err != nil {
			return err
		}
		if proof {
			if err := flowLive(ctx, tx, current.Proof.ExpiresAt); err != nil {
				return integration.ErrStale
			}
		}
		return nil
	})
}
func (s *Store) ImportRepositories(ctx context.Context, token string, expected integration.Snapshot, install integration.Installation, repos []integration.Repository) ([]integration.Imported, error) {
	result := []integration.Imported{}
	if len(repos) == 0 || len(repos) > 20 || !identity.ValidGitHubSubject(install.ID) || !identity.ValidGitHubSubject(install.AccountID) {
		return nil, integration.ErrConfig
	}
	err := s.integrationTx(ctx, token, false, func(tx pgx.Tx, actor settingsActor, current integration.Snapshot) error {
		if err := sameIntegration(current, expected, true); err != nil {
			return err
		}
		var org uuid.UUID
		err := tx.QueryRow(ctx, `SELECT organization_id FROM github_accounts WHERE account_id=$1`, install.AccountID).Scan(&org)
		if errors.Is(err, pgx.ErrNoRows) {
			org = uuid.New()
			if _, err := tx.Exec(ctx, `INSERT INTO organizations(id,slug,display_name) VALUES($1,$2,$3)`, org, "github-"+install.AccountID, "GitHub / "+install.Account); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO github_accounts(account_id,organization_id) VALUES($1,$2)`, install.AccountID, org); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		updated, err := tx.Exec(ctx, `INSERT INTO github_installations(id,app_id,account_id) VALUES($1,$2,$3)
  ON CONFLICT(id) DO UPDATE SET checked_at=clock_timestamp() WHERE github_installations.app_id=EXCLUDED.app_id AND github_installations.account_id=EXCLUDED.account_id`, install.ID, current.App.AppID, install.AccountID)
		if err != nil {
			return err
		}
		if updated.RowsAffected() != 1 {
			return integration.ErrStale
		}
		for _, repo := range repos {
			var existing, existingOrg uuid.UUID
			var existingInstall *string
			err := tx.QueryRow(ctx, `SELECT id,organization_id,github_installation_id::text FROM repositories WHERE provider='github' AND provider_instance=$1 AND external_id=$2 FOR UPDATE`, identity.GitHubInstance, repo.ID).Scan(&existing, &existingOrg, &existingInstall)
			if err == nil {
				// Never adopt/move an existing project or reactivate a disabled one.
				if existingOrg != org || existingInstall == nil || *existingInstall != install.ID {
					return integration.ErrStale
				}
				result = append(result, integration.Imported{ID: existing, Name: repo.Owner + "/" + repo.Name})
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			id := uuid.New()
			_, err = tx.Exec(ctx, `INSERT INTO repositories(id,organization_id,provider,provider_instance,external_id,owner,name,clone_url,default_branch,github_installation_id)
    VALUES($1,$2,'github',$3,$4,$5,$6,$7,$8,$9)`, id, org, identity.GitHubInstance, repo.ID, repo.Owner, repo.Name, "https://github.com/"+repo.Owner+"/"+repo.Name+".git", repo.DefaultBranch, install.ID)
			if err != nil {
				return err
			}
			if err := appendAudit(ctx, tx, actor.session.UserID, "repository.imported", "repository", id); err != nil {
				return err
			}
			result = append(result, integration.Imported{ID: id, Name: repo.Owner + "/" + repo.Name, Created: true})
		}
		// Check again after all row/uniqueness locks, before transaction commit.
		if err := flowLive(ctx, tx, current.Proof.ExpiresAt); err != nil {
			return integration.ErrStale
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

var _ integration.RepositoryStore = (*Store)(nil)
