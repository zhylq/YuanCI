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

// IssueSession is an internal authentication boundary, not an HTTP/CLI login
// bypass. Its caller must have verified the external identity before calling.
func (s *Store) IssueSession(ctx context.Context, userID uuid.UUID, ttl time.Duration) (identity.Credentials, error) {
	if ttl < time.Minute || ttl > 24*time.Hour {
		return identity.Credentials{}, errors.New("invalid session lifetime")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return identity.Credentials{}, err
	}
	defer tx.Rollback(ctx)
	var name string
	if err := tx.QueryRow(ctx, `SELECT display_name FROM users WHERE id=$1 AND status='active' FOR SHARE`, userID).Scan(&name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.Credentials{}, identity.ErrUnauthenticated
		}
		return identity.Credentials{}, err
	}
	credentials := identity.Credentials{Token: identity.NewToken(), Session: identity.Session{ID: uuid.New(), UserID: userID, DisplayName: name}}
	digest, _ := identity.TokenDigest(credentials.Token)
	err = tx.QueryRow(ctx, `INSERT INTO browser_sessions(id,user_id,token_hash,expires_at)
        VALUES ($1,$2,$3,clock_timestamp()+make_interval(secs => $4)) RETURNING expires_at`,
		credentials.Session.ID, userID, digest[:], ttl.Seconds()).Scan(&credentials.Session.ExpiresAt)
	if err != nil {
		return identity.Credentials{}, err
	}
	if err := appendAudit(ctx, tx, userID, "session.created", "session", credentials.Session.ID); err != nil {
		return identity.Credentials{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identity.Credentials{}, err
	}
	return credentials, nil
}

func (s *Store) AuthenticateSession(ctx context.Context, token string) (identity.Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return identity.Session{}, err
	}
	defer tx.Rollback(ctx)
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return identity.Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identity.Session{}, err
	}
	return session, nil
}

// Lock the session and active user until the authorized operation commits.
// Revocation/suspension therefore cannot complete ahead of an in-flight write.
func authenticateSession(ctx context.Context, tx pgx.Tx, token string) (identity.Session, error) {
	digest, err := identity.TokenDigest(token)
	if err != nil {
		return identity.Session{}, identity.ErrUnauthenticated
	}
	var session identity.Session
	err = tx.QueryRow(ctx, `SELECT s.id,u.id,u.display_name,s.expires_at
        FROM browser_sessions s JOIN users u ON u.id=s.user_id
        WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND u.status='active'
        FOR SHARE OF u,s`, digest[:]).Scan(&session.ID, &session.UserID, &session.DisplayName, &session.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Session{}, identity.ErrUnauthenticated
	}
	if err != nil {
		return identity.Session{}, err
	}
	// Evaluate wall-clock expiry after any lock wait, not at transaction start.
	if err := sessionLive(ctx, tx, session); err != nil {
		return identity.Session{}, err
	}
	return session, nil
}

func sessionLive(ctx context.Context, tx pgx.Tx, session identity.Session) error {
	var live bool
	if err := tx.QueryRow(ctx, `SELECT $1::timestamptz > clock_timestamp()`, session.ExpiresAt).Scan(&live); err != nil {
		return err
	}
	if !live {
		return identity.ErrUnauthenticated
	}
	return nil
}

