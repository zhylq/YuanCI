package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/gitee"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/provisioning"
	"github.com/yuanci/yuanci/internal/scm"
)

const giteeGrantColumns = `id,user_id,login_id,revision,subject,scope,expires_at,status,encrypted_token`

func giteeGrantRow(row pgx.Row) (gitee.Grant, error) {
	var g gitee.Grant
	var encrypted []byte
	err := row.Scan(&g.ID, &g.UserID, &g.LoginID, &g.Revision, &g.Subject, &g.Scope, &g.ExpiresAt, &g.Status, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, gitee.ErrStale
	}
	if err != nil {
		return g, err
	}
	if len(encrypted) > 0 && json.Unmarshal(encrypted, &g.Encrypted) != nil {
		return g, gitee.ErrStale
	}
	return g, nil
}
func giteeSnapshot(ctx context.Context, tx pgx.Tx, actor settingsActor) (gitee.Snapshot, error) {
	snap := gitee.Snapshot{UserID: actor.session.UserID}
	config, err := configRow(tx.QueryRow(ctx, `SELECT `+configColumns+` FROM login_configs WHERE status='active' AND provider='gitee' AND provider_instance=$1`, identity.GiteeInstance))
	if err != nil {
		return snap, gitee.ErrStale
	}
	snap.Config = config
	if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(external_id ORDER BY external_id),'{}') FROM external_identities WHERE user_id=$1 AND provider='gitee' AND provider_instance=$2`, snap.UserID, identity.GiteeInstance).Scan(&snap.Subjects); err != nil {
		return snap, err
	}
	if len(snap.Subjects) == 0 {
		return snap, scm.ErrUnauthorized
	}
	grant, err := giteeGrantRow(tx.QueryRow(ctx, `SELECT `+giteeGrantColumns+` FROM gitee_authorizations WHERE user_id=$1`, snap.UserID))
	if err == nil {
		snap.Grant = &grant
	} else if !errors.Is(err, gitee.ErrStale) {
		return snap, err
	}
	return snap, nil
}
func (s *Store) giteeTx(ctx context.Context, session string, recent bool, fn func(pgx.Tx, settingsActor, gitee.Snapshot) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := oauthLock(ctx, tx); err != nil {
		return err
	}
	actor, err := settingsAccess(ctx, tx, provisioning.Access{SessionToken: session}, recent)
	if err != nil {
		return err
	}
	snap, err := giteeSnapshot(ctx, tx, actor)
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
func sameGitee(a, b gitee.Snapshot) bool {
	return a.Config.ID == b.Config.ID && a.UserID == b.UserID && slices.Equal(a.Subjects, b.Subjects) && (a.Grant == nil && b.Grant == nil || a.Grant != nil && b.Grant != nil && a.Grant.ID == b.Grant.ID && a.Grant.Revision == b.Grant.Revision)
}
func (s *Store) GiteeContext(ctx context.Context, session string, recent bool) (gitee.Snapshot, error) {
	var result gitee.Snapshot
	err := s.giteeTx(ctx, session, recent, func(_ pgx.Tx, _ settingsActor, snap gitee.Snapshot) error { result = snap; return nil })
	return result, err
}
func (s *Store) BeginGiteeFlow(ctx context.Context, session string, expected gitee.Snapshot, state, nonce string) error {
	stateHash, err := identity.TokenDigest(state)
	if err != nil {
		return identity.ErrOAuthFlow
	}
	nonceHash, err := identity.TokenDigest(nonce)
	if err != nil {
		return identity.ErrOAuthFlow
	}
	return s.giteeTx(ctx, session, true, func(tx pgx.Tx, actor settingsActor, current gitee.Snapshot) error {
		if !sameGitee(current, expected) {
			return gitee.ErrStale
		}
		if _, err := tx.Exec(ctx, `DELETE FROM gitee_oauth_flows WHERE expires_at<=clock_timestamp() OR session_id=$1`, actor.session.ID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM gitee_oauth_flows`).Scan(&count); err != nil {
			return err
		}
		if count >= 1000 {
			return identity.ErrFlowCapacity
		}
		_, err := tx.Exec(ctx, `INSERT INTO gitee_oauth_flows(id,session_id,login_id,state_hash,nonce_hash) VALUES($1,$2,$3,$4,$5)`, uuid.New(), actor.session.ID, current.Config.ID, stateHash[:], nonceHash[:])
		return err
	})
}
func (s *Store) ConsumeGiteeFlow(ctx context.Context, session, state, nonce string) (gitee.Snapshot, error) {
	var snap gitee.Snapshot
	stateHash, err := identity.TokenDigest(state)
	if err != nil {
		return snap, identity.ErrOAuthFlow
	}
	nonceHash, err := identity.TokenDigest(nonce)
	if err != nil {
		return snap, identity.ErrOAuthFlow
	}
	err = s.giteeTx(ctx, session, false, func(tx pgx.Tx, actor settingsActor, current gitee.Snapshot) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE gitee_oauth_flows SET consumed=true WHERE session_id=$1 AND login_id=$2 AND state_hash=$3 AND nonce_hash=$4 AND NOT consumed AND expires_at>clock_timestamp() RETURNING id`, actor.session.ID, current.Config.ID, stateHash[:], nonceHash[:]).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.ErrOAuthFlow
		}
		if err != nil {
			return err
		}
		current.FlowID = id
		snap = current
		return nil
	})
	return snap, err
}
func (s *Store) SaveGiteeGrant(ctx context.Context, session string, expected gitee.Snapshot, grant gitee.Grant) error {
	if grant.ID == uuid.Nil || grant.Revision == uuid.Nil || grant.UserID != expected.UserID || grant.LoginID != expected.Config.ID || !slices.Contains(expected.Subjects, grant.Subject) || !gitee.ValidScope(grant.Scope) || grant.Status != "active" {
		return gitee.ErrStale
	}
	return s.giteeTx(ctx, session, false, func(tx pgx.Tx, actor settingsActor, current gitee.Snapshot) error {
		if !sameGitee(current, expected) || (current.Grant != nil && current.Grant.ID != grant.ID) {
			return gitee.ErrStale
		}
		if err := flowLive(ctx, tx, grant.ExpiresAt); err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `DELETE FROM gitee_oauth_flows WHERE id=$1 AND session_id=$2 AND login_id=$3 AND consumed AND expires_at>clock_timestamp()`, expected.FlowID, actor.session.ID, current.Config.ID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return gitee.ErrStale
		}
		encrypted, err := json.Marshal(grant.Encrypted)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO gitee_authorizations(id,user_id,login_id,revision,subject,scope,expires_at,status,encrypted_token) VALUES($1,$2,$3,$4,$5,$6,$7,'active',$8)
   ON CONFLICT(user_id) DO UPDATE SET login_id=EXCLUDED.login_id,revision=EXCLUDED.revision,subject=EXCLUDED.subject,scope=EXCLUDED.scope,expires_at=EXCLUDED.expires_at,status='active',encrypted_token=EXCLUDED.encrypted_token,refresh_claim=NULL,refresh_until=NULL,retry_at='-infinity',updated_at=clock_timestamp()`, grant.ID, grant.UserID, grant.LoginID, grant.Revision, grant.Subject, grant.Scope, grant.ExpiresAt, encrypted)
		if err != nil {
			return err
		}
		return appendAudit(ctx, tx, actor.session.UserID, "gitee.authorized", "gitee_authorization", grant.ID)
	})
}

