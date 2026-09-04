package postgres

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
)

// BindManagedMasterKey fails closed on accidental key replacement, including on restart.
func (s *Store) BindManagedMasterKey(ctx context.Context, key []byte) error {
	if len(key) != 32 {
		return provisioning.ErrConfig
	}
	digest := sha256.Sum256(key)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return err
	}
	var existing []byte
	if err := tx.QueryRow(ctx, `SELECT master_key_digest FROM auth_setup FOR UPDATE`).Scan(&existing); err != nil {
		return err
	}
	if len(existing) > 0 && subtle.ConstantTimeCompare(existing, digest[:]) != 1 {
		return provisioning.ErrConfig
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_setup SET master_key_digest=$1`, digest[:]); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ProvisioningStatus(ctx context.Context) (provisioning.Status, error) {
	var status provisioning.Status
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM oauth_bootstrap WHERE consumed_at IS NOT NULL),
        EXISTS(SELECT 1 FROM login_configs WHERE status='active')`).Scan(&status.Initialized, &status.Configured)
	return status, err
}

// IssueSetupCode is called by the local host CLI only, never exposed over HTTP.
func (s *Store) IssueSetupCode(ctx context.Context, code string) error {
	digest, err := identity.TokenDigest(code)
	if err != nil {
		return provisioning.ErrSetup
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return err
	}
	var closed bool
	if err := tx.QueryRow(ctx, `SELECT finished_at IS NOT NULL OR EXISTS(SELECT 1 FROM oauth_bootstrap)
        OR EXISTS(SELECT 1 FROM memberships WHERE instance_id IS NOT NULL AND role='admin') FROM auth_setup`).Scan(&closed); err != nil {
		return err
	}
	if closed {
		return provisioning.ErrSetup
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_setup SET code_hash=$1,code_expires_at=clock_timestamp()+interval '15 minutes',
        session_hash=NULL,session_expires_at=NULL`, digest[:]); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE login_configs SET status='retired' WHERE setup_hash IS NOT NULL AND status='candidate'`); err != nil {
		return err
	}
	if err := setupAudit(ctx, tx, "setup.code_issued"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func setupAudit(ctx context.Context, tx pgx.Tx, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(action,resource_type,resource_id) VALUES ($1,'auth_setup','singleton')`, action)
	return err
}
func (s *Store) ExchangeSetupCode(ctx context.Context, code, token string) error {
	digest, err := identity.TokenDigest(code)
	if err != nil {
		return provisioning.ErrSetup
	}
	tokenHash, err := identity.TokenDigest(token)
	if err != nil {
		return provisioning.ErrSetup
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE auth_setup SET code_hash=NULL,code_expires_at=NULL,session_hash=$2,
        session_expires_at=clock_timestamp()+interval '30 minutes' WHERE code_hash=$1 AND code_expires_at>clock_timestamp()
        AND finished_at IS NULL AND NOT EXISTS(SELECT 1 FROM oauth_bootstrap)`, digest[:], tokenHash[:])
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return provisioning.ErrSetup
	}
	if err := setupAudit(ctx, tx, "setup.code_redeemed"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type settingsActor struct {
	session   identity.Session
	setupHash []byte
}

func settingsAccess(ctx context.Context, tx pgx.Tx, access provisioning.Access, recent bool) (settingsActor, error) {
	if (access.SessionToken == "") == (access.SetupToken == "") {
		return settingsActor{}, identity.ErrUnauthenticated
	}
	if access.SessionToken != "" {
		session, err := authenticateSession(ctx, tx, access.SessionToken)
		if err != nil {
			return settingsActor{}, err
		}
		var instance uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM instances`).Scan(&instance); err != nil {
			return settingsActor{}, err
		}
		if err := authorize(ctx, tx, session, authorization.Scope{Kind: authorization.Instance, ID: instance}, authorization.InstanceManage); err != nil {
			return settingsActor{}, err
		}
		if recent {
			if err := recentSession(ctx, tx, session.ID); err != nil {
				return settingsActor{}, err
			}
		}
		if err := sessionLive(ctx, tx, session); err != nil {
			return settingsActor{}, err
		}
		return settingsActor{session: session}, nil
	}
	digest, err := identity.TokenDigest(access.SetupToken)
	if err != nil {
		return settingsActor{}, provisioning.ErrSetup
	}
	var expiry time.Time
	err = tx.QueryRow(ctx, `SELECT session_expires_at FROM auth_setup WHERE session_hash=$1 AND finished_at IS NULL
        AND NOT EXISTS(SELECT 1 FROM oauth_bootstrap WHERE consumed_at IS NOT NULL) FOR UPDATE`, digest[:]).Scan(&expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return settingsActor{}, provisioning.ErrSetup
	}
	if err != nil {
		return settingsActor{}, err
	}
	if err := flowLive(ctx, tx, expiry); err != nil {
		return settingsActor{}, provisioning.ErrSetup
	}
	return settingsActor{setupHash: digest[:]}, nil
}

func configRow(row pgx.Row) (provisioning.Config, error) {
	var config provisioning.Config
	var encrypted []byte
	err := row.Scan(&config.ID, &config.Provider, &config.Instance, &config.ClientID, &config.BootstrapSubject, &config.Status, &config.CreatedAt, &config.ExpiresAt, &encrypted, &config.ExpectedActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return config, provisioning.ErrConflict
	}
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(encrypted, &config.Encrypted); err != nil {
		return config, provisioning.ErrConfig
	}
	return config, nil
}

const configColumns = `id,provider,provider_instance,client_id,bootstrap_subject,status,created_at,expires_at,encrypted_secret,expected_active`

func activeID(ctx context.Context, tx pgx.Tx) (*uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM login_configs WHERE status='active'`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}
func sameRevision(a, b *uuid.UUID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func actorOwner(actor settingsActor) any {
	if actor.session.UserID == uuid.Nil {
		return nil
	}
	return actor.session.UserID
}

func (s *Store) LoginSettings(ctx context.Context, access provisioning.Access) (provisioning.Settings, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return provisioning.Settings{}, err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return provisioning.Settings{}, err
	}
	actor, err := settingsAccess(ctx, tx, access, false)
	if err != nil {
		return provisioning.Settings{}, err
	}
	settings := provisioning.Settings{}
	active, err := configRow(tx.QueryRow(ctx, `SELECT `+configColumns+` FROM login_configs WHERE status='active'`))
	if err == nil {
		settings.Active = &active.Info
	} else if !errors.Is(err, provisioning.ErrConflict) {
		return settings, err
	}
	candidate, err := configRow(tx.QueryRow(ctx, `SELECT `+configColumns+` FROM login_configs WHERE status='candidate' AND expires_at>clock_timestamp()
        AND (created_by=$1 OR setup_hash=$2) ORDER BY created_at DESC LIMIT 1`, actorOwner(actor), actor.setupHash))
	if err == nil {
		settings.Candidate = &candidate.Info
	} else if !errors.Is(err, provisioning.ErrConflict) {
		return settings, err
	}
	return settings, tx.Commit(ctx)
}

func (s *Store) SaveLoginCandidate(ctx context.Context, access provisioning.Access, config provisioning.Config) error {
	// Legacy callers create GitHub-only configs without provider metadata. New
	// provider entries must name their canonical instance explicitly.
	if config.Provider == "" && config.Instance == "" {
		config.Provider, config.Instance = "github", identity.GitHubInstance
	}
	if config.ID == uuid.Nil || !identity.ValidProviderInstance(config.Provider, config.Instance) || !identity.ValidGitHubSubject(config.BootstrapSubject) {
		return provisioning.ErrConfig
	}
	encrypted, err := json.Marshal(config.Encrypted)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return err
	}
	actor, err := settingsAccess(ctx, tx, access, true)
	if err != nil {
		return err
	}
	current, err := activeID(ctx, tx)
	if err != nil {
		return err
	}
	if !sameRevision(current, config.ExpectedActive) {
		return provisioning.ErrConflict
	}
	if actor.session.UserID != uuid.Nil {
		var subject string
		var provider, instance string
		if err := tx.QueryRow(ctx, `SELECT provider,provider_instance,github_subject FROM oauth_bootstrap WHERE consumed_at IS NOT NULL`).Scan(&provider, &instance, &subject); err != nil {
			return provisioning.ErrConflict
		}
		if config.Provider != provider || config.Instance != instance || config.BootstrapSubject != subject {
			return provisioning.ErrConfig
		} // Bootstrap identity is immutable.
	}
	if _, err := tx.Exec(ctx, `UPDATE login_configs SET status='retired' WHERE status='candidate'
        AND (expires_at<=clock_timestamp() OR created_by=$1 OR setup_hash=$2)`, actorOwner(actor), actor.setupHash); err != nil {
		return err
	}
	var pending int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM login_configs WHERE status='candidate'`).Scan(&pending); err != nil {
		return err
	}
	if pending >= 100 {
		return identity.ErrFlowCapacity
	}
	_, err = tx.Exec(ctx, `INSERT INTO login_configs(id,provider,provider_instance,client_id,encrypted_secret,bootstrap_subject,expected_active,created_by,setup_hash)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, config.ID, config.Provider, config.Instance, config.ClientID, encrypted, config.BootstrapSubject, config.ExpectedActive, actorOwner(actor), actor.setupHash)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(actor_user_id,action,resource_type,resource_id)
        VALUES ($1,'login_config.candidate_saved','login_config',$2)`, actorOwner(actor), config.ID.String()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func checkCandidate(ctx context.Context, tx pgx.Tx, config provisioning.Config, actor settingsActor) error {
	if config.Status != "candidate" {
		return provisioning.ErrConflict
	}
	if err := flowLive(ctx, tx, config.ExpiresAt); err != nil {
		return provisioning.ErrConflict
	}
	current, err := activeID(ctx, tx)
	if err != nil {
		return err
	}
	if !sameRevision(current, config.ExpectedActive) {
		return provisioning.ErrConflict
	}
	var owns bool
	if err := tx.QueryRow(ctx, `SELECT created_by=$2 OR setup_hash=$3 FROM login_configs WHERE id=$1`, config.ID, actorOwner(actor), actor.setupHash).Scan(&owns); err != nil {
		return provisioning.ErrConflict
	}
	if !owns {
		return authorization.ErrForbidden
	}
	return nil
}

func (s *Store) BeginManagedOAuth(ctx context.Context, state, nonce, linkToken string, candidate uuid.UUID, access provisioning.Access) (provisioning.Config, error) {
	var empty provisioning.Config
	stateHash, err := identity.TokenDigest(state)
	if err != nil {
		return empty, identity.ErrOAuthFlow
	}
	browserHash, err := identity.TokenDigest(nonce)
	if err != nil {
		return empty, identity.ErrOAuthFlow
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return empty, err
	}
	var actor settingsActor
	var verifySession, linkID *uuid.UUID
	var config provisioning.Config
	if candidate != uuid.Nil {
		actor, err = settingsAccess(ctx, tx, access, true)
		if err != nil {
			return empty, err
		}
		config, err = configRow(tx.QueryRow(ctx, `SELECT `+configColumns+` FROM login_configs WHERE id=$1 FOR UPDATE`, candidate))
		if err != nil {
			return empty, err
		}
		if err := checkCandidate(ctx, tx, config, actor); err != nil {
			return empty, err
		}
		if actor.session.ID != uuid.Nil {
			verifySession = &actor.session.ID
		}
	} else {
		config, err = configRow(tx.QueryRow(ctx, `SELECT `+configColumns+` FROM login_configs WHERE status='active'`))
		if err != nil {
			return empty, err
		}
		if linkToken != "" {
			session, err := authenticateSession(ctx, tx, linkToken)
			if err != nil {
				return empty, err
			}
			if err := recentSession(ctx, tx, session.ID); err != nil {
				return empty, err
			}
			linkID = &session.ID
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM oauth_flows WHERE expires_at<=clock_timestamp()`); err != nil {
		return empty, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM oauth_flows`).Scan(&count); err != nil {
		return empty, err
	}
	if count >= 1000 {
		return empty, identity.ErrFlowCapacity
	}
	_, err = tx.Exec(ctx, `INSERT INTO oauth_flows(state_hash,browser_hash,link_session_id,config_id,verify_session_id,verify_setup_hash)
        VALUES ($1,$2,$3,$4,$5,$6)`, stateHash[:], browserHash[:], linkID, config.ID, verifySession, actor.setupHash)
	if err != nil {
		return empty, err
	}
	return config, tx.Commit(ctx)
}
func (s *Store) ManagedFlowConfig(ctx context.Context, ticket string) (provisioning.Config, error) {
	digest, err := identity.TokenDigest(ticket)
	if err != nil {
		return provisioning.Config{}, identity.ErrOAuthFlow
	}
	return configRow(s.pool.QueryRow(ctx, `SELECT `+configColumns+` FROM login_configs WHERE id=(SELECT config_id FROM oauth_flows
        WHERE completion_hash=$1 AND expires_at>clock_timestamp())`, digest[:]))
}

// prepareManagedCompletion runs inside the identity/session transaction under the
// same lock as membership mutations. It never grants privileges on its own.
func prepareManagedCompletion(ctx context.Context, tx pgx.Tx, configID *uuid.UUID, verifySession *uuid.UUID, setupHash []byte, user identity.ExternalUser, currentToken, setupToken string) (*provisioning.Config, uuid.UUID, error) {
	if configID == nil {
		return nil, uuid.Nil, nil
	}
	config, err := configRow(tx.QueryRow(ctx, `SELECT `+configColumns+` FROM login_configs WHERE id=$1 FOR UPDATE`, *configID))
	if err != nil {
		return nil, uuid.Nil, err
	}
	if verifySession == nil && len(setupHash) == 0 {
		if config.Status != "active" {
			return nil, uuid.Nil, provisioning.ErrConflict
		}
		return nil, uuid.Nil, nil
	}
	access := provisioning.Access{SetupToken: setupToken}
	if verifySession != nil {
		access = provisioning.Access{SessionToken: currentToken}
	}
	actor, err := settingsAccess(ctx, tx, access, true)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if verifySession != nil && actor.session.ID != *verifySession {
		return nil, uuid.Nil, identity.ErrUnauthenticated
	}
	if len(setupHash) != 0 && subtle.ConstantTimeCompare(setupHash, actor.setupHash) != 1 {
		return nil, uuid.Nil, provisioning.ErrSetup
	}
	if err := checkCandidate(ctx, tx, config, actor); err != nil {
		return nil, uuid.Nil, err
	}
	if verifySession == nil {
		if user.Provider != config.Provider || user.Instance != config.Instance || user.Subject != config.BootstrapSubject {
			return nil, uuid.Nil, authorization.ErrForbidden
		}
		if _, err := tx.Exec(ctx, `INSERT INTO oauth_bootstrap(github_subject,provider,provider_instance) VALUES ($1,$2,$3)`, config.BootstrapSubject, config.Provider, config.Instance); err != nil {
			return nil, uuid.Nil, provisioning.ErrConflict
		}
	} else {
		if user.Provider != config.Provider || user.Instance != config.Instance {
			return nil, uuid.Nil, authorization.ErrForbidden
		}
		var owner uuid.UUID
		err := tx.QueryRow(ctx, `SELECT user_id FROM external_identities WHERE provider=$1 AND provider_instance=$2 AND external_id=$3`, user.Provider, user.Instance, user.Subject).Scan(&owner)
		if err != nil || owner != actor.session.UserID {
			return nil, uuid.Nil, authorization.ErrForbidden
		}
	}
	return &config, actor.session.UserID, nil
}
func activateLoginConfig(ctx context.Context, tx pgx.Tx, config provisioning.Config, owner uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE login_configs SET status='retired' WHERE status='active'`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE login_configs SET status='active' WHERE id=$1`, config.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_setup SET finished_at=COALESCE(finished_at,clock_timestamp()),code_hash=NULL,
        code_expires_at=NULL,session_hash=NULL,session_expires_at=NULL`); err != nil {
		return err
	}
	return appendAudit(ctx, tx, owner, "login_config.activated", "login_config", config.ID)
}