func (s *Store) RevokeSession(ctx context.Context, token string) error {
	digest, err := identity.TokenDigest(token)
	if err != nil {
		return identity.ErrUnauthenticated
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, userID uuid.UUID
	err = tx.QueryRow(ctx, `UPDATE browser_sessions SET revoked_at=clock_timestamp()
        WHERE token_hash=$1 AND revoked_at IS NULL RETURNING id,user_id`, digest[:]).Scan(&id, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrUnauthenticated
	}
	if err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, userID, "session.revoked", "session", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func appendAudit(ctx context.Context, tx pgx.Tx, userID uuid.UUID, action, resource string, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(actor_user_id,action,resource_type,resource_id)
        VALUES ($1,$2,$3,$4)`, userID, action, resource, id.String())
	return err
}

// scopeResource resolves ownership in the database; clients supply only one
// target ID, never an authoritative ancestor path or protection classification.
func scopeResource(ctx context.Context, tx pgx.Tx, scope authorization.Scope) (authorization.Resource, error) {
	var instanceID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM instances FOR SHARE`).Scan(&instanceID); err != nil {
		return authorization.Resource{}, err
	}
	path := []authorization.Scope{{Kind: authorization.Instance, ID: instanceID}}
	var organizationID, projectID uuid.UUID
	var protected bool
	var err error
	switch scope.Kind {
	case authorization.Instance:
		if scope.ID != instanceID {
			return authorization.Resource{}, authorization.ErrForbidden
		}
	case authorization.Organization:
		err = tx.QueryRow(ctx, `SELECT id FROM organizations WHERE id=$1 FOR SHARE`, scope.ID).Scan(&organizationID)
	case authorization.Project:
		err = tx.QueryRow(ctx, `SELECT r.organization_id,r.id FROM repositories r
            JOIN organizations o ON o.id=r.organization_id WHERE r.id=$1 AND r.active FOR SHARE OF r,o`, scope.ID).Scan(&organizationID, &projectID)
	case authorization.Environment:
		err = tx.QueryRow(ctx, `SELECT r.organization_id,r.id,e.protected FROM environments e
            JOIN repositories r ON r.id=e.repository_id JOIN organizations o ON o.id=r.organization_id
            WHERE e.id=$1 AND r.active FOR SHARE OF e,r,o`, scope.ID).Scan(&organizationID, &projectID, &protected)
	default:
		return authorization.Resource{}, authorization.ErrForbidden
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return authorization.Resource{}, authorization.ErrForbidden
	}
	if err != nil {
		return authorization.Resource{}, err
	}
	if organizationID != uuid.Nil {
		path = append(path, authorization.Scope{Kind: authorization.Organization, ID: organizationID})
	}
	if projectID != uuid.Nil {
		path = append(path, authorization.Scope{Kind: authorization.Project, ID: projectID})
	}
	resource := authorization.Resource{Path: path}
	if scope.Kind == authorization.Environment {
		resource.Path = append(resource.Path, scope)
		resource.Protection = authorization.Unprotected
		if protected {
			resource.Protection = authorization.Protected
		}
	}
	return resource, nil
}

func authorize(ctx context.Context, tx pgx.Tx, session identity.Session, scope authorization.Scope, action authorization.Action) error {
	resource, err := scopeResource(ctx, tx, scope)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT role,
        CASE WHEN instance_id IS NOT NULL THEN 'instance' WHEN organization_id IS NOT NULL THEN 'organization'
             WHEN repository_id IS NOT NULL THEN 'project' ELSE 'environment' END,
        COALESCE(instance_id,organization_id,repository_id,environment_id)
        FROM memberships WHERE user_id=$1 FOR SHARE`, session.UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	grants := []authorization.Grant{}
	for rows.Next() {
		grant := authorization.Grant{SubjectID: session.UserID}
		if err := rows.Scan(&grant.Role, &grant.Scope.Kind, &grant.Scope.ID); err != nil {
			return err
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !authorization.Allowed(authorization.Principal{ID: session.UserID, Active: true}, authorization.Request{Action: action, Resource: resource}, grants) {
		return authorization.ErrForbidden
	}
	return nil
}

func (s *Store) ChangeMembership(ctx context.Context, token string, subjectID uuid.UUID, scope authorization.Scope, role authorization.Role, add bool) error {
	column := map[authorization.ScopeKind]string{authorization.Instance: "instance_id", authorization.Organization: "organization_id",
		authorization.Project: "repository_id", authorization.Environment: "environment_id"}[scope.Kind]
	if column == "" || subjectID == uuid.Nil || scope.ID == uuid.Nil {
		return authorization.ErrForbidden
	}
	switch role {
	case authorization.Viewer, authorization.Developer, authorization.Maintainer, authorization.Admin:
	case authorization.Deployer, authorization.Approver:
		if scope.Kind != authorization.Environment {
			return authorization.ErrForbidden
		}
	default:
		return authorization.ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize membership edits to prevent reciprocal administrator grant
	// revocations from acquiring shared/exclusive membership locks in reverse order.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(982716421)`); err != nil {
		return err
	}
	session, err := authenticateSession(ctx, tx, token)
	if err != nil {
		return err
	}
	if session.UserID == subjectID {
		return authorization.ErrForbidden
	}
	if err := authorize(ctx, tx, session, scope, authorization.MembersManage); err != nil {
		return err
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT status='active' FROM users WHERE id=$1 FOR SHARE`, subjectID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && add && !active) {
		return authorization.ErrForbidden
	}
	if err != nil {
		return err
	}
	var id uuid.UUID
	action := "membership.revoked"
	if err := sessionLive(ctx, tx, session); err != nil {
		return err
	}
	if add {
		action = "membership.granted"
		err = tx.QueryRow(ctx, `INSERT INTO memberships(user_id,role,`+column+`) VALUES ($1,$2,$3)
            ON CONFLICT DO NOTHING RETURNING id`, subjectID, role, scope.ID).Scan(&id)
	} else {
		err = tx.QueryRow(ctx, `DELETE FROM memberships WHERE user_id=$1 AND role=$2 AND `+column+`=$3 RETURNING id`, subjectID, role, scope.ID).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	// Preserve the revoked grant's subject/scope/role after its row is gone.
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_user_id,action,resource_type,resource_id,metadata)
        VALUES ($1,$2,'membership',$3,jsonb_build_object('subject_id',$4::text,'scope_kind',$5::text,'scope_id',$6::text,'role',$7::text))`,
		session.UserID, action, id.String(), subjectID.String(), string(scope.Kind), scope.ID.String(), string(role))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
