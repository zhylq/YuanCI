package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/authorization"
	"github.com/yuanci/yuanci/internal/identity"
)

// Same lock as membership edits: bootstrap and revocation cannot race to
// recreate privileges. Identity/flow mutations are small, DB-only transactions.
func oauthLock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(982716421)`)
	return err
}

func (s *Store) ConfigureGitHubBootstrap(ctx context.Context, subject string) error {
	if !identity.ValidGitHubSubject(subject) {
		return identity.ErrBootstrap
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return err
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT github_subject FROM oauth_bootstrap`).Scan(&existing)
	if err == nil {
		if existing != subject {
			return identity.ErrBootstrap
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var initialized bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE instance_id IS NOT NULL AND role='admin')`).Scan(&initialized); err != nil {
		return err
	}
	if initialized {
		return identity.ErrBootstrap
	}
	if _, err := tx.Exec(ctx, `INSERT INTO oauth_bootstrap(github_subject) VALUES ($1)`, subject); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) BeginOAuth(ctx context.Context, state, nonce, linkToken string) error {
	stateHash, err := identity.TokenDigest(state)
	if err != nil {
		return identity.ErrOAuthFlow
	}
	browserHash, err := identity.TokenDigest(nonce)
	if err != nil {
		return identity.ErrOAuthFlow
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return err
	}
	var linkID *uuid.UUID
	if linkToken != "" {
		session, err := authenticateSession(ctx, tx, linkToken)
		if err != nil {
			return err
		}
		if err := recentSession(ctx, tx, session.ID); err != nil {
			return err
		}
		linkID = &session.ID
	}
	if _, err := tx.Exec(ctx, `DELETE FROM oauth_flows WHERE expires_at<=clock_timestamp()`); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM oauth_flows`).Scan(&count); err != nil {
		return err
	}
	if count >= 1000 {
		return identity.ErrFlowCapacity
	}
	if _, err := tx.Exec(ctx, `INSERT INTO oauth_flows(state_hash,browser_hash,link_session_id) VALUES ($1,$2,$3)`, stateHash[:], browserHash[:], linkID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ConsumeOAuth(ctx context.Context, state, nonce string) (string, error) {
	stateHash, err := identity.TokenDigest(state)
	if err != nil {
		return "", identity.ErrOAuthFlow
	}
	browserHash, err := identity.TokenDigest(nonce)
	if err != nil {
		return "", identity.ErrOAuthFlow
	}
	ticket := identity.NewToken()
	ticketHash, _ := identity.TokenDigest(ticket)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var expiry time.Time
	err = tx.QueryRow(ctx, `UPDATE oauth_flows SET completion_hash=$3 WHERE state_hash=$1 AND browser_hash=$2
        AND completion_hash IS NULL AND expires_at>clock_timestamp() RETURNING expires_at`, stateHash[:], browserHash[:], ticketHash[:]).Scan(&expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", identity.ErrOAuthFlow
	}
	if err != nil {
		return "", err
	}
	if err := flowLive(ctx, tx, expiry); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return ticket, nil
}

func flowLive(ctx context.Context, tx pgx.Tx, expiry time.Time) error {
	var live bool
	if err := tx.QueryRow(ctx, `SELECT $1::timestamptz>clock_timestamp()`, expiry).Scan(&live); err != nil {
		return err
	}
	if !live {
		return identity.ErrOAuthFlow
	}
	return nil
}

func recentSession(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var recent bool
	if err := tx.QueryRow(ctx, `SELECT created_at>clock_timestamp()-interval '10 minutes' FROM browser_sessions WHERE id=$1`, id).Scan(&recent); err != nil {
		return err
	}
	if !recent {
		return identity.ErrUnauthenticated
	}
	return nil
}

func (s *Store) FinishOAuth(ctx context.Context, ticket string, user identity.ExternalUser, currentToken string) (identity.Credentials, error) {
	return s.finishOAuth(ctx, ticket, user, currentToken, "")
}

func (s *Store) FinishManagedOAuth(ctx context.Context, ticket string, user identity.ExternalUser, currentToken, setupToken string) (identity.Credentials, error) {
	return s.finishOAuth(ctx, ticket, user, currentToken, setupToken)
}

func (s *Store) finishOAuth(ctx context.Context, ticket string, user identity.ExternalUser, currentToken, setupToken string) (identity.Credentials, error) {
	if !user.Valid() {
		return identity.Credentials{}, identity.ErrUnauthenticated
	}
	ticketHash, err := identity.TokenDigest(ticket)
	if err != nil {
		return identity.Credentials{}, identity.ErrOAuthFlow
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return identity.Credentials{}, err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return identity.Credentials{}, err
	}
	var linkID *uuid.UUID
	var expiry time.Time
	var configID, verifySession *uuid.UUID
	var setupHash []byte
	err = tx.QueryRow(ctx, `DELETE FROM oauth_flows WHERE completion_hash=$1 AND expires_at>clock_timestamp()
        RETURNING link_session_id,expires_at,config_id,verify_session_id,verify_setup_hash`, ticketHash[:]).Scan(&linkID, &expiry, &configID, &verifySession, &setupHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Credentials{}, identity.ErrOAuthFlow
	}
	if err != nil {
		return identity.Credentials{}, err
	}
	activation, owner, err := prepareManagedCompletion(ctx, tx, configID, verifySession, setupHash, user, currentToken, setupToken)
	if err != nil {
		return identity.Credentials{}, err
	}
	if linkID != nil {
		session, err := authenticateSession(ctx, tx, currentToken)
		if err != nil {
			return identity.Credentials{}, err
		}
		if session.ID != *linkID {
			return identity.Credentials{}, identity.ErrUnauthenticated
		}
		if err := recentSession(ctx, tx, session.ID); err != nil {
			return identity.Credentials{}, err
		}
		owner = session.UserID
	}
	var existingOwner uuid.UUID
	err = tx.QueryRow(ctx, `SELECT user_id FROM external_identities WHERE provider=$1 AND provider_instance=$2 AND external_id=$3 FOR UPDATE`, user.Provider, user.Instance, user.Subject).Scan(&existingOwner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return identity.Credentials{}, err
	}
	if err == nil {
		if owner != uuid.Nil && owner != existingOwner {
			return identity.Credentials{}, identity.ErrIdentityConflict
		}
		owner = existingOwner
	}
	var bootstrapProvider, bootstrapInstance, bootstrapSubject string
	var bootstrapped bool
	if err := tx.QueryRow(ctx, `SELECT provider,provider_instance,github_subject,consumed_at IS NOT NULL FROM oauth_bootstrap FOR UPDATE`).Scan(&bootstrapProvider, &bootstrapInstance, &bootstrapSubject, &bootstrapped); err != nil {
		return identity.Credentials{}, identity.ErrBootstrap
	}
	if !bootstrapped && (user.Provider != bootstrapProvider || user.Instance != bootstrapInstance || user.Subject != bootstrapSubject) {
		return identity.Credentials{}, authorization.ErrForbidden
	}
	newBinding := errors.Is(err, pgx.ErrNoRows)
	if owner == uuid.Nil {
		owner = uuid.New()
		name := user.Name
		if name == "" {
			name = user.Login
		}
		if _, err := tx.Exec(ctx, `INSERT INTO users(id,display_name) VALUES ($1,$2)`, owner, name); err != nil {
			return identity.Credentials{}, err
		}
	}
	// A disabled account must not gain an identity or a bootstrap grant.
	var active bool
	if err := tx.QueryRow(ctx, `SELECT status='active' FROM users WHERE id=$1 FOR SHARE`, owner).Scan(&active); err != nil {
		return identity.Credentials{}, err
	}
	if !active {
		return identity.Credentials{}, identity.ErrUnauthenticated
	}
	if newBinding {
		var bindingID uuid.UUID
		err := tx.QueryRow(ctx, `INSERT INTO external_identities(user_id,provider,provider_instance,external_id,login)
            VALUES ($1,$2,$3,$4,$5) RETURNING id`, owner, user.Provider, user.Instance, user.Subject, user.Login).Scan(&bindingID)
		if err != nil {
			return identity.Credentials{}, err
		}
		if err := appendAudit(ctx, tx, owner, "identity.linked", "identity", bindingID); err != nil {
			return identity.Credentials{}, err
		}
	}
	if !bootstrapped {
		var instanceID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM instances FOR SHARE`).Scan(&instanceID); err != nil {
			return identity.Credentials{}, err
		}
		var hasAdmin bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE instance_id IS NOT NULL AND role='admin')`).Scan(&hasAdmin); err != nil {
			return identity.Credentials{}, err
		}
		if hasAdmin {
			return identity.Credentials{}, identity.ErrBootstrap
		}
		if _, err := tx.Exec(ctx, `INSERT INTO memberships(user_id,role,instance_id) VALUES ($1,'admin',$2)`, owner, instanceID); err != nil {
			return identity.Credentials{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE oauth_bootstrap SET consumed_at=clock_timestamp(),user_id=$1`, owner); err != nil {
			return identity.Credentials{}, err
		}
		if err := appendAudit(ctx, tx, owner, "instance.bootstrapped", "instance", instanceID); err != nil {
			return identity.Credentials{}, err
		}
	}
	if err := flowLive(ctx, tx, expiry); err != nil {
		return identity.Credentials{}, err
	}
	credentials, err := issueSession(ctx, tx, owner, identity.DefaultTTL)
	if err != nil {
		return identity.Credentials{}, err
	}
	if activation != nil {
		if err := activateLoginConfig(ctx, tx, *activation, owner); err != nil {
			return identity.Credentials{}, err
		}
	}
	if digest, err := identity.TokenDigest(currentToken); err == nil {
		var oldID, oldOwner uuid.UUID
		err = tx.QueryRow(ctx, `UPDATE browser_sessions SET revoked_at=clock_timestamp() WHERE token_hash=$1 AND revoked_at IS NULL RETURNING id,user_id`, digest[:]).Scan(&oldID, &oldOwner)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return identity.Credentials{}, err
		}
		if err == nil {
			if err := appendAudit(ctx, tx, oldOwner, "session.revoked", "session", oldID); err != nil {
				return identity.Credentials{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return identity.Credentials{}, err
	}
	return credentials, nil
}