// A grant remains usable only while its owner is active, still an instance
// administrator, bound to the same external subject and active login revision.
const liveGiteeGrant = `EXISTS(SELECT 1 FROM login_configs l JOIN users u ON u.id=g.user_id
 WHERE l.id=g.login_id AND l.status='active' AND l.provider='gitee' AND l.provider_instance='https://gitee.com' AND u.status='active')
 AND EXISTS(SELECT 1 FROM memberships m WHERE m.user_id=g.user_id AND m.instance_id IS NOT NULL AND m.role='admin')
 AND EXISTS(SELECT 1 FROM external_identities e WHERE e.user_id=g.user_id AND e.provider='gitee' AND e.provider_instance='https://gitee.com' AND e.external_id=g.subject)`

func (s *Store) GiteeGrant(ctx context.Context, id uuid.UUID) (gitee.Grant, provisioning.Config, error) {
	var config provisioning.Config
	// A process crash during refresh makes rotation outcome unknowable.
	if _, err := s.pool.Exec(ctx, `UPDATE gitee_authorizations SET status='revoked',encrypted_token=NULL,refresh_claim=NULL,refresh_until=NULL,revision=$2 WHERE id=$1 AND status='refreshing' AND refresh_until<=clock_timestamp()`, id, uuid.New()); err != nil {
		return gitee.Grant{}, config, err
	}
	grant, err := giteeGrantRow(s.pool.QueryRow(ctx, `SELECT `+giteeGrantColumns+` FROM gitee_authorizations g WHERE id=$1 AND status<>'revoked' AND `+liveGiteeGrant, id))
	if err != nil {
		return grant, config, err
	}
	config, err = configRow(s.pool.QueryRow(ctx, `SELECT `+configColumns+` FROM login_configs WHERE id=$1 AND status='active'`, grant.LoginID))
	return grant, config, err
}
func (s *Store) ClaimGiteeRefresh(ctx context.Context, grant gitee.Grant) (uuid.UUID, error) {
	claim := uuid.New()
	result, err := s.pool.Exec(ctx, `UPDATE gitee_authorizations g SET status='refreshing',refresh_claim=$3,refresh_until=clock_timestamp()+interval '1 minute' WHERE id=$1 AND revision=$2 AND status='active' AND retry_at<=clock_timestamp() AND `+liveGiteeGrant, grant.ID, grant.Revision, claim)
	if err != nil {
		return uuid.Nil, err
	}
	if result.RowsAffected() != 1 {
		return uuid.Nil, gitee.ErrBusy
	}
	return claim, nil
}
func (s *Store) CompleteGiteeRefresh(ctx context.Context, old, updated gitee.Grant, delay time.Duration, revoke bool) error {
	var encrypted any
	status := "active"
	revision := old.Revision
	scope := old.Scope
	expiry := old.ExpiresAt
	if revoke {
		status = "revoked"
		revision = uuid.New()
	} else if delay > 0 {
		encoded, err := json.Marshal(old.Encrypted)
		if err != nil {
			return err
		}
		encrypted = encoded
	} else {
		if updated.ID != old.ID || updated.UserID != old.UserID || updated.LoginID != old.LoginID || updated.Revision == uuid.Nil || !gitee.ValidScope(updated.Scope) || !updated.ExpiresAt.After(time.Now()) {
			return gitee.ErrStale
		}
		encoded, err := json.Marshal(updated.Encrypted)
		if err != nil {
			return err
		}
		encrypted = encoded
		revision = updated.Revision
		scope = updated.Scope
		expiry = updated.ExpiresAt
	}
	result, err := s.pool.Exec(ctx, `UPDATE gitee_authorizations g SET revision=$4,status=$5,encrypted_token=$6,scope=$7,expires_at=$8,refresh_claim=NULL,refresh_until=NULL,retry_at=clock_timestamp()+$9*interval '1 second',updated_at=clock_timestamp()
 WHERE id=$1 AND revision=$2 AND status='refreshing' AND refresh_claim=$3 AND refresh_until>clock_timestamp() AND `+liveGiteeGrant, old.ID, old.Revision, old.Claim, revision, status, encrypted, scope, expiry, int64(delay.Seconds()))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return gitee.ErrStale
	}
	return nil
}
func (s *Store) RevokeGiteeGrant(ctx context.Context, session string) error {
	return s.giteeTx(ctx, session, true, func(tx pgx.Tx, actor settingsActor, snap gitee.Snapshot) error {
		if snap.Grant == nil {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE gitee_authorizations SET status='revoked',revision=$2,encrypted_token=NULL,refresh_claim=NULL,refresh_until=NULL WHERE id=$1`, snap.Grant.ID, uuid.New())
		if err != nil {
			return err
		}
		return appendAudit(ctx, tx, actor.session.UserID, "gitee.revoked", "gitee_authorization", snap.Grant.ID)
	})
}

var _ gitee.Store = (*Store)(nil)
